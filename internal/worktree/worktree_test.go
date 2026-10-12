package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agentbox-dev/agentbox/internal/testutil"
)

func addBox(t *testing.T, repo, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := Add(repo, dir, "agentbox/"+name, ""); err != nil {
		t.Fatal(err)
	}
	return dir
}

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	testutil.Git(t, dir, "add", "-A")
	testutil.Git(t, dir, "commit", "-qm", msg)
}

func TestMainWorktreeFromInsideWorktree(t *testing.T) {
	repo := testutil.NewRepo(t, map[string]string{"sub/x.txt": "x\n"})
	for _, dir := range []string{repo, filepath.Join(repo, "sub")} {
		if got, err := MainWorktree(dir); err != nil || got != repo {
			t.Fatalf("MainWorktree(%s) = %q, %v; want %q", dir, got, err, repo)
		}
	}
	wt := addBox(t, repo, "a")
	if got, err := MainWorktree(filepath.Join(wt, "sub")); err != nil || got != repo {
		t.Fatalf("from worktree: %q, %v; want %q", got, err, repo)
	}
}

func TestMainWorktreeNotARepo(t *testing.T) {
	_, err := MainWorktree(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not inside a git repository") {
		t.Fatalf("err = %v", err)
	}
}

func TestAddRemoveDeleteBranch(t *testing.T) {
	repo := testutil.NewRepo(t, map[string]string{"a.txt": "1\n"})
	wt := addBox(t, repo, "a")
	if b, _ := CurrentBranch(wt); b != "agentbox/a" {
		t.Fatalf("branch = %q", b)
	}
	if !BranchExists(repo, "agentbox/a") {
		t.Fatal("branch missing")
	}
	if err := Add(repo, filepath.Join(t.TempDir(), "again"), "agentbox/a", ""); !errors.Is(err, ErrBranchExists) {
		t.Fatalf("err = %v, want ErrBranchExists", err)
	}
	if err := Remove(repo, wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("worktree dir still exists")
	}
	if err := DeleteBranch(repo, "agentbox/a"); err != nil || BranchExists(repo, "agentbox/a") {
		t.Fatalf("delete branch: %v", err)
	}
	if wts, _ := List(repo); len(wts) != 1 {
		t.Fatalf("worktrees left: %v", wts)
	}
}

func TestRemoveAlreadyDeletedDir(t *testing.T) {
	repo := testutil.NewRepo(t, nil)
	wt := addBox(t, repo, "a")
	os.RemoveAll(wt)
	if err := Remove(repo, wt); err != nil {
		t.Fatalf("Remove on missing dir: %v", err)
	}
	if wts, _ := List(repo); len(wts) != 1 {
		t.Fatalf("stale worktree not pruned: %v", wts)
	}
}

func TestChangesPathWithSpace(t *testing.T) {
	repo := testutil.NewRepoIn(t, filepath.Join(t.TempDir(), "my repo"), map[string]string{"a.txt": "1\n", "old.txt": "o\n"})
	testutil.WriteFiles(t, repo, map[string]string{"a.txt": "2\n", "new dir/b c.txt": "x\n"})
	testutil.Git(t, repo, "mv", "old.txt", "renamed.txt")

	got, err := Changes(repo, true)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if want := []string{"a.txt", "new dir/b c.txt", "renamed.txt"}; !slices.Equal(got, want) {
		t.Fatalf("Changes(untracked) = %q, want %q", got, want)
	}
	got, _ = Changes(repo, false)
	slices.Sort(got)
	if want := []string{"a.txt", "renamed.txt"}; !slices.Equal(got, want) {
		t.Fatalf("Changes(tracked only) = %q, want %q", got, want)
	}
}

func TestAheadBehindAndDiff(t *testing.T) {
	repo := testutil.NewRepo(t, map[string]string{"a.txt": "1\n"})
	wt := addBox(t, repo, "a")
	testutil.WriteFiles(t, wt, map[string]string{"b.txt": "b\n"})
	commitAll(t, wt, "add b")
	testutil.WriteFiles(t, repo, map[string]string{"main.txt": "m\n"})
	commitAll(t, repo, "main moves on")

	ahead, behind, err := AheadBehind(repo, "main", "agentbox/a")
	if err != nil || ahead != 1 || behind != 1 {
		t.Fatalf("ahead=%d behind=%d err=%v", ahead, behind, err)
	}
	testutil.WriteFiles(t, wt, map[string]string{"a.txt": "changed\n"})
	d, err := Diff(wt, "main")
	if err != nil || !strings.Contains(d, "b.txt") || !strings.Contains(d, "+changed") || strings.Contains(d, "main.txt") {
		t.Fatalf("diff = %q, err = %v", d, err)
	}
}

