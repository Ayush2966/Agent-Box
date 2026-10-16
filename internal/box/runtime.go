package box

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Ayush2966/Agent-Box/internal/compose"
	"github.com/Ayush2966/Agent-Box/internal/config"
	"github.com/Ayush2966/Agent-Box/internal/errs"
	"github.com/Ayush2966/Agent-Box/internal/events"
	"github.com/Ayush2966/Agent-Box/internal/proc"
	"github.com/Ayush2966/Agent-Box/internal/state"
	"github.com/Ayush2966/Agent-Box/internal/worktree"
)

// Info is a box plus live git status and its managed env vars.
type Info struct {
	state.Box
	RepoPath string
	Base     string
	Ahead    int
	Behind   int
	Dirty    int
	Env      map[string]string
}

type MergeOptions struct {
	Into   string `json:"into,omitempty"`
	Squash bool   `json:"squash,omitempty"`
}

func (m *Manager) Info(repoID, name string) (Info, error) {
	b, err := m.get(repoID, name)
	if err != nil {
		return Info{}, err
	}
	return m.info(b), nil
}

// List returns boxes of one repo, or of all repos when repoID is empty.
func (m *Manager) List(repoID string) ([]Info, error) {
	boxes, err := m.d.Store.ListBoxes(repoID)
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(boxes))
	for _, b := range boxes {
		out = append(out, m.info(b))
	}
	return out, nil
}

func (m *Manager) info(b state.Box) Info {
	i := Info{Box: b}
	repo, err := m.d.Store.RepoByID(b.RepoID)
	if err != nil {
		return i
	}
	i.RepoPath = repo.Path
	cfg, _ := config.Load(repo.Path)
	// No git while the worktree is being made or torn down: a `git status`
	// in a half-checked-out worktree takes its index.lock, and the
	// `git worktree add` still running there then fails with "index.lock:
	// File exists" — seen on CI, where the dashboard/CLI polling GET /v1/boxes
	// during creation raced the checkout.
	if b.State != state.Creating && b.State != state.Removing {
		if base, err := worktree.CurrentBranch(repo.Path); err == nil {
			i.Base = base
			i.Ahead, i.Behind, _ = worktree.AheadBehind(repo.Path, base, b.Branch)
		}
		if ch, err := userChanges(b, cfg); err == nil {
			i.Dirty = len(ch)
		}
	}
	if cfg != nil {
		i.Env = map[string]string{}
		for _, kv := range m.envVars(cfg, b) {
			i.Env[kv.Key] = kv.Value
		}
	}
	return i
}

func (m *Manager) Start(ctx context.Context, repoID, name string) error {
	lock := m.repoLock(repoID)
	lock.Lock()
	defer lock.Unlock()
	b, err := m.get(repoID, name)
	if err != nil {
		return err
	}
	switch b.State {
	case state.Running:
		return nil
	case state.Ready, state.Stopped:
	default:
		return errs.New(errs.Precondition, "box %q is %s", name, b.State).
			WithHint("remove it with `agentbox rm --force " + name + "`")
	}
	repo, err := m.repo(repoID)
	if err != nil {
		return err
	}
	cfg, err := config.Load(repo.Path)
	if err != nil {
		return errs.New(errs.Invalid, "%v", err)
	}
	if cfg.Dev == "" && cfg.Compose == nil {
		return errs.New(errs.Precondition, "no dev command in %s", config.FileName).WithHint("add a `dev:` line")
	}
	if cfg.Compose != nil {
		st, err := m.stack(cfg, b)
		if err != nil {
			return errs.From(err).WithStep(StepCompose)
		}
		if b.Compose != st.Project {
			b.Compose = st.Project
			_ = m.d.Store.UpdateBox(b)
		}
		if err := st.Up(ctx); err != nil {
			return errs.From(err).WithStep(StepCompose)
		}
	}
	if cfg.Dev == "" {
		return nil // stack only
	}
	if err := m.refreshEnvFile(repo.Path, cfg, b); err != nil {
		return errs.From(err).WithStep(StepEnv)
	}
	p, err := m.startProc(b, cfg)
	if err != nil {
		return err
	}
	_, err = m.saveRunning(b, p)
	return err
}

