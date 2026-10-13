package box

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Ayush2966/Agent-Box/internal/errs"
	"github.com/Ayush2966/Agent-Box/internal/state"
	"github.com/Ayush2966/Agent-Box/internal/testutil"
	"github.com/Ayush2966/Agent-Box/internal/worktree"
)

const plainConfig = "version: 1\nports: [APP_PORT]\ndev: echo started; sleep 30\n"

func newPlainRepo(t *testing.T, config string) string {
	t.Helper()
	repo := testutil.NewRepo(t, map[string]string{"README.md": "base\n", ".gitignore": ".env\n"})
	testutil.WriteFiles(t, repo, map[string]string{"agentbox.yaml": config})
	return repo
}

func commitIn(t *testing.T, dir string, files map[string]string, msg string) {
	t.Helper()
	testutil.WriteFiles(t, dir, files)
	testutil.Git(t, dir, "add", "-A")
	testutil.Git(t, dir, "commit", "-qm", msg)
}

func TestStartStop(t *testing.T) {
	m := newTestManager(t)
	b, err := m.New(ctx, NewOptions{RepoPath: newPlainRepo(t, plainConfig), Name: "a", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx, b.RepoID, "a"); err != nil {
		t.Fatal(err)
	}
	info, _ := m.Info(b.RepoID, "a")
	if info.State != state.Running || !alive(info.PID) {
		t.Fatalf("after start: %+v", info.Box)
	}
	pid := info.PID
	eventually(t, "log output", func() bool {
		lines, _ := m.LogTail(b.RepoID, "a", 10)
		return strings.Contains(strings.Join(lines, "\n"), "started")
	})
	if err := m.Stop(ctx, b.RepoID, "a"); err != nil {
		t.Fatal(err)
	}
	info, _ = m.Info(b.RepoID, "a")
	if info.State != state.Stopped || info.PID != 0 {
		t.Fatalf("after stop: %+v", info.Box)
	}
	eventually(t, "process exit", func() bool { return !alive(pid) })
	if err := m.Stop(ctx, b.RepoID, "a"); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if err := m.Start(ctx, b.RepoID, "a"); err != nil {
		t.Fatalf("restart from stopped: %v", err)
	}
}

func TestStartWithoutDevCommand(t *testing.T) {
	m := newTestManager(t)
	b, _ := m.New(ctx, NewOptions{RepoPath: newPlainRepo(t, "version: 1\n"), Name: "a"})
	err := m.Start(ctx, b.RepoID, "a")
	if e := errs.From(err); e.Code != errs.Precondition || !strings.Contains(e.Message, "no dev command") {
		t.Fatalf("err = %v", err)
	}
}

func TestCrashLoopMarksBoxStopped(t *testing.T) {
	m := newTestManager(t)
	b, err := m.New(ctx, NewOptions{RepoPath: newPlainRepo(t, "version: 1\ndev: exit 1\n"), Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "box to be stopped", func() bool {
		info, _ := m.Info(b.RepoID, "a")
		return info.State == state.Stopped && strings.Contains(info.LastError, "crashed")
	})
}

func TestInfoReportsGitAndEnv(t *testing.T) {
	m := newTestManager(t)
	repo := newPlainRepo(t, plainConfig)
	b, _ := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	commitIn(t, b.Dir, map[string]string{"feature.txt": "f\n"}, "feature")
	testutil.WriteFiles(t, b.Dir, map[string]string{"wip.txt": "w\n"})

	info, err := m.Info(b.RepoID, "a")
	if err != nil {
		t.Fatal(err)
	}
	if info.RepoPath != repo || info.Base != "main" || info.Ahead != 1 || info.Behind != 0 || info.Dirty != 1 {
		t.Fatalf("info %+v", info)
	}
	if info.Env["APP_PORT"] != strconv.Itoa(b.Ports["APP_PORT"]) || info.Env["AGENTBOX_NAME"] != "a" {
		t.Fatalf("env %v", info.Env)
	}
	d, err := m.Diff(b.RepoID, "a", "")
	if err != nil || !strings.Contains(d, "feature.txt") {
		t.Fatalf("diff %q, %v", d, err)
	}
	list, _ := m.List(b.RepoID)
	if len(list) != 1 || list[0].Name != "a" {
		t.Fatalf("list %v", list)
	}
}

func TestMergeThenRemoveWithoutForce(t *testing.T) {
	for _, squash := range []bool{false, true} {
		m := newTestManager(t)
		repo := newPlainRepo(t, plainConfig)
		b, _ := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
		commitIn(t, b.Dir, map[string]string{"feature.txt": "f\n"}, "feature")

		into, err := m.Merge(ctx, b.RepoID, "a", MergeOptions{Squash: squash})
		if err != nil || into != "main" {
			t.Fatalf("squash=%v: into=%q err=%v", squash, into, err)
		}
		if _, err := os.Stat(filepath.Join(repo, "feature.txt")); err != nil {
			t.Fatalf("squash=%v: merged file missing", squash)
		}
		if squash {
			if msg := testutil.Git(t, repo, "log", "-1", "--format=%s"); msg != "agentbox: merge box a" {
				t.Fatalf("squash commit message %q", msg)
			}
		}
		if err := m.Remove(ctx, b.RepoID, "a", RemoveOptions{}); err != nil {
			t.Fatalf("squash=%v: rm after merge refused: %v", squash, err)
		}
	}
}

func TestMergePreconditions(t *testing.T) {
	m := newTestManager(t)
	repo := newPlainRepo(t, plainConfig)
	b, _ := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})

	testutil.WriteFiles(t, b.Dir, map[string]string{"wip.txt": "w\n"})
	_, err := m.Merge(ctx, b.RepoID, "a", MergeOptions{})
	if e := errs.From(err); e.Code != errs.Precondition || !strings.Contains(e.Message, "uncommitted") {
		t.Fatalf("dirty box: %v", err)
	}
	testutil.Git(t, b.Dir, "add", "-A")
	testutil.Git(t, b.Dir, "commit", "-qm", "wip")

	testutil.Git(t, repo, "switch", "-q", "-c", "other")
	_, err = m.Merge(ctx, b.RepoID, "a", MergeOptions{Into: "main"})
	if e := errs.From(err); e.Code != errs.Precondition || !strings.Contains(e.Message, `on "other"`) {
		t.Fatalf("wrong branch: %v", err)
	}
	testutil.Git(t, repo, "switch", "-q", "main")

	testutil.WriteFiles(t, repo, map[string]string{"README.md": "dirty\n"})
	_, err = m.Merge(ctx, b.RepoID, "a", MergeOptions{})
	if e := errs.From(err); e.Code != errs.Precondition || !strings.Contains(e.Message, "main checkout has uncommitted changes") {
		t.Fatalf("dirty main: %v", err)
	}
}