func TestMergeNormalAndSquash(t *testing.T) {
	repo := testutil.NewRepo(t, map[string]string{"a.txt": "1\n"})
	a := addBox(t, repo, "a")
	testutil.WriteFiles(t, a, map[string]string{"from-a.txt": "a\n"})
	commitAll(t, a, "a work")
	if err := Merge(repo, "agentbox/a", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "from-a.txt")); err != nil {
		t.Fatal("normal merge did not bring the file")
	}

	b := addBox(t, repo, "b")
	testutil.WriteFiles(t, b, map[string]string{"from-b.txt": "b\n"})
	commitAll(t, b, "b work")
	if err := Merge(repo, "agentbox/b", true, "squash b"); err != nil {
		t.Fatal(err)
	}
	if msg := testutil.Git(t, repo, "log", "-1", "--format=%s"); msg != "squash b" {
		t.Fatalf("last commit %q", msg)
	}
}

func TestMergeConflictAborts(t *testing.T) {
	for _, squash := range []bool{false, true} {
		repo := testutil.NewRepo(t, map[string]string{"a.txt": "base\n"})
		wt := addBox(t, repo, "a")
		testutil.WriteFiles(t, wt, map[string]string{"a.txt": "box\n"})
		commitAll(t, wt, "box edit")
		testutil.WriteFiles(t, repo, map[string]string{"a.txt": "main\n"})
		commitAll(t, repo, "main edit")

		err := Merge(repo, "agentbox/a", squash, "m")
		var ce *ConflictError
		if !errors.As(err, &ce) || !slices.Equal(ce.Files, []string{"a.txt"}) {
			t.Fatalf("squash=%v: err = %v", squash, err)
		}
		if ch, _ := Changes(repo, true); len(ch) != 0 {
			t.Fatalf("squash=%v: repo left dirty: %v", squash, ch)
		}
		if _, err := git(repo, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
			t.Fatalf("squash=%v: MERGE_HEAD still present", squash)
		}
	}
}

func TestMergeIsNoopAfterSquash(t *testing.T) {
	repo := testutil.NewRepo(t, map[string]string{"a.txt": "1\n"})
	b := addBox(t, repo, "b")
	testutil.WriteFiles(t, b, map[string]string{"from-b.txt": "b\n"})
	commitAll(t, b, "b work")
	if MergeIsNoop(repo, "main", "agentbox/b") {
		t.Fatal("unmerged branch reported as merged")
	}
	if err := Merge(repo, "agentbox/b", true, "squash b"); err != nil {
		t.Fatal(err)
	}
	if !MergeIsNoop(repo, "main", "agentbox/b") {
		t.Fatal("squash-merged branch not recognised as merged")
	}
}

func TestListBranches(t *testing.T) {
	repo := testutil.NewRepo(t, nil)
	addBox(t, repo, "a")
	testutil.Git(t, repo, "branch", "other")
	got, err := ListBranches(repo, "agentbox/")
	if err != nil || !slices.Equal(got, []string{"agentbox/a"}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestOptionLikeRefsAreRejected(t *testing.T) {
	repo := testutil.NewRepo(t, map[string]string{"a": "1\n"})
	dir := filepath.Join(t.TempDir(), "wt")
	if err := Add(repo, dir, "agentbox/x", "--force"); err == nil {
		t.Fatal("Add accepted --force as a ref")
	}
	if BranchExists(repo, "agentbox/x") {
		t.Error("a branch was created from an option-like ref")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("a worktree was created from an option-like ref")
	}
	if _, err := Diff(repo, "--no-index"); err == nil {
		t.Error("Diff accepted an option as its base")
	}
	if _, _, err := AheadBehind(repo, "--all", "HEAD"); err == nil {
		t.Error("AheadBehind accepted an option as its base")
	}
	if err := Add(repo, dir, "agentbox/y", "HEAD"); err != nil {
		t.Fatalf("a real ref must still work: %v", err)
	}
}
