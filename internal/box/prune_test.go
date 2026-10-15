package box

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Ayush2966/Agent-Box/internal/state"
)

func TestPruneRemovesStoppedAndFailedOnly(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig)
	// a: ready (--no-dev), keep. b: stopped, prune. c: failed, prune.
	a, _ := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	b, _ := m.New(ctx, NewOptions{RepoPath: repo, Name: "b", NoDev: true})
	c, _ := m.New(ctx, NewOptions{RepoPath: repo, Name: "c", NoDev: true})
	for _, x := range []struct {
		box state.Box
		st  state.BoxState
	}{{b, state.Stopped}, {c, state.Failed}} {
		row, _ := m.d.Store.GetBox(x.box.RepoID, x.box.Name)
		row.State = x.st
		m.d.Store.UpdateBox(row)
	}
	res, err := m.Prune(ctx, a.RepoID, false)
	if err != nil {
		t.Fatal(err)
	}
	removed := map[string]bool{}
	for _, r := range res {
		if r.Removed {
			removed[r.Name] = true
		}
	}
	if !removed["b"] || !removed["c"] || removed["a"] {
		t.Fatalf("pruned %v, want b and c only", removed)
	}
	assertGone(t, m, repo, "b")
	assertGone(t, m, repo, "c")
	if _, err := m.d.Store.GetBox(a.RepoID, "a"); err != nil {
		t.Fatal("ready box a must survive prune")
	}
}

func TestDiskUsageCountsTheWorktree(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig)
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	before, err := m.DiskUsage(b.RepoID, "a")
	if err != nil || before == 0 {
		t.Fatalf("usage %d %v", before, err)
	}
	if err := os.WriteFile(filepath.Join(b.Dir, "big.bin"), make([]byte, 100_000), 0o600); err != nil {
		t.Fatal(err)
	}
	after, _ := m.DiskUsage(b.RepoID, "a")
	if after-before < 100_000 {
		t.Fatalf("usage did not grow by the file: %d → %d", before, after)
	}
}