func (m *Manager) Stop(ctx context.Context, repoID, name string) error {
	lock := m.repoLock(repoID)
	lock.Lock()
	defer lock.Unlock()
	b, err := m.get(repoID, name)
	if err != nil {
		return err
	}
	m.stopProc(b.ID())
	var stackErr error
	if b.Compose != "" {
		stackErr = compose.StopProject(ctx, m.d.Compose, m.stackDir(), b.Compose, m.stackLog(b))
	}
	if b.State != state.Running {
		return stackErr
	}
	b, err = m.get(repoID, name) // the process may have updated the row while stopping
	if err != nil {
		return err
	}
	b.State, b.PID = state.Stopped, 0
	if err := m.d.Store.UpdateBox(b); err != nil {
		return err
	}
	m.publish(events.BoxState, b, nil)
	return stackErr
}

// Merge merges the box branch into the main checkout's current branch. It never
// switches branches, stashes or forces anything; conflicts abort the merge.
func (m *Manager) Merge(ctx context.Context, repoID, name string, opts MergeOptions) (string, error) {
	lock := m.repoLock(repoID)
	lock.Lock()
	defer lock.Unlock()
	b, err := m.get(repoID, name)
	if err != nil {
		return "", err
	}
	repo, err := m.repo(repoID)
	if err != nil {
		return "", err
	}
	cfg, _ := config.Load(repo.Path)
	ch, err := userChanges(b, cfg)
	if err != nil {
		return "", err
	}
	if len(ch) > 0 {
		return "", errs.New(errs.Precondition, "box %q has uncommitted changes: %s", name, summarize(ch)).
			WithHint(fmt.Sprintf("commit them in the box first, e.g. `agentbox exec %s -- git commit -am \"…\"`", name))
	}
	cur, err := worktree.CurrentBranch(repo.Path)
	if err != nil {
		return "", err
	}
	into := opts.Into
	if into == "" {
		into = cur
	}
	if cur != into {
		return "", errs.New(errs.Precondition, "the main checkout is on %q, not %q", cur, into).
			WithHint(fmt.Sprintf("run `git -C %q switch %s` first", repo.Path, into))
	}
	if ch, err := worktree.Changes(repo.Path, false); err != nil {
		return "", err
	} else if len(ch) > 0 {
		return "", errs.New(errs.Precondition, "the main checkout has uncommitted changes: %s", summarize(ch)).
			WithHint("commit or stash them first")
	}
	err = worktree.Merge(repo.Path, b.Branch, opts.Squash, "agentbox: merge box "+name)
	var ce *worktree.ConflictError
	if errors.As(err, &ce) {
		return "", errs.New(errs.Conflict, "merging %s into %s conflicts in: %s", b.Branch, into, strings.Join(ce.Files, ", ")).
			WithHint(fmt.Sprintf("the merge was aborted and nothing changed; rebase the box with `agentbox exec %s -- git rebase %s`, resolve, then merge again", name, into))
	}
	if err != nil {
		return "", err
	}
	return into, nil
}

func (m *Manager) Diff(repoID, name, against string) (string, error) {
	b, err := m.get(repoID, name)
	if err != nil {
		return "", err
	}
	if against == "" {
		repo, err := m.repo(repoID)
		if err != nil {
			return "", err
		}
		if against, err = worktree.CurrentBranch(repo.Path); err != nil {
			return "", err
		}
	}
	return worktree.Diff(b.Dir, against)
}

func (m *Manager) LogTail(repoID, name string, n int) ([]string, error) {
	b, err := m.get(repoID, name)
	if err != nil {
		return nil, err
	}
	return proc.TailFile(m.logPath(b), n)
}

func (m *Manager) LogSubscribe(repoID, name string) (<-chan string, func(), error) {
	b, err := m.get(repoID, name)
	if err != nil {
		return nil, nil, err
	}
	l, err := m.openLog(b)
	if err != nil {
		return nil, nil, err
	}
	ch, cancel := l.Ring.Subscribe()
	return ch, cancel, nil
}

// Recover fixes up state left by a daemon that exited without cleaning up.
// It never restarts anything on its own.
func (m *Manager) Recover() error {
	boxes, err := m.d.Store.ListBoxes("")
	if err != nil {
		return err
	}
	for _, b := range boxes {
		switch b.State {
		case state.Running:
			b.State, b.PID = state.Stopped, 0
			b.LastError = "agentbox daemon restarted; start it again with `agentbox start " + b.Name + "`"
		case state.Creating, state.Removing:
			b.State = state.Failed
			b.LastError = "interrupted by a daemon restart; clean up with `agentbox rm --force " + b.Name + "`"
		default:
			continue
		}
		if err := m.d.Store.UpdateBox(b); err != nil {
			return err
		}
	}
	return nil
}
