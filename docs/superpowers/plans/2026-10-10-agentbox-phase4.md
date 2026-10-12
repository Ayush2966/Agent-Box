# agentbox Phase 4 — Docker Compose services and MySQL driver Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `compose:` in `agentbox.yaml` gives every box its own Docker Compose project (containers, volumes, host ports) that lives and dies with the box; `db.driver: mysql` clones the source database per box like the Postgres driver does.

**Architecture:** A new `internal/compose` package wraps the `docker compose` CLI behind an `Executor` interface (real `Docker{}` or a recording fake), builds one `Stack` per box (`agentbox-<repo-slug>-<box>`, run from the box dir, host ports via `${VAR}` interpolation from the box env file), and lists/removes orphan projects. `box.Manager` gets a `compose` step between env and setup (with `down -v` as its undo) and runs the stack on start/stop/rm/doctor. `internal/db/mysql.go` implements the existing `Driver` interface with `mysqldump | mysql` per clone (no template; credentials via a temporary `--defaults-extra-file`).

**Tech Stack:** Go 1.24 (go directive stays `1.24.2`), `github.com/go-sql-driver/mysql v1.9.3` (pinned; the only new dependency), the `docker` CLI with the compose plugin at runtime, `mysql`/`mysqldump` at runtime.

**Spec:** `docs/superpowers/specs/2026-10-10-agentbox-phase4-design.md` — read it first.

## Global Constraints

- Dependencies only via `go get github.com/go-sql-driver/mysql@v1.9.3`. **Never `go mod tidy`**, never a bare `go get` (phase 1 ledger: tidy re-raises a test-only dep to a go-1.25 version).
- `git commit` is never run by this plan (standing rule). "Commit"/"Checkpoint" steps mean: run the verification and record the result in the ledger.
- `go test ./...` must pass with **no Docker daemon and no MySQL server**: compose unit tests use `compose.Recorder`; MySQL live tests skip without `AGENTBOX_TEST_MYSQL_URL`; Docker-backed tests skip without `AGENTBOX_TEST_DOCKER=1`.
- Credentials never appear on a command line (spec R7): MySQL tools get a 0600 temp `--defaults-extra-file`, deleted afterwards.
- Compose project name is exactly `agentbox-<repo-slug>-<box>`; every compose invocation runs with the box dir as cwd and is echoed (`$ docker …`) into the box log (spec R4, §6).
- Step order in `new`: worktree → ports → db → env → **compose** → setup → dev (spec §6).
- Box env gains `COMPOSE_PROJECT_NAME=<project>` after `AGENTBOX_ROOT` when `compose:` is configured.
- Daemon homes in tests live under `/tmp` (macOS unix-socket cap).

## Review Focus

1. **A compose file whose service has a literal host port (`"6379:6379"`)** — the second box's `up` fails with a port-conflict; the user expects the box to roll back cleanly (containers of the failed box gone) and an error naming the `compose` step with the docker output. *(Task 3: `TestComposeUpFailureRollsBack`; Task 5 `init` warning test.)*
2. **`agentbox rm` after the repo's compose file was deleted or `compose:` removed from the config** — the stack must still be torn down, because it is addressed by project name, not by file. *(Task 3: `TestRemoveDownsStackWithoutConfig`.)*
3. **A MySQL clone name longer than 64 characters** — refused with a clear error before any dump runs. *(Task 4: `TestMySQLNameTooLong`.)*
4. **A clone database that already exists (a leftover, or another checkout's live box)** — `new` must refuse with `exists` and point at `doctor --fix`, exactly like Postgres; it must never drop it. *(Task 4 as amended by the final review: `TestMySQLCloneRefusesExisting`; gated `TestMySQLCloneRefusesExistingLive`.)*
5. **`docker` missing or the compose plugin absent** — `new` must fail at the `compose` step with the install hint, and `doctor` must report it only for repos that configure `compose:`. *(Task 3: `TestComposeMissingDockerHint`, `TestDoctorReportsMissingDockerOnlyWhenConfigured`.)*

---

## File structure

```
internal/config/config.go                 + Compose, mysql driver validation            (Task 1)
internal/compose/compose.go               Executor, Docker, Stack, ProjectName, ResolveFile, Projects, DownProject  (Task 2)
internal/compose/recorder.go              Recorder (test executor)                        (Task 2)
internal/compose/compose_test.go                                                           (Task 2)
internal/box/manager.go, runtime.go, doctor.go   compose step, start/stop/rm, doctor       (Task 3)
internal/db/mysql.go, mysql_test.go       MySQL driver + pure and gated tests             (Task 4)
internal/db/db.go                         New(): mysql                                    (Task 4)
internal/initcfg/initcfg.go               mysql detection, compose block + warning        (Task 5)
examples/compose-redis/*, examples/node-mysql/*                                            (Task 5)
e2e/e2e_test.go                           TestThreeBoxesMySQL, TestComposeRedis           (Task 5)
.github/workflows/ci.yml, README.md                                                        (Task 5)
```

---

### Task 1: Config — `compose:` block and `db.driver: mysql`

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `type Compose struct{ File string; Services []string; Wait string }`, `Config.Compose *Compose`, `func (c *Compose) WaitTimeout() time.Duration` (default 60s); `db.driver` accepts `mysql` with a `mysql://`/`mariadb://` URL that has a host and a database name.

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/config_test.go`:

```go
func TestComposeKeys(t *testing.T) {
	c, err := Parse([]byte("version: 1\nports: [APP_PORT, REDIS_PORT]\ncompose:\n  file: deploy/compose.yaml\n  services: [redis, db]\n  wait: 90s\n"))
	if err != nil || c.Compose == nil || c.Compose.File != "deploy/compose.yaml" || len(c.Compose.Services) != 2 || c.Compose.WaitTimeout() != 90*time.Second {
		t.Fatalf("%+v %v", c.Compose, err)
	}
	c, err = Parse([]byte("version: 1\ncompose: {}\n"))
	if err != nil || c.Compose == nil || c.Compose.File != "" || c.Compose.WaitTimeout() != 60*time.Second {
		t.Fatalf("defaults: %+v %v", c.Compose, err)
	}
	for _, bad := range []string{
		"version: 1\ncompose:\n  file: ../x/compose.yaml\n",
		"version: 1\ncompose:\n  services: ['bad name']\n",
		"version: 1\ncompose:\n  wait: 500ms\n",
		"version: 1\ncompose:\n  wait: soon\n",
		"version: 1\ncompose:\n  servcies: [redis]\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := Parse([]byte("version: 1\ncompose:\n  servcies: [redis]\n")); err == nil || !strings.Contains(err.Error(), `did you mean "services"`) {
		t.Fatalf("typo suggestion: %v", err)
	}
}

func TestMySQLDriverConfig(t *testing.T) {
	c, err := Parse([]byte("version: 1\ndb:\n  driver: mysql\n  url_env: DATABASE_URL\n  source: mysql://app:secret@127.0.0.1:3306/myapp\n"))
	if err != nil || c.DB.Driver != "mysql" {
		t.Fatalf("%+v %v", c.DB, err)
	}
	if _, err := Parse([]byte("version: 1\ndb:\n  driver: mysql\n  url_env: DATABASE_URL\n  source: mariadb://app@db/myapp\n")); err != nil {
		t.Fatalf("mariadb scheme: %v", err)
	}
	for _, bad := range []string{
		"mysql://app@127.0.0.1:3306/",          // no database
		"mysql:///myapp",                         // no host
		"postgres://app@127.0.0.1:5432/myapp",   // wrong scheme
		"127.0.0.1:3306/myapp",                   // not a URL
	} {
		if _, err := Parse([]byte("version: 1\ndb:\n  driver: mysql\n  url_env: DATABASE_URL\n  source: " + bad + "\n")); err == nil || !strings.Contains(err.Error(), "db.source") {
			t.Errorf("accepted %q (%v)", bad, err)
		}
	}
	if _, err := Parse([]byte("version: 1\ndb:\n  driver: oracle\n  url_env: X\n  source: y\n")); err == nil || !strings.Contains(err.Error(), `"mysql"`) {
		t.Fatalf("driver list in the error: %v", err)
	}
}
```

Add `"time"` to the test file's imports.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/config/ -run 'Compose|MySQL'`
Expected: FAIL — `c.Compose undefined`; mysql rejected.

- [ ] **Step 3: Implement**

In `internal/config/config.go`:

1. Imports: add `"net/url"` and `"time"`.
2. `Config` gains, after `Env`:

```go
	Compose *Compose `yaml:"compose"`
```

3. Add the type and its method after `Env`:

```go
// Compose describes the per-box Docker Compose stack.
type Compose struct {
	File     string   `yaml:"file"`     // repo-relative; "" = the first of compose.yaml, compose.yml, docker-compose.yml, docker-compose.yaml
	Services []string `yaml:"services"` // subset to bring up; empty = all
	Wait     string   `yaml:"wait"`     // `up --wait` timeout; "" = 60s
}

// WaitTimeout is how long `up --wait` may take before the step fails.
func (c *Compose) WaitTimeout() time.Duration {
	if c.Wait == "" {
		return 60 * time.Second
	}
	d, _ := time.ParseDuration(c.Wait) // validated in validate()
	return d
}

var serviceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
```

4. `knownKeys`: root list gains `"compose"` (after `"env"`); add `"compose": {"file", "services", "wait"},`. In `checkKeys`, extend the recursion condition to `key.Value == "db" || key.Value == "env" || key.Value == "compose"`.
5. In `validate()`, replace the driver switch and add MySQL URL checks:

```go
	if c.DB != nil {
		switch c.DB.Driver {
		case "postgres", "sqlite", "mysql":
		default:
			return fmt.Errorf(`%s: db.driver must be "postgres", "mysql" or "sqlite" (got %q)`, FileName, c.DB.Driver)
		}
```
and after the existing `if c.DB.Driver == "sqlite" {…}` block:

```go
		if c.DB.Driver == "mysql" {
			u, err := url.Parse(c.DB.Source)
			if err != nil || (u.Scheme != "mysql" && u.Scheme != "mariadb") || u.Host == "" || len(strings.Trim(u.Path, "/")) == 0 {
				return fmt.Errorf("%s: db.source for mysql must look like mysql://user:pass@host:3306/database (got %q)", FileName, c.DB.Source)
			}
		}
```

6. After the `env.file` check, add:

```go
	if c.Compose != nil {
		if c.Compose.File != "" {
			if err := localPath("compose.file", c.Compose.File); err != nil {
				return err
			}
		}
		for _, s := range c.Compose.Services {
			if !serviceName.MatchString(s) {
				return fmt.Errorf("%s: compose.services: %q is not a valid service name", FileName, s)
			}
		}
		if c.Compose.Wait != "" {
			d, err := time.ParseDuration(c.Compose.Wait)
			if err != nil || d < time.Second {
				return fmt.Errorf("%s: compose.wait must be a duration of at least 1s (got %q)", FileName, c.Compose.Wait)
			}
		}
	}
```

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/config/`
Expected: PASS. (The existing error-message test for `db.driver`, if any, asserts the old text — update it to the new list only if it fails.)

- [ ] **Step 5: Checkpoint**

Run `go vet ./internal/config/ && go test ./internal/config/`; record "Task 1 complete". Do not commit.

---

### Task 2: `internal/compose` — the Compose CLI driver

**Files:**
- Create: `internal/compose/compose.go`, `internal/compose/recorder.go`
- Test: `internal/compose/compose_test.go`

**Interfaces:**
- Produces (consumed by Task 3):

```go
package compose
type Executor interface {
	Run(ctx context.Context, dir string, env []string, out io.Writer, args ...string) error // docker <args…>
	Output(ctx context.Context, args ...string) ([]byte, error)                             // docker <args…> → stdout
}
type Docker struct{}                                        // the real CLI
var DefaultFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"}
func ProjectName(repoSlug, box string) string               // "agentbox-<slug>-<box>"
func Check(ctx context.Context, ex Executor) error          // docker + compose plugin present (errs.Precondition otherwise)
func ResolveFile(dir, configured string) (string, error)    // configured if set (must exist), else the first DefaultFiles entry present in dir
type Stack struct{ Project, Dir, File, EnvFile string; Services []string; Wait time.Duration; Env []string; Exec Executor; Log io.Writer }
func (s *Stack) Up(ctx context.Context) error               // up -d --wait --wait-timeout N [services]
func (s *Stack) Stop(ctx context.Context) error             // stop
func (s *Stack) Down(ctx context.Context) error             // down -v --remove-orphans (file-based)
func Projects(ctx context.Context, ex Executor, prefix string) ([]string, error) // compose ls --all --format json, names with prefix
func DownProject(ctx context.Context, ex Executor, project string, log io.Writer) error // -p <project> down -v --remove-orphans (no file needed)
func StopProject(ctx context.Context, ex Executor, project string, log io.Writer) error // -p <project> stop (no file needed)
type Recorder struct{ Calls [][]string; Dirs []string; Envs [][]string; Fail map[string]error; Out string; Stdout map[string]string } // test executor
```

- [ ] **Step 1: Write the failing tests**

Create `internal/compose/compose_test.go`:

```go
package compose

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ayush2966/Agent-Box/internal/errs"
)

