package box

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agentbox-dev/agentbox/internal/errs"
	"github.com/agentbox-dev/agentbox/internal/proc"
	"github.com/agentbox-dev/agentbox/internal/state"
	"github.com/agentbox-dev/agentbox/internal/testutil"
	"github.com/agentbox-dev/agentbox/internal/worktree"
)

var ctx = context.Background()

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	m := NewManager(Deps{
		Store: store, BoxesDir: t.TempDir(), LogsDir: t.TempDir(), PortLo: 21000, PortHi: 21999,
		ProcOptions: proc.Options{Backoff: []time.Duration{10 * time.Millisecond}, CrashWindow: 5 * time.Second, MaxCrashes: 3, StopTimeout: 2 * time.Second},
	})
	t.Cleanup(m.Shutdown)
	return m
}

const sqliteConfig = `version: 1
setup: echo setting up
dev: sleep 30
ports: [APP_PORT, API_PORT]
db:
  driver: sqlite
  url_env: DATABASE_PATH
  source: dev.db
env:
  template: .env.example
`

func seedSQLite(t *testing.T, path string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	c, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, q := range []string{`CREATE TABLE items(name TEXT)`, `INSERT INTO items VALUES('one')`} {
		if _, err := c.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

// newSQLiteRepo leaves agentbox.yaml uncommitted on purpose: config must be
// read from the main checkout, not from the (committed-only) worktree.
func newSQLiteRepo(t *testing.T, config string) string {
	t.Helper()
	repo := testutil.NewRepo(t, map[string]string{
		".env.example": "# app\nAPP_PORT=3000\nDATABASE_PATH=dev.db\nSECRET=keep-me\n",
		".gitignore":   ".env\ndev.db\n",
	})
	testutil.WriteFiles(t, repo, map[string]string{"agentbox.yaml": config})
	seedSQLite(t, filepath.Join(repo, "dev.db"))
	return repo
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

func assertGone(t *testing.T, m *Manager, repo, name string) {
	t.Helper()
	r, err := m.ResolveRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.d.Store.GetBox(r.ID, name); !errors.Is(err, state.ErrNotFound) {
		t.Errorf("box row still exists (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(m.d.BoxesDir, r.ID, name)); !os.IsNotExist(err) {
		t.Errorf("box dir still exists")
	}
	if worktree.BranchExists(repo, BranchPrefix+name) {
		t.Errorf("branch still exists")
	}
	rows, _ := m.d.Store.AllPorts()
	for _, p := range rows {
		if p.RepoID == r.ID && p.Box == name {
			t.Errorf("port %d still reserved", p.Port)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.procs[r.ID+"/"+name]; ok {
		t.Errorf("process still tracked")
	}
}

func TestNewCreatesBox(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig)
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	if b.State != state.Ready {
		t.Fatalf("state %s", b.State)
	}
	if cur, _ := worktree.CurrentBranch(b.Dir); cur != "agentbox/a" {
		t.Fatalf("branch %q", cur)
	}
	app, api := b.Ports["APP_PORT"], b.Ports["API_PORT"]
	if app < 21000 || app > 21999 || api < 21000 || api == app {
		t.Fatalf("ports %v", b.Ports)
	}
	env, err := os.ReadFile(filepath.Join(b.Dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# app\n", "APP_PORT=" + strconv.Itoa(app) + "\n", "SECRET=keep-me\n",
		"DATABASE_PATH=dev.db\n", "AGENTBOX_NAME=a\n", "API_PORT=" + strconv.Itoa(api) + "\n"} {
		if !strings.Contains(string(env), want) {
			t.Errorf(".env missing %q:\n%s", want, env)
		}
	}
	c, _ := sql.Open("sqlite", filepath.Join(b.Dir, "dev.db"))
	var n int
	if err := c.QueryRow(`SELECT count(*) FROM items`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("box db rows = %d, err = %v", n, err)
	}
	c.Close()
	lines, _ := proc.TailFile(m.logPath(b), 10)
	if !strings.Contains(strings.Join(lines, "\n"), "setting up") {
		t.Fatalf("setup output not logged: %v", lines)
	}
}

func TestNewRunsDevServer(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig)
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if b.State != state.Running || !alive(b.PID) {
		t.Fatalf("box %+v", b)
	}
	if stored, _ := m.d.Store.GetBox(b.RepoID, "a"); stored.State != state.Running {
		t.Fatalf("stored state %s", stored.State)
	}
}

func TestNewFromInsideBoxDir(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig)
	a, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.New(ctx, NewOptions{RepoPath: a.Dir, Name: "b", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	if b.RepoID != a.RepoID {
		t.Fatalf("box created from inside a box got repo %q, want %q", b.RepoID, a.RepoID)
	}
}

func TestNewRollsBackEachStep(t *testing.T) {
	for _, step := range []string{StepWorktree, StepPorts, StepDB, StepEnv, StepSetup, StepDev} {
		t.Run(step, func(t *testing.T) {
			m := newTestManager(t)
			repo := newSQLiteRepo(t, sqliteConfig)
			m.testHook = func(s string) error {
				if s == step {
					return errors.New("injected failure")
				}
				return nil
			}
			_, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a"})
			if e := errs.From(err); e.Step != step || !strings.Contains(e.Message, "injected failure") {
				t.Fatalf("err = %v", err)
			}
			assertGone(t, m, repo, "a")
		})
	}
}

func TestKeepFailedKeepsResourcesUntilRemoved(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig)
	m.testHook = func(s string) error {
		if s == StepSetup {
			return errors.New("injected")
		}
		return nil
	}
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", KeepFailed: true})
	if err == nil {
		t.Fatal("expected failure")
	}
	stored, _ := m.d.Store.GetBox(b.RepoID, "a")
	if stored.State != state.Failed || stored.LastStep != StepSetup || !strings.Contains(stored.LastError, "injected") {
		t.Fatalf("stored %+v", stored)
	}
	if _, err := os.Stat(b.Dir); err != nil {
		t.Fatal("keep-failed removed the box dir")
	}
	m.testHook = nil
	if err := m.Remove(ctx, b.RepoID, "a", RemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	assertGone(t, m, repo, "a")
}

func TestSetupFailureIncludesOutput(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, strings.Replace(sqliteConfig, "setup: echo setting up", "setup: echo boom-output; exit 3", 1))
	_, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a"})
	if e := errs.From(err); e.Step != StepSetup || !strings.Contains(e.Message, "boom-output") {
		t.Fatalf("err = %v", err)
	}
	assertGone(t, m, repo, "a")
}

func TestValidate(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig)
	if _, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "taken", NoDev: true}); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, repo, "branch", "agentbox/leftover")
	missingTmpl := testutil.NewRepo(t, map[string]string{"agentbox.yaml": "version: 1\nenv:\n  template: missing.env\n"})

	cases := []struct {
		name string
		opts NewOptions
		code errs.Code
		want string
	}{
		{"bad name", NewOptions{RepoPath: repo, Name: "Bad_Name"}, errs.Invalid, "invalid box name"},
		{"duplicate", NewOptions{RepoPath: repo, Name: "taken"}, errs.Exists, `box "taken" already exists`},
		{"leftover branch", NewOptions{RepoPath: repo, Name: "leftover"}, errs.Exists, "git branch -D agentbox/leftover"},
		{"not a repo", NewOptions{RepoPath: t.TempDir(), Name: "x"}, errs.Invalid, "not inside a git repository"},
		{"missing template", NewOptions{RepoPath: missingTmpl, Name: "x"}, errs.Invalid, "env.template missing.env not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := m.New(ctx, tc.opts)
			if e := errs.From(err); e.Code != tc.code || !strings.Contains(e.Error(), tc.want) {
				t.Fatalf("err = %v (code %s), want code %s containing %q", err, e.Code, tc.code, tc.want)
			}
		})
	}
	r, _ := m.ResolveRepo(repo)
	if _, err := os.Stat(filepath.Join(m.d.BoxesDir, r.ID, "leftover")); !os.IsNotExist(err) {
		t.Error("a box dir was created despite the leftover branch")
	}
}

