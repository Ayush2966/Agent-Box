# agentbox Phase 1 (Core + CLI) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a single Go binary, `agentbox`, that gives each parallel AI coding agent an isolated dev environment (git worktree, free ports, cloned database, generated env file, supervised dev server), driven by a CLI that talks to an auto-started local daemon over a versioned HTTP API.

**Architecture:** Small focused packages under `internal/`: `config` (parses agentbox.yaml), `envfile`, `state` (SQLite store), `ports`, `worktree` (git CLI wrapper), `db` (driver interface plus sqlite and postgres drivers), `proc` (supervisor and logs), `box` (lifecycle orchestration with undo stack), `api` (HTTP + SSE), `daemon` (process, listeners, auto-start), `client`, `cli` (cobra). The daemon owns all state and child processes. The CLI and all future UIs are just API clients.

**Tech Stack:** Go 1.24, cobra, gopkg.in/yaml.v3, modernc.org/sqlite (pure Go, no cgo), jackc/pgx/v5, `git` CLI, `pg_dump`/`pg_restore`, GitHub Actions, GoReleaser.

**Spec:** `docs/superpowers/specs/2026-09-25-agentbox-phase1-design.md`

## Global Constraints

- Go module path: `github.com/agentbox-dev/agentbox`. To rename it later, change `go.mod` and run `grep -rl agentbox-dev/agentbox . | xargs sed -i '' 's#agentbox-dev/agentbox#<owner>/agentbox#g'`.
- Go 1.24. `CGO_ENABLED=0` builds must work, so use no cgo dependencies.
- Platforms: macOS and Linux only.
- Box name must match `^[a-z0-9][a-z0-9-]{0,30}$`. Branch is `agentbox/<name>`.
- Box dir: `~/.agentbox/boxes/<repo-slug>/<name>/`. `repo-slug` = lowercased base name + `-` + first 6 hex chars of sha256(abs repo path).
- Postgres names: template `<srcdb>_agentbox_tpl`, box DB `<srcdb>_agentbox_<box>` (dashes in box name become underscores).
- Port range default `20000–29999`. API default `127.0.0.1:7777`.
- Home: `~/.agentbox/` (override with `AGENTBOX_HOME`). `daemon.sock` and `token` use mode 0600.
- Git is driven through the `git` CLI (≥ 2.38, for `rev-parse --path-format` and `merge-tree --write-tree`), never a Go git library.
- The config file and env template are read from the **main checkout's working tree**, never from the box worktree.
- Every user-facing error names the step, the cause, and a hint where one exists (`errs.E`).
- CLI exit codes: 0 ok, 1 error, 2 usage, 3 precondition refused.
- **Do not run `git commit` in this repo unless the user has explicitly said to.** Each task's last step is a checkpoint. The suggested commit command is there for when the user asks.

## Review Focus

1. **Files agentbox generates must not count as "uncommitted work".** `.env`, `copy:` files and the SQLite copy in a box must not block `rm`/`merge`, even when the repo doesn't gitignore them. Pinned by `TestGeneratedFilesAreNotUserChanges` (Task 10).
2. **Running any command from inside a box dir** must resolve to the main repo, not treat the worktree as a new repo. Pinned by `TestMainWorktreeFromInsideWorktree` (Task 5) and `TestNewFromInsideBoxDir` (Task 10).
3. **Uncommitted `agentbox.yaml` / `.env.example`** in the main checkout must be the ones used, since new worktrees only contain committed files. Pinned by `newSQLiteRepo`, which deliberately leaves agentbox.yaml uncommitted (Task 10).
4. **Repo paths containing spaces** must work end to end (git, sh -c, env). Pinned by `TestChangesPathWithSpace` (Task 5) and `TestRepoPathWithSpaces` (Task 10).
5. **A leftover `agentbox/<name>` branch** (e.g. after `rm --keep-branch`) must make `new <name>` fail cleanly with a hint, creating nothing. Pinned by the `leftover branch` case of `TestValidate` (Task 10).

## Deliberate deviations from the spec (tell the reviewer)

- API box routes are `/v1/repos/{repo}/boxes/{name}/...`, not `/v1/boxes/{id}`. A slash-containing `{id}` segment would need `%2F` handling in `net/http` routing. `POST /v1/repos {path}` is added so clients can resolve a path to a repo ID. Doctor is `GET` (report) and `POST` (fix) on `/v1/repos/{repo}/doctor`.
- Both DB drivers live in one package (`internal/db`, files `sqlite.go` and `postgres.go`) instead of sub-packages. Drivers receive their config at construction. `Drop` takes the clone name, and `List` is added so doctor can find orphan DBs.
- `--verbose` (spec §14) is deferred. Commands run inside the daemon, so the CLI can't print them without extra plumbing. Instead, every git command is logged at debug level to `~/.agentbox/agentbox.log`.
- After a `--squash` merge the box branch still has "unmerged" commits, so `rm` uses `worktree.MergeIsNoop` (`git merge-tree`) to recognise already-merged work instead of asking for `--force`.
- Postgres integration tests run against `AGENTBOX_TEST_PG_URL` (a GitHub Actions service container in CI) instead of testcontainers-go. This means less machinery and the same coverage.

## File Structure

```
agentbox/
  go.mod, go.sum, .gitignore, LICENSE, README.md, .goreleaser.yaml
  .github/workflows/ci.yml, .github/workflows/release.yml
  cmd/agentbox/main.go                 entry point → cli.Execute()
  internal/config/config.go            agentbox.yaml types, Parse, Load, validation
  internal/envfile/envfile.go          Render (rewrite template), Parse, Pairs, Quote
  internal/state/state.go              SQLite store: repos, boxes, ports; AllocatePorts
  internal/ports/ports.go              IsFree, default range
  internal/testutil/git.go             test helpers: temp git repos
  internal/worktree/worktree.go        git wrapper: MainWorktree, Add, Remove, Changes, Merge, Diff…
  internal/errs/errs.go                user-facing error type E{Code,Message,Step,Hint}
  internal/events/events.go            in-process pub/sub Bus
  internal/proc/ring.go                line ring buffer with subscribers
  internal/proc/rotate.go              size-rotated log file
  internal/proc/log.go                 Log = ring + file; TailFile
  internal/proc/proc.go                supervised process with restart/backoff
  internal/proc/sysproc_linux.go       Setpgid + Pdeathsig
  internal/proc/sysproc_other.go       Setpgid
  internal/db/db.go                    Driver interface, Clone, Options, New, BusyError
  internal/db/sqlite.go                SQLite driver (VACUUM INTO)
  internal/db/postgres.go              Postgres driver (pg_dump template + CREATE DATABASE TEMPLATE)
  internal/box/manager.go              Manager, Deps, Validate, New, Remove, helpers
  internal/box/runtime.go              Start, Stop, Merge, Info, List, Diff, Recover, Shutdown, logs
  internal/box/doctor.go               RefreshDB, Doctor
  internal/api/types.go                wire types
  internal/api/server.go               routes, handlers, auth, SSE
  internal/daemon/daemon.go            Paths, Settings, Run, EnsureRunning, Ping, Stop
  internal/client/client.go            HTTP-over-unix-socket client + SSE reader
  internal/cli/root.go                 cobra root, exit codes, app helpers
  internal/cli/box_cmds.go             new, ls, logs, start, stop, exec, merge, rm
  internal/cli/admin_cmds.go           db refresh, doctor, daemon, init, version
  internal/cli/print.go                output formatting
  internal/initcfg/initcfg.go          repo detection for `agentbox init`
  examples/sqlite/…, examples/node-postgres/…
  e2e/e2e_test.go                      (build tag e2e) builds the binary, drives 3 boxes
```

---

### Task 1: Module scaffold + `config` package

**Files:**
- Create: `go.mod`, `.gitignore`, `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces:
  - `const config.FileName = "agentbox.yaml"`
  - `type config.Config struct { Version int; Setup, Dev string; Ports []string; DB *DB; Env Env; Copy []string }`
  - `type config.DB struct { Driver, URLEnv, Source string }` with `func (d *DB) Path() string` (SQLite: source minus `file:` prefix, cleaned)
  - `type config.Env struct { Template, File string; Set map[string]string }` (File defaults to `.env`)
  - `func config.Parse(data []byte) (*Config, error)`, `func config.Load(repoRoot string) (*Config, error)`

- [ ] **Step 1: Scaffold the module**

```bash
cd ~/Desktop/agentbox
go mod init github.com/agentbox-dev/agentbox
go get gopkg.in/yaml.v3
printf '/dist/\n/agentbox\n*.test\n' > .gitignore
```

- [ ] **Step 2: Write the failing test** at `internal/config/config_test.go`

```go
package config

import (
	"strings"
	"testing"
)

