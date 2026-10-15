package box

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Ayush2966/Agent-Box/internal/state"
)

// Prunable is a box state that `prune` removes: work the dev server is not
// doing. A `ready` box (created with --no-dev) is deliberately left alone.
func prunable(s state.BoxState) bool { return s == state.Stopped || s == state.Failed }

// PruneResult names a box prune removed, or tried to.
type PruneResult struct {
	Repo    string `json:"repo"`
	Name    string `json:"name"`
	Removed bool   `json:"removed"`
	Error   string `json:"error,omitempty"`
}

// Prune removes every stopped or failed box in repoID (all repos when it is
// ""). It never discards a box with unmerged work unless force is set, and
// reports what it did box by box.
func (m *Manager) Prune(ctx context.Context, repoID string, force bool) ([]PruneResult, error) {
	var repos []string
	if repoID != "" {
		repos = []string{repoID}
	} else {
		rs, err := m.d.Store.Repos()
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			repos = append(repos, r.ID)
		}
	}
	var out []PruneResult
	for _, rid := range repos {
		boxes, err := m.d.Store.ListBoxes(rid)
		if err != nil {
			return out, err
		}
		for _, b := range boxes {
			if !prunable(b.State) {
				continue
			}
			res := PruneResult{Repo: rid, Name: b.Name}
			if err := m.Remove(ctx, rid, b.Name, RemoveOptions{Force: force}); err != nil {
				res.Error = err.Error()
			} else {
				res.Removed = true
			}
			out = append(out, res)
		}
	}
	return out, nil
}

// DiskUsage is the size in bytes of the box's worktree on disk. It walks the
// directory, so it is computed on demand, never while listing boxes.
func (m *Manager) DiskUsage(repoID, name string) (int64, error) {
	b, err := m.get(repoID, name)
	if err != nil {
		return 0, err
	}
	var total int64
	err = filepath.WalkDir(b.Dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // a file vanished mid-walk (a dev server's temp)
			}
			return err
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				total += fi.Size()
			}
		}
		return nil
	})
	return total, err
}