var ctx = context.Background()

func stack(rec *Recorder, log *bytes.Buffer) *Stack {
	return &Stack{Project: "agentbox-app-1a2b3c-a", Dir: "/tmp/boxes/a", File: "compose.yaml", EnvFile: ".env",
		Services: []string{"redis"}, Wait: 90 * time.Second, Env: []string{"REDIS_PORT=20001"}, Exec: rec, Log: log}
}

func TestProjectName(t *testing.T) {
	if got := ProjectName("app-1a2b3c", "feature-x"); got != "agentbox-app-1a2b3c-feature-x" {
		t.Fatal(got)
	}
}

func TestUpStopDownArgv(t *testing.T) {
	rec, log := &Recorder{}, &bytes.Buffer{}
	s := stack(rec, log)
	if err := s.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Down(ctx); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"compose", "-p", "agentbox-app-1a2b3c-a", "-f", "compose.yaml", "up", "-d", "--wait", "--wait-timeout", "90", "redis"},
		{"compose", "-p", "agentbox-app-1a2b3c-a", "-f", "compose.yaml", "stop"},
		{"compose", "-p", "agentbox-app-1a2b3c-a", "-f", "compose.yaml", "down", "-v", "--remove-orphans"},
	}
	if len(rec.Calls) != 3 {
		t.Fatalf("calls %v", rec.Calls)
	}
	for i := range want {
		if strings.Join(rec.Calls[i], " ") != strings.Join(want[i], " ") {
			t.Errorf("call %d = %v, want %v", i, rec.Calls[i], want[i])
		}
		if rec.Dirs[i] != "/tmp/boxes/a" || !contains(rec.Envs[i], "REDIS_PORT=20001") {
			t.Errorf("call %d ran in %q with env %v", i, rec.Dirs[i], rec.Envs[i])
		}
	}
	if !strings.Contains(log.String(), "$ docker compose -p agentbox-app-1a2b3c-a -f compose.yaml up") {
		t.Errorf("commands are echoed to the log:\n%s", log.String())
	}
}

func TestCustomEnvFileAndNoServices(t *testing.T) {
	rec := &Recorder{}
	s := stack(rec, &bytes.Buffer{})
	s.EnvFile, s.Services = ".env.development", nil
	if err := s.Up(ctx); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(rec.Calls[0], " ")
	if !strings.Contains(got, "--env-file .env.development up") || strings.HasSuffix(got, " redis") {
		t.Fatalf("%s", got)
	}
}

func TestUpFailureCarriesOutput(t *testing.T) {
	rec := &Recorder{Fail: map[string]error{"up": errors.New("exit status 1")}, Out: "Error response from daemon: port is already allocated\n"}
	s := stack(rec, &bytes.Buffer{})
	err := s.Up(ctx)
	e := errs.From(err)
	if e == nil || !strings.Contains(e.Message, "docker compose up failed") || !strings.Contains(e.Message, "port is already allocated") {
		t.Fatalf("%v", err)
	}
}

func TestResolveFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := ResolveFile(dir, ""); err == nil || !strings.Contains(err.Error(), "compose.yaml") {
		t.Fatalf("no file: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}\n"), 0o644)
	if f, err := ResolveFile(dir, ""); err != nil || f != "docker-compose.yml" {
		t.Fatalf("%q %v", f, err)
	}
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0o644)
	if f, _ := ResolveFile(dir, ""); f != "compose.yaml" {
		t.Fatalf("precedence: %q", f)
	}
	if _, err := ResolveFile(dir, "deploy/compose.yaml"); err == nil {
		t.Fatal("configured file that does not exist must error")
	}
	os.MkdirAll(filepath.Join(dir, "deploy"), 0o755)
	os.WriteFile(filepath.Join(dir, "deploy", "compose.yaml"), []byte("services: {}\n"), 0o644)
	if f, err := ResolveFile(dir, "deploy/compose.yaml"); err != nil || f != "deploy/compose.yaml" {
		t.Fatalf("%q %v", f, err)
	}
}

func TestProjectsAndDownProject(t *testing.T) {
	rec := &Recorder{Stdout: map[string]string{"ls": `[{"Name":"agentbox-app-1a2b3c-a","Status":"running(1)"},{"Name":"other","Status":"exited(1)"},{"Name":"agentbox-app-1a2b3c-ghost","Status":"exited(1)"},{"Name":"agentbox-web-9z9z9z-a","Status":"running(2)"}]`}}
	got, err := Projects(ctx, rec, "agentbox-app-1a2b3c-")
	if err != nil || strings.Join(got, ",") != "agentbox-app-1a2b3c-a,agentbox-app-1a2b3c-ghost" {
		t.Fatalf("%v %v", got, err)
	}
	if strings.Join(rec.Calls[0], " ") != "compose ls --all --format json" {
		t.Fatalf("%v", rec.Calls[0])
	}
	log := &bytes.Buffer{}
	if err := DownProject(ctx, rec, "agentbox-app-1a2b3c-ghost", log); err != nil {
		t.Fatal(err)
	}
	if strings.Join(rec.Calls[1], " ") != "compose -p agentbox-app-1a2b3c-ghost down -v --remove-orphans" {
		t.Fatalf("%v", rec.Calls[1])
	}
	if err := StopProject(ctx, rec, "agentbox-app-1a2b3c-a", log); err != nil {
		t.Fatal(err)
	}
	if strings.Join(rec.Calls[2], " ") != "compose -p agentbox-app-1a2b3c-a stop" {
		t.Fatalf("%v", rec.Calls[2])
	}
	bad := &Recorder{Stdout: map[string]string{"ls": "not json"}}
	if _, err := Projects(ctx, bad, "agentbox-"); err == nil {
		t.Fatal("unparsable ls output must error")
	}
}