func TestParseValid(t *testing.T) {
	c, err := Parse([]byte(`
version: 1
setup: npm install
dev: npm run dev
ports: [APP_PORT, API_PORT]
db:
  driver: postgres
  url_env: DATABASE_URL
  source: postgres://localhost:5432/myapp
env:
  template: .env.example
  set:
    NODE_ENV: development
copy: [.env.local]
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Env.File != ".env" {
		t.Errorf("Env.File = %q, want default .env", c.Env.File)
	}
	if len(c.Ports) != 2 || c.DB.URLEnv != "DATABASE_URL" || c.Env.Set["NODE_ENV"] != "development" || c.Copy[0] != ".env.local" {
		t.Errorf("unexpected config: %+v", c)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"bad version", "version: 2", "version must be 1"},
		{"missing version", "dev: x", "version must be 1"},
		{"unknown key", "version: 1\nport: [A]", `unknown key "port" (did you mean "ports"?)`},
		{"unknown nested key", "version: 1\ndb:\n  drivr: sqlite", `unknown key "db.drivr" (did you mean "driver"?)`},
		{"bad port name", "version: 1\nports: [app-port]", `"app-port" is not a valid env var name`},
		{"duplicate port", "version: 1\nports: [A, A]", `"A" listed twice`},
		{"bad driver", "version: 1\ndb: {driver: mongo, url_env: X, source: y}", "db.driver must be"},
		{"url_env in ports", "version: 1\nports: [X]\ndb: {driver: sqlite, url_env: X, source: a.db}", "also listed in ports"},
		{"missing source", "version: 1\ndb: {driver: sqlite, url_env: X}", "db.source is required"},
		{"escaping copy path", "version: 1\ncopy: [../secret]", "must be a relative path inside the repo"},
		{"absolute template", "version: 1\nenv: {template: /etc/passwd}", "must be a relative path inside the repo"},
		{"escaping sqlite source", "version: 1\ndb: {driver: sqlite, url_env: X, source: 'file:../x.db'}", "must be a relative path inside the repo"},
		{"bad set key", "version: 1\nenv: {set: {'A-B': x}}", `env.set: "A-B" is not a valid env var name`},
		{"empty file", "", "is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "run `agentbox init`") {
		t.Fatalf("err = %v", err)
	}
}

func TestSQLitePathStripsFilePrefix(t *testing.T) {
	c, err := Parse([]byte("version: 1\ndb: {driver: sqlite, url_env: DATABASE_URL, source: 'file:./data/dev.db'}"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.DB.Path(); got != "data/dev.db" {
		t.Fatalf("Path() = %q", got)
	}
}
```

- [ ] **Step 3: Run it to confirm it fails**

Run: `go test ./internal/config/`
Expected: FAIL, compile errors (`undefined: Parse`, `undefined: Load`).

- [ ] **Step 4: Implement** `internal/config/config.go`

```go
// Package config loads and validates a repo's agentbox.yaml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileName is the per-repo config file, read from the main checkout's root.
const FileName = "agentbox.yaml"

type Config struct {
	Version int      `yaml:"version"`
	Setup   string   `yaml:"setup"`
	Dev     string   `yaml:"dev"`
	Ports   []string `yaml:"ports"`
	DB      *DB      `yaml:"db"`
	Env     Env      `yaml:"env"`
	Copy    []string `yaml:"copy"`
}

type DB struct {
	Driver string `yaml:"driver"`
	URLEnv string `yaml:"url_env"`
	Source string `yaml:"source"`
}

// Path returns a SQLite source as a clean relative file path, without any "file:" prefix.
func (d *DB) Path() string { return filepath.Clean(strings.TrimPrefix(d.Source, "file:")) }

type Env struct {
	Template string            `yaml:"template"`
	File     string            `yaml:"file"`
	Set      map[string]string `yaml:"set"`
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var knownKeys = map[string][]string{
	"":    {"version", "setup", "dev", "ports", "db", "env", "copy"},
	"db":  {"driver", "url_env", "source"},
	"env": {"template", "file", "set"},
}

// Load reads and validates <repoRoot>/agentbox.yaml.
func Load(repoRoot string) (*Config, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, FileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s not found in %s (run `agentbox init`)", FileName, repoRoot)
		}
		return nil, err
	}
	return Parse(data)
}

// Parse validates raw agentbox.yaml contents.
func Parse(data []byte) (*Config, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	if len(root.Content) == 0 {
		return nil, fmt.Errorf("%s is empty", FileName)
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: top level must be a mapping", FileName)
	}
	if err := checkKeys(doc, ""); err != nil {
		return nil, err
	}
	var c Config
	if err := doc.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	if c.Env.File == "" {
		c.Env.File = ".env"
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func checkKeys(n *yaml.Node, section string) error {
	if n.Kind != yaml.MappingNode {
		return nil // Decode reports the type error
	}
	known := knownKeys[section]
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i]
		if !slices.Contains(known, key.Value) {
			name := key.Value
			if section != "" {
				name = section + "." + key.Value
			}
			msg := fmt.Sprintf("%s:%d: unknown key %q", FileName, key.Line, name)
			if s := suggest(key.Value, known); s != "" {
				msg += fmt.Sprintf(" (did you mean %q?)", s)
			}
			return errors.New(msg)
		}
		if section == "" && (key.Value == "db" || key.Value == "env") {
			if err := checkKeys(n.Content[i+1], key.Value); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Config) validate() error {
	if c.Version != 1 {
		return fmt.Errorf("%s: version must be 1 (got %d)", FileName, c.Version)
	}
	seen := map[string]bool{}
	for _, p := range c.Ports {
		if !envName.MatchString(p) {
			return fmt.Errorf("%s: ports: %q is not a valid env var name", FileName, p)
		}
		if seen[p] {
			return fmt.Errorf("%s: ports: %q listed twice", FileName, p)
		}
		seen[p] = true
	}
	if c.DB != nil {
		switch c.DB.Driver {
		case "postgres", "sqlite":
		default:
			return fmt.Errorf(`%s: db.driver must be "postgres" or "sqlite" (got %q)`, FileName, c.DB.Driver)
		}
		if !envName.MatchString(c.DB.URLEnv) {
			return fmt.Errorf("%s: db.url_env must be a valid env var name (got %q)", FileName, c.DB.URLEnv)
		}
		if seen[c.DB.URLEnv] {
			return fmt.Errorf("%s: db.url_env %q is also listed in ports", FileName, c.DB.URLEnv)
		}
		if c.DB.Source == "" {
			return fmt.Errorf("%s: db.source is required", FileName)
		}
		if c.DB.Driver == "sqlite" {
			if err := localPath("db.source", c.DB.Path()); err != nil {
				return err
			}
		}
	}
	for k := range c.Env.Set {
		if !envName.MatchString(k) {
			return fmt.Errorf("%s: env.set: %q is not a valid env var name", FileName, k)
		}
	}
	if c.Env.Template != "" {
		if err := localPath("env.template", c.Env.Template); err != nil {
			return err
		}
	}
	if err := localPath("env.file", c.Env.File); err != nil {
		return err
	}
	for i, p := range c.Copy {
		if err := localPath(fmt.Sprintf("copy[%d]", i), p); err != nil {
			return err
		}
	}
	return nil
}

func localPath(field, p string) error {
	if !filepath.IsLocal(p) {
		return fmt.Errorf("%s: %s: %q must be a relative path inside the repo", FileName, field, p)
	}
	return nil
}

func suggest(s string, options []string) string {
	best, bestDist := "", 3
	for _, o := range options {
		if d := levenshtein(s, o); d < bestDist {
			best, bestDist = o, d
		}
	}
	return best
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/config/`
Expected: PASS

- [ ] **Step 6: Checkpoint.** Commit only if the user has asked: `git add go.mod go.sum .gitignore internal/config && git commit -m "feat(config): parse and validate agentbox.yaml"`

---

### Task 2: `envfile` package

**Files:**
- Create: `internal/envfile/envfile.go`
- Test: `internal/envfile/envfile_test.go`

**Interfaces:**
- Produces:
  - `type envfile.KV struct { Key, Value string }`
  - `func envfile.Render(template []byte, vars []KV) []byte`. It rewrites managed keys in place (preserving comments, order and `export `), appends missing keys under `# added by agentbox`, and on duplicate keys in `vars` the later value wins while keeping the first key's position.
  - `func envfile.Parse(data []byte) []KV` (strips matching quotes and trailing ` # comment` on unquoted values)
  - `func envfile.Pairs(vars []KV) []string` (`K=V` strings for `exec.Cmd.Env`)
  - `func envfile.Quote(v string) string`

- [ ] **Step 1: Write the failing test**

```go
package envfile

import (
	"reflect"
	"testing"
)

func TestRenderRewritesInPlaceAndAppends(t *testing.T) {
	tmpl := "# app settings\nAPP_PORT=3000\n\nexport DATABASE_URL=postgres://x/app # main db\nSECRET=keep-me\n"
	got := string(Render([]byte(tmpl), []KV{
		{"APP_PORT", "20001"},
		{"DATABASE_URL", "postgres://x/app_agentbox_a"},
		{"AGENTBOX_NAME", "a"},
	}))
	want := "# app settings\nAPP_PORT=20001\n\nexport DATABASE_URL=postgres://x/app_agentbox_a\nSECRET=keep-me\n\n# added by agentbox\nAGENTBOX_NAME=a\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderLaterVarWins(t *testing.T) {
	got := string(Render(nil, []KV{{"A", "1"}, {"B", "2"}, {"A", "3"}}))
	want := "# added by agentbox\nA=3\nB=2\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRenderReplacesDuplicateTemplateLines(t *testing.T) {
	got := string(Render([]byte("A=1\nA=2\n"), []KV{{"A", "9"}}))
	if got != "A=9\nA=9\n" {
		t.Fatalf("got %q", got)
	}
}

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"plain":       "plain",
		"":            `""`,
		"has space":   "'has space'",
		"p@ss#word":   "'p@ss#word'",
		"it's":        `"it's"`,
		"line\nbreak": `"line\nbreak"`,
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestParse(t *testing.T) {
	got := Parse([]byte("# c\nA=1\nexport B=\"two words\"\nC='x' \nD=val # comment\r\nnot a line\n"))
	want := []KV{{"A", "1"}, {"B", "two words"}, {"C", "x"}, {"D", "val"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPairs(t *testing.T) {
	got := Pairs([]KV{{"A", "1"}, {"B", "x y"}})
	if !reflect.DeepEqual(got, []string{"A=1", "B=x y"}) {
		t.Fatalf("got %v", got)
	}
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./internal/envfile/`
Expected: FAIL (`undefined: Render`).

- [ ] **Step 3: Implement** `internal/envfile/envfile.go`

```go
// Package envfile renders a box's .env file from a template and agentbox-managed variables.
package envfile

import (
	"bytes"
	"regexp"
	"strings"
)

type KV struct{ Key, Value string }

var lineKey = regexp.MustCompile(`^(\s*)(export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=`)

// Render rewrites managed keys in the template and appends the ones it doesn't contain.
func Render(template []byte, vars []KV) []byte {
	var order []string
	vals := map[string]string{}
	for _, kv := range vars {
		if _, ok := vals[kv.Key]; !ok {
			order = append(order, kv.Key)
		}
		vals[kv.Key] = kv.Value
	}

	var out bytes.Buffer
	written := map[string]bool{}
	if text := string(template); text != "" {
		for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
			if m := lineKey.FindStringSubmatch(line); m != nil {
				if v, ok := vals[m[3]]; ok {
					out.WriteString(m[1] + m[2] + m[3] + "=" + Quote(v) + "\n")
					written[m[3]] = true
					continue
				}
			}
			out.WriteString(line + "\n")
		}
	}

	var missing []string
	for _, k := range order {
		if !written[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString("# added by agentbox\n")
		for _, k := range missing {
			out.WriteString(k + "=" + Quote(vals[k]) + "\n")
		}
	}
	return out.Bytes()
}

// Quote formats a value so dotenv loaders, Docker and shells all read it back unchanged.
func Quote(v string) string {
	if v == "" {
		return `""`
	}
	if !strings.ContainsAny(v, " \t\"'#$\\`\n") {
		return v
	}
	if !strings.ContainsAny(v, "'\n") {
		return "'" + v + "'"
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(v) + `"`
}

// Parse reads KEY=VALUE lines, ignoring comments and anything that isn't an assignment.
func Parse(data []byte) []KV {
	var out []KV
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		m := lineKey.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v := strings.TrimSpace(line[len(m[0]):])
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		out = append(out, KV{m[3], v})
	}
	return out
}

// Pairs returns KEY=VALUE strings for exec.Cmd.Env.
func Pairs(vars []KV) []string {
	out := make([]string, 0, len(vars))
	for _, kv := range vars {
		out = append(out, kv.Key+"="+kv.Value)
	}
	return out
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/envfile/`
Expected: PASS

- [ ] **Step 5: Checkpoint.** Commit only if asked: `git add internal/envfile && git commit -m "feat(envfile): render box env files from templates"`

---

### Task 3: `state` package (SQLite store + port allocation)

**Files:**
- Create: `internal/state/state.go`
- Test: `internal/state/state_test.go`

**Interfaces:**
- Produces:
  - `type state.BoxState string`, constants `Creating, Ready, Running, Stopped, Removing, Failed`
  - `var state.ErrNotFound, state.ErrExists error`
  - `type state.Repo struct { ID, Path string }`
  - `type state.Box struct { RepoID, Name, Dir, Branch string; State BoxState; DBName, DBURL string; PID int; LastStep, LastError string; CreatedAt time.Time; Ports map[string]int }` with `func (b Box) ID() string` → `"<repoID>/<name>"`
  - `type state.PortRow struct { Port int; RepoID, Box, EnvName string }`
  - `func state.RepoSlug(path string) string`
  - `func state.Open(path string) (*Store, error)`, `(*Store).Close() error`
  - `(*Store).UpsertRepo(path string) (Repo, error)`, `RepoByID(id string) (Repo, error)`, `Repos() ([]Repo, error)`
  - `(*Store).InsertBox(b Box) error` (ErrExists on duplicate), `UpdateBox(b Box) error` (ErrNotFound), `GetBox(repoID, name string) (Box, error)`, `ListBoxes(repoID string) ([]Box, error)` (`""` = all repos), `DeleteBox(repoID, name string) error` (also frees ports)
  - `(*Store).AllocatePorts(repoID, box string, names []string, lo, hi int, isFree func(int) bool) (map[string]int, error)`, `FreePorts(repoID, box string) error`, `AllPorts() ([]PortRow, error)`

- [ ] **Step 1: Add the dependency**

Run: `go get modernc.org/sqlite`

- [ ] **Step 2: Write the failing test**

```go
package state

import (
	"errors"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func always(int) bool { return true }

func TestRepoSlug(t *testing.T) {
	a := RepoSlug("/Users/me/My App")
	if !regexp.MustCompile(`^my-app-[0-9a-f]{6}$`).MatchString(a) {
		t.Fatalf("slug %q", a)
	}
	if a != RepoSlug("/Users/me/My App") {
		t.Fatal("slug not stable")
	}
	if a == RepoSlug("/Users/other/My App") {
		t.Fatal("different paths gave the same slug")
	}
}

func TestRepos(t *testing.T) {
	s := openTest(t)
	r, err := s.UpsertRepo("/x/app")
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := s.UpsertRepo("/x/app"); again != r {
		t.Fatalf("upsert not idempotent: %v vs %v", again, r)
	}
	got, err := s.RepoByID(r.ID)
	if err != nil || got.Path != "/x/app" {
		t.Fatalf("RepoByID = %v, %v", got, err)
	}
	if _, err := s.RepoByID("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestBoxCRUD(t *testing.T) {
	s := openTest(t)
	b := Box{RepoID: "r", Name: "a", Dir: "/d/a", Branch: "agentbox/a", State: Creating, CreatedAt: time.Now()}
	if err := s.InsertBox(b); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertBox(b); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate insert err = %v", err)
	}
	b.State, b.PID, b.DBName = Running, 42, "app_agentbox_a"
	if err := s.UpdateBox(b); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBox("r", "a")
	if err != nil || got.State != Running || got.PID != 42 || got.DBName != "app_agentbox_a" || got.ID() != "r/a" {
		t.Fatalf("GetBox = %+v, %v", got, err)
	}
	list, _ := s.ListBoxes("r")
	all, _ := s.ListBoxes("")
	if len(list) != 1 || len(all) != 1 {
		t.Fatalf("list %d all %d", len(list), len(all))
	}
	if err := s.UpdateBox(Box{RepoID: "r", Name: "zz"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing err = %v", err)
	}
	if err := s.DeleteBox("r", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetBox("r", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestAllocatePortsSkipsBusyAndReuses(t *testing.T) {
	s := openTest(t)
	busy := func(p int) bool { return p != 20000 }
	got, err := s.AllocatePorts("r", "a", []string{"APP_PORT", "API_PORT"}, 20000, 20100, busy)
	if err != nil {
		t.Fatal(err)
	}
	if got["APP_PORT"] != 20001 || got["API_PORT"] != 20002 {
		t.Fatalf("got %v", got)
	}
	b, _ := s.AllocatePorts("r", "b", []string{"APP_PORT"}, 20000, 20100, busy)
	if b["APP_PORT"] != 20003 {
		t.Fatalf("second box got %v", b)
	}
	if err := s.FreePorts("r", "a"); err != nil {
		t.Fatal(err)
	}
	c, _ := s.AllocatePorts("r", "c", []string{"P"}, 20000, 20100, busy)
	if c["P"] != 20001 {
		t.Fatalf("freed port not reused: %v", c)
	}
}

func TestAllocatePortsExhausted(t *testing.T) {
	s := openTest(t)
	if _, err := s.AllocatePorts("r", "a", []string{"A", "B", "C"}, 20000, 20001, always); err == nil {
		t.Fatal("expected exhaustion error")
	}
	if rows, _ := s.AllPorts(); len(rows) != 0 {
		t.Fatalf("partial allocation leaked: %v", rows)
	}
}

func TestAllocatePortsConcurrent(t *testing.T) {
	s := openTest(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[int]bool{}
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.AllocatePorts("r", string(rune('a'+i)), []string{"A", "B"}, 20000, 20100, always)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, p := range got {
				if seen[p] {
					t.Errorf("port %d handed out twice", p)
				}
				seen[p] = true
			}
		}()
	}
	wg.Wait()
	if len(seen) != 20 {
		t.Fatalf("allocated %d ports, want 20", len(seen))
	}
}

func TestDeleteBoxFreesPorts(t *testing.T) {
	s := openTest(t)
	_ = s.InsertBox(Box{RepoID: "r", Name: "a", State: Ready, CreatedAt: time.Now()})
	_, _ = s.AllocatePorts("r", "a", []string{"A"}, 20000, 20100, always)
	b, _ := s.GetBox("r", "a")
	if b.Ports["A"] != 20000 {
		t.Fatalf("ports not loaded: %v", b.Ports)
	}
	_ = s.DeleteBox("r", "a")
	if rows, _ := s.AllPorts(); len(rows) != 0 {
		t.Fatalf("ports left: %v", rows)
	}
}
```

- [ ] **Step 3: Run it to confirm it fails**

Run: `go test ./internal/state/`
Expected: FAIL (`undefined: Open`).

- [ ] **Step 4: Implement** `internal/state/state.go`

```go
// Package state persists repos, boxes and port reservations in ~/.agentbox/state.db.
package state

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type BoxState string

const (
	Creating BoxState = "creating"
	Ready    BoxState = "ready"
	Running  BoxState = "running"
	Stopped  BoxState = "stopped"
	Removing BoxState = "removing"
	Failed   BoxState = "failed"
)

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
)

type Repo struct {
	ID   string
	Path string
}

type Box struct {
	RepoID    string
	Name      string
	Dir       string
	Branch    string
	State     BoxState
	DBName    string
	DBURL     string
	PID       int
	LastStep  string
	LastError string
	CreatedAt time.Time
	Ports     map[string]int
}

func (b Box) ID() string { return b.RepoID + "/" + b.Name }

type PortRow struct {
	Port    int
	RepoID  string
	Box     string
	EnvName string
}

const schema = `
CREATE TABLE IF NOT EXISTS repos (
	id   TEXT PRIMARY KEY,
	path TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS boxes (
	repo_id    TEXT NOT NULL,
	name       TEXT NOT NULL,
	dir        TEXT NOT NULL,
	branch     TEXT NOT NULL,
	state      TEXT NOT NULL,
	db_name    TEXT NOT NULL DEFAULT '',
	db_url     TEXT NOT NULL DEFAULT '',
	pid        INTEGER NOT NULL DEFAULT 0,
	last_step  TEXT NOT NULL DEFAULT '',
	last_error TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	PRIMARY KEY (repo_id, name)
);
CREATE TABLE IF NOT EXISTS ports (
	port     INTEGER PRIMARY KEY,
	repo_id  TEXT NOT NULL,
	box      TEXT NOT NULL,
	env_name TEXT NOT NULL
);`

// Store is safe for concurrent use. It uses a single connection, so every
// query must close its rows before issuing the next one.
type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init state db %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// RepoSlug is the stable, filesystem-safe ID of the repo at path.
func RepoSlug(path string) string {
	base := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(filepath.Base(path)), "-"), "-")
	if base == "" {
		base = "repo"
	}
	sum := sha256.Sum256([]byte(path))
	return base + "-" + hex.EncodeToString(sum[:])[:6]
}

func (s *Store) UpsertRepo(path string) (Repo, error) {
	r := Repo{ID: RepoSlug(path), Path: path}
	_, err := s.db.Exec(`INSERT INTO repos(id, path) VALUES(?, ?) ON CONFLICT(id) DO NOTHING`, r.ID, r.Path)
	return r, err
}

func (s *Store) RepoByID(id string) (Repo, error) {
	var r Repo
	err := s.db.QueryRow(`SELECT id, path FROM repos WHERE id = ?`, id).Scan(&r.ID, &r.Path)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func (s *Store) Repos() ([]Repo, error) {
	rows, err := s.db.Query(`SELECT id, path FROM repos ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		var r Repo
		if err := rows.Scan(&r.ID, &r.Path); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const boxCols = `repo_id, name, dir, branch, state, db_name, db_url, pid, last_step, last_error, created_at`

func (s *Store) InsertBox(b Box) error {
	_, err := s.db.Exec(`INSERT INTO boxes(`+boxCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		b.RepoID, b.Name, b.Dir, b.Branch, string(b.State), b.DBName, b.DBURL, b.PID, b.LastStep, b.LastError, b.CreatedAt.Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrExists
	}
	return err
}

func (s *Store) UpdateBox(b Box) error {
	res, err := s.db.Exec(`UPDATE boxes SET dir=?, branch=?, state=?, db_name=?, db_url=?, pid=?, last_step=?, last_error=?
		WHERE repo_id=? AND name=?`,
		b.Dir, b.Branch, string(b.State), b.DBName, b.DBURL, b.PID, b.LastStep, b.LastError, b.RepoID, b.Name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface{ Scan(dest ...any) error }

func scanBox(sc scanner) (Box, error) {
	var b Box
	var st string
	var created int64
	err := sc.Scan(&b.RepoID, &b.Name, &b.Dir, &b.Branch, &st, &b.DBName, &b.DBURL, &b.PID, &b.LastStep, &b.LastError, &created)
	b.State = BoxState(st)
	b.CreatedAt = time.Unix(created, 0)
	return b, err
}

func (s *Store) GetBox(repoID, name string) (Box, error) {
	b, err := scanBox(s.db.QueryRow(`SELECT `+boxCols+` FROM boxes WHERE repo_id=? AND name=?`, repoID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Box{}, ErrNotFound
	}
	if err != nil {
		return Box{}, err
	}
	b.Ports, err = s.boxPorts(repoID, name)
	return b, err
}

func (s *Store) ListBoxes(repoID string) ([]Box, error) {
	q := `SELECT ` + boxCols + ` FROM boxes`
	var args []any
	if repoID != "" {
		q += ` WHERE repo_id=?`
		args = append(args, repoID)
	}
	rows, err := s.db.Query(q+` ORDER BY repo_id, name`, args...)
	if err != nil {
		return nil, err
	}
	var out []Box
	for rows.Next() {
		b, err := scanBox(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Ports, err = s.boxPorts(out[i].RepoID, out[i].Name); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) DeleteBox(repoID, name string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM boxes WHERE repo_id=? AND name=?`, repoID, name); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM ports WHERE repo_id=? AND box=?`, repoID, name); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) boxPorts(repoID, name string) (map[string]int, error) {
	rows, err := s.db.Query(`SELECT env_name, port FROM ports WHERE repo_id=? AND box=?`, repoID, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var n string
		var p int
		if err := rows.Scan(&n, &p); err != nil {
			return nil, err
		}
		out[n] = p
	}
	return out, rows.Err()
}

// AllocatePorts reserves one free port per name, atomically. isFree is
// consulted for ports not already reserved, so ports in use by other
// programs are skipped without being recorded.
func (s *Store) AllocatePorts(repoID, box string, names []string, lo, hi int, isFree func(int) bool) (map[string]int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	used := map[int]bool{}
	rows, err := tx.Query(`SELECT port FROM ports`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		used[p] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}

	out := map[string]int{}
	p := lo
	for _, name := range names {
		for p <= hi && (used[p] || !isFree(p)) {
			p++
		}
		if p > hi {
			return nil, fmt.Errorf("no free ports left in range %d-%d", lo, hi)
		}
		if _, err := tx.Exec(`INSERT INTO ports(port, repo_id, box, env_name) VALUES(?,?,?,?)`, p, repoID, box, name); err != nil {
			return nil, err
		}
		out[name] = p
		used[p] = true
		p++
	}
	return out, tx.Commit()
}

func (s *Store) FreePorts(repoID, box string) error {
	_, err := s.db.Exec(`DELETE FROM ports WHERE repo_id=? AND box=?`, repoID, box)
	return err
}

func (s *Store) AllPorts() ([]PortRow, error) {
	rows, err := s.db.Query(`SELECT port, repo_id, box, env_name FROM ports ORDER BY port`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PortRow
	for rows.Next() {
		var r PortRow
		if err := rows.Scan(&r.Port, &r.RepoID, &r.Box, &r.EnvName); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
```

- [ ] **Step 5: Run the tests (with the race detector)**

Run: `go test -race ./internal/state/`
Expected: PASS

- [ ] **Step 6: Checkpoint.** Commit only if asked: `git add go.mod go.sum internal/state && git commit -m "feat(state): sqlite store with atomic port allocation"`

---

### Task 4: `ports` package

**Files:**
- Create: `internal/ports/ports.go`
- Test: `internal/ports/ports_test.go`

**Interfaces:**
- Produces: `const ports.DefaultLo = 20000`, `const ports.DefaultHi = 29999`, `func ports.IsFree(port int) bool`

- [ ] **Step 1: Write the failing test**

```go
package ports

import (
	"net"
	"testing"
)

func TestIsFreeLoopbackListener(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if IsFree(port) {
		t.Fatalf("port %d reported free while listening", port)
	}
	l.Close()
	if !IsFree(port) {
		t.Fatalf("port %d reported busy after close", port)
	}
}

func TestIsFreeWildcardListener(t *testing.T) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if IsFree(l.Addr().(*net.TCPAddr).Port) {
		t.Fatal("wildcard listener not detected")
	}
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./internal/ports/`
Expected: FAIL (`undefined: IsFree`).

- [ ] **Step 3: Implement** `internal/ports/ports.go`

```go
// Package ports checks whether local TCP ports are available.
package ports

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
)

const (
	DefaultLo = 20000
	DefaultHi = 29999
)

// IsFree reports whether nothing is listening on port on this machine. It
// both dials and binds, because on macOS a bind can succeed next to an
// existing wildcard listener.
func IsFree(port int) bool {
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond); err == nil {
		c.Close()
		return false
	}
	for _, addr := range []string{fmt.Sprintf("127.0.0.1:%d", port), fmt.Sprintf(":%d", port), fmt.Sprintf("[::1]:%d", port)} {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			if errors.Is(err, syscall.EADDRINUSE) {
				return false
			}
			continue // e.g. no IPv6 loopback on this host
		}
		l.Close()
	}
	return true
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/ports/`
Expected: PASS

- [ ] **Step 5: Checkpoint.** Commit only if asked: `git add internal/ports && git commit -m "feat(ports): detect free local ports"`

---

### Task 5: `testutil` helpers + `worktree` git wrapper

**Files:**
- Create: `internal/testutil/git.go`, `internal/worktree/worktree.go`
- Test: `internal/worktree/worktree_test.go`

**Interfaces:**
- Produces (testutil, used by every later test):
  - `func testutil.Git(t testing.TB, dir string, args ...string) string`
  - `func testutil.WriteFiles(t testing.TB, dir string, files map[string]string)`
  - `func testutil.InitRepo(t testing.TB, dir string) string` (git init on `main`, commit everything; returns the symlink-resolved path)
  - `func testutil.NewRepo(t testing.TB, files map[string]string) string`, `func testutil.NewRepoIn(t testing.TB, dir string, files map[string]string) string`
- Produces (worktree):
  - `var worktree.ErrBranchExists error`; `type worktree.ConflictError struct { Files []string }`
  - `MainWorktree(dir string) (string, error)`, the main checkout root (symlink-resolved) from any dir inside the repo or any of its worktrees
  - `Add(repo, dir, branch, from string) error` (`from == ""` means HEAD), `Remove(repo, dir string) error`, `Prune(repo string) error`, `DeleteBranch(repo, branch string) error`, `BranchExists(repo, branch string) bool`
  - `CurrentBranch(dir string) (string, error)`, `Changes(dir string, untracked bool) ([]string, error)` (repo-relative, slash-separated)
  - `AheadBehind(repo, base, branch string) (ahead, behind int, err error)`
  - `Merge(repo, branch string, squash bool, message string) error`. On conflict it aborts and returns `*ConflictError`.
  - `Diff(dir, base string) (string, error)` (committed + uncommitted tracked changes since the merge-base with base)
  - `ListBranches(repo, prefix string) ([]string, error)`, `List(repo string) ([]string, error)` (worktree paths)
  - `MergeIsNoop(repo, base, branch string) bool`: merging branch into base would change nothing (e.g. it was already squash-merged)

- [ ] **Step 1: Write the test helpers** at `internal/testutil/git.go`

```go
// Package testutil holds helpers shared by tests.
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var gitEnv = []string{
	"GIT_CONFIG_NOSYSTEM=1",
	"GIT_AUTHOR_NAME=agentbox-test", "GIT_AUTHOR_EMAIL=test@example.com",
	"GIT_COMMITTER_NAME=agentbox-test", "GIT_COMMITTER_EMAIL=test@example.com",
}

// Git runs git in dir and fails the test on error.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// WriteFiles writes name → content pairs under dir, creating parent dirs.
func WriteFiles(t testing.TB, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// InitRepo turns dir into a git repo on branch main with everything in it
// committed, and returns dir with symlinks resolved.
func InitRepo(t testing.TB, dir string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	Git(t, dir, "init", "-q", "-b", "main")
	Git(t, dir, "config", "user.email", "test@example.com")
	Git(t, dir, "config", "user.name", "agentbox-test")
	Git(t, dir, "config", "commit.gpgsign", "false")
	Git(t, dir, "add", "-A")
	Git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

// NewRepo creates a committed repo containing files in a fresh temp dir.
func NewRepo(t testing.TB, files map[string]string) string {
	t.Helper()
	return NewRepoIn(t, t.TempDir(), files)
}

// NewRepoIn creates a committed repo containing files at dir.
func NewRepoIn(t testing.TB, dir string, files map[string]string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	WriteFiles(t, dir, files)
	return InitRepo(t, dir)
}
```

- [ ] **Step 2: Write the failing test** at `internal/worktree/worktree_test.go`

```go
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
```

- [ ] **Step 3: Run it to confirm it fails**

Run: `go test ./internal/worktree/`
Expected: FAIL (`undefined: Add`, …).

- [ ] **Step 4: Implement** `internal/worktree/worktree.go`

```go
// Package worktree wraps the git CLI for worktree, branch, status and merge operations.
package worktree

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var ErrBranchExists = errors.New("branch already exists")

// ConflictError reports a merge that was aborted because of conflicts.
type ConflictError struct{ Files []string }

func (e *ConflictError) Error() string { return "merge conflict in: " + strings.Join(e.Files, ", ") }

func gitRaw(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	slog.Debug("git", "dir", dir, "args", args)
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

func git(dir string, args ...string) (string, error) {
	out, err := gitRaw(dir, args...)
	return strings.TrimSpace(out), err
}

// MainWorktree returns the root of the main checkout for any directory inside
// the repo or inside one of its linked worktrees (such as a box).
func MainWorktree(dir string) (string, error) {
	common, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git repository", dir)
	}
	if filepath.Base(common) != ".git" {
		return "", fmt.Errorf("%s: bare repositories and custom git dirs are not supported", dir)
	}
	return filepath.EvalSymlinks(filepath.Dir(common))
}

func BranchExists(repo, branch string) bool {
	_, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// Add creates a worktree at dir on a new branch started from `from` (HEAD if empty).
func Add(repo, dir, branch, from string) error {
	if BranchExists(repo, branch) {
		return fmt.Errorf("%w: %s", ErrBranchExists, branch)
	}
	if from == "" {
		from = "HEAD"
	}
	_, err := git(repo, "worktree", "add", "-q", "-b", branch, dir, from)
	return err
}

// Remove deletes the worktree at dir, discarding its changes. A worktree
// whose directory is already gone is pruned instead.
func Remove(repo, dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return Prune(repo)
	}
	_, err := git(repo, "worktree", "remove", "--force", "--force", dir)
	return err
}

func Prune(repo string) error {
	_, err := git(repo, "worktree", "prune")
	return err
}

func DeleteBranch(repo, branch string) error {
	_, err := git(repo, "branch", "-D", branch)
	return err
}

func CurrentBranch(dir string) (string, error) {
	return git(dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// Changes lists changed paths relative to the worktree root. Untracked files
// are listed individually when untracked is true.
func Changes(dir string, untracked bool) ([]string, error) {
	mode := "--untracked-files=no"
	if untracked {
		mode = "--untracked-files=all"
	}
	out, err := gitRaw(dir, "status", "--porcelain", "-z", mode)
	if err != nil {
		return nil, err
	}
	var paths []string
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		paths = append(paths, e[3:])
		if e[0] == 'R' || e[0] == 'C' {
			i++ // skip the rename/copy source path
		}
	}
	return paths, nil
}

// AheadBehind counts commits on branch that base lacks (ahead) and vice versa.
func AheadBehind(repo, base, branch string) (ahead, behind int, err error) {
	out, err := git(repo, "rev-list", "--left-right", "--count", base+"..."+branch)
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output %q", out)
	}
	behind, _ = strconv.Atoi(f[0])
	ahead, _ = strconv.Atoi(f[1])
	return ahead, behind, nil
}

// Merge merges branch into the branch checked out in repo. On conflict the
// merge is aborted, the checkout is left as it was, and *ConflictError is returned.
func Merge(repo, branch string, squash bool, message string) error {
	var err error
	if squash {
		_, err = git(repo, "merge", "--squash", branch)
	} else {
		_, err = git(repo, "merge", "--no-edit", branch)
	}
	if err != nil {
		files, _ := git(repo, "diff", "--name-only", "--diff-filter=U")
		_, _ = git(repo, "reset", "--merge")
		if files != "" {
			return &ConflictError{Files: strings.Split(files, "\n")}
		}
		return err
	}
	if !squash {
		return nil
	}
	if _, err := git(repo, "diff", "--cached", "--quiet"); err == nil {
		return nil // nothing to commit
	}
	if _, err := git(repo, "commit", "-q", "-m", message); err != nil {
		_, _ = git(repo, "reset", "--merge")
		return err
	}
	return nil
}

// Diff shows tracked changes in dir (committed or not) since it diverged from base.
func Diff(dir, base string) (string, error) {
	mb, err := git(dir, "merge-base", base, "HEAD")
	if err != nil {
		return "", err
	}
	return gitRaw(dir, "diff", mb)
}

func ListBranches(repo, prefix string) ([]string, error) {
	out, err := git(repo, "for-each-ref", "--format=%(refname:short)", "refs/heads/"+prefix)
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// MergeIsNoop reports whether merging branch into base would change nothing,
// e.g. because the branch was already squash-merged.
func MergeIsNoop(repo, base, branch string) bool {
	out, err := git(repo, "merge-tree", "--write-tree", base, branch)
	if err != nil {
		return false // conflicts (exit 1) or an old git
	}
	tree, _, _ := strings.Cut(out, "\n")
	baseTree, err := git(repo, "rev-parse", base+"^{tree}")
	return err == nil && tree == baseTree
}

// List returns the paths of all worktrees of repo, the main checkout first.
func List(repo string) ([]string, error) {
	out, err := git(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			paths = append(paths, p)
		}
	}
	return paths, nil
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/worktree/`
Expected: PASS

- [ ] **Step 6: Checkpoint.** Commit only if asked: `git add internal/testutil internal/worktree && git commit -m "feat(worktree): git worktree, status and merge wrapper"`

---

### Task 6: `errs` and `events` packages

**Files:**
- Create: `internal/errs/errs.go`, `internal/events/events.go`
- Test: `internal/errs/errs_test.go`, `internal/events/events_test.go`

**Interfaces:**
- Produces (errs):
  - `type errs.Code string`, constants `NotFound, Exists, Invalid, UnmergedWork, Precondition, Conflict, Internal`
  - `type errs.E struct { Code Code; Message, Step, Hint string }` (JSON tags `code,message,step,hint`) implementing `error`
  - `func errs.New(code Code, format string, a ...any) *E`, `(*E).WithHint(h string) *E`, `(*E).WithStep(s string) *E` (both return copies)
  - `func errs.From(err error) *E`. It finds an `*E` anywhere in the chain, and otherwise wraps the error as `Internal`.
- Produces (events):
  - `type events.Event struct { Type, Box string; Data any }` (JSON `type,box,data`)
  - constants `BoxCreated = "box.created"`, `BoxState = "box.state"`, `BoxStep = "box.step"`, `BoxRemoved = "box.removed"`
  - `func events.NewBus() *Bus`, `(*Bus).Publish(e Event)` (never blocks; drops for full subscribers), `(*Bus).Subscribe() (<-chan Event, func())`

- [ ] **Step 1: Write the failing tests**

`internal/errs/errs_test.go`:

```go
package errs

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorFormatting(t *testing.T) {
	e := New(Precondition, "role %q lacks CREATEDB", "app").WithStep("db").WithHint("ALTER ROLE app CREATEDB;")
	want := "db failed: role \"app\" lacks CREATEDB\nhint: ALTER ROLE app CREATEDB;"
	if e.Error() != want {
		t.Fatalf("got %q", e.Error())
	}
}

func TestWithHintCopies(t *testing.T) {
	base := New(Invalid, "x")
	_ = base.WithHint("h")
	if base.Hint != "" {
		t.Fatal("WithHint mutated the receiver")
	}
}

func TestFrom(t *testing.T) {
	if e := From(errors.New("boom")); e.Code != Internal || e.Message != "boom" {
		t.Fatalf("plain error: %+v", e)
	}
	wrapped := fmt.Errorf("context: %w", New(NotFound, "no box"))
	if e := From(wrapped); e.Code != NotFound || e.Message != "no box" {
		t.Fatalf("wrapped: %+v", e)
	}
}
```

`internal/events/events_test.go`:

```go
package events

import (
	"testing"
	"time"
)

func TestPublishSubscribe(t *testing.T) {
	b := NewBus()
	ch, cancel := b.Subscribe()
	b.Publish(Event{Type: BoxState, Box: "r/a"})
	select {
	case ev := <-ch:
		if ev.Box != "r/a" || ev.Type != BoxState {
			t.Fatalf("got %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	cancel()
	cancel() // idempotent
	b.Publish(Event{Type: BoxState})
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after cancel")
	}
}

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	b := NewBus()
	_, cancel := b.Subscribe()
	defer cancel()
	done := make(chan struct{})
	go func() {
		for range 10_000 {
			b.Publish(Event{Type: BoxStep})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a subscriber that never reads")
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/errs/ ./internal/events/`
Expected: FAIL (undefined identifiers).

- [ ] **Step 3: Implement** `internal/errs/errs.go`

```go
// Package errs defines the user-facing error type shared by the daemon, API and CLI.
package errs

import (
	"errors"
	"fmt"
)

type Code string

const (
	NotFound     Code = "not_found"
	Exists       Code = "exists"
	Invalid      Code = "invalid"
	UnmergedWork Code = "unmerged_work"
	Precondition Code = "precondition"
	Conflict     Code = "conflict"
	Internal     Code = "internal"
)

// E says which step failed, why, and what the user can do about it.
type E struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	Step    string `json:"step,omitempty"`
	Hint    string `json:"hint,omitempty"`
}

func New(code Code, format string, a ...any) *E {
	return &E{Code: code, Message: fmt.Sprintf(format, a...)}
}

func (e *E) Error() string {
	s := e.Message
	if e.Step != "" {
		s = e.Step + " failed: " + s
	}
	if e.Hint != "" {
		s += "\nhint: " + e.Hint
	}
	return s
}

func (e *E) WithHint(h string) *E { c := *e; c.Hint = h; return &c }

func (e *E) WithStep(s string) *E { c := *e; c.Step = s; return &c }

// From returns the *E in err's chain, or wraps err as an Internal error.
func From(err error) *E {
	var e *E
	if errors.As(err, &e) {
		return e
	}
	return &E{Code: Internal, Message: err.Error()}
}
```

- [ ] **Step 4: Implement** `internal/events/events.go`

```go
// Package events is the daemon's in-process pub/sub used for live UI updates.
package events

import "sync"

const (
	BoxCreated = "box.created"
	BoxState   = "box.state"
	BoxStep    = "box.step"
	BoxRemoved = "box.removed"
)

type Event struct {
	Type string `json:"type"`
	Box  string `json:"box"`
	Data any    `json:"data,omitempty"`
}

type Bus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewBus() *Bus { return &Bus{subs: map[chan Event]struct{}{}} }

// Publish delivers e to every subscriber that has room; it never blocks.
func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Subscribe returns a channel of future events and an idempotent cancel func.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
	}
}
```

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/errs/ ./internal/events/`
Expected: PASS

- [ ] **Step 6: Checkpoint.** Commit only if asked: `git add internal/errs internal/events && git commit -m "feat: user-facing errors and event bus"`

---

### Task 7: `proc` package (logs + supervisor)

**Files:**
- Create: `internal/proc/ring.go`, `internal/proc/rotate.go`, `internal/proc/log.go`, `internal/proc/proc.go`, `internal/proc/sysproc_linux.go`, `internal/proc/sysproc_other.go`
- Test: `internal/proc/proc_test.go`

**Interfaces:**
- Produces:
  - `func proc.NewRing(max int) *Ring`. `*Ring` implements `io.Writer`, with `Tail(n int) []string`, `Subscribe() (<-chan string, func())` and `CloseSubs()`.
  - `func proc.OpenLog(path string) (*Log, error)`. `*Log` implements `io.Writer`, has field `Ring *Ring`, and has `Close() error`. The file is rotated at 10 MB with 3 kept, and the ring holds 2,000 lines.
  - `func proc.TailFile(path string, n int) ([]string, error)` (missing file → `nil, nil`)
  - `type proc.Spec struct { Command, Dir string; Env []string; Out io.Writer }`
  - `type proc.Options struct { Backoff []time.Duration; CrashWindow time.Duration; MaxCrashes int; StopTimeout time.Duration }`, `var proc.DefaultOptions`
  - `type proc.Status struct { Running bool; PID, Restarts int; LastError string; GaveUp bool }`
  - `func proc.New(spec Spec, opts Options, onChange func(Status)) *Proc`, `(*Proc).Start() error` (single use), `(*Proc).Stop() error` (idempotent), `(*Proc).Status() Status`

- [ ] **Step 1: Write the failing test** at `internal/proc/proc_test.go`

```go
package proc

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var fast = Options{Backoff: []time.Duration{10 * time.Millisecond}, CrashWindow: 5 * time.Second, MaxCrashes: 3, StopTimeout: 2 * time.Second}

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

func dead(pid int) bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }

func TestRingKeepsLastLinesAndJoinsPartialWrites(t *testing.T) {
	r := NewRing(3)
	r.Write([]byte("one\ntw"))
	r.Write([]byte("o\nthree\nfour\n"))
	if got := r.Tail(10); !slices.Equal(got, []string{"two", "three", "four"}) {
		t.Fatalf("Tail(10) = %v", got)
	}
	if got := r.Tail(1); !slices.Equal(got, []string{"four"}) {
		t.Fatalf("Tail(1) = %v", got)
	}
}

func TestRingSubscribe(t *testing.T) {
	r := NewRing(10)
	ch, cancel := r.Subscribe()
	r.Write([]byte("x\n"))
	if l := <-ch; l != "x" {
		t.Fatalf("got %q", l)
	}
	cancel()
	cancel()
	r.Write([]byte("y\n")) // must not panic
	ch2, _ := r.Subscribe()
	r.CloseSubs()
	if _, ok := <-ch2; ok {
		t.Fatal("CloseSubs should close subscriber channels")
	}
}

func TestRotatingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "box.log")
	w, err := openRotating(path, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	line := append(bytes.Repeat([]byte("x"), 49), '\n')
	for range 10 {
		if _, err := w.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	for _, suffix := range []string{"", ".1", ".2", ".3"} {
		if _, err := os.Stat(path + suffix); err != nil {
			t.Errorf("missing %s: %v", path+suffix, err)
		}
	}
	if _, err := os.Stat(path + ".4"); !os.IsNotExist(err) {
		t.Error(".4 should not exist")
	}
}

func TestLogAndTailFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "a.log")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(l, "hello\nworld\n")
	if got := l.Ring.Tail(5); !slices.Equal(got, []string{"hello", "world"}) {
		t.Fatalf("ring = %v", got)
	}
	l.Close()
	if got, _ := TailFile(path, 1); !slices.Equal(got, []string{"world"}) {
		t.Fatalf("TailFile = %v", got)
	}
	if got, err := TailFile(filepath.Join(t.TempDir(), "missing.log"), 5); got != nil || err != nil {
		t.Fatalf("missing file: %v, %v", got, err)
	}
}

func TestProcCapturesOutputAndStops(t *testing.T) {
	l, _ := OpenLog(filepath.Join(t.TempDir(), "a.log"))
	defer l.Close()
	var mu sync.Mutex
	var last Status
	p := New(Spec{Command: "echo hello; echo oops >&2; sleep 30", Dir: t.TempDir(), Env: os.Environ(), Out: l}, fast,
		func(s Status) { mu.Lock(); last = s; mu.Unlock() })
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	pid := p.Status().PID
	if pid == 0 || !p.Status().Running {
		t.Fatalf("status after start: %+v", p.Status())
	}
	eventually(t, "output", func() bool {
		tail := strings.Join(l.Ring.Tail(10), "\n")
		return strings.Contains(tail, "hello") && strings.Contains(tail, "oops")
	})
	if err := p.Start(); err == nil {
		t.Fatal("second Start should fail")
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if p.Status().Running || last.Running {
		t.Fatalf("still running: %+v / %+v", p.Status(), last)
	}
	eventually(t, "process exit", func() bool { return dead(pid) })
}

func TestStopKillsWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	p := New(Spec{Command: "sleep 300 & echo $! > child.pid; wait", Dir: dir, Env: os.Environ()}, fast, nil)
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	var child int
	eventually(t, "child pid file", func() bool {
		b, err := os.ReadFile(filepath.Join(dir, "child.pid"))
		child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && child > 0
	})
	p.Stop()
	eventually(t, "grandchild to die", func() bool { return dead(child) })
}

func TestCrashLoopGivesUp(t *testing.T) {
	p := New(Spec{Command: "exit 1", Dir: t.TempDir(), Env: os.Environ()}, fast, nil)
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "give up", func() bool { return p.Status().GaveUp })
	st := p.Status()
	if st.Restarts != 2 || !strings.Contains(st.LastError, "crashed 3 times") || st.Running {
		t.Fatalf("status %+v", st)
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestRestartsAfterCrash(t *testing.T) {
	dir := t.TempDir()
	p := New(Spec{Command: "if [ -f marker ]; then sleep 30; else touch marker; exit 1; fi", Dir: dir, Env: os.Environ()}, fast, nil)
	p.Start()
	defer p.Stop()
	eventually(t, "restart", func() bool { s := p.Status(); return s.Running && s.Restarts == 1 })
}

func TestStopBeforeStart(t *testing.T) {
	if err := New(Spec{Command: "true"}, fast, nil).Stop(); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/proc/`
Expected: FAIL (undefined identifiers).

- [ ] **Step 3: Implement** `internal/proc/ring.go`

```go
package proc

import (
	"bytes"
	"sync"
)

// Ring keeps the last max lines written to it and fans new lines out to subscribers.
type Ring struct {
	mu      sync.Mutex
	max     int
	lines   []string
	partial []byte
	subs    map[chan string]struct{}
}

func NewRing(max int) *Ring { return &Ring{max: max, subs: map[chan string]struct{}{}} }

func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partial = append(r.partial, p...)
	for {
		i := bytes.IndexByte(r.partial, '\n')
		if i < 0 {
			break
		}
		line := string(r.partial[:i])
		r.partial = r.partial[i+1:]
		r.lines = append(r.lines, line)
		if len(r.lines) > r.max {
			r.lines = r.lines[len(r.lines)-r.max:]
		}
		for ch := range r.subs {
			select {
			case ch <- line:
			default: // slow subscriber: drop rather than block the process
			}
		}
	}
	return len(p), nil
}

func (r *Ring) Tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	start := max(0, len(r.lines)-n)
	return append([]string(nil), r.lines[start:]...)
}

func (r *Ring) Subscribe() (<-chan string, func()) {
	ch := make(chan string, 256)
	r.mu.Lock()
	r.subs[ch] = struct{}{}
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if _, ok := r.subs[ch]; ok {
			delete(r.subs, ch)
			close(ch)
		}
	}
}

// CloseSubs ends every subscription, e.g. when the box's log is closed.
func (r *Ring) CloseSubs() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for ch := range r.subs {
		delete(r.subs, ch)
		close(ch)
	}
}
```

- [ ] **Step 4: Implement** `internal/proc/rotate.go`

```go
package proc

import (
	"fmt"
	"os"
	"path/filepath"
)

// rotatingFile appends to path and rotates it to path.1..path.<keep> once it exceeds max bytes.
type rotatingFile struct {
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

func openRotating(path string, max int64, keep int) (*rotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &rotatingFile{path: path, max: max, keep: keep, f: f, size: st.Size()}, nil
}

func (w *rotatingFile) Write(p []byte) (int, error) {
	if w.size > 0 && w.size+int64(len(p)) > w.max {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingFile) rotate() error {
	w.f.Close()
	for i := w.keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
	}
	_ = os.Rename(w.path, w.path+".1")
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w.f, w.size = f, 0
	return nil
}

func (w *rotatingFile) Close() error { return w.f.Close() }
```

- [ ] **Step 5: Implement** `internal/proc/log.go`

```go
package proc

import (
	"errors"
	"io"
	"os"
	"strings"
	"sync"
)

// Log is a box's output sink: a rotated file on disk plus an in-memory ring for live followers.
type Log struct {
	mu   sync.Mutex
	Ring *Ring
	file *rotatingFile
}

func OpenLog(path string) (*Log, error) {
	f, err := openRotating(path, 10<<20, 3)
	if err != nil {
		return nil, err
	}
	return &Log{Ring: NewRing(2000), file: f}, nil
}

func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.file.Write(p); err != nil {
		return 0, err
	}
	return l.Ring.Write(p)
}

func (l *Log) Close() error {
	l.Ring.CloseSubs()
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}

// TailFile returns up to the last n lines of the log file at path (reading at most 1 MB).
func TailFile(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const window = 1 << 20
	off := max(0, st.Size()-window)
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	if off > 0 {
		lines = lines[1:] // first line is probably cut in half
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}
```

- [ ] **Step 6: Implement** `internal/proc/sysproc_linux.go` and `internal/proc/sysproc_other.go`

```go
//go:build linux

package proc

import "syscall"

// Own process group so Stop can signal the whole tree; die with the daemon.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
```

```go
//go:build !linux

package proc

import "syscall"

// Own process group so Stop can signal the whole tree.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
```

- [ ] **Step 7: Implement** `internal/proc/proc.go`

```go
// Package proc supervises long-running dev-server processes and captures their output.
package proc

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type Spec struct {
	Command string // run with sh -c
	Dir     string
	Env     []string
	Out     io.Writer // receives stdout and stderr; nil discards
}

type Options struct {
	Backoff     []time.Duration // delay before restart N (last value repeats)
	CrashWindow time.Duration
	MaxCrashes  int // give up after this many crashes within CrashWindow
	StopTimeout time.Duration
}

var DefaultOptions = Options{
	Backoff:     []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second},
	CrashWindow: time.Minute,
	MaxCrashes:  5,
	StopTimeout: 10 * time.Second,
}

type Status struct {
	Running   bool
	PID       int
	Restarts  int
	LastError string
	GaveUp    bool
}

// Proc runs one command, restarting it when it exits unexpectedly. A Proc is single use.
type Proc struct {
	spec     Spec
	opts     Options
	onChange func(Status)

	mu       sync.Mutex
	status   Status
	started  bool
	stopping bool
	stop     chan struct{}
	done     chan struct{}
}

func New(spec Spec, opts Options, onChange func(Status)) *Proc {
	if onChange == nil {
		onChange = func(Status) {}
	}
	return &Proc{spec: spec, opts: opts, onChange: onChange}
}

func (p *Proc) Start() error {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return errors.New("process already started")
	}
	p.started = true
	p.stop = make(chan struct{})
	p.done = make(chan struct{})
	cmd, err := p.spawnLocked()
	if err != nil {
		close(p.done)
		p.mu.Unlock()
		return err
	}
	st := p.status
	p.mu.Unlock()
	p.onChange(st)
	go p.supervise(cmd)
	return nil
}

func (p *Proc) spawnLocked() (*exec.Cmd, error) {
	cmd := exec.Command("sh", "-c", p.spec.Command)
	cmd.Dir = p.spec.Dir
	cmd.Env = p.spec.Env
	cmd.Stdout = p.spec.Out
	cmd.Stderr = p.spec.Out
	cmd.SysProcAttr = sysProcAttr()
	cmd.WaitDelay = 2 * time.Second // don't hang on pipes held open by orphaned children
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %q: %w", p.spec.Command, err)
	}
	p.status.Running = true
	p.status.PID = cmd.Process.Pid
	return cmd, nil
}