func TestRepoPathWithSpaces(t *testing.T) {
	m := newTestManager(t)
	m.d.BoxesDir = filepath.Join(t.TempDir(), "boxes dir")
	repo := testutil.NewRepoIn(t, filepath.Join(t.TempDir(), "my project"), map[string]string{".env.example": "APP_PORT=1\n"})
	testutil.WriteFiles(t, repo, map[string]string{"agentbox.yaml": "version: 1\nsetup: echo \"$AGENTBOX_ROOT\" > root.txt\ndev: sleep 30\nports: [APP_PORT]\nenv:\n  template: .env.example\n"})
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if b.State != state.Running {
		t.Fatalf("state %s", b.State)
	}
	root, _ := os.ReadFile(filepath.Join(b.Dir, "root.txt"))
	if strings.TrimSpace(string(root)) != b.Dir {
		t.Fatalf("AGENTBOX_ROOT = %q, want %q", root, b.Dir)
	}
	if err := m.Remove(ctx, b.RepoID, "a", RemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveStopsDevAndAllowsNameReuse(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig)
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(ctx, b.RepoID, "a", RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	assertGone(t, m, repo, "a")
	eventually(t, "dev server to exit", func() bool { return !alive(b.PID) })
	if _, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true}); err != nil {
		t.Fatalf("name not reusable after rm: %v", err)
	}
}

func TestRemoveRefusesUnmergedWork(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig)
	b, _ := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})

	testutil.WriteFiles(t, b.Dir, map[string]string{"work.txt": "x\n"})
	err := m.Remove(ctx, b.RepoID, "a", RemoveOptions{})
	if e := errs.From(err); e.Code != errs.UnmergedWork || !strings.Contains(e.Message, "uncommitted") {
		t.Fatalf("err = %v", err)
	}
	testutil.Git(t, b.Dir, "add", "-A")
	testutil.Git(t, b.Dir, "commit", "-qm", "work")
	err = m.Remove(ctx, b.RepoID, "a", RemoveOptions{})
	if e := errs.From(err); e.Code != errs.UnmergedWork || !strings.Contains(e.Message, "not merged into main") {
		t.Fatalf("err = %v", err)
	}
	if err := m.Remove(ctx, b.RepoID, "a", RemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	assertGone(t, m, repo, "a")
}