func TestCheck(t *testing.T) {
	ok := &Recorder{Stdout: map[string]string{"version": "Docker Compose version v2.29.0\n"}}
	if err := Check(ctx, ok); err != nil {
		t.Fatal(err)
	}
	missing := &Recorder{Fail: map[string]error{"version": errors.New(`exec: "docker": executable file not found in $PATH`)}}
	err := Check(ctx, missing)
	if e := errs.From(err); e == nil || e.Code != errs.Precondition || !strings.Contains(e.Hint, "Docker") {
		t.Fatalf("%v", err)
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
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/compose/`
Expected: FAIL — package has no non-test files / symbols undefined.

- [ ] **Step 3: Implement `compose.go`**

Create `internal/compose/compose.go`:

```go
// Package compose runs one Docker Compose project per box through the docker CLI.
package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Ayush2966/Agent-Box/internal/errs"
	"github.com/Ayush2966/Agent-Box/internal/proc"
)

// Executor runs the docker CLI. Docker is the real one; Recorder is for tests.
type Executor interface {
	// Run executes `docker args…` in dir with env, streaming combined output to out.
	Run(ctx context.Context, dir string, env []string, out io.Writer, args ...string) error
	// Output executes `docker args…` and returns its stdout.
	Output(ctx context.Context, args ...string) ([]byte, error)
}

// Docker shells out to the docker binary on PATH.
type Docker struct{}

func (Docker) Run(ctx context.Context, dir string, env []string, out io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 2 * time.Second
	return cmd.Run()
}

func (Docker) Output(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "docker", args...).Output()
}

// DefaultFiles are the compose file names Docker itself looks for, in order.
var DefaultFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"}

// ProjectName is deterministic so doctor can recognise agentbox's projects.
func ProjectName(repoSlug, box string) string { return "agentbox-" + repoSlug + "-" + box }

const installHint = "install Docker Desktop, or the docker CLI with the compose plugin, and make sure the daemon is running"

// Check verifies docker and its compose plugin answer.
func Check(ctx context.Context, ex Executor) error {
	out, err := ex.Output(ctx, "compose", "version")
	if err != nil {
		return errs.New(errs.Precondition, "docker compose is not available: %v", err).WithHint(installHint)
	}
	if !strings.Contains(strings.ToLower(string(out)), "compose") {
		return errs.New(errs.Precondition, "docker answered but the compose plugin is missing: %s", strings.TrimSpace(string(out))).WithHint(installHint)
	}
	return nil
}

// ResolveFile returns the compose file to use, relative to dir: the configured
// one (which must exist) or the first default name present.
func ResolveFile(dir, configured string) (string, error) {
	if configured != "" {
		if _, err := os.Stat(filepath.Join(dir, configured)); err != nil {
			return "", errs.New(errs.Precondition, "compose file %s not found in %s", configured, dir)
		}
		return configured, nil
	}
	for _, name := range DefaultFiles {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return name, nil
		}
	}
	return "", errs.New(errs.Precondition, "no compose file found in %s (looked for %s)", dir, strings.Join(DefaultFiles, ", ")).
		WithHint("set compose.file in agentbox.yaml")
}

// Stack is one box's Compose project.
type Stack struct {
	Project  string        // agentbox-<repo-slug>-<box>
	Dir      string        // the box directory; every command runs here
	File     string        // compose file, relative to Dir
	EnvFile  string        // env file, relative to Dir; ".env" or "" means compose's default
	Services []string      // subset to bring up; nil = all
	Wait     time.Duration // `up --wait` timeout
	Env      []string      // process environment (the box env on top of the daemon's)
	Exec     Executor
	Log      io.Writer // the box log
}

func (s *Stack) base() []string {
	args := []string{"compose", "-p", s.Project, "-f", s.File}
	if s.EnvFile != "" && s.EnvFile != ".env" {
		args = append(args, "--env-file", s.EnvFile)
	}
	return args
}

func (s *Stack) Up(ctx context.Context) error {
	args := append(s.base(), "up", "-d", "--wait", "--wait-timeout", strconv.Itoa(int(s.Wait.Seconds())))
	return s.run(ctx, append(args, s.Services...))
}

func (s *Stack) Stop(ctx context.Context) error { return s.run(ctx, append(s.base(), "stop")) }

func (s *Stack) Down(ctx context.Context) error {
	return s.run(ctx, append(s.base(), "down", "-v", "--remove-orphans"))
}

// run echoes the command into the log, runs it, and on failure reports the
// verb (up/stop/down) with the last lines of output.
func (s *Stack) run(ctx context.Context, args []string) error {
	return runLogged(ctx, s.Exec, s.Dir, s.Env, s.Log, args)
}

func runLogged(ctx context.Context, ex Executor, dir string, env []string, log io.Writer, args []string) error {
	if log == nil {
		log = io.Discard
	}
	fmt.Fprintf(log, "$ docker %s\n", strings.Join(args, " "))
	tail := proc.NewRing(20)
	w := io.MultiWriter(log, tail)
	if err := ex.Run(ctx, dir, env, w, args...); err != nil {
		verb := "compose"
		for i, a := range args {
			if a == "up" || a == "stop" || a == "down" {
				verb = "compose " + args[i]
				break
			}
		}
		return errs.New(errs.Internal, "docker %s failed (%v); last output:\n%s", verb, err, strings.Join(tail.Tail(20), "\n")).
			WithHint("the full output is in the box log (`agentbox logs <name>`)")
	}
	return nil
}