func (p *Proc) supervise(cmd *exec.Cmd) {
	defer close(p.done)
	var crashes []time.Time
	for {
		waitErr := cmd.Wait()
		killGroup(cmd.Process.Pid, syscall.SIGKILL) // reap anything the command left behind

		p.mu.Lock()
		p.status.Running, p.status.PID = false, 0
		if p.stopping {
			st := p.status
			p.mu.Unlock()
			p.onChange(st)
			return
		}
		now := time.Now()
		crashes = append(crashes, now)
		for len(crashes) > 0 && now.Sub(crashes[0]) > p.opts.CrashWindow {
			crashes = crashes[1:]
		}
		reason := "exited with status 0"
		if waitErr != nil {
			reason = waitErr.Error()
		}
		p.status.LastError = reason
		if len(crashes) >= p.opts.MaxCrashes {
			p.status.GaveUp = true
			p.status.LastError = fmt.Sprintf("crashed %d times within %s (last: %s); not restarting", len(crashes), p.opts.CrashWindow, reason)
			st := p.status
			p.mu.Unlock()
			p.onChange(st)
			return
		}
		delay := p.opts.Backoff[min(len(crashes), len(p.opts.Backoff))-1]
		st := p.status
		p.mu.Unlock()
		p.onChange(st)

		select {
		case <-p.stop:
			return
		case <-time.After(delay):
		}

		p.mu.Lock()
		if p.stopping {
			p.mu.Unlock()
			return
		}
		next, err := p.spawnLocked()
		if err != nil {
			p.status.GaveUp, p.status.LastError = true, err.Error()
			st := p.status
			p.mu.Unlock()
			p.onChange(st)
			return
		}
		p.status.Restarts++
		st = p.status
		p.mu.Unlock()
		p.onChange(st)
		cmd = next
	}
}

// Stop sends SIGTERM to the process group, then SIGKILL after StopTimeout.
func (p *Proc) Stop() error {
	p.mu.Lock()
	if !p.started {
		p.mu.Unlock()
		return nil
	}
	done := p.done
	if !p.stopping {
		p.stopping = true
		close(p.stop)
	}
	pid := 0
	if p.status.Running {
		pid = p.status.PID
	}
	p.mu.Unlock()

	killGroup(pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(p.opts.StopTimeout):
		killGroup(pid, syscall.SIGKILL)
		<-done
	}
	return nil
}

func (p *Proc) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func killGroup(pid int, sig syscall.Signal) {
	if pid > 0 {
		_ = syscall.Kill(-pid, sig)
	}
}
```

- [ ] **Step 8: Run the tests**

Run: `go test -race ./internal/proc/`
Expected: PASS

- [ ] **Step 9: Checkpoint.** Commit only if asked: `git add internal/proc && git commit -m "feat(proc): supervised processes with rotating logs"`

---

### Task 8: `db` interface + SQLite driver

**Files:**
- Create: `internal/db/db.go`, `internal/db/sqlite.go`
- Test: `internal/db/sqlite_test.go`

**Interfaces:**
- Consumes: `config.DB` and `(*config.DB).Path()` (Task 1)
- Produces:
  - `type db.Clone struct { Name, URL string }`
  - `type db.Driver interface { Prepare(ctx) error; Clone(ctx, box, boxDir string) (Clone, error); Drop(ctx, name, boxDir string) error; Refresh(ctx) error; Check(ctx) error; List(ctx) ([]string, error) }`
  - `type db.Options struct { Terminate bool }`
  - `func db.New(cfg *config.DB, repoRoot string, opts Options) (Driver, error)`
  - `type db.BusyError struct { DB string; PIDs []int32 }`
  - `type db.SQLite struct { Root, Rel, Source string }`. The clone name is `Rel`, and the URL is `Source` verbatim (e.g. `file:./dev.db`).

- [ ] **Step 1: Write the failing test** at `internal/db/sqlite_test.go`

```go
package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentbox-dev/agentbox/internal/config"
)

