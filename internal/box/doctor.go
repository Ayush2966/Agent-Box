package box

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agentbox-dev/agentbox/internal/config"
	"github.com/agentbox-dev/agentbox/internal/db"
	"github.com/agentbox-dev/agentbox/internal/errs"
	"github.com/agentbox-dev/agentbox/internal/events"
	"github.com/agentbox-dev/agentbox/internal/state"
	"github.com/agentbox-dev/agentbox/internal/worktree"
)

type Finding struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
	Fixed  bool   `json:"fixed"`
}

// RefreshDB re-snapshots the source database used for new boxes.
func (m *Manager) RefreshDB(ctx context.Context, repoID string, terminate bool) error {
	repo, err := m.repo(repoID)
	if err != nil {
		return err
	}
	lock := m.repoLock(repoID)
	lock.Lock()
	defer lock.Unlock()
	cfg, err := config.Load(repo.Path)
	if err != nil {
		return errs.New(errs.Invalid, "%v", err)
	}
	if cfg.DB == nil {
		return errs.New(errs.Precondition, "%s has no db section", config.FileName)
	}
	drv, err := m.d.NewDriver(cfg.DB, repo.Path, db.Options{Terminate: terminate})
	if err != nil {
		return err
	}
	return dbError(drv.Refresh(ctx))
}

// Doctor finds drift between agentbox's records and reality. With fix it
// repairs only what is safe; anything that could hold the user's work is reported.
func (m *Manager) Doctor(ctx context.Context, repoID string, fix bool) ([]Finding, error) {
	repo, err := m.repo(repoID)
	if err != nil {
		return nil, err
	}
	// The report is read-only and must never wait behind a long `new`
	// (spec: reads are never blocked). Repairs do serialise with lifecycle
	// operations, since they delete rows and drop databases.
	if fix {
		lock := m.repoLock(repoID)
		lock.Lock()
		defer lock.Unlock()
	}

	var out []Finding
	add := func(kind string, fixed bool, format string, a ...any) {
		out = append(out, Finding{Kind: kind, Detail: fmt.Sprintf(format, a...), Fixed: fixed})
	}

	cfg, err := config.Load(repo.Path)
	if err != nil {
		add("config", false, "%v", err)
	}
	var drv db.Driver
	if cfg != nil && cfg.DB != nil {
		if drv, err = m.d.NewDriver(cfg.DB, repo.Path, db.Options{}); err != nil {
			add("db", false, "%v", err)
			drv = nil
		} else if err := drv.Check(ctx); err != nil {
			add("db", false, "%v", err)
		}
	}

	boxes, err := m.d.Store.ListBoxes(repoID)
	if err != nil {
		return nil, err
	}
	dirs, branches, dbNames := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, b := range boxes {
		dirs[b.Dir], branches[b.Branch] = true, true
		if b.DBName != "" {
			dbNames[b.DBName] = true
		}
		if b.State == state.Creating || b.State == state.Removing {
			continue // mid-transition without the lock: a missing dir is expected, not drift
		}
		if _, err := os.Stat(b.Dir); !os.IsNotExist(err) {
			continue
		}
		fixed := false
		if fix {
			m.stopProc(b.ID())
			if drv != nil && b.DBName != "" {
				_ = drv.Drop(ctx, b.DBName, b.Dir)
			}
			_ = m.d.Store.DeleteBox(b.RepoID, b.Name)
			m.closeLog(b, true)
			m.publish(events.BoxRemoved, b, nil)
			delete(branches, b.Branch)
			fixed = true
		}
		add("missing_worktree", fixed, "box %q: directory %s is gone", b.Name, b.Dir)
	}
	if fix {
		_ = worktree.Prune(repo.Path)
	}

	if list, err := worktree.ListBranches(repo.Path, BranchPrefix); err == nil {
		for _, br := range list {
			if !branches[br] {
				add("orphan_branch", false, "branch %s has no box (delete it with `git branch -D %s` if you don't need it)", br, br)
			}
		}
	}
	if wts, err := worktree.List(repo.Path); err == nil {
		root := filepath.Join(m.d.BoxesDir, repoID) + string(filepath.Separator)
		for _, wt := range wts {
			if strings.HasPrefix(wt, root) && !dirs[wt] {
				add("orphan_worktree", false, "worktree %s has no box (remove it with `git worktree remove %s`)", wt, wt)
			}
		}
	}
	if drv != nil {
		if names, err := drv.List(ctx); err == nil {
			for _, n := range names {
				if dbNames[n] {
					continue
				}
				fixed := fix && drv.Drop(ctx, n, "") == nil
				add("orphan_db", fixed, "database %s has no box", n)
			}
		}
	}
	if rows, err := m.d.Store.AllPorts(); err == nil {
		m.mu.Lock()
		running := map[string]bool{}
		for id := range m.procs {
			running[id] = true
		}
		m.mu.Unlock()
		for _, r := range rows {
			if r.RepoID != repoID || running[r.RepoID+"/"+r.Box] {
				continue
			}
			if !m.d.IsFree(r.Port) {
				add("port_in_use", false, "port %d (%s of box %q) is held by another process", r.Port, r.EnvName, r.Box)
			}
		}
	}
	return out, nil
}
