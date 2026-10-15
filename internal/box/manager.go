// Package box orchestrates a box's lifecycle: worktree, ports, database,
// env file, setup command and supervised dev server.
package box

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Ayush2966/Agent-Box/internal/compose"
	"github.com/Ayush2966/Agent-Box/internal/config"
	"github.com/Ayush2966/Agent-Box/internal/db"
	"github.com/Ayush2966/Agent-Box/internal/envfile"
	"github.com/Ayush2966/Agent-Box/internal/errs"
	"github.com/Ayush2966/Agent-Box/internal/events"
	"github.com/Ayush2966/Agent-Box/internal/ports"
	"github.com/Ayush2966/Agent-Box/internal/proc"
	"github.com/Ayush2966/Agent-Box/internal/state"
	"github.com/Ayush2966/Agent-Box/internal/worktree"
)

const BranchPrefix = "agentbox/"

const (
	StepWorktree = "worktree"
	StepPorts    = "ports"
	StepDB       = "db"
	StepEnv      = "env"
	StepCompose  = "compose"
	StepSetup    = "setup"
	StepDev      = "dev"
)

type Deps struct {
	Store       *state.Store
	BoxesDir    string // parent of every box worktree
	LogsDir     string // parent of every box log
	PortLo      int
	PortHi      int
	IsFree      func(port int) bool
	NewDriver   func(cfg *config.DB, repoRoot string, opts db.Options) (db.Driver, error)
	Bus         *events.Bus
	ProcOptions proc.Options
	Compose     compose.Executor // docker; nil = the real CLI
	// APIPort reports the daemon's HTTP port (0 when the API is off): the
	// `url` form of links points at the proxy there. nil behaves like 0.
	APIPort func() int
	// Open: how a box is opened for the user. Editor/Terminal are the
	// configured commands ("" = detect); Launch runs them (nil = exec).
	Editor, Terminal string
	Launch           Launcher
}

// Manager owns every box on the machine. Methods are safe for concurrent use;
// lifecycle operations within one repo are serialized.
type Manager struct {
	d        Deps
	mu       sync.Mutex
	locks    map[string]*sync.Mutex
	procs    map[string]*proc.Proc
	logs     map[string]*proc.Log
	testHook func(step string) error // tests only: runs after each step of New
}

func NewManager(d Deps) *Manager {
	if d.IsFree == nil {
		d.IsFree = ports.IsFree
	}
	if d.NewDriver == nil {
		d.NewDriver = db.New
	}
	if d.Compose == nil {
		d.Compose = compose.Docker{}
	}
	if d.Bus == nil {
		d.Bus = events.NewBus()
	}
	if d.PortLo == 0 && d.PortHi == 0 {
		d.PortLo, d.PortHi = ports.DefaultLo, ports.DefaultHi
	}
	if d.ProcOptions.MaxCrashes == 0 {
		d.ProcOptions = proc.DefaultOptions
	}
	if p, err := filepath.EvalSymlinks(d.BoxesDir); err == nil {
		d.BoxesDir = p // git reports resolved paths; keep ours comparable
	}
	return &Manager{d: d, locks: map[string]*sync.Mutex{}, procs: map[string]*proc.Proc{}, logs: map[string]*proc.Log{}}
}

func (m *Manager) Bus() *events.Bus { return m.d.Bus }

// stackDir is where by-name compose commands (stop/down -p <project>) run: the
// boxes dir holds no compose file, and it is recreated here because exec fails
// outright when its working directory is gone (a user may rm -rf it).
func (m *Manager) stackDir() string {
	_ = os.MkdirAll(m.d.BoxesDir, 0o700)
	return m.d.BoxesDir
}

type NewOptions struct {
	RepoPath   string `json:"repo_path"`
	Name       string `json:"name"`
	From       string `json:"from,omitempty"`
	NoDev      bool   `json:"no_dev,omitempty"`
	KeepFailed bool   `json:"keep_failed,omitempty"`
	Terminate  bool   `json:"terminate,omitempty"`
}