func countItems(t *testing.T, path string) int {
	t.Helper()
	c, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var n int
	if err := c.QueryRow(`SELECT count(*) FROM items`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSQLiteCloneIsIndependentAndIncludesWAL(t *testing.T) {
	ctx := context.Background()
	root, boxDir := t.TempDir(), t.TempDir()
	src := filepath.Join(root, "data", "dev.db")
	os.MkdirAll(filepath.Dir(src), 0o755)
	conn, err := sql.Open("sqlite", src)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() // kept open so the row may still live only in the WAL
	for _, q := range []string{`PRAGMA journal_mode=WAL`, `CREATE TABLE items(name TEXT)`, `INSERT INTO items VALUES('one')`} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// A stale file that arrived with the worktree must be replaced, not appended to.
	os.MkdirAll(filepath.Join(boxDir, "data"), 0o755)
	os.WriteFile(filepath.Join(boxDir, "data", "dev.db"), []byte("stale"), 0o644)

	d, err := New(&config.DB{Driver: "sqlite", URLEnv: "DATABASE_URL", Source: "file:./data/dev.db"}, root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c, err := d.Clone(ctx, "a", boxDir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "data/dev.db" || c.URL != "file:./data/dev.db" {
		t.Fatalf("clone = %+v", c)
	}
	dst := filepath.Join(boxDir, "data", "dev.db")
	if n := countItems(t, dst); n != 1 {
		t.Fatalf("clone has %d rows", n)
	}
	w, _ := sql.Open("sqlite", dst)
	w.Exec(`INSERT INTO items VALUES('box only')`)
	w.Close()
	if n := countItems(t, src); n != 1 {
		t.Fatalf("write to clone leaked into source (%d rows)", n)
	}
	if err := d.Drop(ctx, c.Name, boxDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("clone file still exists after Drop")
	}
}

func TestSQLiteMissingSource(t *testing.T) {
	d, _ := New(&config.DB{Driver: "sqlite", URLEnv: "X", Source: "nope.db"}, t.TempDir(), Options{})
	_, err := d.Clone(context.Background(), "a", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "sqlite source nope.db") {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/db/`
Expected: FAIL (`undefined: New`).

- [ ] **Step 3: Implement** `internal/db/db.go`

```go
// Package db clones and drops the per-box copies of a project's database.
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/agentbox-dev/agentbox/internal/config"
)

// Clone identifies a box's database copy. URL is what goes into db.url_env.
type Clone struct {
	Name string
	URL  string
}

type Driver interface {
	// Prepare does one-time per-repo setup (e.g. building a template); cheap when already done.
	Prepare(ctx context.Context) error
	// Clone creates the copy for box. boxDir is the box's worktree (used by file-based drivers).
	Clone(ctx context.Context, box, boxDir string) (Clone, error)
	// Drop deletes the clone called name. It succeeds if the clone is already gone.
	Drop(ctx context.Context, name, boxDir string) error
	// Refresh re-snapshots the source for future clones.
	Refresh(ctx context.Context) error
	// Check verifies required tools and permissions.
	Check(ctx context.Context) error
	// List returns the names of the box clones that currently exist.
	List(ctx context.Context) ([]string, error)
}

type Options struct {
	// Terminate lets the driver kill stray connections to its template instead of refusing.
	Terminate bool
}

func New(cfg *config.DB, repoRoot string, opts Options) (Driver, error) {
	switch cfg.Driver {
	case "sqlite":
		return &SQLite{Root: repoRoot, Rel: cfg.Path(), Source: cfg.Source}, nil
	case "postgres":
		return nil, errors.New("postgres driver not implemented yet") // Task 9 replaces this line
	}
	return nil, fmt.Errorf("unknown db driver %q", cfg.Driver)
}

// BusyError means a database that must be idle has open connections.
type BusyError struct {
	DB   string
	PIDs []int32
}

func (e *BusyError) Error() string {
	return fmt.Sprintf("database %s has %d active connection(s) (pids %v)", e.DB, len(e.PIDs), e.PIDs)
}
```

- [ ] **Step 4: Implement** `internal/db/sqlite.go`

```go
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// SQLite copies the database file into the box with VACUUM INTO, which is
// consistent even while the source is open or in WAL mode.
type SQLite struct {
	Root   string // main checkout
	Rel    string // clean repo-relative path of the database file
	Source string // db.source as written in agentbox.yaml; used verbatim as the box URL
}

func (s *SQLite) src() string { return filepath.Join(s.Root, s.Rel) }

func (s *SQLite) Prepare(ctx context.Context) error { return s.Check(ctx) }

func (s *SQLite) Check(context.Context) error {
	if _, err := os.Stat(s.src()); err != nil {
		return fmt.Errorf("sqlite source %s: %w", s.Rel, err)
	}
	return nil
}

func (s *SQLite) Clone(ctx context.Context, _ string, boxDir string) (Clone, error) {
	if err := s.Check(ctx); err != nil {
		return Clone{}, err
	}
	dst := filepath.Join(boxDir, s.Rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return Clone{}, err
	}
	// A tracked database file arrives with the worktree; VACUUM INTO needs a fresh target.
	if err := s.Drop(ctx, s.Rel, boxDir); err != nil {
		return Clone{}, err
	}
	conn, err := sql.Open("sqlite", s.src())
	if err != nil {
		return Clone{}, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		return Clone{}, fmt.Errorf("copy sqlite database %s: %w", s.Rel, err)
	}
	return Clone{Name: s.Rel, URL: s.Source}, nil
}

func (s *SQLite) Drop(_ context.Context, _ string, boxDir string) error {
	if boxDir == "" {
		return nil
	}
	base := filepath.Join(boxDir, s.Rel)
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(base + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *SQLite) Refresh(context.Context) error { return nil }

// List returns nothing: SQLite clones live inside box dirs and go away with them.
func (s *SQLite) List(context.Context) ([]string, error) { return nil, nil }
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/db/`
Expected: PASS

- [ ] **Step 6: Checkpoint.** Commit only if asked: `git add internal/db && git commit -m "feat(db): driver interface and sqlite driver"`

---

### Task 9: Postgres driver

**Files:**
- Create: `internal/db/postgres.go`
- Modify: `internal/db/db.go` (postgres branch of `New`)
- Test: `internal/db/postgres_test.go`

**Interfaces:**
- Consumes: `errs.New`, `(*errs.E).WithHint` (Task 6)
- Produces: `func db.NewPostgres(source string, opts Options) (*Postgres, error)`. `*Postgres` implements `Driver`. Template: `<srcdb>_agentbox_tpl`. Box DB: `<srcdb>_agentbox_<box with - → _>`. `Drop` refuses any name that isn't a box clone of this source.

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/jackc/pgx/v5`

- [ ] **Step 2: Write the failing test** at `internal/db/postgres_test.go`

```go
package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func pgAdminURL(t *testing.T) string {
	u := os.Getenv("AGENTBOX_TEST_PG_URL")
	if u == "" {
		t.Skip("set AGENTBOX_TEST_PG_URL (postgres:// URL of a role with CREATEDB) to run Postgres tests")
	}
	return u
}

func withDB(t *testing.T, raw, name string) string {
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// newSource creates a throwaway source database with one row in items.
func newSource(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	admin := pgAdminURL(t)
	name := fmt.Sprintf("abtest_%d", time.Now().UnixNano()%1_000_000_000)
	c, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		rows, _ := c.Query(ctx, "SELECT datname FROM pg_database WHERE starts_with(datname, $1)", name)
		names, _ := pgx.CollectRows(rows, pgx.RowTo[string])
		for _, n := range names {
			_, _ = c.Exec(ctx, "DROP DATABASE IF EXISTS "+ident(n)+" WITH (FORCE)")
		}
		c.Close(ctx)
	})
	src := withDB(t, admin, name)
	sc, err := pgx.Connect(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close(ctx)
	if _, err := sc.Exec(ctx, "CREATE TABLE items(name text); INSERT INTO items VALUES ('one')"); err != nil {
		t.Fatal(err)
	}
	return src
}

func pgCount(t *testing.T, dbURL string) int {
	t.Helper()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	var n int
	if err := c.QueryRow(ctx, "SELECT count(*) FROM items").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func pgExec(t *testing.T, dbURL, q string) {
	t.Helper()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, q); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresCloneWhileSourceInUse(t *testing.T) {
	ctx := context.Background()
	src := newSource(t)
	hold, err := pgx.Connect(ctx, src) // stands in for the main checkout's dev server
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close(ctx)

	p, err := NewPostgres(src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := p.Clone(ctx, "my-box", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := p.srcDB + "_agentbox_my_box"; c.Name != want {
		t.Fatalf("clone name %q, want %q", c.Name, want)
	}
	if n := pgCount(t, c.URL); n != 1 {
		t.Fatalf("clone has %d rows", n)
	}
	pgExec(t, c.URL, "INSERT INTO items VALUES ('box only')")
	if n := pgCount(t, src); n != 1 {
		t.Fatalf("write to clone leaked into source (%d rows)", n)
	}
	names, _ := p.List(ctx)
	if !slices.Contains(names, c.Name) || slices.Contains(names, p.tplName()) {
		t.Fatalf("List = %v", names)
	}
	if err := p.Drop(ctx, c.Name, ""); err != nil {
		t.Fatal(err)
	}
	if names, _ := p.List(ctx); slices.Contains(names, c.Name) {
		t.Fatal("clone still listed after Drop")
	}
}

func TestPostgresTemplateBusy(t *testing.T) {
	ctx := context.Background()
	src := newSource(t)
	p, _ := NewPostgres(src, Options{})
	if err := p.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	hold, err := pgx.Connect(ctx, p.urlFor(p.tplName()))
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close(ctx)

	_, err = p.Clone(ctx, "a", "")
	var busy *BusyError
	if !errors.As(err, &busy) || len(busy.PIDs) != 1 {
		t.Fatalf("err = %v, want BusyError", err)
	}
	pt, _ := NewPostgres(src, Options{Terminate: true})
	if _, err := pt.Clone(ctx, "a", ""); err != nil {
		t.Fatalf("with Terminate: %v", err)
	}
}

func TestPostgresRefreshPicksUpNewData(t *testing.T) {
	ctx := context.Background()
	src := newSource(t)
	p, _ := NewPostgres(src, Options{})
	if err := p.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	pgExec(t, src, "INSERT INTO items VALUES ('two')")
	if err := p.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := p.Clone(ctx, "a", "")
	if err != nil {
		t.Fatal(err)
	}
	if n := pgCount(t, c.URL); n != 2 {
		t.Fatalf("clone has %d rows after refresh, want 2", n)
	}
}

func TestPostgresDropRefusesForeignDatabases(t *testing.T) {
	p, err := NewPostgres("postgres://localhost:5432/app", Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app", "app_agentbox_tpl", "other_agentbox_a"} {
		if err := p.Drop(context.Background(), name, ""); err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Errorf("Drop(%q) err = %v", name, err)
		}
	}
}

func TestNewPostgresValidatesURL(t *testing.T) {
	for _, bad := range []string{"mysql://x/y", "postgres://localhost", "postgres://localhost/a/b"} {
		if _, err := NewPostgres(bad, Options{}); err == nil {
			t.Errorf("NewPostgres(%q) accepted", bad)
		}
	}
}

func TestPostgresNameTooLong(t *testing.T) {
	p, _ := NewPostgres("postgres://localhost/"+strings.Repeat("x", 40), Options{})
	_, err := p.Clone(context.Background(), strings.Repeat("b", 20), "")
	if err == nil || !strings.Contains(err.Error(), "63-character") {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 3: Run to confirm failure**

Run: `go test ./internal/db/`
Expected: FAIL (`undefined: NewPostgres`, `undefined: ident`).

- [ ] **Step 4: Implement** `internal/db/postgres.go`

```go
package db

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agentbox-dev/agentbox/internal/errs"
)

// Postgres clones from a template snapshot (<src>_agentbox_tpl) built with
// pg_dump | pg_restore, so the live source database may stay in use.
type Postgres struct {
	src       *url.URL
	srcDB     string
	terminate bool
}

const toolsHint = "install the PostgreSQL client tools, e.g. `brew install postgresql@16` or `apt install postgresql-client`"

func NewPostgres(source string, opts Options) (*Postgres, error) {
	u, err := url.Parse(source)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return nil, errs.New(errs.Invalid, "db.source must be a postgres:// URL (got %q)", source)
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" || strings.Contains(name, "/") {
		return nil, errs.New(errs.Invalid, "db.source must name a database, e.g. postgres://localhost:5432/myapp")
	}
	return &Postgres{src: u, srcDB: name, terminate: opts.Terminate}, nil
}

func (p *Postgres) tplName() string    { return p.srcDB + "_agentbox_tpl" }
func (p *Postgres) clonePrefix() string { return p.srcDB + "_agentbox_" }

func (p *Postgres) boxDBName(box string) string {
	return p.clonePrefix() + strings.ReplaceAll(box, "-", "_")
}

func (p *Postgres) urlFor(dbName string) string {
	u := *p.src
	u.Path = "/" + dbName
	return u.String()
}

func ident(s string) string { return pgx.Identifier{s}.Sanitize() }

func (p *Postgres) admin(ctx context.Context) (*pgx.Conn, error) {
	c, err := pgx.Connect(ctx, p.src.String())
	if err != nil {
		return nil, errs.New(errs.Precondition, "connect to %s: %v", p.src.Redacted(), err).
			WithHint("is Postgres running? check db.source in agentbox.yaml")
	}
	return c, nil
}

func (p *Postgres) exists(ctx context.Context, c *pgx.Conn, name string) (bool, error) {
	var ok bool
	err := c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&ok)
	return ok, err
}

func (p *Postgres) connections(ctx context.Context, c *pgx.Conn, name string) ([]int32, error) {
	rows, err := c.Query(ctx, `SELECT pid FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int32])
}

// ensureIdle makes sure nobody is connected to name, which CREATE DATABASE … TEMPLATE requires.
func (p *Postgres) ensureIdle(ctx context.Context, c *pgx.Conn, name string) error {
	pids, err := p.connections(ctx, c, name)
	if err != nil || len(pids) == 0 {
		return err
	}
	if !p.terminate {
		return &BusyError{DB: name, PIDs: pids}
	}
	if _, err := c.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name); err != nil {
		return err
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if pids, err = p.connections(ctx, c, name); err != nil || len(pids) == 0 {
			return err
		}
	}
	return &BusyError{DB: name, PIDs: pids}
}

func (p *Postgres) Check(ctx context.Context) error {
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(tool); err != nil {
			return errs.New(errs.Precondition, "%s not found on PATH", tool).WithHint(toolsHint)
		}
	}
	c, err := p.admin(ctx)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	var user string
	var can bool
	if err := c.QueryRow(ctx, `SELECT current_user, rolcreatedb OR rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&user, &can); err != nil {
		return err
	}
	if !can {
		return errs.New(errs.Precondition, "role %q cannot create databases", user).
			WithHint(fmt.Sprintf("ALTER ROLE %s CREATEDB;", ident(user)))
	}
	return nil
}

func (p *Postgres) Prepare(ctx context.Context) error {
	c, err := p.admin(ctx)
	if err != nil {
		return err
	}
	ok, err := p.exists(ctx, c, p.tplName())
	c.Close(ctx)
	if err != nil || ok {
		return err
	}
	if err := p.Check(ctx); err != nil {
		return err
	}
	return p.Refresh(ctx)
}

func (p *Postgres) Refresh(ctx context.Context) error {
	c, err := p.admin(ctx)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	tpl := p.tplName()
	exists, err := p.exists(ctx, c, tpl)
	if err != nil {
		return err
	}
	if exists {
		if err := p.ensureIdle(ctx, c, tpl); err != nil {
			return err
		}
		if _, err := c.Exec(ctx, "DROP DATABASE "+ident(tpl)); err != nil {
			return err
		}
	}
	if _, err := c.Exec(ctx, "CREATE DATABASE "+ident(tpl)+" TEMPLATE template0"); err != nil {
		return fmt.Errorf("create template database: %w", err)
	}
	if err := p.dumpRestore(ctx, tpl); err != nil {
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+ident(tpl)+" WITH (FORCE)")
		return err
	}
	return nil
}

func (p *Postgres) dumpRestore(ctx context.Context, target string) error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	dump := exec.CommandContext(ctx, "pg_dump", "--format=custom", "--no-owner", "--no-acl", "--dbname", p.src.String())
	restore := exec.CommandContext(ctx, "pg_restore", "--no-owner", "--no-acl", "--dbname", p.urlFor(target))
	var dumpErr, restoreErr bytes.Buffer
	dump.Stdout, dump.Stderr = w, &dumpErr
	restore.Stdin, restore.Stderr = r, &restoreErr

	if err := dump.Start(); err != nil {
		r.Close()
		w.Close()
		return errs.New(errs.Precondition, "run pg_dump: %v", err).WithHint(toolsHint)
	}
	if err := restore.Start(); err != nil {
		r.Close()
		w.Close()
		_ = dump.Process.Kill()
		_ = dump.Wait()
		return errs.New(errs.Precondition, "run pg_restore: %v", err).WithHint(toolsHint)
	}
	// The children hold their own copies; closing ours lets EOF/SIGPIPE propagate.
	r.Close()
	w.Close()
	rErr := restore.Wait()
	dErr := dump.Wait()
	if dErr != nil {
		return fmt.Errorf("pg_dump failed: %v: %s", dErr, strings.TrimSpace(dumpErr.String()))
	}
	if rErr != nil {
		return fmt.Errorf("pg_restore failed: %v: %s", rErr, strings.TrimSpace(restoreErr.String()))
	}
	return nil
}

func (p *Postgres) Clone(ctx context.Context, box, _ string) (Clone, error) {
	name := p.boxDBName(box)
	if len(name) > 63 {
		return Clone{}, errs.New(errs.Invalid, "database name %s is longer than Postgres's 63-character limit", name).
			WithHint("use a shorter box name")
	}
	c, err := p.admin(ctx)
	if err != nil {
		return Clone{}, err
	}
	defer c.Close(ctx)
	tpl := p.tplName()
	if err := p.ensureIdle(ctx, c, tpl); err != nil {
		return Clone{}, err
	}
	ok, err := p.exists(ctx, c, name)
	if err != nil {
		return Clone{}, err
	}
	if ok {
		return Clone{}, errs.New(errs.Exists, "database %s already exists", name).
			WithHint("run `agentbox doctor --fix` to drop orphaned box databases")
	}
	if _, err := c.Exec(ctx, "CREATE DATABASE "+ident(name)+" TEMPLATE "+ident(tpl)); err != nil {
		return Clone{}, fmt.Errorf("clone database: %w", err)
	}
	return Clone{Name: name, URL: p.urlFor(name)}, nil
}

func (p *Postgres) Drop(ctx context.Context, name, _ string) error {
	if !strings.HasPrefix(name, p.clonePrefix()) || name == p.tplName() {
		return fmt.Errorf("refusing to drop %s: not a box database of %s", name, p.srcDB)
	}
	c, err := p.admin(ctx)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	_, err = c.Exec(ctx, "DROP DATABASE IF EXISTS "+ident(name)+" WITH (FORCE)")
	return err
}

func (p *Postgres) List(ctx context.Context) ([]string, error) {
	c, err := p.admin(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close(ctx)
	rows, err := c.Query(ctx, `SELECT datname FROM pg_database`)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, p.clonePrefix()) && n != p.tplName() {
			out = append(out, n)
		}
	}
	return out, nil
}
```

- [ ] **Step 5: Wire the driver in `internal/db/db.go`**. Replace the placeholder line in `New` with `return NewPostgres(cfg.Source, opts)`, and remove the now-unused `"errors"` import.

- [ ] **Step 6: Run the tests.** Without PG, the integration tests skip.

Run: `go test ./internal/db/`
Expected: PASS. The `TestPostgres*` integration tests SKIP, while `TestPostgresDropRefusesForeignDatabases`, `TestNewPostgresValidatesURL` and `TestPostgresNameTooLong` run and pass.

With a local Postgres: `AGENTBOX_TEST_PG_URL=postgres://postgres:postgres@localhost:5432/postgres go test ./internal/db/ -run Postgres -v`
Expected: all PASS.

- [ ] **Step 7: Checkpoint.** Commit only if asked: `git add go.mod go.sum internal/db && git commit -m "feat(db): postgres driver with pg_dump template snapshots"`

---

### Task 10: `box` Manager: validation, `New` with rollback, `Remove`

**Files:**
- Create: `internal/box/manager.go`
- Test: `internal/box/manager_test.go`

**Interfaces:**
- Consumes: `config.Load`, `envfile.Render/Pairs/KV`, `state.*`, `ports.IsFree/DefaultLo/DefaultHi`, `worktree.*`, `db.New/Driver/Options/BusyError`, `proc.New/OpenLog/TailFile/NewRing/Options/DefaultOptions`, `errs.*`, `events.*`
- Produces:
  - `const box.BranchPrefix = "agentbox/"`; step names `StepWorktree, StepPorts, StepDB, StepEnv, StepSetup, StepDev` (`"worktree","ports","db","env","setup","dev"`)
  - `type box.Deps struct { Store *state.Store; BoxesDir, LogsDir string; PortLo, PortHi int; IsFree func(int) bool; NewDriver func(*config.DB, string, db.Options) (db.Driver, error); Bus *events.Bus; ProcOptions proc.Options }`
  - `func box.NewManager(d Deps) *Manager`, `(*Manager).Bus() *events.Bus`
  - `type box.NewOptions struct { RepoPath, Name, From string; NoDev, KeepFailed, Terminate bool }` (JSON `repo_path,name,from,no_dev,keep_failed,terminate`)
  - `type box.RemoveOptions struct { Force, KeepBranch bool }`
  - `(*Manager).ResolveRepo(path string) (state.Repo, error)`, `Repos() ([]state.Repo, error)`
  - `(*Manager).Validate(opts NewOptions) (state.Repo, *config.Config, error)`
  - `(*Manager).New(ctx, opts NewOptions) (state.Box, error)`
  - `(*Manager).Remove(ctx, repoID, name string, opts RemoveOptions) error`
  - `(*Manager).Shutdown()` stops every dev server and marks running boxes stopped
  - Internal helpers used by Tasks 11–12: `get`, `repo`, `repoLock`, `publish`, `startProc`, `stopProc`, `saveRunning`, `openLog`, `closeLog`, `logPath`, `envVars`, `userChanges`, `summarize`, `dbError`