func TestMergeConflictLeavesMainClean(t *testing.T) {
	m := newTestManager(t)
	repo := newPlainRepo(t, plainConfig)
	b, _ := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	commitIn(t, b.Dir, map[string]string{"README.md": "box\n"}, "box edit")
	commitIn(t, repo, map[string]string{"README.md": "main\n"}, "main edit")

	_, err := m.Merge(ctx, b.RepoID, "a", MergeOptions{})
	e := errs.From(err)
	if e.Code != errs.Conflict || !strings.Contains(e.Message, "README.md") || !strings.Contains(e.Hint, "rebase") {
		t.Fatalf("err = %v", err)
	}
	if ch, _ := worktree.Changes(repo, false); len(ch) != 0 {
		t.Fatalf("main left dirty: %v", ch)
	}
}

func TestRecoverAfterDaemonRestart(t *testing.T) {
	m1 := newTestManager(t)
	b, err := m1.New(ctx, NewOptions{RepoPath: newPlainRepo(t, plainConfig), Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	m1.stopProc(b.ID()) // the old daemon died; state still says running
	stuck := state.Box{RepoID: b.RepoID, Name: "half", Dir: "/nowhere", Branch: "agentbox/half", State: state.Creating, CreatedAt: time.Now()}
	if err := m1.d.Store.InsertBox(stuck); err != nil {
		t.Fatal(err)
	}

	m2 := NewManager(m1.d)
	t.Cleanup(m2.Shutdown)
	if err := m2.Recover(); err != nil {
		t.Fatal(err)
	}
	if info, _ := m2.Info(b.RepoID, "a"); info.State != state.Stopped || !strings.Contains(info.LastError, "restarted") {
		t.Fatalf("running box after recover: %+v", info.Box)
	}
	if info, _ := m2.Info(b.RepoID, "half"); info.State != state.Failed {
		t.Fatalf("creating box after recover: %+v", info.Box)
	}
}

func TestInfoRunsNoGitWhileCreatingOrRemoving(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig)
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	testutil.WriteFiles(t, b.Dir, map[string]string{"scratch.txt": "dirty\n"})
	if got := m.info(b); got.Dirty != 1 || got.Base == "" {
		t.Fatalf("a ready box is inspected with git: %+v", got)
	}
	for _, st := range []state.BoxState{state.Creating, state.Removing} {
		b.State = st
		if got := m.info(b); got.Dirty != 0 || got.Ahead != 0 || got.Behind != 0 || got.Base != "" {
			t.Fatalf("state %s: git ran in a worktree that is being made or torn down: %+v", st, got)
		}
	}
}