func TestRemoveKeepBranch(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig)
	b, _ := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	if err := m.Remove(ctx, b.RepoID, "a", RemoveOptions{KeepBranch: true}); err != nil {
		t.Fatal(err)
	}
	if !worktree.BranchExists(repo, "agentbox/a") {
		t.Fatal("branch deleted despite KeepBranch")
	}
}

func TestGeneratedFilesAreNotUserChanges(t *testing.T) {
	m := newTestManager(t)
	repo := testutil.NewRepo(t, map[string]string{".env.example": "APP_PORT=1\n"}) // no .gitignore on purpose
	testutil.WriteFiles(t, repo, map[string]string{
		"agentbox.yaml":     "version: 1\nports: [APP_PORT]\ncopy: [config/local.json]\ndb:\n  driver: sqlite\n  url_env: DB\n  source: data/dev.db\nenv:\n  template: .env.example\n",
		"config/local.json": "{}\n",
	})
	seedSQLite(t, filepath.Join(repo, "data", "dev.db"))
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if ch, _ := worktree.Changes(b.Dir, true); len(ch) == 0 {
		t.Fatal("test setup: generated files should show as untracked")
	}
	if err := m.Remove(ctx, b.RepoID, "a", RemoveOptions{}); err != nil {
		t.Fatalf("generated files blocked rm: %v", err)
	}
}

// git worktree add creates the branch before the directory, so a failure
// inside Add must still roll the branch back — otherwise the name is burnt
// and every later `new <name>` fails with "branch already exists".
func TestNewRollsBackBranchWhenWorktreeAddFails(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig)

	// Make the boxes parent unwritable so `git worktree add` fails after it
	// has already created the branch.
	parent := filepath.Join(m.d.BoxesDir, state.RepoSlug(repo))
	if err := os.MkdirAll(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o755) })

	if _, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true}); err == nil {
		t.Fatal("expected the worktree step to fail")
	}
	if worktree.BranchExists(repo, BranchPrefix+"a") {
		t.Fatal("branch agentbox/a survived a failed worktree step")
	}
	assertGone(t, m, repo, "a")

	// The name must be reusable once the parent is writable again.
	os.Chmod(parent, 0o755)
	if _, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true}); err != nil {
		t.Fatalf("name not reusable after a failed create: %v", err)
	}
}

// Which files agentbox generated is a fact about the box, not about whatever
// agentbox.yaml happens to say later. Editing (or breaking) the config must
// not reclassify agentbox's own .env, copy: files and SQLite copy as the
// agent's uncommitted work and block rm/merge.
func TestGeneratedFilesStayManagedAfterConfigChanges(t *testing.T) {
	cfg := "version: 1\nports: [APP_PORT]\ncopy: [config/local.json]\ndb:\n  driver: sqlite\n  url_env: DB\n  source: data/dev.db\nenv:\n  template: .env.example\n"
	for _, tc := range []struct{ name, after string }{
		{"env.file renamed", strings.Replace(cfg, "  template: .env.example\n", "  template: .env.example\n  file: .env.development\n", 1)},
		{"config no longer parses", "version: 1\nenv:\n  tmplate: .env.example\n"},
		{"config deleted", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestManager(t)
			repo := testutil.NewRepo(t, map[string]string{".env.example": "APP_PORT=1\n"}) // no .gitignore on purpose
			testutil.WriteFiles(t, repo, map[string]string{"agentbox.yaml": cfg, "config/local.json": "{}\n"})
			seedSQLite(t, filepath.Join(repo, "data", "dev.db"))
			b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
			if err != nil {
				t.Fatal(err)
			}
			if info, err := m.Info(b.RepoID, "a"); err != nil || info.Dirty != 0 {
				t.Fatalf("before the config change: Dirty = %d, err = %v", info.Dirty, err)
			}

			if tc.after == "" {
				os.Remove(filepath.Join(repo, "agentbox.yaml"))
			} else {
				testutil.WriteFiles(t, repo, map[string]string{"agentbox.yaml": tc.after})
			}

			if info, err := m.Info(b.RepoID, "a"); err != nil || info.Dirty != 0 {
				t.Errorf("after the config change: Dirty = %d, want 0 (err %v)", info.Dirty, err)
			}
			// rm must not be REFUSED for unmerged work. (A broken config can
			// still produce a post-removal warning about the database; that is
			// a separate behaviour and the box is gone either way.)
			if err := m.Remove(ctx, b.RepoID, "a", RemoveOptions{}); err != nil && errs.From(err).Code == errs.UnmergedWork {
				t.Fatalf("generated files blocked rm after the config change: %v", err)
			}
			assertGone(t, m, repo, "a")
		})
	}
}