Events published: `box.created` on insert; `box.step` `{step}` as each step begins; `box.state` `{state[, last_error]}` when the box reaches ready or running; on failure, either `box.removed` `{error}` or, with KeepFailed, `box.state` `{state: failed, error}`.

- [ ] **Step 1: Write the failing test** at `internal/box/manager_test.go`

```go
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
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/box/`
Expected: FAIL (`undefined: NewManager`, …).

- [ ] **Step 3: Implement** `internal/box/manager.go`

```go
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

	"github.com/agentbox-dev/agentbox/internal/config"
	"github.com/agentbox-dev/agentbox/internal/db"
	"github.com/agentbox-dev/agentbox/internal/envfile"
	"github.com/agentbox-dev/agentbox/internal/errs"
	"github.com/agentbox-dev/agentbox/internal/events"
	"github.com/agentbox-dev/agentbox/internal/ports"
	"github.com/agentbox-dev/agentbox/internal/proc"
	"github.com/agentbox-dev/agentbox/internal/state"
	"github.com/agentbox-dev/agentbox/internal/worktree"
)

const BranchPrefix = "agentbox/"

const (
	StepWorktree = "worktree"
	StepPorts    = "ports"
	StepDB       = "db"
	StepEnv      = "env"
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
	repo, cfg, err := m.Validate(opts)
	if err != nil {
		return state.Box{}, err
	}
	lock := m.repoLock(repo.ID)
	lock.Lock()
	defer lock.Unlock()

	b := state.Box{
		RepoID: repo.ID, Name: opts.Name, Branch: BranchPrefix + opts.Name,
		Dir:   filepath.Join(m.d.BoxesDir, repo.ID, opts.Name),
		State: state.Creating, CreatedAt: time.Now(),
	}
	if err := m.d.Store.InsertBox(b); err != nil {
		if errors.Is(err, state.ErrExists) {
			return b, errs.New(errs.Exists, "box %q already exists", opts.Name)
		}
		return b, err
	}
	m.publish(events.BoxCreated, b, nil)

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
	if err := worktree.Add(repo.Path, b.Dir, b.Branch, opts.From); err != nil {
		return fail(StepWorktree, err)
	}
	undo.push(func() error {
		if err := worktree.Remove(repo.Path, b.Dir); err != nil {
			return err
		}
		return worktree.DeleteBranch(repo.Path, b.Branch)
	})
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
	if err := writeEnv(repo.Path, cfg, b); err != nil {
		return fail(StepEnv, err)
	}
	if err := hook(StepEnv); err != nil {
		return fail(StepEnv, err)
	}

	if cfg.Setup != "" {
		begin(StepSetup)
		logw, err := m.openLog(b)
		if err != nil {
			return fail(StepSetup, err)
		}
		if err := runSetup(ctx, cfg.Setup, b.Dir, processEnv(envVars(cfg, b)), logw); err != nil {
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
	if b.DBName != "" {
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

func (m *Manager) checkNoUnmergedWork(repo state.Repo, b state.Box, cfg *config.Config) error {
	if ch, err := userChanges(b.Dir, cfg); err == nil && len(ch) > 0 {
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

func (m *Manager) startProc(b state.Box, cfg *config.Config) (*proc.Proc, error) {
	m.stopProc(b.ID())
	logw, err := m.openLog(b)
	if err != nil {
		return nil, err
	}
	repoID, name := b.RepoID, b.Name
	p := proc.New(proc.Spec{Command: cfg.Dev, Dir: b.Dir, Env: processEnv(envVars(cfg, b)), Out: logw}, m.d.ProcOptions,
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
	if st := p.Status(); st.GaveUp {
		b.State, b.PID, b.LastError = state.Stopped, 0, st.LastError
		if err := m.d.Store.UpdateBox(b); err != nil {
			return b, err
		}
	}
	m.publish(events.BoxState, b, nil)
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
	if s.GaveUp {
		b.State, b.PID = state.Stopped, 0
	}
	if err := m.d.Store.UpdateBox(b); err != nil {
		slog.Warn("update box after process change", "box", b.ID(), "err", err)
		return
	}
	m.publish(events.BoxState, b, nil)
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
func envVars(cfg *config.Config, b state.Box) []envfile.KV {
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
	return append(kv, envfile.KV{Key: "AGENTBOX_NAME", Value: b.Name}, envfile.KV{Key: "AGENTBOX_ROOT", Value: b.Dir})
}

func processEnv(vars []envfile.KV) []string {
	return append(os.Environ(), envfile.Pairs(vars)...)
}

func writeEnv(repoRoot string, cfg *config.Config, b state.Box) error {
	for _, rel := range cfg.Copy {
		if err := copyFile(filepath.Join(repoRoot, rel), filepath.Join(b.Dir, rel)); err != nil {
			return err
		}
	}
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
	return os.WriteFile(dst, envfile.Render(tmpl, envVars(cfg, b)), 0o600)
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

func userChanges(dir string, cfg *config.Config) ([]string, error) {
	all, err := worktree.Changes(dir, true)
	if err != nil {
		return nil, err
	}
	managed := managedPaths(cfg)
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
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/box/`
Expected: PASS

- [ ] **Step 5: Checkpoint.** Commit only if asked: `git add internal/box && git commit -m "feat(box): create and remove boxes with full rollback"`

---

### Task 11: `box` runtime: start/stop, merge, info, diff, logs, recovery

**Files:**
- Create: `internal/box/runtime.go`
- Test: `internal/box/runtime_test.go`

**Interfaces:**
- Consumes: Task 10 helpers (`get`, `repo`, `repoLock`, `startProc`, `stopProc`, `saveRunning`, `openLog`, `logPath`, `envVars`, `userChanges`, `summarize`, `publish`)
- Produces:
  - `type box.Info struct { state.Box; RepoPath, Base string; Ahead, Behind, Dirty int; Env map[string]string }`
  - `type box.MergeOptions struct { Into string; Squash bool }` (JSON `into,squash`)
  - `(*Manager).Info(repoID, name string) (Info, error)`, `List(repoID string) ([]Info, error)`
  - `(*Manager).Start(ctx, repoID, name string) error`, `Stop(ctx, repoID, name string) error`
  - `(*Manager).Merge(ctx, repoID, name string, opts MergeOptions) (into string, err error)`
  - `(*Manager).Diff(repoID, name, against string) (string, error)`
  - `(*Manager).LogTail(repoID, name string, n int) ([]string, error)`, `LogSubscribe(repoID, name string) (<-chan string, func(), error)`
  - `(*Manager).Recover() error`

- [ ] **Step 1: Write the failing test** at `internal/box/runtime_test.go`

```go
package box

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agentbox-dev/agentbox/internal/errs"
	"github.com/agentbox-dev/agentbox/internal/state"
	"github.com/agentbox-dev/agentbox/internal/testutil"
	"github.com/agentbox-dev/agentbox/internal/worktree"
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
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/box/ -run 'Start|Crash|Info|Merge|Recover'`
Expected: FAIL (`m.Start undefined`, …).

- [ ] **Step 3: Implement** `internal/box/runtime.go`

```go
package box

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/agentbox-dev/agentbox/internal/config"
	"github.com/agentbox-dev/agentbox/internal/errs"
	"github.com/agentbox-dev/agentbox/internal/events"
	"github.com/agentbox-dev/agentbox/internal/proc"
	"github.com/agentbox-dev/agentbox/internal/state"
	"github.com/agentbox-dev/agentbox/internal/worktree"
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
	if base, err := worktree.CurrentBranch(repo.Path); err == nil {
		i.Base = base
		i.Ahead, i.Behind, _ = worktree.AheadBehind(repo.Path, base, b.Branch)
	}
	if ch, err := userChanges(b.Dir, cfg); err == nil {
		i.Dirty = len(ch)
	}
	if cfg != nil {
		i.Env = map[string]string{}
		for _, kv := range envVars(cfg, b) {
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
	if cfg.Dev == "" {
		return errs.New(errs.Precondition, "no dev command in %s", config.FileName).WithHint("add a `dev:` line")
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
	if b.State != state.Running {
		return nil
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
	return nil
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
	ch, err := userChanges(b.Dir, cfg)
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
```

- [ ] **Step 4: Run the whole package**

Run: `go test -race ./internal/box/`
Expected: PASS

- [ ] **Step 5: Checkpoint.** Commit only if asked: `git add internal/box && git commit -m "feat(box): start/stop, merge, info, diff, logs, recovery"`

---

### Task 12: `box` maintenance: `RefreshDB` and `Doctor`

**Files:**
- Create: `internal/box/doctor.go`
- Test: `internal/box/doctor_test.go`

**Interfaces:**
- Produces:
  - `type box.Finding struct { Kind, Detail string; Fixed bool }` (JSON `kind,detail,fixed`). Kinds: `config`, `db`, `missing_worktree`, `orphan_branch`, `orphan_worktree`, `orphan_db`, `port_in_use`.
  - `(*Manager).RefreshDB(ctx, repoID string, terminate bool) error`
  - `(*Manager).Doctor(ctx, repoID string, fix bool) ([]Finding, error)`. With `fix`, it only repairs what is safe: removing records whose worktree is gone, dropping orphaned box DBs, and `git worktree prune`. Branches and worktree dirs are only ever reported.

- [ ] **Step 1: Write the failing test** at `internal/box/doctor_test.go`

```go
package box

import (
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/agentbox-dev/agentbox/internal/errs"
	"github.com/agentbox-dev/agentbox/internal/state"
	"github.com/agentbox-dev/agentbox/internal/testutil"
)

func kinds(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Kind)
	}
	return out
}

func TestDoctorCleanRepo(t *testing.T) {
	m := newTestManager(t)
	b, err := m.New(ctx, NewOptions{RepoPath: newSQLiteRepo(t, sqliteConfig), Name: "a", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	fs, err := m.Doctor(ctx, b.RepoID, false)
	if err != nil || len(fs) != 0 {
		t.Fatalf("findings %v, err %v", fs, err)
	}
}

func TestDoctorFixesMissingWorktree(t *testing.T) {
	m := newTestManager(t)
	b, _ := m.New(ctx, NewOptions{RepoPath: newSQLiteRepo(t, sqliteConfig), Name: "a", NoDev: true})
	os.RemoveAll(b.Dir)

	fs, _ := m.Doctor(ctx, b.RepoID, false)
	if !slices.Contains(kinds(fs), "missing_worktree") || fs[0].Fixed {
		t.Fatalf("report: %v", fs)
	}
	fs, _ = m.Doctor(ctx, b.RepoID, true)
	if len(fs) == 0 || !fs[0].Fixed {
		t.Fatalf("fix: %v", fs)
	}
	if _, err := m.d.Store.GetBox(b.RepoID, "a"); !errors.Is(err, state.ErrNotFound) {
		t.Fatal("box record not removed")
	}
	if rows, _ := m.d.Store.AllPorts(); len(rows) != 0 {
		t.Fatalf("ports not freed: %v", rows)
	}
}

func TestDoctorReportsOrphanBranchAndPortInUse(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig)
	b, _ := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	testutil.Git(t, repo, "branch", "agentbox/zzz")
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", b.Ports["APP_PORT"]))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	fs, _ := m.Doctor(ctx, b.RepoID, true)
	got := kinds(fs)
	if !slices.Contains(got, "orphan_branch") || !slices.Contains(got, "port_in_use") {
		t.Fatalf("findings %v", fs)
	}
	for _, f := range fs {
		if f.Kind == "orphan_branch" && (f.Fixed || !strings.Contains(f.Detail, "agentbox/zzz")) {
			t.Fatalf("orphan branch must only be reported: %+v", f)
		}
	}
}

func TestRefreshDBRequiresDB(t *testing.T) {
	m := newTestManager(t)
	r, _ := m.ResolveRepo(newPlainRepo(t, plainConfig))
	err := m.RefreshDB(ctx, r.ID, false)
	if e := errs.From(err); e.Code != errs.Precondition || !strings.Contains(e.Message, "no db section") {
		t.Fatalf("err = %v", err)
	}
	if err := m.RefreshDB(ctx, "nope", false); errs.From(err).Code != errs.NotFound {
		t.Fatalf("unknown repo err = %v", err)
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/box/ -run 'Doctor|Refresh'`
Expected: FAIL (`m.Doctor undefined`).

- [ ] **Step 3: Implement** `internal/box/doctor.go`

```go
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
	lock := m.repoLock(repoID)
	lock.Lock()
	defer lock.Unlock()

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
```

- [ ] **Step 4: Run the whole package**

Run: `go test -race ./internal/box/`
Expected: PASS

- [ ] **Step 5: Checkpoint.** Commit only if asked: `git add internal/box && git commit -m "feat(box): doctor and db refresh"`

---

### Task 13: `api` package (HTTP handlers, SSE, auth)

**Files:**
- Create: `internal/api/types.go`, `internal/api/server.go`
- Test: `internal/api/server_test.go`

**Interfaces:**
- Consumes: `box.Manager` methods from Tasks 10–12, `events.Bus`, `errs`
- Produces:
  - Wire types: `api.RepoView{ID, Path}`, `api.BoxView{ID, Repo, RepoPath, Name, Dir, Branch, State string; Ports map[string]int; DBName, DBURL string; PID int; LastStep, LastError, Base string; Ahead, Behind, Dirty int; Env map[string]string; CreatedAt time.Time}`, `api.Created{ID, Repo, Name}`, `api.MergeResult{Into}`, `api.ErrorBody{Error *errs.E}`
  - `type api.Server struct { M *box.Manager; Bus *events.Bus; Token string; Origins []string }` with `(*Server).Handler(requireToken bool) http.Handler`
  - Routes (all JSON unless noted):
    - `GET /v1/health` → `{"ok":true}`
    - `GET /v1/repos` → `[]RepoView`; `POST /v1/repos {path}` → `RepoView`
    - `GET /v1/boxes?repo=` → `[]BoxView`; `POST /v1/boxes` (body `box.NewOptions`) → `202 Created` (creation continues in the background; progress arrives on `/v1/events`)
    - `GET /v1/repos/{repo}/boxes/{name}` → `BoxView`
    - `POST …/{name}/start`, `POST …/{name}/stop` → `204`
    - `POST …/{name}/merge` (body `box.MergeOptions`) → `MergeResult`
    - `DELETE …/{name}?force=true&keep_branch=true` → `204`
    - `GET …/{name}/logs?tail=N` → `text/plain`; `&follow=true` → SSE `event: line` / `data: "<json string>"`
    - `GET …/{name}/diff?against=` → `text/plain`
    - `POST /v1/repos/{repo}/db/refresh?terminate=true` → `204`
    - `GET /v1/repos/{repo}/doctor` → `[]box.Finding`; `POST` does the same with fix
    - `GET /v1/events` → SSE, `event: <type>` / `data: <events.Event JSON>`, with a `: ping` comment every 15 s
  - Status mapping: not_found 404, exists/unmerged_work/conflict 409, invalid 400, precondition 412, internal 500. Auth failures: 401 (token), 403 (origin).

- [ ] **Step 1: Write the failing test** at `internal/api/server_test.go`

```go
package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentbox-dev/agentbox/internal/box"
	"github.com/agentbox-dev/agentbox/internal/proc"
	"github.com/agentbox-dev/agentbox/internal/state"
	"github.com/agentbox-dev/agentbox/internal/testutil"
)

func newServer(t *testing.T) (*Server, string) {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	m := box.NewManager(box.Deps{Store: store, BoxesDir: t.TempDir(), LogsDir: t.TempDir(), PortLo: 22000, PortHi: 22999,
		ProcOptions: proc.Options{Backoff: []time.Duration{10 * time.Millisecond}, CrashWindow: 5 * time.Second, MaxCrashes: 3, StopTimeout: 2 * time.Second}})
	t.Cleanup(m.Shutdown)
	repo := testutil.NewRepo(t, map[string]string{"agentbox.yaml": "version: 1\ndev: echo started; sleep 30\nports: [APP_PORT]\n"})
	return &Server{M: m, Bus: m.Bus(), Token: "secret", Origins: []string{"http://127.0.0.1:7777"}}, repo
}

func call(t *testing.T, method, u string, body, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, u, rd)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if s, ok := out.(*string); ok {
			b, _ := io.ReadAll(resp.Body)
			*s = string(b)
		} else {
			json.NewDecoder(resp.Body).Decode(out)
		}
	}
	return resp.StatusCode
}

func waitState(t *testing.T, base, repo, name, want string) BoxView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var v BoxView
	for time.Now().Before(deadline) {
		if call(t, "GET", base+"/v1/repos/"+repo+"/boxes/"+name, nil, &v) == 200 && v.State == want {
			return v
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("box %s never reached %s (last %+v)", name, want, v)
	return v
}

func TestBoxLifecycleOverHTTP(t *testing.T) {
	srv, repo := newServer(t)
	ts := httptest.NewServer(srv.Handler(false))
	t.Cleanup(ts.Close)

	var rv RepoView
	if code := call(t, "POST", ts.URL+"/v1/repos", map[string]string{"path": repo}, &rv); code != 200 || rv.Path != repo {
		t.Fatalf("resolve repo: %d %+v", code, rv)
	}
	var created Created
	if code := call(t, "POST", ts.URL+"/v1/boxes", box.NewOptions{RepoPath: repo, Name: "a"}, &created); code != 202 || created.ID != rv.ID+"/a" {
		t.Fatalf("create: %d %+v", code, created)
	}
	v := waitState(t, ts.URL, rv.ID, "a", "running")
	if v.Ports["APP_PORT"] == 0 || v.Env["APP_PORT"] == "" || v.PID == 0 {
		t.Fatalf("view %+v", v)
	}
	var list []BoxView
	if call(t, "GET", ts.URL+"/v1/boxes?repo="+url.QueryEscape(rv.ID), nil, &list); len(list) != 1 {
		t.Fatalf("list %v", list)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var logs string
		call(t, "GET", ts.URL+"/v1/repos/"+rv.ID+"/boxes/a/logs?tail=10", nil, &logs)
		if strings.Contains(logs, "started") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("logs never showed output: %q", logs)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code := call(t, "POST", ts.URL+"/v1/repos/"+rv.ID+"/boxes/a/stop", nil, nil); code != 204 {
		t.Fatalf("stop: %d", code)
	}
	waitState(t, ts.URL, rv.ID, "a", "stopped")
	if code := call(t, "DELETE", ts.URL+"/v1/repos/"+rv.ID+"/boxes/a", nil, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	var eb ErrorBody
	if code := call(t, "GET", ts.URL+"/v1/repos/"+rv.ID+"/boxes/a", nil, &eb); code != 404 || eb.Error == nil || eb.Error.Code != "not_found" {
		t.Fatalf("after delete: %d %+v", code, eb)
	}
}

func TestCreateInvalidName(t *testing.T) {
	srv, repo := newServer(t)
	ts := httptest.NewServer(srv.Handler(false))
	t.Cleanup(ts.Close)
	var eb ErrorBody
	if code := call(t, "POST", ts.URL+"/v1/boxes", box.NewOptions{RepoPath: repo, Name: "Bad Name"}, &eb); code != 400 || eb.Error.Code != "invalid" {
		t.Fatalf("%d %+v", code, eb.Error)
	}
}

func TestEventsStream(t *testing.T) {
	srv, repo := newServer(t)
	ts := httptest.NewServer(srv.Handler(false))
	t.Cleanup(ts.Close)
	resp, err := http.Get(ts.URL + "/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	call(t, "POST", ts.URL+"/v1/boxes", box.NewOptions{RepoPath: repo, Name: "a"}, nil)

	sc := bufio.NewScanner(resp.Body)
	seen := map[string]bool{}
	timeout := time.AfterFunc(10*time.Second, func() { resp.Body.Close() })
	defer timeout.Stop()
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		json.Unmarshal([]byte(line), &ev)
		seen[ev.Type] = true
		if ev.Type == "box.state" && ev.Data["state"] == "running" {
			break
		}
	}
	for _, want := range []string{"box.created", "box.step", "box.state"} {
		if !seen[want] {
			t.Errorf("never saw %s (saw %v)", want, seen)
		}
	}
}

func TestAuth(t *testing.T) {
	srv, _ := newServer(t)
	ts := httptest.NewServer(srv.Handler(true))
	t.Cleanup(ts.Close)
	get := func(token, origin string) int {
		req, _ := http.NewRequest("GET", ts.URL+"/v1/health", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := get("", ""); c != 401 {
		t.Errorf("no token: %d", c)
	}
	if c := get("wrong", ""); c != 401 {
		t.Errorf("wrong token: %d", c)
	}
	if c := get("secret", ""); c != 200 {
		t.Errorf("good token: %d", c)
	}
	if c := get("secret", "http://evil.example"); c != 403 {
		t.Errorf("bad origin: %d", c)
	}
	if c := get("secret", "http://127.0.0.1:7777"); c != 200 {
		t.Errorf("allowed origin: %d", c)
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/api/`
Expected: FAIL (`undefined: Server`).

- [ ] **Step 3: Implement** `internal/api/types.go`

```go
// Package api is the daemon's versioned local HTTP API. The CLI and every UI are clients of it.
package api

import (
	"time"

	"github.com/agentbox-dev/agentbox/internal/box"
	"github.com/agentbox-dev/agentbox/internal/errs"
)

type RepoView struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type BoxView struct {
	ID        string            `json:"id"`
	Repo      string            `json:"repo"`
	RepoPath  string            `json:"repo_path"`
	Name      string            `json:"name"`
	Dir       string            `json:"dir"`
	Branch    string            `json:"branch"`
	State     string            `json:"state"`
	Ports     map[string]int    `json:"ports"`
	DBName    string            `json:"db_name,omitempty"`
	DBURL     string            `json:"db_url,omitempty"`
	PID       int               `json:"pid,omitempty"`
	LastStep  string            `json:"last_step,omitempty"`
	LastError string            `json:"last_error,omitempty"`
	Base      string            `json:"base,omitempty"`
	Ahead     int               `json:"ahead"`
	Behind    int               `json:"behind"`
	Dirty     int               `json:"dirty"`
	Env       map[string]string `json:"env,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

type Created struct {
	ID   string `json:"id"`
	Repo string `json:"repo"`
	Name string `json:"name"`
}

type MergeResult struct {
	Into string `json:"into"`
}

type ErrorBody struct {
	Error *errs.E `json:"error"`
}