// Projects lists the Compose projects Docker knows (running or not) whose
// names start with prefix.
func Projects(ctx context.Context, ex Executor, prefix string) ([]string, error) {
	out, err := ex.Output(ctx, "compose", "ls", "--all", "--format", "json")
	if err != nil {
		return nil, errs.New(errs.Precondition, "docker compose ls: %v", err).WithHint(installHint)
	}
	var rows []struct {
		Name string `json:"Name"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("could not parse `docker compose ls` output: %w", err)
	}
	var names []string
	for _, r := range rows {
		if strings.HasPrefix(r.Name, prefix) {
			names = append(names, r.Name)
		}
	}
	return names, nil
}

// DownProject removes a project by name alone (Compose finds its containers
// by label), so it works after the box dir or its compose file is gone.
func DownProject(ctx context.Context, ex Executor, project string, log io.Writer) error {
	return runLogged(ctx, ex, "", nil, log, []string{"compose", "-p", project, "down", "-v", "--remove-orphans"})
}

// StopProject stops a project's containers by name, keeping them and their volumes.
func StopProject(ctx context.Context, ex Executor, project string, log io.Writer) error {
	return runLogged(ctx, ex, "", nil, log, []string{"compose", "-p", project, "stop"})
}
```

- [ ] **Step 4: Implement `recorder.go`**

Create `internal/compose/recorder.go`:

```go
package compose

import (
	"context"
	"io"
	"sync"
)

// Recorder is an Executor for tests: it records every invocation and can be
// scripted to fail a verb (up/stop/down/ls/version) or to answer with stdout.
type Recorder struct {
	mu     sync.Mutex
	Calls  [][]string
	Dirs   []string
	Envs   [][]string
	Fail   map[string]error  // verb → error returned by Run/Output
	Out    string            // written to out by Run (e.g. docker's error text)
	Stdout map[string]string // verb → stdout returned by Output
}

func verb(args []string) string {
	for _, a := range args {
		switch a {
		case "up", "stop", "down", "ls", "version":
			return a
		}
	}
	return ""
}

func (r *Recorder) record(dir string, env []string, args []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls = append(r.Calls, append([]string(nil), args...))
	r.Dirs = append(r.Dirs, dir)
	r.Envs = append(r.Envs, append([]string(nil), env...))
}

func (r *Recorder) Run(_ context.Context, dir string, env []string, out io.Writer, args ...string) error {
	r.record(dir, env, args)
	if r.Out != "" && out != nil {
		io.WriteString(out, r.Out)
	}
	return r.Fail[verb(args)]
}

func (r *Recorder) Output(_ context.Context, args ...string) ([]byte, error) {
	r.record("", nil, args)
	if err := r.Fail[verb(args)]; err != nil {
		return nil, err
	}
	return []byte(r.Stdout[verb(args)]), nil
}

// Verbs returns the verb of every recorded call, in order (for assertions).
func (r *Recorder) Verbs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.Calls))
	for _, c := range r.Calls {
		out = append(out, verb(c))
	}
	return out
}
```

- [ ] **Step 5: Run the tests**

Run: `gofmt -l internal/compose; go vet ./internal/compose/ && go test -race ./internal/compose/`
Expected: PASS (7 tests).

- [ ] **Step 6: Checkpoint**

Record "Task 2 complete". Do not commit.

---

### Task 3: The compose lifecycle step in `box.Manager`

**Files:**
- Modify: `internal/state/state.go` (new `Compose` column), `internal/box/manager.go`, `internal/box/runtime.go`, `internal/box/doctor.go`
- Test: `internal/box/compose_test.go` (new), `internal/state/state_test.go` (one assertion)

**Interfaces:**
- Consumes: Task 1 `config.Compose`/`WaitTimeout`; Task 2 `compose.Executor`, `Docker`, `Recorder`, `Check`, `ResolveFile`, `Stack`, `ProjectName`, `Projects`, `DownProject`, `StopProject`.
- Produces: `state.Box.Compose string` (the project name; "" = no stack), `box.StepCompose = "compose"`, `box.Deps.Compose compose.Executor` (nil → `compose.Docker{}`), env var `COMPOSE_PROJECT_NAME`, doctor finding kind `orphan_compose` and a `tools` finding for a missing docker when the repo configures `compose:`.

- [ ] **Step 1: Write the failing tests**

Create `internal/box/compose_test.go`:

```go
package box

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ayush2966/Agent-Box/internal/compose"
	"github.com/Ayush2966/Agent-Box/internal/errs"
	"github.com/Ayush2966/Agent-Box/internal/state"
	"github.com/Ayush2966/Agent-Box/internal/testutil"
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
```

(`kinds` exists in `doctor_test.go`; `newSQLiteRepo`, `sqliteConfig`, `assertGone`, `ctx` in `manager_test.go`.)

In `internal/state/state_test.go`, find the test that inserts and reads back a box with `Managed` set (grep `Managed`) and add `Compose: "agentbox-r-a"` to the inserted box plus an assertion that it round-trips through `GetBox` and `UpdateBox`. If no such test exists, add:

```go
func TestBoxComposeRoundTrips(t *testing.T) {
	s := openTestStore(t) // whatever helper the file uses to open a temp store
	r, _ := s.UpsertRepo("/tmp/r")
	b := Box{RepoID: r.ID, Name: "a", Dir: "/tmp/boxes/a", Branch: "agentbox/a", State: Ready, Compose: "agentbox-" + r.ID + "-a"}
	if err := s.InsertBox(b); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBox(r.ID, "a")
	if err != nil || got.Compose != b.Compose {
		t.Fatalf("%+v %v", got, err)
	}
	got.Compose = ""
	if err := s.UpdateBox(got); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetBox(r.ID, "a"); again.Compose != "" {
		t.Fatalf("UpdateBox did not clear Compose: %+v", again)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/box/ -run 'Compose|RemoveDownsStack|DoctorFindsOrphan|DoctorReportsMissingDocker'`
Expected: FAIL — `m.d.Compose undefined`, `b.Compose undefined`, `StepCompose` undefined.

- [ ] **Step 3: State column**

In `internal/state/state.go`:
1. `Box` gains, after `Managed`:
```go
	// Compose is the box's Docker Compose project name, "" when the box has no stack.
	Compose string
```
2. In the `CREATE TABLE boxes` statement add `compose TEXT NOT NULL DEFAULT '',` after the `managed` line (mind the trailing comma of the previous line).
3. After the existing `ALTER TABLE boxes ADD COLUMN managed …` migration add the same idempotent shape:
```go
	if _, err := db.Exec(`ALTER TABLE boxes ADD COLUMN compose TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return nil, err
	}
```
(copy the exact error-matching condition the `managed` migration uses).
4. `boxCols` gains `, compose`; `InsertBox` gets a 13th `?` and passes `b.Compose` after `encodeManaged(b.Managed)`; `UpdateBox` sets `compose=?` after `managed=?` and passes `b.Compose` after `encodeManaged(b.Managed)` (before `b.RepoID, b.Name`); `scanBox` scans `&b.Compose` last.

- [ ] **Step 4: Manager — deps, step, env, start/stop/remove**

In `internal/box/manager.go`:
1. Imports: add `"github.com/Ayush2966/Agent-Box/internal/compose"`.
2. Steps: add `StepCompose = "compose"` after `StepEnv`.
3. `Deps` gains `Compose compose.Executor // docker; nil = the real CLI`; in `NewManager`: `if d.Compose == nil { d.Compose = compose.Docker{} }`.
4. `envVars`: replace the final `return append(kv, …)` with:
```go
	kv = append(kv, envfile.KV{Key: "AGENTBOX_NAME", Value: b.Name}, envfile.KV{Key: "AGENTBOX_ROOT", Value: b.Dir})
	if cfg.Compose != nil {
		kv = append(kv, envfile.KV{Key: "COMPOSE_PROJECT_NAME", Value: compose.ProjectName(b.RepoID, b.Name)})
	}
	return kv
```
5. Add the helpers (near `startProc`):
```go
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
		Services: cfg.Compose.Services, Wait: cfg.Compose.WaitTimeout(), Env: processEnv(envVars(cfg, b)),
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
```
6. In `Build`, after the env step's `hook(StepEnv)` check and before `if cfg.Setup != ""`:
```go
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
		undo.push(func() error { return compose.DownProject(context.Background(), m.d.Compose, st.Project, m.stackLog(b)) })
		if err := st.Up(ctx); err != nil {
			return fail(StepCompose, err)
		}
		if err := hook(StepCompose); err != nil {
			return fail(StepCompose, err)
		}
	}
```
(The project name is saved before `up` so a crash mid-`up` still leaves `doctor`/`rm` able to find the stack.)
7. In `Remove`, right after `m.stopProc(b.ID())` and before the DB block:
```go
	if b.Compose != "" {
		if err := compose.DownProject(ctx, m.d.Compose, b.Compose, m.stackLog(b)); err != nil {
			note("compose project %s was not removed: %v", b.Compose, err)
		}
	}
```
8. `dbError` is untouched.

In `internal/box/runtime.go`:
9. `Start`: replace the `if cfg.Dev == "" {…}` check and what follows with:
```go
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
	p, err := m.startProc(b, cfg)
	if err != nil {
		return err
	}
	_, err = m.saveRunning(b, p)
	return err
```
10. `Stop`: after `m.stopProc(b.ID())` add:
```go
	var stackErr error
	if b.Compose != "" {
		stackErr = compose.StopProject(ctx, m.d.Compose, b.Compose, m.stackLog(b))
	}
```
and make the two early `return nil` / final `return nil` of the function return `stackErr` instead (the state update must still happen before returning).
Add the `compose` import to `runtime.go` as well.

- [ ] **Step 5: Doctor**

In `internal/box/doctor.go` (import `compose`, `io`):
1. Where `dirs, branches, dbNames` are built, also build `names := map[string]bool{}` with `names[b.Name] = true`.
2. In the `missing_worktree` fix branch, after the DB drop: `if b.Compose != "" { _ = compose.DownProject(ctx, m.d.Compose, b.Compose, io.Discard) }`.
3. Before the ports check, add:
```go
	if cfg != nil && cfg.Compose != nil {
		if err := compose.Check(ctx, m.d.Compose); err != nil {
			add("tools", false, "docker compose is not available but %s configures compose: %v", config.FileName, err)
		} else {
			prefix := compose.ProjectName(repoID, "")
			projects, err := compose.Projects(ctx, m.d.Compose, prefix)
			if err != nil {
				add("compose", false, "could not list compose projects: %v", err)
			}
			for _, p := range projects {
				if names[strings.TrimPrefix(p, prefix)] {
					continue
				}
				fixed := fix && compose.DownProject(ctx, m.d.Compose, p, io.Discard) == nil
				add("orphan_compose", fixed, "compose project %s has no box (remove it with `docker compose -p %s down -v`)", p, p)
			}
		}
	}
```

- [ ] **Step 6: Run the tests**

Run: `gofmt -l internal; go vet ./internal/box/ ./internal/state/ && go test -race ./internal/state/ ./internal/box/`
Expected: PASS — the 8 new box tests, the state round-trip, and every existing test (`TestNewRollsBackEachStep` and friends use `sqliteConfig`, which has no `compose:`, so the real `Docker{}` is never invoked).

- [ ] **Step 7: Checkpoint**

Run `go vet ./... && go test -race ./...`; record "Task 3 complete". Do not commit.

---

### Task 4: The MySQL driver

**Files:**
- Create: `internal/db/mysql.go`
- Modify: `internal/db/db.go`, `go.mod`/`go.sum` (the one pinned `go get`)
- Test: `internal/db/mysql_test.go`

**Interfaces:**
- Consumes: `db.Driver`, `db.Clone`, `dumpRestoreError` (postgres.go), `errs`.
- Produces: `func NewMySQL(source string) (*MySQL, error)`; `db.New` dispatches `"mysql"`.

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/go-sql-driver/mysql@v1.9.3
grep '^go ' go.mod   # must still be: go 1.24.2
```

- [ ] **Step 2: Write the failing tests**

Create `internal/db/mysql_test.go`:

```go
package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNewMySQLValidatesURL(t *testing.T) {
	m, err := NewMySQL("mysql://app:s%40cret@127.0.0.1:3306/myapp?tls=false")
	if err != nil || m.srcDB != "myapp" {
		t.Fatalf("%+v %v", m, err)
	}
	for _, bad := range []string{"mysql://app@127.0.0.1:3306/", "mysql:///db", "postgres://x@y/z", "mysql://x@y/bad-name"} {
		if _, err := NewMySQL(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestMySQLDSNAndURL(t *testing.T) {
	m, _ := NewMySQL("mysql://app:s%40cret@db.internal/myapp?tls=skip-verify")
	if got := m.dsn("myapp_agentbox_a"); got != "app:s@cret@tcp(db.internal:3306)/myapp_agentbox_a?tls=skip-verify" {
		t.Fatalf("dsn %q", got)
	}
	if got := m.dsn(""); got != "app:s@cret@tcp(db.internal:3306)/?tls=skip-verify" {
		t.Fatalf("admin dsn %q", got)
	}
	if got := m.urlFor("myapp_agentbox_a"); got != "mysql://app:s%40cret@db.internal/myapp_agentbox_a?tls=skip-verify" {
		t.Fatalf("url %q", got)
	}
	m2, _ := NewMySQL("mariadb://u@h:3307/d")
	if m2.dsn("d") != "u@tcp(h:3307)/d" || !strings.HasPrefix(m2.urlFor("x"), "mariadb://") {
		t.Fatalf("%q %q", m2.dsn("d"), m2.urlFor("x"))
	}
}

func TestMySQLNameTooLong(t *testing.T) {
	m, _ := NewMySQL("mysql://u@h/" + strings.Repeat("a", 50))
	_, err := m.Clone(context.Background(), strings.Repeat("b", 20), "")
	if err == nil || !strings.Contains(err.Error(), "64") {
		t.Fatalf("want a length error before any connection, got %v", err)
	}
}

func TestMySQLDropRefusesForeignDatabases(t *testing.T) {
	m, _ := NewMySQL("mysql://u@h/myapp")
	for _, name := range []string{"myapp", "other_agentbox_a", "myapp_agentbox", ""} {
		if err := m.Drop(context.Background(), name, ""); err == nil {
			t.Errorf("Drop(%q) must refuse", name)
		}
	}
}

func TestMySQLDefaultsFileKeepsSecretsOffArgv(t *testing.T) {
	m, _ := NewMySQL("mysql://app:hunter2@db.internal:3307/myapp")
	path, cleanup, err := m.defaultsFile()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	for _, want := range []string{"[client]", "host=db.internal", "port=3307", "user=app", "password=hunter2"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("defaults file lacks %q:\n%s", want, data)
		}
	}
	args := m.dumpArgs(path)
	if strings.Contains(strings.Join(args, " "), "hunter2") || args[0] != "--defaults-extra-file="+path {
		t.Fatalf("argv %v", args)
	}
	for _, want := range []string{"--single-transaction", "--no-tablespaces", "myapp"} {
		if !contains(args, want) {
			t.Fatalf("dump argv lacks %s: %v", want, args)
		}
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("cleanup must delete the defaults file")
	}
}

func TestMySQLCloneStatementsDropFirst(t *testing.T) {
	m, _ := NewMySQL("mysql://u@h/myapp")
	stmts := m.cloneStatements("myapp_agentbox_a")
	if len(stmts) != 2 || !strings.HasPrefix(stmts[0], "DROP DATABASE IF EXISTS `myapp_agentbox_a`") || !strings.HasPrefix(stmts[1], "CREATE DATABASE `myapp_agentbox_a`") {
		t.Fatalf("%v", stmts)
	}
	if got := m.listPattern(); got != `myapp\_agentbox\_%` {
		t.Fatalf("pattern %q", got)
	}
}

// --- live tests: need AGENTBOX_TEST_MYSQL_URL (a mysql:// URL of a role that can CREATE DATABASE) ---

func mysqlAdminURL(t *testing.T) string {
	u := os.Getenv("AGENTBOX_TEST_MYSQL_URL")
	if u == "" {
		t.Skip("set AGENTBOX_TEST_MYSQL_URL (mysql:// URL of a role with CREATE privileges) to run MySQL tests")
	}
	if _, err := NewMySQL(u); err != nil {
		t.Fatalf("AGENTBOX_TEST_MYSQL_URL: %v", err)
	}
	return u
}

// newMySQLSource creates a throwaway source database with one row in items
// and returns its URL.
func newMySQLSource(t *testing.T) (string, *sql.DB) {
	t.Helper()
	admin := mysqlAdminURL(t)
	am, _ := NewMySQL(admin)
	name := fmt.Sprintf("abtest_%d", time.Now().UnixNano()%1_000_000_000)
	conn, err := sql.Open("mysql", am.dsn(""))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"CREATE DATABASE " + mysqlIdent(name), "CREATE TABLE " + mysqlIdent(name) + ".items(name VARCHAR(20))", "INSERT INTO " + mysqlIdent(name) + ".items VALUES('one')"} {
		if _, err := conn.ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		rows, _ := conn.Query("SHOW DATABASES LIKE ?", strings.ReplaceAll(name, "_", `\_`)+"%")
		var dbs []string
		for rows != nil && rows.Next() {
			var d string
			rows.Scan(&d)
			dbs = append(dbs, d)
		}
		for _, d := range dbs {
			conn.Exec("DROP DATABASE IF EXISTS " + mysqlIdent(d))
		}
		conn.Close()
	})
	return am.urlFor(name), conn
}

func mysqlCount(t *testing.T, conn *sql.DB, dbName string) int {
	t.Helper()
	var n int
	if err := conn.QueryRow("SELECT COUNT(*) FROM " + mysqlIdent(dbName) + ".items").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMySQLCloneHasSourceRows(t *testing.T) {
	src, conn := newMySQLSource(t)
	m, err := NewMySQL(src)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := m.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := m.Clone(ctx, "a", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != m.srcDB+"_agentbox_a" || !strings.HasSuffix(c.URL, "/"+c.Name) {
		t.Fatalf("%+v", c)
	}
	if mysqlCount(t, conn, c.Name) != 1 {
		t.Fatal("clone lacks the source row")
	}
	conn.Exec("INSERT INTO " + mysqlIdent(c.Name) + ".items VALUES('two')")
	if mysqlCount(t, conn, m.srcDB) != 1 {
		t.Fatal("writing to the clone touched the source")
	}
	names, err := m.List(ctx)
	if err != nil || len(names) != 1 || names[0] != c.Name {
		t.Fatalf("list %v %v", names, err)
	}
	if err := m.Drop(ctx, c.Name, ""); err != nil {
		t.Fatal(err)
	}
	if names, _ := m.List(ctx); len(names) != 0 {
		t.Fatalf("after drop: %v", names)
	}
	if err := m.Drop(ctx, c.Name, ""); err != nil {
		t.Fatalf("dropping twice must succeed: %v", err)
	}
}

func TestMySQLCloneReplacesLeftover(t *testing.T) {
	src, conn := newMySQLSource(t)
	m, _ := NewMySQL(src)
	ctx := context.Background()
	leftover := m.srcDB + "_agentbox_a"
	if _, err := conn.Exec("CREATE DATABASE " + mysqlIdent(leftover)); err != nil { // an empty leftover from a crash
		t.Fatal(err)
	}
	if _, err := m.Clone(ctx, "a", ""); err != nil {
		t.Fatal(err)
	}
	if mysqlCount(t, conn, leftover) != 1 {
		t.Fatal("the leftover was not replaced by a fresh clone")
	}
}

func TestMySQLCheckReportsMissingTools(t *testing.T) {
	src, _ := newMySQLSource(t)
	m, _ := NewMySQL(src)
	t.Setenv("PATH", t.TempDir())
	err := m.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "mysqldump") {
		t.Fatalf("want a missing-tools error, got %v", err)
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
```

(If `postgres_test.go` already defines a `contains` helper, drop this one.)

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/db/ -run MySQL`
Expected: FAIL — `NewMySQL` undefined.

- [ ] **Step 4: Implement `mysql.go`**

Create `internal/db/mysql.go`:

```go
package db

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"

	_ "github.com/go-sql-driver/mysql"

	"github.com/Ayush2966/Agent-Box/internal/errs"
)

// MySQL clones the source database per box with mysqldump | mysql. There is
// no template: MySQL cannot copy a database instantly, so a dump is needed
// per clone anyway, and Refresh is therefore a no-op.
type MySQL struct {
	src   *url.URL
	srcDB string
}

var mysqlName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

const mysqlToolsHint = "install the MySQL client tools (mysql and mysqldump) and put them on PATH"

func NewMySQL(source string) (*MySQL, error) {
	u, err := url.Parse(source)
	if err != nil || (u.Scheme != "mysql" && u.Scheme != "mariadb") || u.Host == "" {
		return nil, errs.New(errs.Invalid, "db.source must be a mysql:// URL with a host (got %q)", source)
	}
	name := strings.Trim(u.Path, "/")
	if name == "" {
		return nil, errs.New(errs.Invalid, "db.source %q names no database", source)
	}
	if !mysqlName.MatchString(name) {
		return nil, errs.New(errs.Invalid, "database name %q must be letters, digits and underscores", name)
	}
	return &MySQL{src: u, srcDB: name}, nil
}

func (m *MySQL) hostPort() (host, port string) {
	host, port, err := net.SplitHostPort(m.src.Host)
	if err != nil {
		return m.src.Host, "3306"
	}
	return host, port
}

// dsn is the Go driver's form for dbName ("" = no database selected).
func (m *MySQL) dsn(dbName string) string {
	host, port := m.hostPort()
	var b strings.Builder
	if m.src.User != nil {
		b.WriteString(m.src.User.Username())
		if pw, ok := m.src.User.Password(); ok {
			b.WriteString(":" + pw)
		}
		b.WriteString("@")
	}
	fmt.Fprintf(&b, "tcp(%s:%s)/%s", host, port, dbName)
	if m.src.RawQuery != "" {
		b.WriteString("?" + m.src.RawQuery)
	}
	return b.String()
}

// urlFor is the source URL with the database replaced; it is what apps get.
func (m *MySQL) urlFor(dbName string) string {
	u := *m.src
	u.Path = "/" + dbName
	return u.String()
}

func (m *MySQL) clonePrefix() string { return m.srcDB + "_agentbox_" }

func (m *MySQL) cloneName(box string) (string, error) {
	name := m.clonePrefix() + box
	if len(name) > 64 {
		return "", errs.New(errs.Invalid, "database name %s is longer than MySQL's 64-character limit", name).
			WithHint("use a shorter box name")
	}
	return name, nil
}

func mysqlIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

func (m *MySQL) admin(ctx context.Context) (*sql.DB, error) {
	conn, err := sql.Open("mysql", m.dsn(""))
	if err != nil {
		return nil, err
	}
	if err := conn.PingContext(ctx); err != nil {
		conn.Close()
		return nil, errs.New(errs.Precondition, "connect to %s: %v", m.src.Redacted(), err).
			WithHint("is the server running and the user allowed to connect?")
	}
	return conn, nil
}

func (m *MySQL) Check(ctx context.Context) error {
	for _, tool := range []string{"mysql", "mysqldump"} {
		if _, err := exec.LookPath(tool); err != nil {
			return errs.New(errs.Precondition, "%s not found on PATH", tool).WithHint(mysqlToolsHint)
		}
	}
	conn, err := m.admin(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return nil
}

func (m *MySQL) Prepare(ctx context.Context) error { return m.Check(ctx) }

func (m *MySQL) Refresh(context.Context) error { return nil }

// cloneStatements drop a leftover of the same name (a crash between
// CREATE and the restore) and create the clone afresh.
func (m *MySQL) cloneStatements(name string) []string {
	return []string{
		"DROP DATABASE IF EXISTS " + mysqlIdent(name),
		"CREATE DATABASE " + mysqlIdent(name),
	}
}

func (m *MySQL) Clone(ctx context.Context, box, _ string) (Clone, error) {
	name, err := m.cloneName(box)
	if err != nil {
		return Clone{}, err
	}
	conn, err := m.admin(ctx)
	if err != nil {
		return Clone{}, err
	}
	defer conn.Close()
	for _, q := range m.cloneStatements(name) {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return Clone{}, errs.New(errs.Precondition, "%s: %v", q, err).
				WithHint("the user in db.source needs the CREATE privilege (GRANT CREATE, DROP ON *.* TO ...)")
		}
	}
	if err := m.dumpRestore(ctx, name); err != nil {
		_, _ = conn.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+mysqlIdent(name))
		return Clone{}, err
	}
	return Clone{Name: name, URL: m.urlFor(name)}, nil
}

func (m *MySQL) Drop(ctx context.Context, name, _ string) error {
	if !strings.HasPrefix(name, m.clonePrefix()) || len(name) <= len(m.clonePrefix()) || !mysqlName.MatchString(name) {
		return errs.New(errs.Invalid, "refusing to drop %q: not an agentbox clone of %s", name, m.srcDB)
	}
	conn, err := m.admin(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.ExecContext(ctx, "DROP DATABASE IF EXISTS "+mysqlIdent(name))
	return err
}

func (m *MySQL) listPattern() string {
	return strings.ReplaceAll(m.clonePrefix(), "_", `\_`) + "%"
}

func (m *MySQL) List(ctx context.Context) ([]string, error) {
	conn, err := m.admin(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	rows, err := conn.QueryContext(ctx, "SHOW DATABASES LIKE ?", m.listPattern())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// defaultsFile writes the connection details for the mysql tools to a 0600
// temp file, so the password never appears on a command line.
func (m *MySQL) defaultsFile() (path string, cleanup func(), err error) {
	host, port := m.hostPort()
	var b strings.Builder
	fmt.Fprintf(&b, "[client]\nhost=%s\nport=%s\n", host, port)
	if m.src.User != nil {
		fmt.Fprintf(&b, "user=%s\n", m.src.User.Username())
		if pw, ok := m.src.User.Password(); ok {
			fmt.Fprintf(&b, "password=%s\n", pw)
		}
	}
	f, err := os.CreateTemp("", "agentbox-my-*.cnf")
	if err != nil {
		return "", nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	f.Close()
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

func (m *MySQL) dumpArgs(defaults string) []string {
	return []string{"--defaults-extra-file=" + defaults, "--single-transaction", "--routines", "--triggers", "--events", "--no-tablespaces", m.srcDB}
}

func (m *MySQL) dumpRestore(ctx context.Context, target string) error {
	defaults, cleanup, err := m.defaultsFile()
	if err != nil {
		return err
	}
	defer cleanup()
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	dump := exec.CommandContext(ctx, "mysqldump", m.dumpArgs(defaults)...)
	restore := exec.CommandContext(ctx, "mysql", "--defaults-extra-file="+defaults, target)
	var dumpErr, restoreErr bytes.Buffer
	dump.Stdout, dump.Stderr = w, &dumpErr
	restore.Stdin, restore.Stderr = r, &restoreErr
	if err := dump.Start(); err != nil {
		r.Close()
		w.Close()
		return errs.New(errs.Precondition, "run mysqldump: %v", err).WithHint(mysqlToolsHint)
	}
	if err := restore.Start(); err != nil {
		r.Close()
		w.Close()
		_ = dump.Process.Kill()
		_ = dump.Wait()
		return errs.New(errs.Precondition, "run mysql: %v", err).WithHint(mysqlToolsHint)
	}
	r.Close()
	w.Close()
	rErr := restore.Wait()
	dErr := dump.Wait()
	if err := dumpRestoreError(dErr, dumpErr.String(), rErr, restoreErr.String()); err != nil {
		return strings.NewReplacer("pg_restore", "mysql", "pg_dump", "mysqldump").Replace(err.Error()) // see note
	}
	return nil
}
```

**Note on the last lines:** `dumpRestoreError` (postgres.go) hard-codes the tool names. Make it generic instead of string-replacing: change its signature to `dumpRestoreError(dumpTool string, dErr error, dumpMsg string, restoreTool string, rErr error, restoreMsg string) error`, build the messages as `"%s failed: %v: %s"` with the tool names, update the Postgres call to `dumpRestoreError("pg_dump", dErr, dumpErr.String(), "pg_restore", rErr, restoreErr.String())` and the existing `TestDumpRestoreErrorReportsTheRealCause` to pass `"pg_dump"`/`"pg_restore"`; then in `mysql.go` call `return dumpRestoreError("mysqldump", dErr, dumpErr.String(), "mysql", rErr, restoreErr.String())` and delete the `strings.NewReplacer` line (and the `strings` import stays — it is used elsewhere in the file).

In `internal/db/db.go` `New`, add `case "mysql": return NewMySQL(cfg.Source)`.

- [ ] **Step 5: Run the tests**

Run: `gofmt -l internal/db; go vet ./internal/db/ && go test -race ./internal/db/`
Expected: PASS — the pure MySQL tests, the updated `dumpRestoreError` test, every Postgres test (skipping the live ones), and the live MySQL tests SKIP without `AGENTBOX_TEST_MYSQL_URL`. If a MySQL server is reachable (CI, or `docker run -e MYSQL_ROOT_PASSWORD=root -p 3306:3306 mysql:8` locally with the `mysql-client` tools installed), run once more with `AGENTBOX_TEST_MYSQL_URL=mysql://root:root@127.0.0.1:3306/mysql` and record the result.

- [ ] **Step 6: Checkpoint**

Run `go vet ./... && go test -race ./...`; record "Task 4 complete" with the `go.mod` go directive (expect `1.24.2`). Do not commit.

---

### Task 5: `init` detection, examples, e2e, CI, README

**Files:**
- Modify: `internal/initcfg/initcfg.go`, `internal/initcfg/initcfg_test.go`
- Create: `examples/compose-redis/{compose.yaml,server.js,package.json,agentbox.yaml,.env.example,.gitignore,README.md}`, `examples/node-mysql/{server.js,package.json,agentbox.yaml,.env.example,.gitignore,README.md}`
- Modify: `e2e/e2e_test.go`, `.github/workflows/ci.yml`, `README.md`

**Interfaces:**
- Consumes: `compose.DefaultFiles` (Task 2), `db` mysql driver (Task 4) through the real binary, the e2e helpers `newEnv`, `copyExample`, `getJSON`, `threeBoxes`, `env.run/runErr`.

- [ ] **Step 1: Failing `init` tests**

Append to `internal/initcfg/initcfg_test.go`:

```go
func TestDetectMySQL(t *testing.T) {
	_, cfg := detect(t, map[string]string{
		"package.json": `{"scripts": {"dev": "node server.js"}}`,
		".env.example": "PORT=3000\nDATABASE_URL=mysql://app:secret@127.0.0.1:3306/app\n",
	})
	if cfg.DB == nil || cfg.DB.Driver != "mysql" || cfg.DB.Source != "mysql://app:secret@127.0.0.1:3306/app" {
		t.Fatalf("db = %+v", cfg.DB)
	}
}

func TestDetectComposeFile(t *testing.T) {
	res, cfg := detect(t, map[string]string{
		"package.json": `{"scripts": {"dev": "node server.js"}}`,
		".env.example": "PORT=3000\n",
		"compose.yaml": "services:\n  redis:\n    image: redis:7\n    ports: [\"6379:6379\"]\n",
	})
	if cfg.Compose == nil || cfg.Compose.File != "compose.yaml" {
		t.Fatalf("compose = %+v\n%s", cfg.Compose, res.YAML)
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, "\n"), "6379:6379") {
		t.Fatalf("want a fixed-host-port warning, got %v", res.Warnings)
	}
	res, cfg = detect(t, map[string]string{
		"package.json":       `{"scripts": {"dev": "node server.js"}}`,
		".env.example":       "PORT=3000\nREDIS_PORT=6379\n",
		"docker-compose.yml": "services:\n  redis:\n    image: redis:7\n    ports: [\"${REDIS_PORT}:6379\"]\n",
	})
	if cfg.Compose == nil || cfg.Compose.File != "docker-compose.yml" {
		t.Fatalf("compose = %+v", cfg.Compose)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "host port") {
			t.Fatalf("no warning expected for an interpolated port: %v", res.Warnings)
		}
	}
}
```

Run: `go test ./internal/initcfg/ -run 'MySQL|ComposeFile'` — Expected: FAIL.

- [ ] **Step 2: Implement the detection**

In `internal/initcfg/initcfg.go`:
1. Import `"github.com/Ayush2966/Agent-Box/internal/compose"`.
2. `driverOf`: add `case strings.HasPrefix(v, "mysql://"), strings.HasPrefix(v, "mariadb://"): return "mysql"` before the sqlite case.
3. Add a package-level `var fixedHostPort = regexp.MustCompile(`"?\b(\d{2,5}):(\d{2,5})\b"?`)`.
4. In `Detect`, after the `ports` lines are written and before the `db` switch:

```go
	for _, name := range compose.DefaultFiles {
		if !exists(root, name) {
			continue
		}
		fmt.Fprintf(&b, "compose:\n  file: %s\n", name)
		if data, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			if m := fixedHostPort.FindString(string(data)); m != "" {
				r.Warnings = append(r.Warnings, fmt.Sprintf(
					"%s publishes a fixed host port (%s); write it as \"${VAR}:containerPort\" and list VAR under ports so boxes don't collide", name, m))
			}
		}
		break
	}
```

Run: `go test ./internal/initcfg/` — Expected: PASS (every existing test too; `TestDetectNodePostgres` has no compose file).

- [ ] **Step 3: Examples**

Create `examples/compose-redis/compose.yaml`:

```yaml
services:
  redis:
    image: redis:7-alpine
    ports: ["${REDIS_PORT}:6379"]   # REDIS_PORT is allocated per box by agentbox (see agentbox.yaml)
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 2s
      timeout: 2s
      retries: 15
```

Create `examples/compose-redis/server.js`:

```js
// Tiny app: every request increments a counter in this box's own Redis and reports it.
const http = require('http');
const net = require('net');

const port = Number(process.env.APP_PORT || 3000);
const redisPort = Number(process.env.REDIS_PORT || 6379);

function incr(key) {
  return new Promise((resolve, reject) => {
    const sock = net.connect(redisPort, '127.0.0.1');
    sock.on('error', reject);
    sock.on('data', (data) => {
      const m = /^:(\d+)/.exec(data.toString());
      sock.end();
      m ? resolve(Number(m[1])) : reject(new Error(data.toString()));
    });
    sock.write(`*2\r\n$4\r\nINCR\r\n$${key.length}\r\n${key}\r\n`);
  });
}

http
  .createServer(async (req, res) => {
    res.setHeader('content-type', 'application/json');
    try {
      const hits = await incr('hits');
      res.end(JSON.stringify({ box: process.env.AGENTBOX_NAME || null, redisPort, hits }));
    } catch (e) {
      res.statusCode = 500;
      res.end(JSON.stringify({ error: e.message }));
    }
  })
  .listen(port, '127.0.0.1', () => console.log(`listening on ${port}`));
```

Create `examples/compose-redis/package.json`:

```json
{ "name": "agentbox-example-compose-redis", "private": true, "version": "0.0.0", "scripts": { "dev": "node server.js" } }
```

Create `examples/compose-redis/agentbox.yaml`:

```yaml
version: 1
dev: node server.js
ports: [APP_PORT, REDIS_PORT]
compose:
  services: [redis]      # compose.yaml next to this file; each box gets its own project
env:
  template: .env.example
```

Create `examples/compose-redis/.env.example`:

```
APP_PORT=3000
REDIS_PORT=6379
```

Create `examples/compose-redis/.gitignore` with `.env`, and `examples/compose-redis/README.md`:

```markdown
# Compose example

Needs Docker with the compose plugin. Each box gets its own Compose project
(`agentbox-<repo>-<box>`) with its own Redis on its own host port.

    agentbox new a && agentbox new b
    curl http://a.localhost:7777/   # {"box":"a","redisPort":20001,"hits":1}
    curl http://b.localhost:7777/   # {"box":"b","redisPort":20003,"hits":1}
    docker compose ls               # two agentbox-… projects
    agentbox rm a && agentbox rm b  # containers and volumes gone
```

Create `examples/node-mysql/` as a copy of `examples/node-postgres/` with these changes: `package.json` name `agentbox-example-mysql`; `agentbox.yaml` `driver: mysql` and `source: mysql://root:root@127.0.0.1:3306/myapp`; `.env.example` `DATABASE_URL=mysql://root:root@127.0.0.1:3306/myapp`; `README.md`:

```markdown
# MySQL example

Needs a local MySQL/MariaDB with a `myapp` database and the `mysql`/`mysqldump` client tools.

    mysql -uroot -proot -e 'CREATE DATABASE myapp'
    agentbox new a && agentbox new b
    agentbox ls        # databases myapp_agentbox_a and myapp_agentbox_b, each a fresh dump of myapp
```

(`server.js` is identical: it reports the database name from `DATABASE_URL`.)

- [ ] **Step 4: e2e tests**

In `e2e/e2e_test.go` add the imports `_ "github.com/go-sql-driver/mysql"` and `"os/exec"` is already there; append:

```go
// mysqlDSN converts a mysql:// URL to the Go driver's DSN (no database when dbName is "").
func mysqlDSN(u *url.URL, dbName string) string {
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":3306"
	}
	dsn := ""
	if u.User != nil {
		dsn = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			dsn += ":" + pw
		}
		dsn += "@"
	}
	dsn += "tcp(" + host + ")/" + dbName
	if u.RawQuery != "" {
		dsn += "?" + u.RawQuery
	}
	return dsn
}

func TestThreeBoxesMySQL(t *testing.T) {
	admin := os.Getenv("AGENTBOX_TEST_MYSQL_URL")
	if admin == "" {
		t.Skip("set AGENTBOX_TEST_MYSQL_URL to run the MySQL e2e test")
	}
	for _, tool := range []string{"mysql", "mysqldump"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	e := newEnv(t)
	repo := copyExample(t, "node-mysql")
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("mysql", mysqlDSN(u, ""))
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("abe2e_%d", time.Now().UnixNano()%1_000_000_000)
	for _, q := range []string{"CREATE DATABASE `" + name + "`", "CREATE TABLE `" + name + "`.items(name VARCHAR(20))", "INSERT INTO `" + name + "`.items VALUES('one')"} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		rows, err := conn.Query("SHOW DATABASES LIKE ?", strings.ReplaceAll(name, "_", `\_`)+"%")
		if err == nil {
			var dbs []string
			for rows.Next() {
				var d string
				rows.Scan(&d)
				dbs = append(dbs, d)
			}
			rows.Close()
			for _, d := range dbs {
				conn.Exec("DROP DATABASE IF EXISTS `" + d + "`")
			}
		}
		conn.Close()
	})
	src := *u
	src.Path = "/" + name
	cfgPath := filepath.Join(repo, "agentbox.yaml")
	cfg, _ := os.ReadFile(cfgPath)
	os.WriteFile(cfgPath, []byte(strings.Replace(string(cfg), "mysql://root:root@127.0.0.1:3306/myapp", src.String(), 1)), 0o644)

	threeBoxes(t, e, repo, func(b api.BoxView, resp map[string]any) {
		if want := name + "_agentbox_" + b.Name; resp["db"] != want {
			t.Errorf("box %s got database %v, want %s", b.Name, resp["db"], want)
		}
	})
}

func composeProjects(t *testing.T, prefix string) []string {
	t.Helper()
	out, err := exec.Command("docker", "compose", "ls", "--all", "--format", "json").Output()
	if err != nil {
		t.Fatalf("docker compose ls: %v", err)
	}
	var rows []struct{ Name string }
	json.Unmarshal(out, &rows)
	var names []string
	for _, r := range rows {
		if strings.HasPrefix(r.Name, prefix) {
			names = append(names, r.Name)
		}
	}
	return names
}

func TestComposeRedis(t *testing.T) {
	if os.Getenv("AGENTBOX_TEST_DOCKER") != "1" {
		t.Skip("set AGENTBOX_TEST_DOCKER=1 (with a running Docker daemon) to run the Compose e2e test")
	}
	e := newEnv(t)
	repo := copyExample(t, "compose-redis")
	e.run(repo, "new", "a")
	e.run(repo, "new", "b")
	t.Cleanup(func() {
		e.run(repo, "rm", "--force", "a")
		e.run(repo, "rm", "--force", "b")
	})
	var boxes []api.BoxView
	if err := json.Unmarshal([]byte(e.run(repo, "ls", "--json")), &boxes); err != nil || len(boxes) != 2 {
		t.Fatalf("ls --json: %v (%d boxes)", err, len(boxes))
	}
	byName := map[string]api.BoxView{}
	for _, b := range boxes {
		byName[b.Name] = b
	}
	prefix := "agentbox-" + boxes[0].Repo + "-"
	if got := composeProjects(t, prefix); len(got) != 2 {
		t.Fatalf("compose projects: %v", got)
	}
	a := fmt.Sprintf("http://127.0.0.1:%d/", byName["a"].Ports["APP_PORT"])
	b := fmt.Sprintf("http://127.0.0.1:%d/", byName["b"].Ports["APP_PORT"])
	getJSON(t, a)
	if r := getJSON(t, a); r["hits"] != float64(2) || r["box"] != "a" {
		t.Fatalf("box a second hit: %v", r)
	}
	if r := getJSON(t, b); r["hits"] != float64(1) || r["box"] != "b" {
		t.Fatalf("box b has its own redis: %v", r)
	}
	e.run(repo, "rm", "--force", "a")
	e.run(repo, "rm", "--force", "b")
	if got := composeProjects(t, prefix); len(got) != 0 {
		t.Fatalf("projects left after rm: %v", got)
	}
	if out := e.run(repo, "doctor"); !strings.Contains(out, "no problems found") {
		t.Errorf("doctor after cleanup:\n%s", out)
	}
}
```

(The cleanup's `rm --force` of an already-removed box fails; make the cleanup use `e.runErr` and ignore the error — adjust the brief's code accordingly: `_, _ = e.runErr(repo, "rm", "--force", "a")`.)

- [ ] **Step 5: CI**

In `.github/workflows/ci.yml` add two jobs after `postgres`:

```yaml
  mysql:
    runs-on: ubuntu-latest
    services:
      mysql:
        image: mysql:8
        env: { MYSQL_ROOT_PASSWORD: root }
        ports: ['3306:3306']
        options: >-
          --health-cmd "mysqladmin ping -uroot -proot" --health-interval 5s --health-timeout 5s --health-retries 20
    env:
      AGENTBOX_TEST_MYSQL_URL: mysql://root:root@127.0.0.1:3306/mysql
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.24' }
      - uses: actions/setup-node@v4
        with: { node-version: '22' }
      - run: sudo apt-get update && sudo apt-get install -y mysql-client
      - run: go test -race ./internal/db/...
      - run: go test -tags e2e -run MySQL ./e2e/...

  docker:
    runs-on: ubuntu-latest
    env:
      AGENTBOX_TEST_DOCKER: '1'
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.24' }
      - uses: actions/setup-node@v4
        with: { node-version: '22' }
      - run: docker compose version
      - run: go test -race ./internal/compose/... ./internal/box/...
      - run: go test -tags e2e -run Compose ./e2e/...
```

Validate with `ruby -ryaml -e 'YAML.load_file(".github/workflows/ci.yml")'`.

- [ ] **Step 6: README**

In `README.md`:
1. Configuration YAML: after the `db:` block add
```yaml
compose:                      # optional: a Docker Compose project per box
  file: compose.yaml          # default: compose.yaml / docker-compose.yml
  services: [redis]           # default: all services
```
and change the db comment to `driver: postgres            # postgres | mysql | sqlite`.
2. New section before **How it works**:

```markdown
## Docker Compose services

If your dev environment is `docker compose up`, add `compose:` to `agentbox.yaml`. Every box gets its
own Compose project (`agentbox-<repo>-<box>`): own containers, own volumes, own host ports. Write host
ports as `"${REDIS_PORT}:6379"` in the compose file and list `REDIS_PORT` under `ports:` — agentbox
allocates a free port per box and the box's `.env` feeds it to Compose. The stack comes up with the
box (`up -d --wait`), stops and starts with it, and is removed with `rm` (`down -v`). `doctor` finds
projects without a box. See `examples/compose-redis`.

MySQL and MariaDB work like Postgres: `driver: mysql`, a `mysql://` source, and `mysql`/`mysqldump`
on PATH. Each box gets `<db>_agentbox_<box>`, a fresh dump of the source. See `examples/node-mysql`.
```
3. Install line: `Needs git ≥ 2.38, plus pg_dump/pg_restore for Postgres, mysql/mysqldump for MySQL, and Docker for compose.`
4. Roadmap line: `Desktop app.`

- [ ] **Step 7: Run everything, then the gated tests where possible**

```bash
gofmt -l internal e2e; go vet ./... && go test -race -count=1 ./...
go test -count=1 -tags e2e ./e2e/              # MySQL and Compose tests SKIP without their env
```

Then try the Docker-backed tests locally (spec R8 ruling): `docker info` — if the daemon is not running and `/Applications/Docker.app` exists, `open -a Docker` and poll `docker info` for up to 120 s. If it comes up:

```bash
AGENTBOX_TEST_DOCKER=1 go test -count=1 -race ./internal/compose/... ./internal/box/...
AGENTBOX_TEST_DOCKER=1 go test -count=1 -tags e2e -run Compose ./e2e/
docker compose ls --all                        # must show no agentbox-… projects afterwards
osascript -e 'quit app "Docker"'               # leave the machine as found
```

Record exactly what ran and what it showed; if Docker could not start within the time, say so — CI covers it. MySQL has no local server and no client tools; it is CI-only (say so).

- [ ] **Step 8: Checkpoint**

Record "Task 5 complete" with the outputs. `git status` should list the new examples, the e2e/CI/README/initcfg changes and nothing unexpected. Do not commit.

---

## Self-review (done while writing)

- **Spec coverage:** §4 R1 (compose = lifecycle step, db independent) → T3; R2 (ports via env interpolation, `--env-file` when custom) → T2 `base()`, T3 `stack()`; R3 (CLI, logged) → T2 `runLogged`; R4 (project name, cwd) → T2/T3; R5 (stop/start/rm semantics) → T3 runtime/Remove; R6 (no template, Refresh no-op) → T4; R7 (defaults file) → T4; R8 (Recorder, gated tests, local Docker attempt) → T2/T4/T5. §5 config + validation → T1. §6 step order, commands, Check, env var, doctor → T3. §7 driver behaviours → T4. §8 init → T5. §9 examples → T5. §11 tests → every task; CI → T5.
- **Placeholders:** none.
- **Type consistency:** `compose.Executor/Recorder/Stack/ProjectName/ResolveFile/Projects/DownProject/StopProject/Check` (T2) used by T3 with the same names; `state.Box.Compose` (T3) read by Remove/Stop/doctor; `dumpRestoreError` signature change (T4) applied to both callers and its test; `compose.DefaultFiles` (T2) used by T5 `init`; e2e helpers reused.
- **Review Focus:** 1 → `TestComposeUpFailureRollsBack` + `TestDetectComposeFile`; 2 → `TestRemoveDownsStackWithoutConfig`; 3 → `TestMySQLNameTooLong`; 4 → `TestMySQLCloneStatementsDropFirst` (+ gated `TestMySQLCloneReplacesLeftover`); 5 → `TestComposeMissingDockerHint`, `TestDoctorReportsMissingDockerOnlyWhenConfigured`.
