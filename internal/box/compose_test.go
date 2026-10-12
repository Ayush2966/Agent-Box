package box

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentbox-dev/agentbox/internal/compose"
	"github.com/agentbox-dev/agentbox/internal/errs"
	"github.com/agentbox-dev/agentbox/internal/state"
	"github.com/agentbox-dev/agentbox/internal/testutil"
)

const composeConfig = sqliteConfig + "compose:\n  services: [redis]\n  wait: 30s\n"

func newRecorder() *compose.Recorder {
	return &compose.Recorder{Stdout: map[string]string{"version": "Docker Compose version v2.29.0\n", "ls": "[]"}}
}

// newComposeManager is newTestManager with a recording docker.
func newComposeManager(t *testing.T, rec *compose.Recorder) *Manager {
	t.Helper()
	m := newTestManager(t)
	m.d.Compose = rec
	return m
}

// newComposeRepo commits a compose file (so it exists in every worktree) and
// leaves agentbox.yaml uncommitted like newSQLiteRepo does.
func newComposeRepo(t *testing.T, cfg string) string {
	t.Helper()
	repo := newSQLiteRepo(t, cfg)
	testutil.WriteFiles(t, repo, map[string]string{"compose.yaml": "services:\n  redis:\n    image: redis:7-alpine\n    ports: [\"${API_PORT}:6379\"]\n"})
	testutil.Git(t, repo, "add", "compose.yaml")
	testutil.Git(t, repo, "commit", "-q", "-m", "compose file")
	return repo
}

func TestComposeStepUpsStackAndSetsEnv(t *testing.T) {
	rec := newRecorder()
	m := newComposeManager(t, rec)
	repo := newComposeRepo(t, composeConfig)
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	project := "agentbox-" + b.RepoID + "-a"
	if b.Compose != project {
		t.Fatalf("state.Compose = %q, want %q", b.Compose, project)
	}
	verbs := rec.Verbs()
	if strings.Join(verbs, ",") != "version,up" {
		t.Fatalf("docker calls: %v", rec.Calls)
	}
	up := strings.Join(rec.Calls[1], " ")
	if !strings.Contains(up, "-p "+project+" -f compose.yaml up -d --wait --wait-timeout 30 redis") {
		t.Fatalf("up argv: %s", up)
	}
	if rec.Dirs[1] != b.Dir {
		t.Fatalf("up ran in %q, want the box dir %q", rec.Dirs[1], b.Dir)
	}
	if !contains(rec.Envs[1], "COMPOSE_PROJECT_NAME="+project) || !hasPrefix(rec.Envs[1], "API_PORT=") {
		t.Fatalf("up env lacks the box env: %v", rec.Envs[1])
	}
	env, _ := os.ReadFile(filepath.Join(b.Dir, ".env"))
	if !strings.Contains(string(env), "COMPOSE_PROJECT_NAME="+project) {
		t.Fatalf(".env lacks COMPOSE_PROJECT_NAME:\n%s", env)
	}
	if info, _ := m.Info(b.RepoID, "a"); info.Env["COMPOSE_PROJECT_NAME"] != project {
		t.Fatalf("Info env: %v", info.Env)
	}
	log, _ := m.LogTail(b.RepoID, "a", 50)
	if !strings.Contains(strings.Join(log, "\n"), "$ docker compose -p "+project) {
		t.Fatalf("box log lacks the echoed command: %v", log)
	}
}

func TestComposeUpFailureRollsBack(t *testing.T) {
	rec := newRecorder()
	rec.Fail = map[string]error{"up": errors.New("exit status 1")}
	rec.Out = "Error response from daemon: Bind for 0.0.0.0:6379 failed: port is already allocated\n"
	m := newComposeManager(t, rec)
	repo := newComposeRepo(t, composeConfig)
	_, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	e := errs.From(err)
	if e == nil || e.Step != StepCompose || !strings.Contains(e.Message, "port is already allocated") {
		t.Fatalf("err = %v", err)
	}
	if verbs := rec.Verbs(); verbs[len(verbs)-1] != "down" {
		t.Fatalf("the failed stack must be torn down: %v", verbs)
	}
	assertGone(t, m, repo, "a")
}

func TestComposeStepRollsBackOnLaterFailure(t *testing.T) {
	rec := newRecorder()
	m := newComposeManager(t, rec)
	repo := newComposeRepo(t, composeConfig)
	m.testHook = func(s string) error {
		if s == StepSetup {
			return errors.New("injected failure")
		}
		return nil
	}
	_, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	if e := errs.From(err); e == nil || e.Step != StepSetup {
		t.Fatalf("err = %v", err)
	}
	if verbs := rec.Verbs(); strings.Join(verbs, ",") != "version,up,down" {
		t.Fatalf("docker calls: %v", verbs)
	}
	assertGone(t, m, repo, "a")
}