func viewOf(i box.Info) BoxView {
	return BoxView{
		ID: i.ID(), Repo: i.RepoID, RepoPath: i.RepoPath, Name: i.Name, Dir: i.Dir, Branch: i.Branch,
		State: string(i.State), Ports: i.Ports, DBName: i.DBName, DBURL: i.DBURL, PID: i.PID,
		LastStep: i.LastStep, LastError: i.LastError, Base: i.Base, Ahead: i.Ahead, Behind: i.Behind,
		Dirty: i.Dirty, Env: i.Env, CreatedAt: i.CreatedAt,
	}
}
```

- [ ] **Step 4: Implement** `internal/api/server.go`

```go
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/agentbox-dev/agentbox/internal/box"
	"github.com/agentbox-dev/agentbox/internal/errs"
	"github.com/agentbox-dev/agentbox/internal/events"
)

type Server struct {
	M       *box.Manager
	Bus     *events.Bus
	Token   string   // required on the TCP listener
	Origins []string // browser origins allowed on the TCP listener
}

// Handler serves the v1 API. requireToken is true for the TCP listener and
// false for the Unix socket, which file permissions already protect.
func (s *Server) Handler(requireToken bool) http.Handler {
	mux := http.NewServeMux()
	const b = "/v1/repos/{repo}/boxes/{name}"
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })
	mux.HandleFunc("GET /v1/repos", s.listRepos)
	mux.HandleFunc("POST /v1/repos", s.resolveRepo)
	mux.HandleFunc("GET /v1/boxes", s.listBoxes)
	mux.HandleFunc("POST /v1/boxes", s.createBox)
	mux.HandleFunc("GET "+b, s.getBox)
	mux.HandleFunc("POST "+b+"/start", s.startBox)
	mux.HandleFunc("POST "+b+"/stop", s.stopBox)
	mux.HandleFunc("POST "+b+"/merge", s.mergeBox)
	mux.HandleFunc("DELETE "+b, s.removeBox)
	mux.HandleFunc("GET "+b+"/logs", s.logs)
	mux.HandleFunc("GET "+b+"/diff", s.diff)
	mux.HandleFunc("POST /v1/repos/{repo}/db/refresh", s.refreshDB)
	mux.HandleFunc("GET /v1/repos/{repo}/doctor", s.doctor(false))
	mux.HandleFunc("POST /v1/repos/{repo}/doctor", s.doctor(true))
	mux.HandleFunc("GET /v1/events", s.events)
	if !requireToken {
		return mux
	}
	return s.auth(mux)
}

func (s *Server) auth(next http.Handler) http.Handler {
	want := []byte("Bearer " + s.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && !slices.Contains(s.Origins, o) {
			writeJSON(w, http.StatusForbidden, ErrorBody{errs.New(errs.Invalid, "origin %q is not allowed", o)})
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			writeJSON(w, http.StatusUnauthorized, ErrorBody{errs.New(errs.Invalid, "missing or wrong API token").
				WithHint("send `Authorization: Bearer <token>` using the token in ~/.agentbox/token")})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) listRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := s.M.Repos()
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]RepoView, 0, len(repos))
	for _, rp := range repos {
		out = append(out, RepoView{ID: rp.ID, Path: rp.Path})
	}
	writeJSON(w, 200, out)
}

func (s *Server) resolveRepo(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !decode(w, r, &body) {
		return
	}
	repo, err := s.M.ResolveRepo(body.Path)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, RepoView{ID: repo.ID, Path: repo.Path})
}

func (s *Server) listBoxes(w http.ResponseWriter, r *http.Request) {
	infos, err := s.M.List(r.URL.Query().Get("repo"))
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]BoxView, 0, len(infos))
	for _, i := range infos {
		out = append(out, viewOf(i))
	}
	writeJSON(w, 200, out)
}

func (s *Server) createBox(w http.ResponseWriter, r *http.Request) {
	var opts box.NewOptions
	if !decode(w, r, &opts) {
		return
	}
	repo, _, err := s.M.Validate(opts)
	if err != nil {
		writeError(w, err)
		return
	}
	go func() {
		// Detached from the request: creation must finish even if the client goes away.
		if _, err := s.M.New(context.Background(), opts); err != nil {
			slog.Warn("box creation failed", "repo", repo.ID, "box", opts.Name, "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, Created{ID: repo.ID + "/" + opts.Name, Repo: repo.ID, Name: opts.Name})
}

func (s *Server) getBox(w http.ResponseWriter, r *http.Request) {
	info, err := s.M.Info(r.PathValue("repo"), r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, viewOf(info))
}

func (s *Server) startBox(w http.ResponseWriter, r *http.Request) {
	noContent(w, s.M.Start(r.Context(), r.PathValue("repo"), r.PathValue("name")))
}

func (s *Server) stopBox(w http.ResponseWriter, r *http.Request) {
	noContent(w, s.M.Stop(r.Context(), r.PathValue("repo"), r.PathValue("name")))
}

func (s *Server) mergeBox(w http.ResponseWriter, r *http.Request) {
	var opts box.MergeOptions
	if r.ContentLength != 0 && !decode(w, r, &opts) {
		return
	}
	into, err := s.M.Merge(r.Context(), r.PathValue("repo"), r.PathValue("name"), opts)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, MergeResult{Into: into})
}

func (s *Server) removeBox(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	noContent(w, s.M.Remove(r.Context(), r.PathValue("repo"), r.PathValue("name"),
		box.RemoveOptions{Force: q.Get("force") == "true", KeepBranch: q.Get("keep_branch") == "true"}))
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	repo, name, q := r.PathValue("repo"), r.PathValue("name"), r.URL.Query()
	tail := 200
	if v := q.Get("tail"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, errs.New(errs.Invalid, "tail must be a non-negative number"))
			return
		}
		tail = n
	}
	if q.Get("follow") != "true" {
		lines, err := s.M.LogTail(repo, name, tail)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
		return
	}
	ch, cancel, err := s.M.LogSubscribe(repo, name) // subscribe first so no line falls between tail and follow
	if err != nil {
		writeError(w, err)
		return
	}
	defer cancel()
	lines, err := s.M.LogTail(repo, name, tail)
	if err != nil {
		writeError(w, err)
		return
	}
	sse, ok := newSSE(w)
	if !ok {
		return
	}
	for _, l := range lines {
		if sse.send("line", l) != nil {
			return
		}
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case l, ok := <-ch:
			if !ok || sse.send("line", l) != nil {
				return
			}
		}
	}
}

func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	d, err := s.M.Diff(r.PathValue("repo"), r.PathValue("name"), r.URL.Query().Get("against"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, d)
}

func (s *Server) refreshDB(w http.ResponseWriter, r *http.Request) {
	noContent(w, s.M.RefreshDB(r.Context(), r.PathValue("repo"), r.URL.Query().Get("terminate") == "true"))
}

func (s *Server) doctor(fix bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fs, err := s.M.Doctor(r.Context(), r.PathValue("repo"), fix)
		if err != nil {
			writeError(w, err)
			return
		}
		if fs == nil {
			fs = []box.Finding{}
		}
		writeJSON(w, 200, fs)
	}
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	ch, cancel := s.Bus.Subscribe() // before headers go out, so clients can't miss events
	defer cancel()
	sse, ok := newSSE(w)
	if !ok {
		return
	}
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if sse.comment("ping") != nil {
				return
			}
		case ev, ok := <-ch:
			if !ok || sse.send(ev.Type, ev) != nil {
				return
			}
		}
	}
}

// --- helpers ---

type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSE(w http.ResponseWriter) (*sseWriter, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		writeError(w, errs.New(errs.Internal, "streaming not supported"))
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	f.Flush()
	return &sseWriter{w: w, f: f}, true
}

func (s *sseWriter) send(event string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

func (s *sseWriter) comment(text string) error {
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, errs.New(errs.Invalid, "bad request body: %v", err))
		return false
	}
	return true
}

func statusFor(c errs.Code) int {
	switch c {
	case errs.NotFound:
		return http.StatusNotFound
	case errs.Exists, errs.UnmergedWork, errs.Conflict:
		return http.StatusConflict
	case errs.Invalid:
		return http.StatusBadRequest
	case errs.Precondition:
		return http.StatusPreconditionFailed
	}
	return http.StatusInternalServerError
}

func writeError(w http.ResponseWriter, err error) {
	e := errs.From(err)
	writeJSON(w, statusFor(e.Code), ErrorBody{Error: e})
}