type RemoveOptions struct {
	Force      bool `json:"force,omitempty"`
	KeepBranch bool `json:"keep_branch,omitempty"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// ResolveRepo maps any path inside a repo (or inside one of its boxes) to the repo record.
func (m *Manager) ResolveRepo(path string) (state.Repo, error) {
	if path == "" {
		return state.Repo{}, errs.New(errs.Invalid, "repo path is required")
	}
	root, err := worktree.MainWorktree(path)
	if err != nil {
		return state.Repo{}, errs.New(errs.Invalid, "%v", err)
	}
	return m.d.Store.UpsertRepo(root)
}

func (m *Manager) Repos() ([]state.Repo, error) { return m.d.Store.Repos() }

// Validate checks everything about a new box that can be checked without creating anything.
func (m *Manager) Validate(opts NewOptions) (state.Repo, *config.Config, error) {
	if !nameRe.MatchString(opts.Name) {
		return state.Repo{}, nil, errs.New(errs.Invalid, "invalid box name %q", opts.Name).
			WithHint("use 1-31 characters: lowercase letters, digits and dashes, starting with a letter or digit")
	}
	repo, err := m.ResolveRepo(opts.RepoPath)
	if err != nil {
		return repo, nil, err
	}
	cfg, err := config.Load(repo.Path)
	if err != nil {
		return repo, nil, errs.New(errs.Invalid, "%v", err)
	}
	if t := cfg.Env.Template; t != "" {
		if _, err := os.Stat(filepath.Join(repo.Path, t)); err != nil {
			return repo, nil, errs.New(errs.Invalid, "env.template %s not found in %s", t, repo.Path)
		}
	}
	for _, c := range cfg.Copy {
		if _, err := os.Stat(filepath.Join(repo.Path, c)); err != nil {
			return repo, nil, errs.New(errs.Invalid, "copy: %s not found in %s", c, repo.Path)
		}
	}
	if err := m.checkLinks(cfg); err != nil {
		return repo, nil, err
	}
	if _, err := m.d.Store.GetBox(repo.ID, opts.Name); err == nil {
		return repo, nil, errs.New(errs.Exists, "box %q already exists", opts.Name).
			WithHint("pick another name or remove it with `agentbox rm " + opts.Name + "`")
	}
	if branch := BranchPrefix + opts.Name; worktree.BranchExists(repo.Path, branch) {
		return repo, nil, errs.New(errs.Exists, "branch %s already exists", branch).
			WithHint(fmt.Sprintf("pick another name, or delete the branch with `git branch -D %s` if you don't need it", branch))
	}
	return repo, cfg, nil
}

// New creates a box. Every step registers an undo; on failure the undos run in
// reverse and nothing is left behind (unless opts.KeepFailed).
func (m *Manager) New(ctx context.Context, opts NewOptions) (state.Box, error) {
	b, cfg, err := m.Claim(opts)
	if err != nil {
		return b, err
	}
	return m.Build(ctx, b, cfg, opts)
}

// Claim validates a new box and reserves its name, holding the repo lock
// across both so the check and the reservation cannot interleave. It is the
// synchronous half of New: callers that build in the background (the API)
// call it before accepting the request, so a duplicate name is rejected
// outright instead of being accepted and silently losing a race.
func (m *Manager) Claim(opts NewOptions) (state.Box, *config.Config, error) {
	repo, err := m.ResolveRepo(opts.RepoPath)
	if err != nil {
		return state.Box{}, nil, err
	}
	lock := m.repoLock(repo.ID)
	lock.Lock()
	defer lock.Unlock()

	repo, cfg, err := m.Validate(opts)
	if err != nil {
		return state.Box{}, nil, err
	}
	b := state.Box{
		RepoID: repo.ID, Name: opts.Name, Branch: BranchPrefix + opts.Name,
		Dir:   filepath.Join(m.d.BoxesDir, repo.ID, opts.Name),
		State: state.Creating, CreatedAt: time.Now(),
		// Recorded now, from the config this box was built with: a later edit
		// to agentbox.yaml must not turn agentbox's own files into the
		// agent's uncommitted work.
		Managed: slices.Sorted(maps.Keys(managedPaths(cfg))),
	}
	if err := m.d.Store.InsertBox(b); err != nil {
		if errors.Is(err, state.ErrExists) {
			return b, nil, errs.New(errs.Exists, "box %q already exists", opts.Name).
				WithHint("pick another name or remove it with `agentbox rm " + opts.Name + "`")
		}
		return b, nil, err
	}
	m.publish(events.BoxCreated, b, nil)
	return b, cfg, nil
}

// Build finishes a box that Claim reserved, running the create steps and
// rolling them back on failure. Progress is published on the event bus.
func (m *Manager) Build(ctx context.Context, b state.Box, cfg *config.Config, opts NewOptions) (state.Box, error) {
	repo, err := m.repo(b.RepoID)
	if err != nil {
		return b, err
	}
	lock := m.repoLock(repo.ID)
	lock.Lock()
	defer lock.Unlock()

	var undo undoStack
	fail := func(step string, err error) (state.Box, error) {
		e := errs.From(err).WithStep(step)
		if opts.KeepFailed {
			m.stopProc(b.ID())
			b.State, b.LastStep, b.LastError = state.Failed, step, e.Message
			_ = m.d.Store.UpdateBox(b)
			m.publish(events.BoxState, b, map[string]any{"error": e.Error()})
			return b, e
		}
		undo.run()
		m.closeLog(b, true)
		_ = m.d.Store.DeleteBox(b.RepoID, b.Name)
		m.publish(events.BoxRemoved, b, map[string]any{"error": e.Error()})
		return b, e
	}
	begin := func(step string) {
		b.LastStep = step
		m.publish(events.BoxStep, b, map[string]any{"step": step})
	}
	hook := func(step string) error {
		if m.testHook != nil {
			return m.testHook(step)
		}
		return nil
	}

	begin(StepWorktree)
	if err := os.MkdirAll(filepath.Dir(b.Dir), 0o755); err != nil {
		return fail(StepWorktree, err)
	}
	// Registered before Add, not after: `git worktree add -b` creates the
	// branch before the directory, so a failure inside Add still leaves the
	// branch behind. The undo tolerates every partial outcome.
	undo.push(func() error {
		if err := worktree.Remove(repo.Path, b.Dir); err != nil {
			return err
		}
		if !worktree.BranchExists(repo.Path, b.Branch) {
			return nil
		}
		return worktree.DeleteBranch(repo.Path, b.Branch)
	})
	if err := worktree.Add(repo.Path, b.Dir, b.Branch, opts.From); err != nil {
		return fail(StepWorktree, err)
	}
	if err := hook(StepWorktree); err != nil {
		return fail(StepWorktree, err)
	}

	begin(StepPorts)
	alloc, err := m.d.Store.AllocatePorts(b.RepoID, b.Name, cfg.Ports, m.d.PortLo, m.d.PortHi, m.d.IsFree)
	if err != nil {
		return fail(StepPorts, err)
	}
	b.Ports = alloc
	undo.push(func() error { return m.d.Store.FreePorts(b.RepoID, b.Name) })
	if err := hook(StepPorts); err != nil {
		return fail(StepPorts, err)
	}

	if cfg.DB != nil {
		begin(StepDB)
		drv, err := m.d.NewDriver(cfg.DB, repo.Path, db.Options{Terminate: opts.Terminate})
		if err != nil {
			return fail(StepDB, err)
		}
		if err := drv.Prepare(ctx); err != nil {
			return fail(StepDB, dbError(err))
		}
		clone, err := drv.Clone(ctx, b.Name, b.Dir)
		if err != nil {
			return fail(StepDB, dbError(err))
		}
		b.DBName, b.DBURL = clone.Name, clone.URL
		undo.push(func() error { return drv.Drop(context.Background(), clone.Name, b.Dir) })
		if err := hook(StepDB); err != nil {
			return fail(StepDB, err)
		}
	}

	begin(StepEnv)
	if err := m.writeEnv(repo.Path, cfg, b); err != nil {
		return fail(StepEnv, err)
	}
	if err := hook(StepEnv); err != nil {
		return fail(StepEnv, err)
	}

	if cfg.Compose != nil {
		begin(StepCompose)
		if err := compose.Check(ctx, m.d.Compose); err != nil {
			return fail(StepCompose, err)
		}
		st, err := m.stack(cfg, b)
		if err != nil {
			return fail(StepCompose, err)
		}
		b.Compose = st.Project
		if err := m.d.Store.UpdateBox(b); err != nil {
			return fail(StepCompose, err)
		}
		undo.push(func() error {
			return compose.DownProject(context.Background(), m.d.Compose, m.stackDir(), st.Project, m.stackLog(b))
		})
		if err := st.Up(ctx); err != nil {
			return fail(StepCompose, err)
		}
		if err := hook(StepCompose); err != nil {
			return fail(StepCompose, err)
		}
	}

	if cfg.Setup != "" {
		begin(StepSetup)
		logw, err := m.openLog(b)
		if err != nil {
			return fail(StepSetup, err)
		}
		if err := runSetup(ctx, cfg.Setup, b.Dir, processEnv(m.envVars(cfg, b)), logw); err != nil {
			return fail(StepSetup, err)
		}
		if err := hook(StepSetup); err != nil {
			return fail(StepSetup, err)
		}
	}

	if cfg.Dev != "" && !opts.NoDev {
		begin(StepDev)
		p, err := m.startProc(b, cfg)
		if err != nil {
			return fail(StepDev, err)
		}
		undo.push(func() error { m.stopProc(b.ID()); return nil })
		if err := hook(StepDev); err != nil {
			return fail(StepDev, err)
		}
		b.LastStep = ""
		saved, err := m.saveRunning(b, p)
		if err != nil {
			return fail(StepDev, err)
		}
		return saved, nil
	}

	b.State, b.LastStep = state.Ready, ""
	if err := m.d.Store.UpdateBox(b); err != nil {
		return fail(StepEnv, err)
	}
	m.publish(events.BoxState, b, nil)
	return b, nil
}

// Remove tears a box down. Unless opts.Force, it refuses when the box holds
// uncommitted changes or commits that aren't in the main checkout's branch.
func (m *Manager) Remove(ctx context.Context, repoID, name string, opts RemoveOptions) error {
	lock := m.repoLock(repoID)
	lock.Lock()
	defer lock.Unlock()
	b, err := m.get(repoID, name)
	if err != nil {
		return err
	}
	repo, err := m.repo(repoID)
	if err != nil {
		return err
	}
	cfg, cfgErr := config.Load(repo.Path)
	if !opts.Force {
		if err := m.checkNoUnmergedWork(repo, b, cfg); err != nil {
			return err
		}
	}
	b.State = state.Removing
	_ = m.d.Store.UpdateBox(b)
	m.publish(events.BoxState, b, nil)

	var problems []string
	note := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	m.stopProc(b.ID())
	if b.Compose != "" {
		if err := compose.DownProject(ctx, m.d.Compose, m.stackDir(), b.Compose, m.stackLog(b)); err != nil {
			note("compose project %s was not removed: %v", b.Compose, err)
		}
	}
	if b.DBName != "" && !dbLivesInBox(b) {
		if cfgErr != nil || cfg.DB == nil {
			note("database %s was not dropped: agentbox.yaml no longer configures a db", b.DBName)
		} else if drv, err := m.d.NewDriver(cfg.DB, repo.Path, db.Options{}); err != nil {
			note("database %s was not dropped: %v", b.DBName, err)
		} else if err := drv.Drop(ctx, b.DBName, b.Dir); err != nil {
			note("database %s was not dropped: %v", b.DBName, err)
		}
	}
	if err := m.d.Store.FreePorts(repoID, name); err != nil {
		note("ports were not freed: %v", err)
	}
	if err := worktree.Remove(repo.Path, b.Dir); err != nil {
		note("worktree was not removed: %v", err)
	}
	if !opts.KeepBranch && worktree.BranchExists(repo.Path, b.Branch) {
		if err := worktree.DeleteBranch(repo.Path, b.Branch); err != nil {
			note("branch was not deleted: %v", err)
		}
	}
	m.closeLog(b, true)
	if err := m.d.Store.DeleteBox(repoID, name); err != nil {
		note("state was not cleared: %v", err)
	}
	m.publish(events.BoxRemoved, b, nil)
	if len(problems) > 0 {
		return errs.New(errs.Internal, "box %q removed, but:\n  %s", name, strings.Join(problems, "\n  ")).
			WithHint("run `agentbox doctor` to find anything left behind")
	}
	return nil
}

// dbLivesInBox reports whether the box's database is a file inside the box
// directory (the SQLite driver), so removing the worktree removes it too and
// a missing driver is nothing to warn about.
func dbLivesInBox(b state.Box) bool {
	return b.DBURL != "" && !strings.Contains(b.DBURL, "://")
}

func (m *Manager) checkNoUnmergedWork(repo state.Repo, b state.Box, cfg *config.Config) error {
	if ch, err := userChanges(b, cfg); err == nil && len(ch) > 0 {
		return errs.New(errs.UnmergedWork, "box %q has %d uncommitted change(s): %s", b.Name, len(ch), summarize(ch)).
			WithHint("use --force to discard them")
	}
	base, err := worktree.CurrentBranch(repo.Path)
	if err != nil {
		return nil
	}
	ahead, _, err := worktree.AheadBehind(repo.Path, base, b.Branch)
	if err == nil && ahead > 0 && !worktree.MergeIsNoop(repo.Path, base, b.Branch) {
		return errs.New(errs.UnmergedWork, "box %q has %d commit(s) not merged into %s", b.Name, ahead, base).
			WithHint("merge first with `agentbox merge " + b.Name + "`, or use --force to discard them")
	}
	return nil
}

// Shutdown stops every dev server; used when the daemon exits.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	procs, logs := m.procs, m.logs
	m.procs, m.logs = map[string]*proc.Proc{}, map[string]*proc.Log{}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, p := range procs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.Stop()
		}()
	}
	wg.Wait()
	for _, l := range logs {
		_ = l.Close()
	}
	boxes, err := m.d.Store.ListBoxes("")
	if err != nil {
		return
	}
	for _, b := range boxes {
		if b.State == state.Running {
			b.State, b.PID, b.LastError = state.Stopped, 0, "agentbox daemon stopped"
			_ = m.d.Store.UpdateBox(b)
		}
	}
}

// --- helpers shared by the rest of the package ---

func (m *Manager) repoLock(repoID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[repoID]
	if !ok {
		l = &sync.Mutex{}
		m.locks[repoID] = l
	}
	return l
}

func (m *Manager) get(repoID, name string) (state.Box, error) {
	b, err := m.d.Store.GetBox(repoID, name)
	if errors.Is(err, state.ErrNotFound) {
		return b, errs.New(errs.NotFound, "no box %q in this repo", name).WithHint("list boxes with `agentbox ls`")
	}
	return b, err
}

func (m *Manager) repo(id string) (state.Repo, error) {
	r, err := m.d.Store.RepoByID(id)
	if errors.Is(err, state.ErrNotFound) {
		return r, errs.New(errs.NotFound, "unknown repo %q", id).WithHint("run the command from inside the repo")
	}
	return r, err
}

func (m *Manager) publish(typ string, b state.Box, extra map[string]any) {
	data := map[string]any{"state": string(b.State)}
	if b.LastError != "" {
		data["last_error"] = b.LastError
	}
	maps.Copy(data, extra)
	m.d.Bus.Publish(events.Event{Type: typ, Box: b.ID(), Data: data})
}

func (m *Manager) logPath(b state.Box) string {
	return filepath.Join(m.d.LogsDir, b.RepoID, b.Name+".log")
}

func (m *Manager) openLog(b state.Box) (*proc.Log, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.logs[b.ID()]; ok {
		return l, nil
	}
	l, err := proc.OpenLog(m.logPath(b))
	if err != nil {
		return nil, err
	}
	m.logs[b.ID()] = l
	return l, nil
}

func (m *Manager) closeLog(b state.Box, remove bool) {
	m.mu.Lock()
	l := m.logs[b.ID()]
	delete(m.logs, b.ID())
	m.mu.Unlock()
	if l != nil {
		_ = l.Close()
	}
	if remove {
		p := m.logPath(b)
		for _, suffix := range []string{"", ".1", ".2", ".3"} {
			_ = os.Remove(p + suffix)
		}
	}
}

// stack builds the box's Compose project. The compose file is read from the
// box worktree, so the box runs the version of the file it checked out.
func (m *Manager) stack(cfg *config.Config, b state.Box) (*compose.Stack, error) {
	file, err := compose.ResolveFile(b.Dir, cfg.Compose.File)
	if err != nil {
		return nil, err
	}
	logw, err := m.openLog(b)
	if err != nil {
		return nil, err
	}
	return &compose.Stack{
		Project: compose.ProjectName(b.RepoID, b.Name), Dir: b.Dir, File: file, EnvFile: cfg.Env.File,
		Services: cfg.Compose.Services, Wait: cfg.Compose.WaitTimeout(), Env: processEnv(m.envVars(cfg, b)),
		Exec: m.d.Compose, Log: logw,
	}, nil
}

// stackLog is where by-name stack commands (stop/down) write; it tolerates a
// box whose log can no longer be opened.
func (m *Manager) stackLog(b state.Box) io.Writer {
	if logw, err := m.openLog(b); err == nil {
		return logw
	}
	return io.Discard
}

func (m *Manager) startProc(b state.Box, cfg *config.Config) (*proc.Proc, error) {
	m.stopProc(b.ID())
	logw, err := m.openLog(b)
	if err != nil {
		return nil, err
	}
	repoID, name := b.RepoID, b.Name
	p := proc.New(proc.Spec{Command: cfg.Dev, Dir: b.Dir, Env: processEnv(m.envVars(cfg, b)), Out: logw}, m.d.ProcOptions,
		func(s proc.Status) { m.onProcChange(repoID, name, s) })
	if err := p.Start(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.procs[b.ID()] = p
	m.mu.Unlock()
	return p, nil
}

func (m *Manager) stopProc(id string) {
	m.mu.Lock()
	p := m.procs[id]
	delete(m.procs, id)
	m.mu.Unlock()
	if p != nil {
		_ = p.Stop()
	}
}

// saveRunning records a freshly started dev server. It re-checks the process
// afterwards because a crash loop may have ended before the box was saved.
func (m *Manager) saveRunning(b state.Box, p *proc.Proc) (state.Box, error) {
	b.State, b.PID, b.LastError = state.Running, p.Status().PID, ""
	if err := m.d.Store.UpdateBox(b); err != nil {
		return b, err
	}
	var extra map[string]any
	if st := p.Status(); st.GaveUp {
		b.State, b.PID, b.LastError = state.Stopped, 0, st.LastError
		if err := m.d.Store.UpdateBox(b); err != nil {
			return b, err
		}
		extra = map[string]any{"reason": "crashed"}
	}
	m.publish(events.BoxState, b, extra)
	return b, nil
}

// onProcChange mirrors a running box's process status into the store.
func (m *Manager) onProcChange(repoID, name string, s proc.Status) {
	b, err := m.d.Store.GetBox(repoID, name)
	if err != nil || b.State != state.Running {
		return // creating/stopping/removing code paths own the state
	}
	b.PID = s.PID
	if s.LastError != "" {
		b.LastError = s.LastError
	}
	var extra map[string]any
	if s.GaveUp {
		b.State, b.PID = state.Stopped, 0
		extra = map[string]any{"reason": "crashed"} // tells a crash loop from `agentbox stop`
	}
	if err := m.d.Store.UpdateBox(b); err != nil {
		slog.Warn("update box after process change", "box", b.ID(), "err", err)
		return
	}
	m.publish(events.BoxState, b, extra)
}

type undoStack []func() error

func (u *undoStack) push(f func() error) { *u = append(*u, f) }

func (u undoStack) run() {
	for i := len(u) - 1; i >= 0; i-- {
		if err := u[i](); err != nil {
			slog.Warn("rollback step failed", "err", err)
		}
	}
}

// envVars are the agentbox-managed variables, in the spec's precedence order.
// envVars are the variables agentbox manages for a box: env.set, its ports,
// the database URL, its identity, Compose's project name and its links. Links
// resolve now, so a `port`/`addr` link to a box that does not exist yet is
// empty until the next start (Start refreshes the env file).
func (m *Manager) envVars(cfg *config.Config, b state.Box) []envfile.KV {
	return append(baseEnvVars(cfg, b), m.linkVars(cfg, b)...)
}

func baseEnvVars(cfg *config.Config, b state.Box) []envfile.KV {
	var kv []envfile.KV
	for _, k := range slices.Sorted(maps.Keys(cfg.Env.Set)) {
		kv = append(kv, envfile.KV{Key: k, Value: cfg.Env.Set[k]})
	}
	for _, p := range cfg.Ports {
		kv = append(kv, envfile.KV{Key: p, Value: strconv.Itoa(b.Ports[p])})
	}
	if cfg.DB != nil && b.DBURL != "" {
		kv = append(kv, envfile.KV{Key: cfg.DB.URLEnv, Value: b.DBURL})
	}
	kv = append(kv, envfile.KV{Key: "AGENTBOX_NAME", Value: b.Name}, envfile.KV{Key: "AGENTBOX_ROOT", Value: b.Dir})
	if cfg.Compose != nil {
		kv = append(kv, envfile.KV{Key: "COMPOSE_PROJECT_NAME", Value: compose.ProjectName(b.RepoID, b.Name)})
	}
	return kv
}

func processEnv(vars []envfile.KV) []string {
	return append(os.Environ(), envfile.Pairs(vars)...)
}

// linkVars resolves `links:`: for each, the same-named box in the linked repo.
func (m *Manager) linkVars(cfg *config.Config, b state.Box) []envfile.KV {
	if len(cfg.Links) == 0 {
		return nil
	}
	repos, _ := m.d.Store.Repos()
	var out []envfile.KV
	for _, k := range slices.Sorted(maps.Keys(cfg.Links)) {
		l, err := config.ParseLink(cfg.Links[k])
		if err != nil {
			continue // validated by config.Load
		}
		val := ""
		if targets := matchRepos(repos, l.Repo); len(targets) == 1 {
			target := targets[0]
			switch l.Kind {
			case "url":
				if m.d.APIPort != nil {
					if p := m.d.APIPort(); p != 0 {
						val = fmt.Sprintf("http://%s.%s.localhost:%d/", b.Name, target.ID, p)
					}
				}
			default:
				if tb, err := m.d.Store.GetBox(target.ID, b.Name); err == nil {
					if p := m.proxyPort(tb); p != 0 {
						val = strconv.Itoa(p)
						if l.Kind == "addr" {
							val = "127.0.0.1:" + val
						}
					}
				}
			}
		}
		if val == "" {
			slog.Warn("link not resolved yet", "box", b.RepoID+"/"+b.Name, "env", k, "link", cfg.Links[k])
		}
		out = append(out, envfile.KV{Key: k, Value: val})
	}
	return out
}

// matchRepos finds the repos a link names: by id, or by folder name.
func matchRepos(repos []state.Repo, spec string) []state.Repo {
	var out []state.Repo
	for _, r := range repos {
		if r.ID == spec || filepath.Base(r.Path) == spec {
			out = append(out, r)
		}
	}
	return out
}

// checkLinks refuses a config whose links name repos agentbox does not know
// (or cannot tell apart), before any step of New runs.
func (m *Manager) checkLinks(cfg *config.Config) error {
	if len(cfg.Links) == 0 {
		return nil
	}
	repos, err := m.d.Store.Repos()
	if err != nil {
		return err
	}
	for _, k := range slices.Sorted(maps.Keys(cfg.Links)) {
		l, err := config.ParseLink(cfg.Links[k])
		if err != nil {
			return errs.New(errs.Invalid, "%v", err)
		}
		switch targets := matchRepos(repos, l.Repo); len(targets) {
		case 1:
		case 0:
			return errs.New(errs.Invalid, "links: %s: repo %q is not known to agentbox", k, l.Repo).
				WithHint("run `agentbox ls` (or any agentbox command) inside that repo once, then try again")
		default:
			var ids []string
			for _, t := range targets {
				ids = append(ids, t.ID)
			}
			return errs.New(errs.Invalid, "links: %s: %q matches several repos (%s)", k, l.Repo, strings.Join(ids, ", ")).
				WithHint("use the repo id instead of its folder name")
		}
	}
	return nil
}

func (m *Manager) writeEnv(repoRoot string, cfg *config.Config, b state.Box) error {
	for _, rel := range cfg.Copy {
		if err := copyFile(filepath.Join(repoRoot, rel), filepath.Join(b.Dir, rel)); err != nil {
			return err
		}
	}
	return m.renderEnvFile(repoRoot, cfg, b)
}

// renderEnvFile writes the box's env file from env.template plus the managed vars.
func (m *Manager) renderEnvFile(repoRoot string, cfg *config.Config, b state.Box) error {
	var tmpl []byte
	if cfg.Env.Template != "" {
		var err error
		if tmpl, err = os.ReadFile(filepath.Join(repoRoot, cfg.Env.Template)); err != nil {
			return err
		}
	}
	dst := filepath.Join(b.Dir, cfg.Env.File)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, envfile.Render(tmpl, m.envVars(cfg, b)), 0o600)
}

// refreshEnvFile updates the managed keys of the box's env file in place —
// links may have resolved since the box was created — and keeps every other
// line, including edits made inside the box. A missing file is written again.
func (m *Manager) refreshEnvFile(repoRoot string, cfg *config.Config, b state.Box) error {
	dst := filepath.Join(b.Dir, cfg.Env.File)
	cur, err := os.ReadFile(dst)
	if errors.Is(err, os.ErrNotExist) {
		return m.renderEnvFile(repoRoot, cfg, b)
	}
	if err != nil {
		return err
	}
	return os.WriteFile(dst, envfile.Render(cur, m.envVars(cfg, b)), 0o600)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func runSetup(ctx context.Context, command, dir string, env []string, out io.Writer) error {
	tail := proc.NewRing(20)
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir, cmd.Env = dir, env
	w := io.MultiWriter(out, tail)
	cmd.Stdout, cmd.Stderr = w, w
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("setup command %q failed (%v); last output:\n%s", command, err, strings.Join(tail.Tail(20), "\n"))
	}
	return nil
}

// managedPaths are files agentbox itself writes into a box; they never count as the agent's work.
func managedPaths(cfg *config.Config) map[string]bool {
	out := map[string]bool{}
	if cfg == nil {
		return out
	}
	clean := func(p string) string { return filepath.ToSlash(filepath.Clean(p)) }
	out[clean(cfg.Env.File)] = true
	for _, c := range cfg.Copy {
		out[clean(c)] = true
	}
	if cfg.DB != nil && cfg.DB.Driver == "sqlite" {
		p := clean(cfg.DB.Path())
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			out[p+suffix] = true
		}
	}
	return out
}

// userChanges lists the changes in a box that are the agent's own work,
// excluding everything agentbox generated. The generated set comes from the
// box record when it has one, so it survives config edits and a config that
// no longer loads; config-derived paths are only a fallback.
func userChanges(b state.Box, cfg *config.Config) ([]string, error) {
	all, err := worktree.Changes(b.Dir, true)
	if err != nil {
		return nil, err
	}
	managed := managedPaths(cfg)
	for _, p := range b.Managed {
		managed[p] = true
	}
	var out []string
	for _, p := range all {
		if !managed[p] {
			out = append(out, p)
		}
	}
	return out, nil
}

func summarize(paths []string) string {
	if len(paths) > 5 {
		return strings.Join(paths[:5], ", ") + ", …"
	}
	return strings.Join(paths, ", ")
}

func dbError(err error) error {
	if err == nil {
		return nil
	}
	var busy *db.BusyError
	if errors.As(err, &busy) {
		return errs.New(errs.Precondition, "%v", err).
			WithHint("close the connections to the template database, or re-run with --terminate")
	}
	return err
}