func TestComposeStartStopRunTheStack(t *testing.T) {
	rec := newRecorder()
	m := newComposeManager(t, rec)
	repo := newComposeRepo(t, composeConfig)
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(ctx, b.RepoID, "a"); err != nil {
		t.Fatal(err)
	}
	if verbs := rec.Verbs(); strings.Join(verbs, ",") != "version,up,stop" {
		t.Fatalf("after stop: %v", verbs)
	}
	if got, _ := m.get(b.RepoID, "a"); got.State != state.Stopped {
		t.Fatalf("state %s", got.State)
	}
	if err := m.Start(ctx, b.RepoID, "a"); err != nil {
		t.Fatal(err)
	}
	if verbs := rec.Verbs(); strings.Join(verbs, ",") != "version,up,stop,up" {
		t.Fatalf("after start: %v", verbs)
	}
	if got, _ := m.get(b.RepoID, "a"); got.State != state.Running {
		t.Fatalf("state %s", got.State)
	}
	rec.Fail = map[string]error{"up": errors.New("exit status 1")}
	_ = m.Stop(ctx, b.RepoID, "a")
	err = m.Start(ctx, b.RepoID, "a")
	if e := errs.From(err); e == nil || e.Step != StepCompose {
		t.Fatalf("start with a failing stack must fail at the compose step: %v", err)
	}
	if got, _ := m.get(b.RepoID, "a"); got.State != state.Stopped {
		t.Fatalf("the dev server must not start when the stack fails: state %s", got.State)
	}
}

func TestRemoveDownsStackWithoutConfig(t *testing.T) {
	rec := newRecorder()
	m := newComposeManager(t, rec)
	repo := newComposeRepo(t, composeConfig)
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(repo, "agentbox.yaml")) // compose: gone from the config
	if err := m.Remove(ctx, b.RepoID, "a", RemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	verbs := rec.Verbs()
	if verbs[len(verbs)-1] != "down" || !strings.Contains(strings.Join(rec.Calls[len(rec.Calls)-1], " "), "-p agentbox-"+b.RepoID+"-a down -v") {
		t.Fatalf("rm must down the stack by project name: %v", rec.Calls)
	}
	assertGone(t, m, repo, "a")
}

func TestDoctorFindsOrphanComposeProject(t *testing.T) {
	rec := newRecorder()
	m := newComposeManager(t, rec)
	repo := newComposeRepo(t, composeConfig)
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "agentbox-" + b.RepoID + "-"
	rec.Stdout["ls"] = `[{"Name":"` + prefix + `a"},{"Name":"` + prefix + `ghost"},{"Name":"agentbox-other-000000-x"},{"Name":"unrelated"}]`
	fs, err := m.Doctor(ctx, b.RepoID, false)
	if err != nil {
		t.Fatal(err)
	}
	var orphan *Finding
	for i := range fs {
		if fs[i].Kind == "orphan_compose" {
			orphan = &fs[i]
		}
	}
	if orphan == nil || !strings.Contains(orphan.Detail, prefix+"ghost") || orphan.Fixed {
		t.Fatalf("findings %+v", fs)
	}
	if strings.Contains(strings.Join(kinds(fs), ","), "tools") {
		t.Fatalf("docker is present; no tools finding expected: %+v", fs)
	}
	fs, err = m.Doctor(ctx, b.RepoID, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.Kind == "orphan_compose" && !f.Fixed {
			t.Fatalf("fix did not down the orphan: %+v", f)
		}
	}
	last := strings.Join(rec.Calls[len(rec.Calls)-1], " ")
	if last != "compose -p "+prefix+"ghost down -v --remove-orphans" {
		t.Fatalf("last docker call: %s", last)
	}
}

func TestComposeMissingDockerHint(t *testing.T) {
	rec := &compose.Recorder{Fail: map[string]error{"version": errors.New(`exec: "docker": executable file not found in $PATH`)}}
	m := newComposeManager(t, rec)
	repo := newComposeRepo(t, composeConfig)
	_, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	e := errs.From(err)
	if e == nil || e.Step != StepCompose || e.Code != errs.Precondition || !strings.Contains(e.Hint, "Docker") {
		t.Fatalf("err = %v", err)
	}
	assertGone(t, m, repo, "a")
}

func TestDoctorReportsMissingDockerOnlyWhenConfigured(t *testing.T) {
	rec := &compose.Recorder{Fail: map[string]error{"version": errors.New("not found")}}
	m := newComposeManager(t, rec)
	plain := newSQLiteRepo(t, sqliteConfig)
	r, _ := m.ResolveRepo(plain)
	fs, _ := m.Doctor(ctx, r.ID, false)
	if strings.Contains(strings.Join(kinds(fs), ","), "tools") {
		t.Fatalf("no compose configured, but doctor complained about docker: %+v", fs)
	}
	withCompose := newComposeRepo(t, composeConfig)
	r2, _ := m.ResolveRepo(withCompose)
	fs, _ = m.Doctor(ctx, r2.ID, false)
	found := false
	for _, f := range fs {
		if f.Kind == "tools" && strings.Contains(f.Detail, "docker") {
			found = true
		}
	}
	if !found {
		t.Fatalf("compose configured and docker missing: want a tools finding, got %+v", fs)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func hasPrefix(ss []string, p string) bool {
	for _, x := range ss {
		if strings.HasPrefix(x, p) {
			return true
		}
	}
	return false
}