func noContent(w http.ResponseWriter, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
```

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/api/`
Expected: PASS

- [ ] **Step 6: Checkpoint.** Commit only if asked: `git add internal/api && git commit -m "feat(api): v1 HTTP API with SSE and token auth"`

---

### Task 14: `daemon` + `client` packages

**Files:**
- Create: `internal/daemon/daemon.go`, `internal/client/client.go`
- Test: `internal/daemon/daemon_test.go`

**Interfaces:**
- Produces (daemon):
  - `type daemon.Paths struct { Home, State, Sock, PID, Token, Log, Logs, Boxes, Config, Out string }`, `func daemon.PathsFor(home string) Paths`, `func daemon.DefaultHome() (string, error)` (`$AGENTBOX_HOME` or `~/.agentbox`)
  - `type daemon.Settings struct { PortRange []int; APIAddr string }` (YAML `port_range`, `api_addr`), `func daemon.LoadSettings(path string) (Settings, error)`
  - `func daemon.Run(ctx context.Context, p Paths) error` (blocks until ctx is done; refuses if a daemon already answers on the socket)
  - `func daemon.Ping(sock string) bool`, `func daemon.ReadPID(path string) (int, error)`, `func daemon.EnsureRunning(p Paths) error`, `func daemon.Stop(p Paths, timeout time.Duration) error`
- Produces (client):
  - `type client.Event struct { Type, Box string; Data map[string]any }`
  - `func client.New(sock string) *Client` with methods: `Health(ctx) error`, `ResolveRepo(ctx, path) (api.RepoView, error)`, `CreateBox(ctx, box.NewOptions) (api.Created, error)`, `ListBoxes(ctx, repoID) ([]api.BoxView, error)`, `GetBox(ctx, repoID, name) (api.BoxView, error)`, `StartBox/StopBox(ctx, repoID, name) error`, `Merge(ctx, repoID, name, box.MergeOptions) (api.MergeResult, error)`, `RemoveBox(ctx, repoID, name string, force, keepBranch bool) error`, `Logs(ctx, repoID, name string, tail int, follow bool, w io.Writer) error`, `Diff(ctx, repoID, name, against) (string, error)`, `RefreshDB(ctx, repoID string, terminate bool) error`, `Doctor(ctx, repoID string, fix bool) ([]box.Finding, error)`, `Events(ctx) (<-chan Event, error)`
  - API errors come back as `*errs.E`.

- [ ] **Step 1: Write the failing test** at `internal/daemon/daemon_test.go`

```go
package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentbox-dev/agentbox/internal/box"
	"github.com/agentbox-dev/agentbox/internal/client"
	"github.com/agentbox-dev/agentbox/internal/daemon"
	"github.com/agentbox-dev/agentbox/internal/errs"
	"github.com/agentbox-dev/agentbox/internal/testutil"
)

// Unix socket paths are limited to ~104 bytes on macOS, so homes live under /tmp.
func tempHome(t *testing.T) string {
	home, err := os.MkdirTemp("/tmp", "abd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	return home
}

func TestRunServesAndShutsDown(t *testing.T) {
	p := daemon.PathsFor(tempHome(t))
	os.WriteFile(p.Config, []byte("api_addr: 127.0.0.1:0\n"), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- daemon.Run(ctx, p) }()

	deadline := time.Now().Add(5 * time.Second)
	for !daemon.Ping(p.Sock) {
		if time.Now().After(deadline) {
			t.Fatal("daemon never came up")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st, err := os.Stat(p.Token); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v", st, err)
	}
	if pid, err := daemon.ReadPID(p.PID); err != nil || pid != os.Getpid() {
		t.Fatalf("pid file: %d %v", pid, err)
	}

	c := client.New(p.Sock)
	if err := c.Health(ctx); err != nil {
		t.Fatal(err)
	}
	repo := testutil.NewRepo(t, map[string]string{"agentbox.yaml": "version: 1\n"})
	rv, err := c.ResolveRepo(ctx, filepath.Join(repo, "."))
	if err != nil || rv.Path != repo {
		t.Fatalf("resolve: %+v %v", rv, err)
	}
	evs, err := c.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateBox(ctx, box.NewOptions{RepoPath: repo, Name: "a"}); err != nil {
		t.Fatal(err)
	}
	for ev := range evs {
		if ev.Type == "box.state" && ev.Data["state"] == "ready" {
			break
		}
	}
	boxes, err := c.ListBoxes(ctx, rv.ID)
	if err != nil || len(boxes) != 1 || boxes[0].Name != "a" {
		t.Fatalf("list: %v %v", boxes, err)
	}
	_, err = c.GetBox(ctx, rv.ID, "nope")
	if e := errs.From(err); e.Code != errs.NotFound {
		t.Fatalf("typed error lost over the wire: %v", err)
	}
	if err := c.RemoveBox(ctx, rv.ID, "a", false, false); err != nil {
		t.Fatal(err)
	}

	if err := daemon.Run(context.Background(), p); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second daemon: %v", err)
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not shut down")
	}
	if daemon.Ping(p.Sock) {
		t.Fatal("socket still answering")
	}
	if _, err := os.Stat(p.Sock); !os.IsNotExist(err) {
		t.Fatal("socket file left behind")
	}
}

func TestLoadSettings(t *testing.T) {
	dir := t.TempDir()
	s, err := daemon.LoadSettings(filepath.Join(dir, "missing.yaml"))
	if err != nil || s.APIAddr != "127.0.0.1:7777" || s.PortRange[0] != 20000 || s.PortRange[1] != 29999 {
		t.Fatalf("defaults: %+v %v", s, err)
	}
	bad := filepath.Join(dir, "bad.yaml")
	os.WriteFile(bad, []byte("port_range: [30000, 20000]\n"), 0o600)
	if _, err := daemon.LoadSettings(bad); err == nil {
		t.Fatal("inverted port range accepted")
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/daemon/`
Expected: FAIL (packages `daemon`/`client` don't exist).

- [ ] **Step 3: Implement** `internal/daemon/daemon.go`

```go
// Package daemon runs agentboxd: the long-lived process that owns boxes and serves the API.
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/agentbox-dev/agentbox/internal/api"
	"github.com/agentbox-dev/agentbox/internal/box"
	"github.com/agentbox-dev/agentbox/internal/events"
	"github.com/agentbox-dev/agentbox/internal/ports"
	"github.com/agentbox-dev/agentbox/internal/state"
)

type Paths struct {
	Home, State, Sock, PID, Token, Log, Logs, Boxes, Config, Out string
}

func PathsFor(home string) Paths {
	j := func(n string) string { return filepath.Join(home, n) }
	return Paths{
		Home: home, State: j("state.db"), Sock: j("daemon.sock"), PID: j("daemon.pid"), Token: j("token"),
		Log: j("agentbox.log"), Logs: j("logs"), Boxes: j("boxes"), Config: j("config.yaml"), Out: j("daemon.out"),
	}
}

func DefaultHome() (string, error) {
	if h := os.Getenv("AGENTBOX_HOME"); h != "" {
		return filepath.Abs(h)
	}
	uh, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(uh, ".agentbox"), nil
}

type Settings struct {
	PortRange []int  `yaml:"port_range"`
	APIAddr   string `yaml:"api_addr"`
}

func LoadSettings(path string) (Settings, error) {
	s := Settings{PortRange: []int{ports.DefaultLo, ports.DefaultHi}, APIAddr: "127.0.0.1:7777"}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := yaml.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}
	if len(s.PortRange) != 2 || s.PortRange[0] < 1024 || s.PortRange[1] > 65535 || s.PortRange[0] > s.PortRange[1] {
		return s, fmt.Errorf("%s: port_range must be [low, high] within 1024-65535 (got %v)", path, s.PortRange)
	}
	return s, nil
}

func Ping(sock string) bool {
	c, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func ReadPID(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

func ensureToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return strings.TrimSpace(string(b)), nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	return tok, os.WriteFile(path, []byte(tok+"\n"), 0o600)
}

// Run is the daemon's main loop. It returns when ctx is cancelled, after
// stopping every dev server.
func Run(ctx context.Context, p Paths) error {
	for _, d := range []string{p.Home, p.Logs, p.Boxes} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if Ping(p.Sock) {
		return errors.New("agentbox daemon is already running")
	}
	logf, err := os.OpenFile(p.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	settings, err := LoadSettings(p.Config)
	if err != nil {
		return err
	}
	token, err := ensureToken(p.Token)
	if err != nil {
		return err
	}
	store, err := state.Open(p.State)
	if err != nil {
		return err
	}
	defer store.Close()

	bus := events.NewBus()
	m := box.NewManager(box.Deps{Store: store, BoxesDir: p.Boxes, LogsDir: p.Logs,
		PortLo: settings.PortRange[0], PortHi: settings.PortRange[1], Bus: bus})
	if err := m.Recover(); err != nil {
		return err
	}
	srv := &api.Server{M: m, Bus: bus, Token: token, Origins: origins(settings.APIAddr)}

	_ = os.Remove(p.Sock) // stale socket from a crashed daemon (Ping above proved nobody answers)
	ul, err := net.Listen("unix", p.Sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(p.Sock, 0o600); err != nil {
		ul.Close()
		return err
	}
	base := func(net.Listener) context.Context { return ctx } // cancels SSE streams on shutdown
	servers := []*http.Server{{Handler: srv.Handler(false), BaseContext: base}}
	listeners := []net.Listener{ul}
	if tl, err := net.Listen("tcp", settings.APIAddr); err != nil {
		slog.Warn("HTTP API disabled", "addr", settings.APIAddr, "err", err)
	} else {
		servers = append(servers, &http.Server{Handler: srv.Handler(true), BaseContext: base})
		listeners = append(listeners, tl)
	}
	if err := os.WriteFile(p.PID, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return err
	}

	errc := make(chan error, len(servers))
	for i, s := range servers {
		go func() {
			if err := s.Serve(listeners[i]); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}
	slog.Info("daemon started", "pid", os.Getpid(), "sock", p.Sock, "api", settings.APIAddr)

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errc:
	}
	m.Shutdown()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(sctx)
	}
	_ = os.Remove(p.Sock)
	_ = os.Remove(p.PID)
	slog.Info("daemon stopped")
	return runErr
}

func origins(addr string) []string {
	out := []string{"http://" + addr}
	if _, port, err := net.SplitHostPort(addr); err == nil {
		out = append(out, "http://localhost:"+port, "http://127.0.0.1:"+port)
	}
	return out
}

// EnsureRunning starts the daemon in the background if it isn't answering.
func EnsureRunning(p Paths) error {
	if Ping(p.Sock) {
		return nil
	}
	if err := os.MkdirAll(p.Home, 0o700); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(p.Out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	cmd := exec.Command(exe, "daemon", "run")
	cmd.Env = append(os.Environ(), "AGENTBOX_HOME="+p.Home)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start agentbox daemon: %w", err)
	}
	_ = cmd.Process.Release()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if Ping(p.Sock) {
			return nil
		}
	}
	return fmt.Errorf("agentbox daemon did not start within 3s; see %s", p.Out)
}

// Stop asks the running daemon to exit (SIGTERM) and waits for its socket to go away.
func Stop(p Paths, timeout time.Duration) error {
	if !Ping(p.Sock) {
		return nil
	}
	pid, err := ReadPID(p.PID)
	if err != nil {
		return fmt.Errorf("daemon is running but %s is unreadable: %w", p.PID, err)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal daemon (pid %d): %w", pid, err)
	}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if !Ping(p.Sock) {
			return nil
		}
	}
	return fmt.Errorf("daemon (pid %d) did not stop within %s", pid, timeout)
}
```

- [ ] **Step 4: Implement** `internal/client/client.go`

```go
// Package client talks to the local agentbox daemon over its Unix socket.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/agentbox-dev/agentbox/internal/api"
	"github.com/agentbox-dev/agentbox/internal/box"
)

const base = "http://agentbox"

type Client struct{ hc *http.Client }

type Event struct {
	Type string         `json:"type"`
	Box  string         `json:"box"`
	Data map[string]any `json:"data"`
}

func New(sock string) *Client {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	return &Client{hc: &http.Client{Transport: tr}}
}

func boxPath(repoID, name string) string {
	return "/v1/repos/" + url.PathEscape(repoID) + "/boxes/" + url.PathEscape(name)
}

func (c *Client) open(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("talk to agentbox daemon: %w", err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var eb api.ErrorBody
		if json.NewDecoder(resp.Body).Decode(&eb) == nil && eb.Error != nil {
			return nil, eb.Error
		}
		return nil, fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	return resp, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.open(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch o := out.(type) {
	case nil:
		return nil
	case *string:
		b, err := io.ReadAll(resp.Body)
		*o = string(b)
		return err
	default:
		return json.NewDecoder(resp.Body).Decode(out)
	}
}

func (c *Client) Health(ctx context.Context) error { return c.do(ctx, "GET", "/v1/health", nil, nil) }

func (c *Client) ResolveRepo(ctx context.Context, path string) (api.RepoView, error) {
	var rv api.RepoView
	return rv, c.do(ctx, "POST", "/v1/repos", map[string]string{"path": path}, &rv)
}

func (c *Client) CreateBox(ctx context.Context, opts box.NewOptions) (api.Created, error) {
	var out api.Created
	return out, c.do(ctx, "POST", "/v1/boxes", opts, &out)
}

func (c *Client) ListBoxes(ctx context.Context, repoID string) ([]api.BoxView, error) {
	var out []api.BoxView
	return out, c.do(ctx, "GET", "/v1/boxes?repo="+url.QueryEscape(repoID), nil, &out)
}

func (c *Client) GetBox(ctx context.Context, repoID, name string) (api.BoxView, error) {
	var out api.BoxView
	return out, c.do(ctx, "GET", boxPath(repoID, name), nil, &out)
}

func (c *Client) StartBox(ctx context.Context, repoID, name string) error {
	return c.do(ctx, "POST", boxPath(repoID, name)+"/start", nil, nil)
}

func (c *Client) StopBox(ctx context.Context, repoID, name string) error {
	return c.do(ctx, "POST", boxPath(repoID, name)+"/stop", nil, nil)
}

func (c *Client) Merge(ctx context.Context, repoID, name string, opts box.MergeOptions) (api.MergeResult, error) {
	var out api.MergeResult
	return out, c.do(ctx, "POST", boxPath(repoID, name)+"/merge", opts, &out)
}

func (c *Client) RemoveBox(ctx context.Context, repoID, name string, force, keepBranch bool) error {
	q := url.Values{}
	if force {
		q.Set("force", "true")
	}
	if keepBranch {
		q.Set("keep_branch", "true")
	}
	return c.do(ctx, "DELETE", boxPath(repoID, name)+"?"+q.Encode(), nil, nil)
}

func (c *Client) Diff(ctx context.Context, repoID, name, against string) (string, error) {
	var out string
	return out, c.do(ctx, "GET", boxPath(repoID, name)+"/diff?against="+url.QueryEscape(against), nil, &out)
}

func (c *Client) RefreshDB(ctx context.Context, repoID string, terminate bool) error {
	return c.do(ctx, "POST", "/v1/repos/"+url.PathEscape(repoID)+"/db/refresh?terminate="+strconv.FormatBool(terminate), nil, nil)
}

func (c *Client) Doctor(ctx context.Context, repoID string, fix bool) ([]box.Finding, error) {
	method := "GET"
	if fix {
		method = "POST"
	}
	var out []box.Finding
	return out, c.do(ctx, method, "/v1/repos/"+url.PathEscape(repoID)+"/doctor", nil, &out)
}

// Logs writes the last tail lines to w; with follow it keeps streaming until ctx is done.
func (c *Client) Logs(ctx context.Context, repoID, name string, tail int, follow bool, w io.Writer) error {
	path := fmt.Sprintf("%s/logs?tail=%d&follow=%t", boxPath(repoID, name), tail, follow)
	resp, err := c.open(ctx, "GET", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if !follow {
		_, err := io.Copy(w, resp.Body)
		return err
	}
	err = readSSE(resp.Body, func(_ string, data []byte) error {
		var line string
		if err := json.Unmarshal(data, &line); err != nil {
			return err
		}
		_, err := fmt.Fprintln(w, line)
		return err
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Events streams daemon events until ctx is done. The subscription is live
// when Events returns, so actions taken afterwards can't be missed.
func (c *Client) Events(ctx context.Context) (<-chan Event, error) {
	resp, err := c.open(ctx, "GET", "/v1/events", nil)
	if err != nil {
		return nil, err
	}
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		_ = readSSE(resp.Body, func(_ string, data []byte) error {
			var ev Event
			if json.Unmarshal(data, &ev) != nil {
				return nil
			}
			select {
			case ch <- ev:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	return ch, nil
}

func readSSE(r io.Reader, fn func(event string, data []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	var event string
	var data []byte
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if data != nil {
				if err := fn(event, data); err != nil {
					return err
				}
			}
			event, data = "", nil
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event: "):
			event = line[len("event: "):]
		case strings.HasPrefix(line, "data: "):
			data = append(data, line[len("data: "):]...)
		}
	}
	return sc.Err()
}
```

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/daemon/ ./internal/client/`
Expected: PASS (`client` has no test files of its own and is exercised through the daemon test).

- [ ] **Step 6: Checkpoint.** Commit only if asked: `git add internal/daemon internal/client && git commit -m "feat: daemon with auto-start and API client"`

---

### Task 15: `initcfg` package (`agentbox init` detection)

**Files:**
- Create: `internal/initcfg/initcfg.go`
- Test: `internal/initcfg/initcfg_test.go`

**Interfaces:**
- Consumes: `envfile.Parse` (Task 2), `config.Parse` (tests only)
- Produces: `type initcfg.Result struct { YAML string; Warnings []string }`, `func initcfg.Detect(root string) (Result, error)`. The YAML it returns always passes `config.Parse`.

- [ ] **Step 1: Write the failing test**

```go
package initcfg

import (
	"slices"
	"strings"
	"testing"

	"github.com/agentbox-dev/agentbox/internal/config"
	"github.com/agentbox-dev/agentbox/internal/testutil"
)

func detect(t *testing.T, files map[string]string) (Result, *config.Config) {
	t.Helper()
	dir := t.TempDir()
	testutil.WriteFiles(t, dir, files)
	res, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte(res.YAML))
	if err != nil {
		t.Fatalf("generated YAML does not parse: %v\n%s", err, res.YAML)
	}
	return res, cfg
}

func TestDetectNodePostgres(t *testing.T) {
	res, cfg := detect(t, map[string]string{
		"package.json":   `{"scripts": {"dev": "vite --port 5173"}}`,
		"pnpm-lock.yaml": "",
		".env.example":   "PORT=3000\nAPI_PORT=4000\nDATABASE_URL=postgres://u:p@localhost:5432/app\n",
	})
	if cfg.Setup != "pnpm install" || cfg.Dev != "pnpm run dev" {
		t.Errorf("setup/dev = %q / %q", cfg.Setup, cfg.Dev)
	}
	if !slices.Equal(cfg.Ports, []string{"PORT", "API_PORT"}) {
		t.Errorf("ports = %v", cfg.Ports)
	}
	if cfg.DB == nil || cfg.DB.Driver != "postgres" || cfg.DB.URLEnv != "DATABASE_URL" || cfg.DB.Source != "postgres://u:p@localhost:5432/app" {
		t.Errorf("db = %+v", cfg.DB)
	}
	if cfg.Env.Template != ".env.example" {
		t.Errorf("template = %q", cfg.Env.Template)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "hard-codes a port") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestDetectPrefersRealEnvValue(t *testing.T) {
	_, cfg := detect(t, map[string]string{
		".env.example": "DATABASE_URL=postgres://user:pass@localhost/changeme\n",
		".env":         "DATABASE_URL=postgres://me@localhost:5432/realdb\n",
	})
	if cfg.DB == nil || cfg.DB.Source != "postgres://me@localhost:5432/realdb" {
		t.Errorf("db = %+v", cfg.DB)
	}
}

func TestDetectSQLite(t *testing.T) {
	_, cfg := detect(t, map[string]string{".env.example": "DATABASE_URL=file:./dev.db\n"})
	if cfg.DB == nil || cfg.DB.Driver != "sqlite" || cfg.DB.Path() != "dev.db" {
		t.Errorf("db = %+v", cfg.DB)
	}
}

func TestDetectGoAndEmpty(t *testing.T) {
	_, cfg := detect(t, map[string]string{"go.mod": "module x\n"})
	if cfg.Setup != "go mod download" || cfg.Dev != "go run ." || len(cfg.Ports) != 0 {
		t.Errorf("go: %+v", cfg)
	}
	_, cfg = detect(t, nil)
	if cfg.Setup != "" || cfg.DB != nil {
		t.Errorf("empty: %+v", cfg)
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/initcfg/`
Expected: FAIL (`undefined: Detect`).

- [ ] **Step 3: Implement** `internal/initcfg/initcfg.go`

```go
// Package initcfg inspects a repo and drafts an agentbox.yaml for `agentbox init`.
package initcfg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/agentbox-dev/agentbox/internal/envfile"
)

type Result struct {
	YAML     string
	Warnings []string
}

var (
	portKey  = regexp.MustCompile(`(^|_)PORT$`)
	dbKey    = regexp.MustCompile(`^(DATABASE_URL|DB_URL|POSTGRES_URL|DATABASE_PATH|SQLITE_PATH)$`)
	hardPort = regexp.MustCompile(`(--port[ =]|-p ?|PORT=)\d{2,5}`)
)

func exists(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}

func nodePM(root string) string {
	switch {
	case exists(root, "pnpm-lock.yaml"):
		return "pnpm"
	case exists(root, "yarn.lock"):
		return "yarn"
	case exists(root, "bun.lockb"), exists(root, "bun.lock"):
		return "bun"
	}
	return "npm"
}

func Detect(root string) (Result, error) {
	var r Result
	setup, dev := "", ""
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if err := json.Unmarshal(data, &pkg); err != nil {
			r.Warnings = append(r.Warnings, fmt.Sprintf("package.json is not valid JSON: %v", err))
		}
		pm := nodePM(root)
		setup = pm + " install"
		if s, ok := pkg.Scripts["dev"]; ok {
			dev = pm + " run dev"
			if hardPort.MatchString(s) {
				r.Warnings = append(r.Warnings, fmt.Sprintf("the dev script hard-codes a port (%q); make it read the port from an env var so boxes don't collide", s))
			}
		}
	} else if exists(root, "go.mod") {
		setup, dev = "go mod download", "go run ."
	}

	tmpl := ""
	for _, c := range []string{".env.example", ".env.sample", ".env.template"} {
		if exists(root, c) {
			tmpl = c
			break
		}
	}
	var vars []envfile.KV
	if tmpl != "" {
		data, err := os.ReadFile(filepath.Join(root, tmpl))
		if err != nil {
			return r, err
		}
		vars = envfile.Parse(data)
	}
	real := map[string]string{}
	if data, err := os.ReadFile(filepath.Join(root, ".env")); err == nil {
		for _, kv := range envfile.Parse(data) {
			real[kv.Key] = kv.Value
		}
	}

	var portKeys []string
	urlEnv, source := "", ""
	for _, kv := range vars {
		if portKey.MatchString(kv.Key) {
			portKeys = append(portKeys, kv.Key)
		}
		if urlEnv == "" && dbKey.MatchString(kv.Key) {
			urlEnv, source = kv.Key, kv.Value
			if v := real[kv.Key]; v != "" {
				source = v
			}
		}
	}
	driver := ""
	switch {
	case strings.HasPrefix(source, "postgres://"), strings.HasPrefix(source, "postgresql://"):
		driver = "postgres"
	case strings.HasPrefix(source, "file:"), strings.HasSuffix(source, ".db"), strings.HasSuffix(source, ".sqlite"), strings.HasSuffix(source, ".sqlite3"):
		driver = "sqlite"
	case urlEnv != "":
		r.Warnings = append(r.Warnings, fmt.Sprintf("found %s but couldn't tell which database it points to; add a db section by hand", urlEnv))
	}

	var b strings.Builder
	b.WriteString("# Generated by `agentbox init`. Review it before committing.\n")
	b.WriteString("# Reference: https://github.com/agentbox-dev/agentbox#configuration\n")
	b.WriteString("version: 1\n")
	line := func(key, val, hint string) {
		if val != "" {
			fmt.Fprintf(&b, "%s: %s\n", key, strconv.Quote(val))
		} else {
			fmt.Fprintf(&b, "# %s: %s\n", key, hint)
		}
	}
	line("setup", setup, "<command that installs dependencies, e.g. npm install>")
	line("dev", dev, "<command that starts the dev server, e.g. npm run dev>")
	if len(portKeys) > 0 {
		fmt.Fprintf(&b, "ports: [%s]\n", strings.Join(portKeys, ", "))
	} else {
		b.WriteString("ports: []  # env var names that get a free port per box, e.g. [PORT]\n")
	}
	if driver != "" {
		fmt.Fprintf(&b, "db:\n  driver: %s\n  url_env: %s\n  source: %s\n", driver, urlEnv, strconv.Quote(source))
	}
	if tmpl != "" {
		fmt.Fprintf(&b, "env:\n  template: %s\n", tmpl)
	}
	r.YAML = b.String()
	return r, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/initcfg/`
Expected: PASS

- [ ] **Step 5: Checkpoint.** Commit only if asked: `git add internal/initcfg && git commit -m "feat(init): detect a starter agentbox.yaml"`

---

### Task 16: `cli` package + `cmd/agentbox` entry point

**Files:**
- Create: `cmd/agentbox/main.go`, `internal/cli/root.go`, `internal/cli/box_cmds.go`, `internal/cli/admin_cmds.go`, `internal/cli/print.go`
- Test: `internal/cli/cli_test.go`

**Interfaces:**
- Consumes: `daemon.*`, `client.*`, `api.*`, `box.NewOptions/MergeOptions/Finding`, `errs`, `events` type constants, `worktree.MainWorktree`, `config.FileName`, `initcfg.Detect` (Task 15)
- Produces: `var cli.Version = "dev"` (set by `-ldflags`), `func cli.Execute() int`, `func cli.Run(args []string, stdout, stderr io.Writer) int`
- Commands: `init`, `new <name> [--from] [--no-dev] [--keep-failed] [--terminate]`, `ls [--all] [--json]`, `logs <name> [-f] [--tail N]`, `start <name>`, `stop <name>`, `exec <name> -- <cmd…>`, `merge <name> [--into] [--squash]`, `rm <name> [--force] [--keep-branch]`, `db refresh [--terminate]`, `doctor [--fix]`, `daemon run|status|stop|restart`, `version`; persistent `--repo <path>`

- [ ] **Step 1: Write the failing test** at `internal/cli/cli_test.go`

```go
package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/agentbox-dev/agentbox/internal/api"
)

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestUsageErrorsExit2(t *testing.T) {
	for _, args := range [][]string{{"new"}, {"bogus"}, {"ls", "--nope"}, {"rm", "a", "b"}} {
		if code, _, stderr := run(args...); code != 2 {
			t.Errorf("%v: exit %d, stderr %q", args, code, stderr)
		}
	}
}

func TestVersion(t *testing.T) {
	code, out, _ := run("version")
	if code != 0 || strings.TrimSpace(out) != "agentbox dev" {
		t.Fatalf("%d %q", code, out)
	}
}

func TestPrintTable(t *testing.T) {
	var b bytes.Buffer
	printTable(&b, []api.BoxView{{Name: "a", State: "running", Ports: map[string]int{"B_PORT": 2, "A_PORT": 1}, DBName: "app_agentbox_a", Ahead: 2, Dirty: 1}}, false)
	got := b.String()
	for _, want := range []string{"NAME", "a", "running", "A_PORT=1 B_PORT=2", "app_agentbox_a"} {
		if !strings.Contains(got, want) {
			t.Errorf("table missing %q:\n%s", want, got)
		}
	}
	b.Reset()
	printTable(&b, nil, false)
	if !strings.Contains(b.String(), "no boxes") {
		t.Errorf("empty table: %q", b.String())
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go get github.com/spf13/cobra && go test ./internal/cli/`
Expected: FAIL (`undefined: Run`).

- [ ] **Step 3: Implement** `internal/cli/root.go`

```go
// Package cli implements the agentbox command line. Every command except
// init, version and daemon run is a thin client of the daemon's API.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/agentbox-dev/agentbox/internal/api"
	"github.com/agentbox-dev/agentbox/internal/client"
	"github.com/agentbox-dev/agentbox/internal/daemon"
	"github.com/agentbox-dev/agentbox/internal/errs"
)

var Version = "dev"

// exitError carries a specific exit code; a nil err prints nothing.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func usage(err error) error { return &exitError{code: 2, err: err} }

func Execute() int { return Run(os.Args[1:], os.Stdout, os.Stderr) }

func Run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := newRoot()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.err != nil {
			fmt.Fprintln(stderr, "error:", ee.err)
		}
		return ee.code
	}
	fmt.Fprintln(stderr, "error:", err)
	if strings.HasPrefix(err.Error(), "unknown command") {
		return 2
	}
	switch errs.From(err).Code {
	case errs.Precondition, errs.UnmergedWork, errs.Conflict:
		return 3
	}
	return 1
}

type app struct {
	repoFlag string
	paths    daemon.Paths
	c        *client.Client
}

func newRoot() *cobra.Command {
	a := &app{}
	root := &cobra.Command{
		Use:           "agentbox",
		Short:         "Isolated dev environments for parallel AI coding agents",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&a.repoFlag, "repo", "", "path to the repo (default: current directory)")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usage(err) })
	root.AddCommand(
		a.initCmd(), a.newCmd(), a.lsCmd(), a.logsCmd(), a.startCmd(), a.stopCmd(), a.execCmd(),
		a.mergeCmd(), a.rmCmd(), a.dbCmd(), a.doctorCmd(), a.daemonCmd(), versionCmd(),
	)
	return root
}

func exactArgs(n int, what string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != n {
			return usage(fmt.Errorf("expected %s", what))
		}
		return nil
	}
}

func (a *app) connect() error {
	home, err := daemon.DefaultHome()
	if err != nil {
		return err
	}
	a.paths = daemon.PathsFor(home)
	if err := daemon.EnsureRunning(a.paths); err != nil {
		return err
	}
	a.c = client.New(a.paths.Sock)
	return nil
}

func (a *app) repoPath() (string, error) {
	p := a.repoFlag
	if p == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		p = wd
	}
	return filepath.Abs(p)
}

// setup connects to the daemon and resolves the current repo.
func (a *app) setup(ctx context.Context) (api.RepoView, error) {
	if err := a.connect(); err != nil {
		return api.RepoView{}, err
	}
	p, err := a.repoPath()
	if err != nil {
		return api.RepoView{}, err
	}
	return a.c.ResolveRepo(ctx, p)
}
```

- [ ] **Step 4: Implement** `internal/cli/box_cmds.go`

```go
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/agentbox-dev/agentbox/internal/box"
	"github.com/agentbox-dev/agentbox/internal/client"
	"github.com/agentbox-dev/agentbox/internal/errs"
	"github.com/agentbox-dev/agentbox/internal/events"
)

func (a *app) newCmd() *cobra.Command {
	var opts box.NewOptions
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Create a box: worktree, ports, database copy, env file and dev server",
		Args:  exactArgs(1, "a box name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			repo, err := a.setup(ctx)
			if err != nil {
				return err
			}
			opts.RepoPath, opts.Name = repo.Path, args[0]
			evs, err := a.c.Events(ctx) // subscribe before creating so no event is missed
			if err != nil {
				return err
			}
			created, err := a.c.CreateBox(ctx, opts)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "creating box %s\n", opts.Name)
			if err := waitCreated(evs, created.ID, out); err != nil {
				return err
			}
			b, err := a.c.GetBox(ctx, repo.ID, opts.Name)
			if err != nil {
				return err
			}
			printBox(out, b)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.From, "from", "", "branch or commit to start from (default: current HEAD)")
	f.BoolVar(&opts.NoDev, "no-dev", false, "don't start the dev server")
	f.BoolVar(&opts.KeepFailed, "keep-failed", false, "keep a box that failed to create, for debugging")
	f.BoolVar(&opts.Terminate, "terminate", false, "terminate stray connections to the Postgres template")
	return cmd
}

func waitCreated(evs <-chan client.Event, id string, out io.Writer) error {
	for ev := range evs {
		if ev.Box != id {
			continue
		}
		switch ev.Type {
		case events.BoxStep:
			fmt.Fprintf(out, "  • %v\n", ev.Data["step"])
		case events.BoxState:
			if e, ok := ev.Data["error"]; ok {
				return errors.New(fmt.Sprint(e))
			}
			switch ev.Data["state"] {
			case "ready", "running":
				return nil
			case "stopped":
				return fmt.Errorf("box created, but its dev server keeps crashing: %v\nhint: see `agentbox logs %s`", ev.Data["last_error"], id[strings.LastIndex(id, "/")+1:])
			}
		case events.BoxRemoved:
			if e, ok := ev.Data["error"]; ok {
				return errors.New(fmt.Sprint(e))
			}
			return errors.New("the box was removed while it was being created")
		}
	}
	return errors.New("lost the connection to the agentbox daemon")
}

func (a *app) lsCmd() *cobra.Command {
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List boxes",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			repoID := ""
			if all {
				if err := a.connect(); err != nil {
					return err
				}
			} else {
				repo, err := a.setup(ctx)
				if err != nil {
					return err
				}
				repoID = repo.ID
			}
			boxes, err := a.c.ListBoxes(ctx, repoID)
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(boxes)
			}
			printTable(cmd.OutOrStdout(), boxes, all)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "list boxes of every repo")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func (a *app) logsCmd() *cobra.Command {
	var follow bool
	var tail int
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Show a box's setup and dev-server output",
		Args:  exactArgs(1, "a box name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			repo, err := a.setup(ctx)
			if err != nil {
				return err
			}
			err = a.c.Logs(ctx, repo.ID, args[0], tail, follow, cmd.OutOrStdout())
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep streaming new output")
	cmd.Flags().IntVar(&tail, "tail", 200, "number of lines to show")
	return cmd
}

func (a *app) simpleBoxCmd(use, short, done string, fn func(ctx context.Context, repoID, name string) error) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <name>",
		Short: short,
		Args:  exactArgs(1, "a box name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := a.setup(cmd.Context())
			if err != nil {
				return err
			}
			if err := fn(cmd.Context(), repo.ID, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", done, args[0])
			return nil
		},
	}
}

func (a *app) startCmd() *cobra.Command {
	return a.simpleBoxCmd("start", "Start a box's dev server", "started", func(ctx context.Context, r, n string) error { return a.c.StartBox(ctx, r, n) })
}

func (a *app) stopCmd() *cobra.Command {
	return a.simpleBoxCmd("stop", "Stop a box's dev server (keeps its files, ports and database)", "stopped", func(ctx context.Context, r, n string) error { return a.c.StopBox(ctx, r, n) })
}

func (a *app) execCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec <name> -- <command> [args...]",
		Short: "Run a command inside a box with its env",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) < 2 {
				return usage(errors.New("expected a box name and a command"))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := a.setup(cmd.Context())
			if err != nil {
				return err
			}
			b, err := a.c.GetBox(cmd.Context(), repo.ID, args[0])
			if err != nil {
				return err
			}
			c := exec.Command(args[1], args[2:]...)
			c.Dir = b.Dir
			c.Env = os.Environ()
			for k, v := range b.Env {
				c.Env = append(c.Env, k+"="+v)
			}
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr()
			if err := c.Run(); err != nil {
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					return &exitError{code: ee.ExitCode()}
				}
				return err
			}
			return nil
		},
	}
	cmd.Flags().SetInterspersed(false) // everything after the box name belongs to the command
	return cmd
}

func (a *app) mergeCmd() *cobra.Command {
	var opts box.MergeOptions
	cmd := &cobra.Command{
		Use:   "merge <name>",
		Short: "Merge a box's branch into the main checkout's current branch",
		Args:  exactArgs(1, "a box name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := a.setup(cmd.Context())
			if err != nil {
				return err
			}
			res, err := a.c.Merge(cmd.Context(), repo.ID, args[0], opts)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "merged agentbox/%s into %s\nremove the box with `agentbox rm %s`\n", args[0], res.Into, args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Into, "into", "", "target branch (must be checked out in the main checkout)")
	cmd.Flags().BoolVar(&opts.Squash, "squash", false, "squash the box's commits into one")
	return cmd
}

func (a *app) rmCmd() *cobra.Command {
	var force, keepBranch bool
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a box: stop it, drop its database, free its ports, delete its worktree",
		Args:  exactArgs(1, "a box name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, out := cmd.Context(), cmd.OutOrStdout()
			repo, err := a.setup(ctx)
			if err != nil {
				return err
			}
			err = a.c.RemoveBox(ctx, repo.ID, args[0], force, keepBranch)
			if err != nil && errs.From(err).Code == errs.UnmergedWork && isTerminal(os.Stdin) {
				fmt.Fprintf(out, "%s\nRemove anyway and discard it? [y/N] ", errs.From(err).Message)
				ans, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.ToLower(strings.TrimSpace(ans)) != "y" {
					return &exitError{code: 3, err: errors.New("aborted")}
				}
				err = a.c.RemoveBox(ctx, repo.ID, args[0], true, keepBranch)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "removed box %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "discard uncommitted or unmerged work without asking")
	cmd.Flags().BoolVar(&keepBranch, "keep-branch", false, "keep the agentbox/<name> branch")
	return cmd
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
```

- [ ] **Step 5: Implement** `internal/cli/admin_cmds.go`

```go
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/agentbox-dev/agentbox/internal/client"
	"github.com/agentbox-dev/agentbox/internal/config"
	"github.com/agentbox-dev/agentbox/internal/daemon"
	"github.com/agentbox-dev/agentbox/internal/errs"
	"github.com/agentbox-dev/agentbox/internal/initcfg"
	"github.com/agentbox-dev/agentbox/internal/worktree"
)

func (a *app) initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Write a starter agentbox.yaml for this repo",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.repoPath()
			if err != nil {
				return err
			}
			root, err := worktree.MainWorktree(p)
			if err != nil {
				return err
			}
			target := filepath.Join(root, config.FileName)
			if _, err := os.Stat(target); err == nil {
				return errs.New(errs.Precondition, "%s already exists", target)
			}
			res, err := initcfg.Detect(root)
			if err != nil {
				return err
			}
			if err := os.WriteFile(target, []byte(res.YAML), 0o644); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "wrote %s\n", target)
			for _, w := range res.Warnings {
				fmt.Fprintf(out, "warning: %s\n", w)
			}
			fmt.Fprintln(out, "review it, then create a box with `agentbox new <name>`")
			return nil
		},
	}
}

func (a *app) dbCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "db", Short: "Database commands"}
	var terminate bool
	refresh := &cobra.Command{
		Use:   "refresh",
		Short: "Re-snapshot the source database that new boxes are cloned from",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			repo, err := a.setup(cmd.Context())
			if err != nil {
				return err
			}
			if err := a.c.RefreshDB(cmd.Context(), repo.ID, terminate); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "database snapshot refreshed")
			return nil
		},
	}
	refresh.Flags().BoolVar(&terminate, "terminate", false, "terminate stray connections to the template")
	cmd.AddCommand(refresh)
	return cmd
}

func (a *app) doctorCmd() *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Find (and with --fix, safely repair) drift between agentbox and reality",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			problems := 0
			if _, err := exec.LookPath("git"); err != nil {
				fmt.Fprintln(out, "✗ git not found on PATH")
				problems++
			}
			repo, err := a.setup(cmd.Context())
			if err != nil {
				return err
			}
			fs, err := a.c.Doctor(cmd.Context(), repo.ID, fix)
			if err != nil {
				return err
			}
			for _, f := range fs {
				mark := "✗"
				if f.Fixed {
					mark = "✓ fixed"
				}
				fmt.Fprintf(out, "%s [%s] %s\n", mark, f.Kind, f.Detail)
				problems++
			}
			if problems == 0 {
				fmt.Fprintln(out, "✓ no problems found")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "repair what can be repaired safely")
	return cmd
}

func (a *app) daemonCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "daemon", Short: "Manage the background daemon (it starts automatically)"}
	paths := func() (daemon.Paths, error) {
		home, err := daemon.DefaultHome()
		return daemon.PathsFor(home), err
	}
	run := &cobra.Command{
		Use:    "run",
		Short:  "Run the daemon in the foreground",
		Hidden: true,
		Args:   exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := paths()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return daemon.Run(ctx, p)
		},
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show whether the daemon is running",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := paths()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !daemon.Ping(p.Sock) {
				fmt.Fprintln(out, "daemon not running (it starts automatically on the next command)")
				return nil
			}
			pid, _ := daemon.ReadPID(p.PID)
			boxes, err := client.New(p.Sock).ListBoxes(cmd.Context(), "")
			if err != nil {
				return err
			}
			running := 0
			for _, b := range boxes {
				if b.State == "running" {
					running++
				}
			}
			fmt.Fprintf(out, "daemon running (pid %d)\nsocket: %s\nboxes: %d total, %d running\n", pid, p.Sock, len(boxes), running)
			return nil
		},
	}
	stopDaemon := func(cmd *cobra.Command, p daemon.Paths) error {
		if daemon.Ping(p.Sock) {
			if boxes, err := client.New(p.Sock).ListBoxes(cmd.Context(), ""); err == nil {
				for _, b := range boxes {
					if b.State == "running" {
						fmt.Fprintf(cmd.OutOrStdout(), "stopping dev server of %s\n", b.Name)
					}
				}
			}
		}
		return daemon.Stop(p, 15*time.Second)
	}
	stop := &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon and every dev server",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := paths()
			if err != nil {
				return err
			}
			return stopDaemon(cmd, p)
		},
	}
	restart := &cobra.Command{
		Use:   "restart",
		Short: "Restart the daemon (dev servers are stopped, not restarted)",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := paths()
			if err != nil {
				return err
			}
			if err := stopDaemon(cmd, p); err != nil {
				return err
			}
			if err := daemon.EnsureRunning(p); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "daemon restarted")
			return nil
		},
	}
	cmd.AddCommand(run, status, stop, restart)
	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "agentbox", Version)
			return nil
		},
	}
}

```


- [ ] **Step 6: Implement** `internal/cli/print.go`

```go
package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/agentbox-dev/agentbox/internal/api"
)

func formatPorts(p map[string]int) string {
	if len(p) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, p[k]))
	}
	return strings.Join(parts, " ")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func printTable(out io.Writer, boxes []api.BoxView, showRepo bool) {
	if len(boxes) == 0 {
		fmt.Fprintln(out, "no boxes (create one with `agentbox new <name>`)")
		return
	}
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	header := "NAME\tSTATE\tPORTS\tDB\tAHEAD\tDIRTY"
	if showRepo {
		header = "REPO\t" + header
	}
	fmt.Fprintln(tw, header)
	for _, b := range boxes {
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%d\t%d", b.Name, b.State, formatPorts(b.Ports), dash(b.DBName), b.Ahead, b.Dirty)
		if showRepo {
			row = b.RepoPath + "\t" + row
		}
		fmt.Fprintln(tw, row)
	}
	tw.Flush()
}

func printBox(out io.Writer, b api.BoxView) {
	fmt.Fprintf(out, "\nbox %s is %s\n", b.Name, b.State)
	fmt.Fprintf(out, "  dir     %s\n", b.Dir)
	fmt.Fprintf(out, "  branch  %s\n", b.Branch)
	fmt.Fprintf(out, "  ports   %s\n", formatPorts(b.Ports))
	if b.DBName != "" {
		fmt.Fprintf(out, "  db      %s\n", b.DBName)
	}
	if b.LastError != "" {
		fmt.Fprintf(out, "  error   %s\n", b.LastError)
	}
	fmt.Fprintf(out, "\npoint your agent at it:\n  cd %q\n", b.Dir)
}
```

- [ ] **Step 7: Implement** `cmd/agentbox/main.go`

```go
package main

import (
	"os"

	"github.com/agentbox-dev/agentbox/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
```

- [ ] **Step 8: Run the tests and build**

Run: `go test ./internal/cli/ && go build -o /tmp/agentbox ./cmd/agentbox && /tmp/agentbox --help`
Expected: tests PASS, and the help output lists all commands.

- [ ] **Step 9: Checkpoint.** Commit only if asked: `git add cmd internal/cli go.mod go.sum && git commit -m "feat(cli): agentbox command line"`

---

### Task 17: Examples, end-to-end test, CI, release, README

**Files:**
- Create: `examples/sqlite/{agentbox.yaml,.env.example,.gitignore,package.json,server.js,README.md}`
- Create: `examples/node-postgres/{agentbox.yaml,.env.example,.gitignore,package.json,server.js,README.md}`
- Create: `e2e/e2e_test.go` (build tag `e2e`)
- Create: `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `.goreleaser.yaml`, `README.md`, `LICENSE`

**Interfaces:**
- Consumes: the built binary, `api.BoxView` (for `ls --json`), `testutil.InitRepo`
- Produces: `go test -tags e2e ./e2e/...`. On any machine with git and node it runs 3 SQLite boxes. With `AGENTBOX_TEST_PG_URL` set it also runs 3 Postgres boxes.

- [ ] **Step 1: Create the SQLite example**

`examples/sqlite/agentbox.yaml`:
```yaml
version: 1
setup: npm install
dev: node server.js
ports: [APP_PORT]
db:
  driver: sqlite
  url_env: DATABASE_PATH
  source: dev.db
env:
  template: .env.example
```

`examples/sqlite/.env.example`:
```
APP_PORT=3000
DATABASE_PATH=dev.db
```

`examples/sqlite/.gitignore`:
```
.env
dev.db
node_modules/
```

`examples/sqlite/package.json`:
```json
{ "name": "agentbox-example-sqlite", "private": true, "version": "0.0.0", "scripts": { "dev": "node server.js" } }
```

`examples/sqlite/server.js`:
```js
// Tiny app that reports which box it runs in and whether its own database copy exists.
const http = require('http');
const fs = require('fs');

const port = Number(process.env.APP_PORT || 3000);
const dbPath = process.env.DATABASE_PATH || 'dev.db';

http
  .createServer((req, res) => {
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ box: process.env.AGENTBOX_NAME || null, db: dbPath, exists: fs.existsSync(dbPath) }));
  })
  .listen(port, '127.0.0.1', () => console.log(`listening on ${port}`));
```

`examples/sqlite/README.md`:
```markdown
# SQLite example

    sqlite3 dev.db 'CREATE TABLE items(name TEXT);'   # any SQLite file works
    agentbox new a && agentbox new b
    agentbox ls        # each box has its own APP_PORT and its own copy of dev.db
```

- [ ] **Step 2: Create the Postgres example.** It's the same app, reporting the database name from `DATABASE_URL`.

`examples/node-postgres/agentbox.yaml`:
```yaml
version: 1
setup: npm install
dev: node server.js
ports: [APP_PORT]
db:
  driver: postgres
  url_env: DATABASE_URL
  source: postgres://postgres:postgres@localhost:5432/myapp
env:
  template: .env.example
```

`examples/node-postgres/.env.example`:
```
APP_PORT=3000
DATABASE_URL=postgres://postgres:postgres@localhost:5432/myapp
```

`examples/node-postgres/.gitignore`:
```
.env
node_modules/
```

`examples/node-postgres/package.json`:
```json
{ "name": "agentbox-example-postgres", "private": true, "version": "0.0.0", "scripts": { "dev": "node server.js" } }
```

`examples/node-postgres/server.js`:
```js
// Tiny app that reports which box it runs in and which database it was given.
const http = require('http');

const port = Number(process.env.APP_PORT || 3000);
const db = new URL(process.env.DATABASE_URL).pathname.slice(1);

http
  .createServer((req, res) => {
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ box: process.env.AGENTBOX_NAME || null, db }));
  })
  .listen(port, '127.0.0.1', () => console.log(`listening on ${port}`));
```

`examples/node-postgres/README.md`:
```markdown
# Postgres example

Needs a local Postgres with a `myapp` database and the `pg_dump`/`pg_restore` tools.

    createdb myapp
    agentbox new a && agentbox new b
    agentbox ls        # databases myapp_agentbox_a and myapp_agentbox_b, cloned from a snapshot of myapp
```

- [ ] **Step 3: Write the e2e test** at `e2e/e2e_test.go`

```go
//go:build e2e

// Package e2e builds the agentbox binary and drives real boxes end to end.
package e2e

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "modernc.org/sqlite"

	"github.com/agentbox-dev/agentbox/internal/api"
	"github.com/agentbox-dev/agentbox/internal/testutil"
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "agentbox-bin")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "agentbox")
	build := exec.Command("go", "build", "-o", bin, "./cmd/agentbox")
	build.Dir = ".."
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type env struct {
	t    *testing.T
	vars []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required for the e2e examples")
	}
	home, err := os.MkdirTemp("/tmp", "abe2e") // short path: unix socket length limit
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("api_addr: 127.0.0.1:0\n"), 0o600)
	e := &env{t: t, vars: append(os.Environ(), "AGENTBOX_HOME="+home)}
	t.Cleanup(func() {
		stop := exec.Command(bin, "daemon", "stop")
		stop.Env = e.vars
		_ = stop.Run()
		os.RemoveAll(home)
	})
	return e
}

func (e *env) run(dir string, args ...string) string {
	e.t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir, cmd.Env = dir, e.vars
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("agentbox %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func copyExample(t *testing.T, name string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), name)
	if err := os.CopyFS(dst, os.DirFS(filepath.Join("..", "examples", name))); err != nil {
		t.Fatal(err)
	}
	return testutil.InitRepo(t, dst)
}

func getJSON(t *testing.T, u string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := http.Get(u)
		if err == nil {
			var v map[string]any
			err = json.NewDecoder(resp.Body).Decode(&v)
			resp.Body.Close()
			if err == nil {
				return v
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s never succeeded: %v", u, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func threeBoxes(t *testing.T, e *env, repo string, check func(b api.BoxView, resp map[string]any)) {
	names := []string{"a", "b", "c"}
	for _, n := range names {
		e.run(repo, "new", n)
	}
	var boxes []api.BoxView
	if err := json.Unmarshal([]byte(e.run(repo, "ls", "--json")), &boxes); err != nil || len(boxes) != 3 {
		t.Fatalf("ls --json: %v (%d boxes)", err, len(boxes))
	}
	seen := map[int]bool{}
	for _, b := range boxes {
		port := b.Ports["APP_PORT"]
		if port == 0 || seen[port] {
			t.Fatalf("box %s: bad or duplicate APP_PORT %d", b.Name, port)
		}
		seen[port] = true
		resp := getJSON(t, fmt.Sprintf("http://127.0.0.1:%d/", port))
		if resp["box"] != b.Name {
			t.Errorf("port %d answered as box %v, want %s", port, resp["box"], b.Name)
		}
		check(b, resp)
	}
	for _, n := range names {
		e.run(repo, "rm", "--force", n)
	}
	if out := e.run(repo, "doctor"); !strings.Contains(out, "no problems found") {
		t.Errorf("doctor after cleanup:\n%s", out)
	}
}

func TestThreeBoxesSQLite(t *testing.T) {
	e := newEnv(t)
	repo := copyExample(t, "sqlite")
	c, err := sql.Open("sqlite", filepath.Join(repo, "dev.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(`CREATE TABLE items(name TEXT)`); err != nil {
		t.Fatal(err)
	}
	c.Close()
	threeBoxes(t, e, repo, func(b api.BoxView, resp map[string]any) {
		if resp["exists"] != true {
			t.Errorf("box %s: its sqlite copy is missing: %v", b.Name, resp)
		}
	})
}

func TestThreeBoxesPostgres(t *testing.T) {
	admin := os.Getenv("AGENTBOX_TEST_PG_URL")
	if admin == "" {
		t.Skip("set AGENTBOX_TEST_PG_URL to run the Postgres e2e test")
	}
	e := newEnv(t)
	repo := copyExample(t, "node-postgres")

	ctx := context.Background()
	name := fmt.Sprintf("abe2e_%d", time.Now().UnixNano()%1_000_000_000)
	ac, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ac.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		rows, _ := ac.Query(ctx, "SELECT datname FROM pg_database WHERE starts_with(datname, $1)", name)
		dbs, _ := pgx.CollectRows(rows, pgx.RowTo[string])
		for _, d := range dbs {
			ac.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{d}.Sanitize()+" WITH (FORCE)")
		}
		ac.Close(ctx)
	})
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	cfgPath := filepath.Join(repo, "agentbox.yaml")
	cfg, _ := os.ReadFile(cfgPath)
	os.WriteFile(cfgPath, []byte(strings.Replace(string(cfg), "postgres://postgres:postgres@localhost:5432/myapp", u.String(), 1)), 0o644)

	threeBoxes(t, e, repo, func(b api.BoxView, resp map[string]any) {
		if want := name + "_agentbox_" + b.Name; resp["db"] != want {
			t.Errorf("box %s got database %v, want %s", b.Name, resp["db"], want)
		}
	})
}
```

- [ ] **Step 4: Run the e2e test locally**

Run: `go test -tags e2e ./e2e/ -v -run SQLite`
Expected: PASS (three boxes answer on distinct ports, then the cleanup leaves `doctor` clean).

- [ ] **Step 5: Write CI** at `.github/workflows/ci.yml`

```yaml
name: ci
on: [push, pull_request]

jobs:
  test:
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.24' }
      - uses: actions/setup-node@v4
        with: { node-version: '22' }
      - run: go vet ./...
      - run: go test -race ./...
      - run: go test -tags e2e ./e2e/...

  postgres:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:16
        env: { POSTGRES_PASSWORD: postgres }
        ports: ['5432:5432']
        options: >-
          --health-cmd pg_isready --health-interval 5s --health-timeout 5s --health-retries 10
    env:
      AGENTBOX_TEST_PG_URL: postgres://postgres:postgres@localhost:5432/postgres
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.24' }
      - uses: actions/setup-node@v4
        with: { node-version: '22' }
      - run: sudo apt-get update && sudo apt-get install -y postgresql-client-16
      - run: go test -race ./internal/db/...
      - run: go test -tags e2e -run Postgres ./e2e/...
```

- [ ] **Step 6: Write the release config.** `.goreleaser.yaml`:

```yaml
version: 2
builds:
  - main: ./cmd/agentbox
    env: [CGO_ENABLED=0]
    goos: [darwin, linux]
    goarch: [amd64, arm64]
    ldflags: ["-s -w -X github.com/agentbox-dev/agentbox/internal/cli.Version={{.Version}}"]
archives:
  - formats: [tar.gz]
checksum:
  name_template: checksums.txt
brews:
  - repository: { owner: agentbox-dev, name: homebrew-tap, token: "{{ .Env.HOMEBREW_TAP_TOKEN }}" }
    homepage: https://github.com/agentbox-dev/agentbox
    description: Isolated dev environments for parallel AI coding agents
    license: MIT
```

`.github/workflows/release.yml`:

```yaml
name: release
on:
  push:
    tags: ['v*']
permissions:
  contents: write
jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      - uses: actions/setup-go@v5
        with: { go-version: '1.24' }
      - uses: goreleaser/goreleaser-action@v6
        with: { args: release --clean }
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          HOMEBREW_TAP_TOKEN: ${{ secrets.HOMEBREW_TAP_TOKEN }}
```

Check locally: `go run github.com/goreleaser/goreleaser/v2@latest check`. Expected: `config is valid`. (Publishing needs the `agentbox-dev/homebrew-tap` repo and a `HOMEBREW_TAP_TOKEN` secret. Those are one-time manual steps for the maintainer.)

- [ ] **Step 7: Write `LICENSE`**

```
MIT License

Copyright (c) 2026 The agentbox authors

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

**and `README.md`**:

````markdown
# agentbox

**Isolated dev environments for parallel AI coding agents.**

Running three agents on one repo? They fight over files, ports and your database. `agentbox new a` gives each agent its own
git worktree, its own ports, its own database copy, its own `.env`, and its own running dev server, in seconds.

```
$ agentbox new a && agentbox new b && agentbox new c
$ agentbox ls
NAME  STATE    PORTS           DB                AHEAD  DIRTY
a     running  APP_PORT=20000  myapp_agentbox_a  0      0
b     running  APP_PORT=20001  myapp_agentbox_b  2      1
c     running  APP_PORT=20002  myapp_agentbox_c  0      3
```

## Install

    go install github.com/agentbox-dev/agentbox/cmd/agentbox@latest
    # or: brew install agentbox-dev/tap/agentbox

macOS and Linux. Needs `git` ≥ 2.38, plus `pg_dump`/`pg_restore` if you use Postgres.

## Quick start

    cd your-repo
    agentbox init          # writes a starter agentbox.yaml. Review it.
    agentbox new feature-x # prints the box dir: point your agent there
    agentbox logs feature-x -f
    agentbox merge feature-x
    agentbox rm feature-x

## Configuration

`agentbox.yaml` at the repo root (read from your main checkout, so uncommitted edits apply):

```yaml
version: 1
setup: npm install            # runs once per new box
dev: npm run dev              # supervised; restarted with backoff if it crashes
ports: [APP_PORT, API_PORT]   # each gets a free port per box
db:
  driver: postgres            # postgres | sqlite
  url_env: DATABASE_URL
  source: postgres://localhost:5432/myapp
env:
  template: .env.example      # copied and rewritten into the box's .env
  set: { NODE_ENV: development }
copy: [.env.local]            # untracked files to copy from your main checkout
```

Your app must read its ports from these env vars. Boxes can't share a hard-coded port.

## Commands

| Command | |
|---|---|
| `init` | draft `agentbox.yaml` from the repo |
| `new <name> [--from REF] [--no-dev]` | create a box |
| `ls [--all] [--json]` | list boxes |
| `logs <name> [-f]` | setup + dev-server output |
| `start` / `stop <name>` | control the dev server |
| `exec <name> -- <cmd>` | run a command in the box with its env |
| `merge <name> [--squash]` | merge into the main checkout's current branch (never forces) |
| `rm <name> [--force] [--keep-branch]` | remove; asks before discarding work |
| `db refresh` | re-snapshot the source database |
| `doctor [--fix]` | find and safely repair drift |

## How it works

A small daemon (started automatically) owns every box and serves a local API on a Unix socket and `127.0.0.1:7777` (token-protected).
Boxes live in `~/.agentbox/boxes/`, outside your repo, so your file watchers never see them. Postgres boxes are cloned with
`CREATE DATABASE … TEMPLATE` from a `pg_dump` snapshot, so cloning is instant and your main database can stay in use.

## Roadmap

Web dashboard → terminal UI → Docker Compose & MySQL drivers → desktop app.
````

- [ ] **Step 8: Full verification**

Run: `go vet ./... && go test -race ./... && go test -tags e2e ./e2e/... && CGO_ENABLED=0 go build ./cmd/agentbox`
Expected: everything PASS, and the cgo-free build succeeds.

- [ ] **Step 9: Checkpoint.** Commit only if asked: `git add . && git commit -m "feat: examples, e2e test, CI, release config and README"`
