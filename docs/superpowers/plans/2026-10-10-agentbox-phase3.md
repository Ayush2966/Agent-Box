# agentbox Phase 3 — Terminal UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `agentbox tui` opens a live, keyboard-driven, full-screen terminal dashboard over the existing daemon API with the same actions as the CLI and web dashboard, plus a shell into a box and open-in-browser.

**Architecture:** One bubbletea root model (`internal/tui`) with a `mode` enum (boxes / box / new / doctor) and an overlay slot (confirm / prompt), fed by `tea.Cmd`s that call a small `API` interface satisfied by `*client.Client`. Live updates come from the daemon's SSE event stream through the standard channel-pump pattern; a reducer applies them exactly like the web app. Leaves use `bubbles` (table, viewport, textinput, help, key); styling is `lipgloss`.

**Tech Stack:** Go 1.24 (go directive moves to `1.24.2`, see Global Constraints), `github.com/charmbracelet/bubbletea v1.3.10`, `bubbles v1.0.0`, `lipgloss v1.1.0`. No other new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-10-agentbox-phase3-design.md` — read it first; this plan argues from it.

## Global Constraints

- Dependencies are added ONLY with pinned versions: `go get github.com/charmbracelet/bubbletea@v1.3.10 github.com/charmbracelet/bubbles@v1.0.0 github.com/charmbracelet/lipgloss@v1.1.0`. **Never run `go mod tidy`** (it would re-raise `rogpeppe/go-internal` to a go-1.25 version, see the phase 1 ledger) and never a bare `go get`. These modules' transitive deps raise `go.mod`'s `go` directive from `1.24.0` to `1.24.2`; that is expected and accepted (spec R6). The local toolchain is go1.24.2; CI uses `go-version: '1.24'`.
- `git commit` is never run by this plan (standing rule). "Commit"/"Checkpoint" steps mean: run the verification command and record the result in the ledger.
- `go test ./...` must stay runnable without a terminal: nothing in tests calls `tea.NewProgram`; tests drive `Model.Update` with messages and assert on `Model.View()` text, against a fake `API`.
- Daemon homes in tests live under `/tmp` (macOS unix-socket path cap of 103 bytes).
- Box names match `^[a-z0-9][a-z0-9-]{0,30}$` (phase 1), so column width math may assume ASCII.
- Keys, columns and screens are exactly those in spec §6; messages/toasts use the error format `step failed: message — hint` (spec §7). Table cells are PLAIN TEXT (bubbles' table truncates with a non-ANSI-aware width); colour is used only outside the table.
- Never write `return m, m.X()` where `X` has a pointer receiver that mutates `m` (`say`, `openBox`, `setTab`, `openDoctor`, `focusField`): Go does not fix the evaluation order. Use `cmd := m.X(); return m, cmd`.
- The TUI talks to the daemon only through the `API` interface (spec R5); `client.Client` must satisfy it (`var _ API = (*client.Client)(nil)`).

## Review Focus

Inputs the spec implies but no task's tests would otherwise exercise; each line's pinning test is added to the owning task.

1. **Typing `q` into the new-box name field** — must insert a `q`, not quit the program. *(Task 3: `TestNewBoxFlow` types a name containing `q`.)*
2. **A `box.removed` event for the box whose screen is open** — the user expects to land back on the list with an explanation, not a blank screen or a crash on a nil map entry. *(Task 3: `TestRemovedBoxLeavesScreen`.)*
3. **A log stream whose box changes underneath** — lines from a previous box's stream must not leak into the newly opened box's log view. *(Task 3: `TestStaleLogLinesIgnored`.)*
4. **The events channel closing repeatedly** — backoff must cap at 16× and never spin; the connection pill must say `reconnecting`. *(Task 2: `TestReconnectBackoff`.)*
5. **An action on a box that no longer exists (row selected, then removed elsewhere)** — the error must surface as a toast, and the table must not keep a stale selection that panics. *(Task 2: `TestActionOnVanishedBoxToasts`.)*

---

## File structure

```
internal/client/client.go            + Repos, LogLines                                   (Task 1)
internal/client/client_test.go       new: unix-socket fake server                        (Task 1)
internal/tui/api.go                  API interface                                       (Task 2)
internal/tui/msgs.go                 message types + API cmds                            (Task 2)
internal/tui/reduce.go               applyEvent                                          (Task 2)
internal/tui/keys.go                 keyMap + help groups                                (Task 2)
internal/tui/styles.go               lipgloss styles, pills, helpers                     (Task 2)
internal/tui/overlay.go              confirm, prompt, dialog rendering                   (Task 2)
internal/tui/model.go                Model, New, Init, Update, View, layout, header/footer (Task 2)
internal/tui/view_boxes.go           boxes table + actions                               (Task 2)
internal/tui/view_box.go             stub in Task 2 → real in Task 3                     (Tasks 2, 3)
internal/tui/view_new.go             stub in Task 2 → real in Task 3                     (Tasks 2, 3)
internal/tui/view_doctor.go          stub in Task 2 → real in Task 3                     (Tasks 2, 3)
internal/tui/tui_test.go             fake API + helpers + tests                          (Tasks 2, 3)
internal/tui/tui.go                  Run                                                 (Task 4)
internal/tui/integration_test.go     real daemon                                         (Task 4)
internal/cli/tui_cmd.go, root.go     `agentbox tui`                                      (Task 4)
README.md                                                                                (Task 4)
```

---

### Task 1: Client additions — `Repos` and `LogLines`

**Files:**
- Modify: `internal/client/client.go`
- Create: `internal/client/client_test.go`

**Interfaces:**
- Produces: `func (c *Client) Repos(ctx context.Context) ([]api.RepoView, error)`; `func (c *Client) LogLines(ctx context.Context, repoID, name string, tail int) (<-chan string, error)` — the channel delivers the last `tail` lines then new ones, and is closed when ctx is done or the stream ends. Task 2's `API` interface names both.

- [ ] **Step 1: Write the failing test**

Create `internal/client/client_test.go`:

```go
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeDaemon serves a tiny slice of the v1 API on a unix socket.
func fakeDaemon(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "abcl") // short: unix socket path limit
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/repos", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":"app-1a2b3c","path":"/tmp/app"}]`)
	})
	mux.HandleFunc("GET /v1/repos/{repo}/boxes/{name}/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("follow") != "true" || r.URL.Query().Get("tail") != "10" {
			http.Error(w, "expected follow=true&tail=10, got "+r.URL.RawQuery, 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for _, line := range []string{"listening on 20000", "GET / 200"} {
			b, _ := json.Marshal(line)
			fmt.Fprintf(w, "event: line\ndata: %s\n\n", b)
			f.Flush()
		}
		<-r.Context().Done() // hold the stream open until the client goes away
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close(); os.RemoveAll(dir) })
	return sock
}

func TestRepos(t *testing.T) {
	c := New(fakeDaemon(t))
	repos, err := c.Repos(context.Background())
	if err != nil || len(repos) != 1 || repos[0].ID != "app-1a2b3c" || repos[0].Path != "/tmp/app" {
		t.Fatalf("%+v %v", repos, err)
	}
}

func TestLogLinesStreamsThenClosesOnCancel(t *testing.T) {
	c := New(fakeDaemon(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.LogLines(ctx, "app-1a2b3c", "a", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"listening on 20000", "GET / 200"} {
		select {
		case got := <-ch:
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("unexpected extra line")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel did not close after cancel")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/client/`
Expected: FAIL — `c.Repos undefined`, `c.LogLines undefined`.

- [ ] **Step 3: Implement**

Append to `internal/client/client.go`:

```go
// Repos lists every repo the daemon knows.
func (c *Client) Repos(ctx context.Context) ([]api.RepoView, error) {
	var out []api.RepoView
	return out, c.do(ctx, "GET", "/v1/repos", nil, &out)
}

// LogLines streams a box's log: the last tail lines, then every new line
// until ctx is done. The channel is closed when the stream ends.
func (c *Client) LogLines(ctx context.Context, repoID, name string, tail int) (<-chan string, error) {
	path := fmt.Sprintf("%s/logs?tail=%d&follow=true", boxPath(repoID, name), tail)
	resp, err := c.open(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	ch := make(chan string, 256)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		_ = readSSE(resp.Body, func(_ string, data []byte) error {
			var line string
			if json.Unmarshal(data, &line) != nil {
				return nil
			}
			select {
			case ch <- line:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	return ch, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/client/`
Expected: PASS (2 tests).

- [ ] **Step 5: Checkpoint**

Run `go vet ./internal/client/ && go test -race ./internal/client/`; record "Task 1 complete". Do not commit.

---

### Task 2: `internal/tui` core — model, messages, reducer, overlays, boxes screen

**Files:**
- Create: `internal/tui/api.go`, `msgs.go`, `reduce.go`, `keys.go`, `styles.go`, `overlay.go`, `model.go`, `view_boxes.go`
- Create stubs (replaced in Task 3): `internal/tui/view_box.go`, `view_new.go`, `view_doctor.go`
- Test: `internal/tui/tui_test.go`
- Modify: `go.mod`, `go.sum` (the pinned `go get` only)

**Interfaces:**
- Consumes: `client.Repos`, `client.LogLines` (Task 1); existing `client` methods and `client.Event{Type, Box, Data map[string]any}`; `api.BoxView`, `api.RepoView`, `api.Created`, `api.MergeResult`; `box.NewOptions`, `box.MergeOptions`, `box.Finding`; `errs.From`, `errs.UnmergedWork`; `events.BoxCreated/BoxState/BoxStep/BoxRemoved`.
- Produces (consumed by Tasks 3–4): `type API interface{…}`, `type Options struct{Repo string; OpenBrowser func(string) error; Shell string; ToastTTL, ReconnectBase time.Duration; NoBlink bool}`, `func New(ctx, api API, opts Options) Model`, `Model.Init/Update/View`; package-private: message types in `msgs.go`, `Model` fields, `m.say`, `m.action`, `m.openBox`, `m.leaveBox`, `m.stopLogs`, `m.openDoctor`, `m.startNew`, `m.setTab`, `boxTab` consts, `newForm`, `doctorState`, `describe`, `formatPorts`, `splitID`, `dialog`, `newPrompt(title, placeholder, toggle, noBlink, submit)`, `confirm`, `toggle`, `tell`, `toastMsg`, test helpers `newFake`, `newModel`, `update`, `press`, `run`, `keyMsg`.

- [ ] **Step 1: Add the dependencies (pinned)**

```bash
go get github.com/charmbracelet/bubbletea@v1.3.10 github.com/charmbracelet/bubbles@v1.0.0 github.com/charmbracelet/lipgloss@v1.1.0
grep '^go ' go.mod        # expect: go 1.24.2  (raised from 1.24.0 by the charm deps; accepted)
go build ./...            # still green
```

Do NOT run `go mod tidy`.

- [ ] **Step 2: Write the failing tests**

Create `internal/tui/tui_test.go`:

```go
package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Ayush2966/Agent-Box/internal/api"
	"github.com/Ayush2966/Agent-Box/internal/box"
	"github.com/Ayush2966/Agent-Box/internal/client"
	"github.com/Ayush2966/Agent-Box/internal/errs"
)

// fake is an in-memory API: one repo with one running box unless a test changes it.
type fake struct {
	mu       sync.Mutex
	repos    []api.RepoView
	boxes    map[string]api.BoxView
	calls    []string
	fail     map[string]error
	findings []box.Finding
	opened   []string
}

func newFake() *fake {
	return &fake{
		repos: []api.RepoView{{ID: "app-1a2b3c", Path: "/tmp/app"}},
		boxes: map[string]api.BoxView{"app-1a2b3c/a": {
			ID: "app-1a2b3c/a", Repo: "app-1a2b3c", RepoPath: "/tmp/app", Name: "a", Dir: "/tmp/boxes/a", Branch: "agentbox/a",
			State: "running", Ports: map[string]int{"APP_PORT": 20000}, URL: "http://a.localhost:7777/", Base: "main", Ahead: 2, Dirty: 1,
			Env: map[string]string{"APP_PORT": "20000", "AGENTBOX_NAME": "a"},
		}},
		fail: map[string]error{},
	}
}

func (f *fake) rec(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }

func (f *fake) called(s string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == s {
			return true
		}
	}
	return false
}

func (f *fake) Repos(context.Context) ([]api.RepoView, error) { f.rec("repos"); return f.repos, f.fail["repos"] }

func (f *fake) ListBoxes(_ context.Context, repo string) ([]api.BoxView, error) {
	f.rec("list")
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []api.BoxView
	for _, b := range f.boxes {
		if repo == "" || b.Repo == repo {
			out = append(out, b)
		}
	}
	return out, f.fail["list"]
}

func (f *fake) GetBox(_ context.Context, repo, name string) (api.BoxView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.boxes[repo+"/"+name]
	if !ok {
		return api.BoxView{}, errs.New(errs.NotFound, "no box called %q", name)
	}
	return b, nil
}

func (f *fake) CreateBox(_ context.Context, opts box.NewOptions) (api.Created, error) {
	f.rec("create " + opts.Name)
	if err := f.fail["create"]; err != nil {
		return api.Created{}, err
	}
	return api.Created{ID: "app-1a2b3c/" + opts.Name, Repo: "app-1a2b3c", Name: opts.Name}, nil
}

func (f *fake) StartBox(_ context.Context, _, name string) error { f.rec("start " + name); return f.fail["start"] }
func (f *fake) StopBox(_ context.Context, _, name string) error  { f.rec("stop " + name); return f.fail["stop"] }

func (f *fake) RemoveBox(_ context.Context, repo, name string, force, keep bool) error {
	f.rec(fmt.Sprintf("remove %s force=%v keep=%v", name, force, keep))
	if !force {
		if err := f.fail["remove"]; err != nil {
			return err
		}
	}
	f.mu.Lock()
	delete(f.boxes, repo+"/"+name)
	f.mu.Unlock()
	return nil
}

func (f *fake) Merge(_ context.Context, _, name string, opts box.MergeOptions) (api.MergeResult, error) {
	f.rec(fmt.Sprintf("merge %s into=%q squash=%v", name, opts.Into, opts.Squash))
	return api.MergeResult{Into: "main"}, f.fail["merge"]
}

func (f *fake) RefreshDB(_ context.Context, repo string, terminate bool) error {
	f.rec(fmt.Sprintf("refresh %s terminate=%v", repo, terminate))
	return f.fail["refresh"]
}

func (f *fake) Doctor(_ context.Context, repo string, fix bool) ([]box.Finding, error) {
	f.rec(fmt.Sprintf("doctor %s fix=%v", repo, fix))
	if fix {
		for i := range f.findings {
			f.findings[i].Fixed = true
		}
	}
	return f.findings, f.fail["doctor"]
}

func (f *fake) Diff(_ context.Context, _, name, against string) (string, error) {
	f.rec("diff " + name + " " + against)
	return "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n", f.fail["diff"]
}

func (f *fake) Events(context.Context) (<-chan client.Event, error) {
	return make(chan client.Event), f.fail["events"]
}

func (f *fake) LogLines(context.Context, string, string, int) (<-chan string, error) {
	return make(chan string), f.fail["logs"]
}

// --- helpers ---

func newModelWith(t *testing.T, f *fake, width, height int) Model {
	t.Helper()
	m := New(context.Background(), f, Options{
		ToastTTL: -1, ReconnectBase: time.Millisecond, Shell: "sh", NoBlink: true,
		OpenBrowser: func(u string) error { f.rec("open " + u); return nil },
	})
	m = update(t, m, tea.WindowSizeMsg{Width: width, Height: height})
	m = update(t, m, reposMsg{repos: f.repos})
	bs, _ := f.ListBoxes(context.Background(), "")
	m = update(t, m, boxesMsg{boxes: bs})
	return m
}

func newModel(t *testing.T, f *fake) Model { return newModelWith(t, f, 120, 30) }

func update(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	mm, _ := m.Update(msg)
	return mm.(Model)
}

// run executes cmd and feeds every message it yields back into the model,
// recursively (batches included), up to a small depth. Never hand it a
// command that blocks on a channel (waitEvent, waitLine).
func run(t *testing.T, m Model, cmd tea.Cmd, depth int) Model {
	t.Helper()
	if cmd == nil || depth > 6 {
		return m
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = run(t, m, c, depth+1)
		}
		return m
	}
	if msg == nil {
		return m
	}
	mm, next := m.Update(msg)
	return run(t, mm.(Model), next, depth+1)
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func press(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		mm, cmd := m.Update(keyMsg(k))
		m = run(t, mm.(Model), cmd, 0)
	}
	return m
}

func typeText(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m = press(t, m, string(r))
	}
	return m
}

func mustContain(t *testing.T, view string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(view, w) {
			t.Fatalf("view lacks %q:\n%s", w, view)
		}
	}
}

func mustNotContain(t *testing.T, view string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if strings.Contains(view, w) {
			t.Fatalf("view should not contain %q:\n%s", w, view)
		}
	}
}

// --- Task 2 tests ---

func TestBoxesTableShowsRowsAndHidesColumnsByWidth(t *testing.T) {
	m := newModel(t, newFake())
	v := m.View()
	mustContain(t, v, "agentbox", "connecting", "a.localhost:7777", "running", "NAME", "STATE", "URL", "AHEAD", "DIRTY", "1 boxes")
	narrow := newModelWith(t, newFake(), 80, 24)
	v = narrow.View()
	mustContain(t, v, "NAME", "running")
	mustNotContain(t, v, "URL", "BEHIND", "PORTS")
}

func TestEmptyListExplains(t *testing.T) {
	f := newFake()
	f.boxes = map[string]api.BoxView{}
	m := newModel(t, f)
	mustContain(t, m.View(), "agentbox new")
}

func TestStartStopToggles(t *testing.T) {
	f := newFake()
	m := newModel(t, f)
	m = press(t, m, "s")
	if !f.called("stop a") {
		t.Fatalf("calls %v", f.calls)
	}
	mustContain(t, m.View(), "stopped a")
	b := f.boxes["app-1a2b3c/a"]
	b.State = "stopped"
	f.boxes["app-1a2b3c/a"] = b
	m = update(t, m, boxesMsg{boxes: []api.BoxView{b}})
	m = press(t, m, "s")
	if !f.called("start a") {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestActionErrorsBecomeToasts(t *testing.T) {
	f := newFake()
	f.fail["stop"] = errs.New(errs.Precondition, "daemon says no").WithStep("stop").WithHint("try later")
	m := newModel(t, f)
	m = press(t, m, "s")
	mustContain(t, m.View(), "stop failed: daemon says no", "try later")
}

func TestActionOnVanishedBoxToasts(t *testing.T) {
	f := newFake()
	m := newModel(t, f)
	f.fail["stop"] = errs.New(errs.NotFound, "no box called \"a\"")
	delete(f.boxes, "app-1a2b3c/a") // removed elsewhere; our table still shows it
	m = press(t, m, "s")
	mustContain(t, m.View(), "no box called")
	// the reload after the action dropped the stale row; no panic, no selection
	if _, ok := m.selected(); ok {
		t.Fatal("stale selection survived the reload")
	}
	m = press(t, m, "s", "x", "enter", "e", "o") // nothing selected: every action is a no-op
}

func TestRemoveAsksThenRemoves(t *testing.T) {
	f := newFake()
	m := newModel(t, f)
	m = press(t, m, "x")
	mustContain(t, m.View(), "Remove a?", "keep the agentbox/a branch")
	m = press(t, m, "n")
	if m.overlay != nil || len(f.calls) > 2 {
		t.Fatalf("cancel: overlay %v calls %v", m.overlay, f.calls)
	}
	m = press(t, m, "x", "K", "y")
	if !f.called("remove a force=false keep=true") {
		t.Fatalf("calls %v", f.calls)
	}
	mustContain(t, m.View(), "removed a")
	mustNotContain(t, m.View(), "a.localhost")
}

func TestRemoveUnmergedWorkAsksAgain(t *testing.T) {
	f := newFake()
	f.fail["remove"] = errs.New(errs.UnmergedWork, "box a has 2 unmerged commits")
	m := newModel(t, f)
	m = press(t, m, "x", "y")
	mustContain(t, m.View(), "Discard unmerged work?", "2 unmerged commits")
	m = press(t, m, "y")
	if !f.called("remove a force=true keep=false") {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestMergePromptWithSquash(t *testing.T) {
	f := newFake()
	m := newModel(t, f)
	m = press(t, m, "m")
	mustContain(t, m.View(), "Merge agentbox/a into")
	m = typeText(t, m, "main")
	m = press(t, m, "ctrl+s", "enter")
	if !f.called(`merge a into="main" squash=true`) {
		t.Fatalf("calls %v", f.calls)
	}
	mustContain(t, m.View(), "merged agentbox/a into main")
}

func TestRefreshDBConfirmWithTerminate(t *testing.T) {
	f := newFake()
	m := newModel(t, f)
	m = press(t, m, "R")
	mustContain(t, m.View(), "Refresh the database template")
	m = press(t, m, "T", "y")
	if !f.called("refresh app-1a2b3c terminate=true") {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestRepoFilterCycles(t *testing.T) {
	f := newFake()
	f.repos = append(f.repos, api.RepoView{ID: "web-9z9z9z", Path: "/tmp/web"})
	f.boxes["web-9z9z9z/z"] = api.BoxView{ID: "web-9z9z9z/z", Repo: "web-9z9z9z", RepoPath: "/tmp/web", Name: "z", State: "ready"}
	m := newModel(t, f)
	mustContain(t, m.View(), "2 boxes", "REPO", "app", "web")
	m = press(t, m, "r")
	mustContain(t, m.View(), "repo: app", "1 boxes")
	mustNotContain(t, m.View(), "z ")
	m = press(t, m, "r")
	mustContain(t, m.View(), "repo: web")
	m = press(t, m, "r")
	mustContain(t, m.View(), "all repos", "2 boxes")
}

func TestQueryFilter(t *testing.T) {
	f := newFake()
	f.boxes["app-1a2b3c/feature-b"] = api.BoxView{ID: "app-1a2b3c/feature-b", Repo: "app-1a2b3c", RepoPath: "/tmp/app", Name: "feature-b", State: "ready"}
	m := newModel(t, f)
	m = press(t, m, "/")
	m = typeText(t, m, "feat")
	mustContain(t, m.View(), "/feat", "feature-b", "1 boxes")
	m = press(t, m, "q") // typing into the query must not quit
	mustContain(t, m.View(), "/featq", "0 boxes")
	m = press(t, m, "esc")
	mustContain(t, m.View(), "2 boxes")
}

func TestEventsUpdateRows(t *testing.T) {
	f := newFake()
	m := newModel(t, f)
	m = update(t, m, eventMsg{client.Event{Type: "box.state", Box: "app-1a2b3c/a", Data: map[string]any{"state": "stopped"}}})
	mustContain(t, m.View(), "stopped")
	m = update(t, m, eventMsg{client.Event{Type: "box.step", Box: "app-1a2b3c/n", Data: map[string]any{"step": "setup"}}})
	if m.steps["app-1a2b3c/n"] != "setup" {
		t.Fatalf("steps %v", m.steps)
	}
	m = update(t, m, eventMsg{client.Event{Type: "box.removed", Box: "app-1a2b3c/a"}})
	mustNotContain(t, m.View(), "a.localhost")
}

func TestReconnectBackoff(t *testing.T) {
	f := newFake()
	m := New(context.Background(), f, Options{ReconnectBase: time.Second, ToastTTL: -1, NoBlink: true})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 20})
	want := []time.Duration{1, 2, 4, 8, 16, 16, 16}
	for i, w := range want {
		m = update(t, m, eventsDownMsg{})
		if m.backoff != w*time.Second {
			t.Fatalf("after %d drops: backoff %v, want %v", i+1, m.backoff, w*time.Second)
		}
	}
	mustContain(t, m.View(), "reconnecting")
	m = update(t, m, subscribedMsg{ch: make(chan client.Event)})
	if m.backoff != 0 || m.conn != connLive {
		t.Fatalf("after resubscribe: backoff %v conn %v", m.backoff, m.conn)
	}
	mustContain(t, m.View(), "live")
}

func TestToastExpiresAndHelpToggles(t *testing.T) {
	f := newFake()
	m := newModel(t, f)
	m = press(t, m, "s")
	mustContain(t, m.View(), "stopped a")
	m = update(t, m, toastGoneMsg{seq: m.toastSeq})
	mustNotContain(t, m.View(), "stopped a")
	m = press(t, m, "?")
	mustContain(t, m.View(), "start/stop", "repo filter", "shell")
	m = press(t, m, "x") // any key closes help without acting
	mustNotContain(t, m.View(), "Remove a?")
}

func TestOpenURLAndShell(t *testing.T) {
	f := newFake()
	m := newModel(t, f)
	m = press(t, m, "o")
	if !f.called("open http://a.localhost:7777/") {
		t.Fatalf("calls %v", f.calls)
	}
	mm, cmd := m.Update(keyMsg("e"))
	if cmd == nil {
		t.Fatal("shell should return an exec command")
	}
	_ = mm
}

func TestTooSmallTerminal(t *testing.T) {
	m := newModelWith(t, newFake(), 50, 8)
	mustContain(t, m.View(), "too small")
}

func TestQuitKeys(t *testing.T) {
	m := newModel(t, newFake())
	for _, k := range []string{"q", "ctrl+c"} {
		_, cmd := m.Update(keyMsg(k))
		if cmd == nil {
			t.Fatalf("%s should quit", k)
		}
		if msg := cmd(); msg != tea.Quit() {
			t.Fatalf("%s: got %T, want tea.QuitMsg", k, msg)
		}
	}
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/tui/`
Expected: FAIL — `New`, `Model`, message types undefined.

- [ ] **Step 4: `api.go`, `msgs.go`, `reduce.go`**

Create `internal/tui/api.go`:

```go
// Package tui is the terminal dashboard: a bubbletea program over the daemon API.
package tui

import (
	"context"

	"github.com/Ayush2966/Agent-Box/internal/api"
	"github.com/Ayush2966/Agent-Box/internal/box"
	"github.com/Ayush2966/Agent-Box/internal/client"
)

// API is the slice of the daemon client the TUI uses. *client.Client satisfies it;
// tests use an in-memory fake.
type API interface {
	Repos(ctx context.Context) ([]api.RepoView, error)
	ListBoxes(ctx context.Context, repoID string) ([]api.BoxView, error)
	GetBox(ctx context.Context, repoID, name string) (api.BoxView, error)
	CreateBox(ctx context.Context, opts box.NewOptions) (api.Created, error)
	StartBox(ctx context.Context, repoID, name string) error
	StopBox(ctx context.Context, repoID, name string) error
	RemoveBox(ctx context.Context, repoID, name string, force, keepBranch bool) error
	Merge(ctx context.Context, repoID, name string, opts box.MergeOptions) (api.MergeResult, error)
	RefreshDB(ctx context.Context, repoID string, terminate bool) error
	Doctor(ctx context.Context, repoID string, fix bool) ([]box.Finding, error)
	Diff(ctx context.Context, repoID, name, against string) (string, error)
	Events(ctx context.Context) (<-chan client.Event, error)
	LogLines(ctx context.Context, repoID, name string, tail int) (<-chan string, error)
}

var _ API = (*client.Client)(nil)
```

Create `internal/tui/msgs.go`:

```go
package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Ayush2966/Agent-Box/internal/api"
	"github.com/Ayush2966/Agent-Box/internal/box"
	"github.com/Ayush2966/Agent-Box/internal/client"
	"github.com/Ayush2966/Agent-Box/internal/errs"
)

type (
	reposMsg      struct{ repos []api.RepoView; err error }
	boxesMsg      struct{ boxes []api.BoxView; err error }
	boxMsg        struct{ id string; box api.BoxView; err error }
	subscribedMsg struct{ ch <-chan client.Event }
	eventMsg      struct{ ev client.Event }
	eventsDownMsg struct{ err error } // subscribe failed or the stream closed
	reconnectMsg  struct{}
	doneMsg       struct{ what string; err error } // an action finished; what is the success toast
	unmergedMsg   struct{ box api.BoxView; keep bool; message string }
	createdMsg    struct{ id string; err error }
	findingsMsg   struct{ repo string; findings []box.Finding; err error }
	diffMsg       struct{ id, text string; err error }
	logsOpenMsg   struct{ id string; ch <-chan string; stop func() }
	logLineMsg    struct{ id, line string }
	logsDoneMsg   struct{ id string; err error }
	toastGoneMsg  struct{ seq int }
	toastMsg      struct{ text string; err bool } // a toast raised from a value-receiver helper
	shellDoneMsg  struct{ err error }
)

// tell is how value-receiver helpers raise a toast: the message reaches
// Update, which owns the model that must change.
func tell(isErr bool, text string) tea.Cmd {
	return func() tea.Msg { return toastMsg{text, isErr} }
}

func (m Model) loadAll() tea.Cmd { return tea.Batch(m.loadRepos(), m.loadBoxes()) }

func (m Model) loadRepos() tea.Cmd {
	a, ctx := m.api, m.ctx
	return func() tea.Msg { rs, err := a.Repos(ctx); return reposMsg{rs, err} }
}

func (m Model) loadBoxes() tea.Cmd {
	a, ctx := m.api, m.ctx
	return func() tea.Msg { bs, err := a.ListBoxes(ctx, ""); return boxesMsg{bs, err} }
}

func (m Model) fetchBox(id string) tea.Cmd {
	a, ctx := m.api, m.ctx
	repo, name := splitID(id)
	return func() tea.Msg { b, err := a.GetBox(ctx, repo, name); return boxMsg{id, b, err} }
}

func (m Model) subscribe() tea.Cmd {
	a, ctx := m.api, m.ctx
	return func() tea.Msg {
		ch, err := a.Events(ctx)
		if err != nil {
			return eventsDownMsg{err}
		}
		return subscribedMsg{ch}
	}
}

// waitEvent delivers the next event, or eventsDownMsg when the stream closes.
func waitEvent(ch <-chan client.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return eventsDownMsg{}
		}
		return eventMsg{ev}
	}
}

func reconnectAfter(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return reconnectMsg{} })
}

// action runs fn and reports its outcome as a toast.
func (m Model) action(what string, fn func(ctx context.Context) error) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg { return doneMsg{what, fn(ctx)} }
}

func (m Model) remove(b api.BoxView, force, keep bool) tea.Cmd {
	a, ctx := m.api, m.ctx
	return func() tea.Msg {
		err := a.RemoveBox(ctx, b.Repo, b.Name, force, keep)
		if e := errs.From(err); e != nil && !force && e.Code == errs.UnmergedWork {
			return unmergedMsg{b, keep, e.Message}
		}
		return doneMsg{"removed " + b.Name, err}
	}
}

func (m Model) create(opts box.NewOptions) tea.Cmd {
	a, ctx := m.api, m.ctx
	return func() tea.Msg { c, err := a.CreateBox(ctx, opts); return createdMsg{c.ID, err} }
}

func (m Model) runDoctor(repo string, fix bool) tea.Cmd {
	a, ctx := m.api, m.ctx
	return func() tea.Msg { fs, err := a.Doctor(ctx, repo, fix); return findingsMsg{repo, fs, err} }
}

func (m Model) loadDiff(id, against string) tea.Cmd {
	a, ctx := m.api, m.ctx
	repo, name := splitID(id)
	return func() tea.Msg { d, err := a.Diff(ctx, repo, name, against); return diffMsg{id, d, err} }
}

func (m Model) openLogs(id string) tea.Cmd {
	a := m.api
	ctx, cancel := context.WithCancel(m.ctx)
	repo, name := splitID(id)
	return func() tea.Msg {
		ch, err := a.LogLines(ctx, repo, name, 500)
		if err != nil {
			cancel()
			return logsDoneMsg{id, err}
		}
		return logsOpenMsg{id, ch, cancel}
	}
}

func waitLine(id string, ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return logsDoneMsg{id, nil}
		}
		return logLineMsg{id, line}
	}
}

func toastTimer(seq int, ttl time.Duration) tea.Cmd {
	return tea.Tick(ttl, func(time.Time) tea.Msg { return toastGoneMsg{seq} })
}
```

Create `internal/tui/reduce.go`:

```go
package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Ayush2966/Agent-Box/internal/client"
	"github.com/Ayush2966/Agent-Box/internal/events"
)

func terminalState(s string) bool {
	return s == "ready" || s == "running" || s == "stopped" || s == "failed"
}

// applyEvent mirrors the web dashboard's reducer. It returns the follow-up
// command: a fetch of the box for created/state events, a toast when the box
// on screen was removed.
func (m *Model) applyEvent(ev client.Event) tea.Cmd {
	str := func(k string) (string, bool) {
		v, ok := ev.Data[k].(string)
		return v, ok
	}
	switch ev.Type {
	case events.BoxStep:
		if s, ok := ev.Data["step"]; ok {
			m.steps[ev.Box] = fmt.Sprint(s)
		}
		return nil
	case events.BoxState:
		state, _ := str("state")
		if terminalState(state) {
			delete(m.steps, ev.Box)
		}
		if e, ok := str("error"); ok {
			m.failures[ev.Box] = e
		}
		if b, ok := m.boxes[ev.Box]; ok {
			b.State = state
			if le, ok := str("last_error"); ok {
				b.LastError = le
			}
			m.boxes[ev.Box] = b
			m.refreshTable()
		}
		return m.fetchBox(ev.Box)
	case events.BoxCreated:
		return m.fetchBox(ev.Box)
	case events.BoxRemoved:
		delete(m.boxes, ev.Box)
		delete(m.steps, ev.Box)
		if e, ok := str("error"); ok {
			m.failures[ev.Box] = e
		}
		m.refreshTable()
		if m.mode == modeBox && m.cur == ev.Box {
			_, name := splitID(ev.Box)
			m.leaveBox()
			return m.say(false, "box "+name+" was removed")
		}
		return nil
	}
	return nil
}
```

- [ ] **Step 5: `keys.go`, `styles.go`, `overlay.go`**

Create `internal/tui/keys.go`:

```go
package tui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Up, Down, Open, Back, New, StartStop, Remove, Merge, Doctor, RefreshDB, Repo, OpenURL, Shell,
	Filter, Reload, Help, Quit, Tab, Tab1, Tab2, Tab3, Follow, Clear, Against, Fix, Recheck key.Binding
}

func newKeyMap() keyMap {
	b := func(help, desc string, keys ...string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(help, desc))
	}
	return keyMap{
		Up:        b("↑/k", "up", "up", "k"),
		Down:      b("↓/j", "down", "down", "j"),
		Open:      b("enter", "open", "enter", "l"),
		Back:      b("esc", "back", "esc", "h"),
		New:       b("n", "new box", "n"),
		StartStop: b("s", "start/stop", "s"),
		Remove:    b("x", "remove", "x"),
		Merge:     b("m", "merge", "m"),
		Doctor:    b("d", "doctor", "d"),
		RefreshDB: b("R", "refresh db", "R"),
		Repo:      b("r", "repo filter", "r"),
		OpenURL:   b("o", "open url", "o"),
		Shell:     b("e", "shell", "e"),
		Filter:    b("/", "filter", "/"),
		Reload:    b("ctrl+r", "reload", "ctrl+r", "f5"),
		Help:      b("?", "help", "?"),
		Quit:      b("q", "quit", "q", "ctrl+c"),
		Tab:       b("tab", "next tab", "tab"),
		Tab1:      b("1", "overview", "1"),
		Tab2:      b("2", "logs", "2"),
		Tab3:      b("3", "diff", "3"),
		Follow:    b("G", "follow", "G"),
		Clear:     b("c", "clear", "c"),
		Against:   b("a", "against…", "a"),
		Fix:       b("f", "fix", "f"),
		Recheck:   b("r", "re-check", "r"),
	}
}

func (m Model) shortHelp() []key.Binding {
	k := m.keys
	switch m.mode {
	case modeBox:
		return []key.Binding{k.Back, k.Tab, k.StartStop, k.Remove, k.Merge, k.Shell, k.Help}
	case modeNew:
		return []key.Binding{key.NewBinding(key.WithHelp("enter", "create")), key.NewBinding(key.WithHelp("tab", "next field")), key.NewBinding(key.WithHelp("esc", "cancel"))}
	case modeDoctor:
		return []key.Binding{k.Fix, k.Recheck, k.Back}
	}
	return []key.Binding{k.Open, k.New, k.StartStop, k.Remove, k.Merge, k.Repo, k.Shell, k.Help, k.Quit}
}

func (m Model) fullHelp() [][]key.Binding {
	k := m.keys
	return [][]key.Binding{
		{k.Up, k.Down, k.Open, k.Back, k.Tab, k.Filter, k.Repo},
		{k.New, k.StartStop, k.Remove, k.Merge, k.Doctor, k.RefreshDB},
		{k.OpenURL, k.Shell, k.Follow, k.Clear, k.Against, k.Reload, k.Quit},
	}
}
```

Create `internal/tui/styles.go`:

```go
package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Ayush2966/Agent-Box/internal/errs"
)

var (
	stBrand  = lipgloss.NewStyle().Bold(true)
	stHeader = lipgloss.NewStyle().Bold(true)
	stMuted  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	stErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	stOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	stAccent = lipgloss.NewStyle().Foreground(lipgloss.Color("75"))
	stAmber  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	stTab    = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Padding(0, 1)
	stTabOn  = lipgloss.NewStyle().Bold(true).Underline(true).Padding(0, 1)
	stDialog = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
)

// pill colours a box state; the text itself always carries the state.
func pill(state string) string {
	switch state {
	case "running":
		return stOK.Render(state)
	case "failed":
		return stErr.Render(state)
	case "creating":
		return stAccent.Render(state)
	case "removing":
		return stAmber.Render(state)
	}
	return stMuted.Render(state)
}

func connPill(c connState) string {
	switch c {
	case connLive:
		return stOK.Render("live")
	case connReconnecting:
		return stAmber.Render("reconnecting")
	}
	return stMuted.Render("connecting")
}

// describe formats any error the way the CLI does: "step failed: message — hint".
func describe(err error) string {
	e := errs.From(err)
	if e == nil {
		return ""
	}
	s := e.Message
	if e.Step != "" {
		s = e.Step + " failed: " + s
	}
	if e.Hint != "" {
		s += " — " + e.Hint
	}
	return s
}

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

func splitID(id string) (repo, name string) {
	if i := strings.Index(id, "/"); i >= 0 {
		return id[:i], id[i+1:]
	}
	return "", id
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func repoBase(path string) string { return filepath.Base(path) }

func envList(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}
```

Create `internal/tui/overlay.go`:

```go
package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// overlay is a modal that swallows every key until it closes itself.
type overlay interface {
	handle(m *Model, k tea.KeyMsg) tea.Cmd
	view(m *Model) string
}

// toggle is one optional boolean on a dialog, flipped by a single key.
type toggle struct {
	key, label string
	on         bool
}

func (t *toggle) line() string {
	box := "[ ]"
	if t.on {
		box = "[x]"
	}
	return stMuted.Render(t.key+" ") + box + " " + t.label
}

type confirm struct {
	title, body string
	toggle      *toggle
	yes         func(m *Model, on bool) tea.Cmd
}

func (c *confirm) handle(m *Model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "y", "enter":
		m.overlay = nil
		return c.yes(m, c.toggle != nil && c.toggle.on)
	case "n", "esc":
		m.overlay = nil
	default:
		if c.toggle != nil && k.String() == c.toggle.key {
			c.toggle.on = !c.toggle.on
		}
	}
	return nil
}

func (c *confirm) view(m *Model) string {
	lines := []string{}
	if c.toggle != nil {
		lines = append(lines, c.toggle.line())
	}
	lines = append(lines, stMuted.Render("y/enter yes · n/esc no"))
	return dialog(m.width, c.title, c.body, lines...)
}

type prompt struct {
	title  string
	input  textinput.Model
	toggle *toggle
	submit func(m *Model, value string, on bool) tea.Cmd
}

func newPrompt(title, placeholder string, t *toggle, noBlink bool, submit func(m *Model, value string, on bool) tea.Cmd) *prompt {
	in := textinput.New()
	in.Placeholder = placeholder
	in.CharLimit = 200
	in.Width = 40
	if noBlink {
		in.Cursor.SetMode(cursor.CursorStatic) // Focus() and typing then return no blink commands
	}
	in.Focus()
	return &prompt{title: title, input: in, toggle: t, submit: submit}
}

func (p *prompt) handle(m *Model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "enter":
		m.overlay = nil
		return p.submit(m, strings.TrimSpace(p.input.Value()), p.toggle != nil && p.toggle.on)
	case "esc":
		m.overlay = nil
		return nil
	}
	if p.toggle != nil && k.String() == p.toggle.key {
		p.toggle.on = !p.toggle.on
		return nil
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(k)
	return cmd
}

func (p *prompt) view(m *Model) string {
	lines := []string{p.input.View()}
	if p.toggle != nil {
		lines = append(lines, p.toggle.line())
	}
	lines = append(lines, stMuted.Render("enter ok · esc cancel"))
	return dialog(m.width, p.title, "", lines...)
}

// dialog renders a bordered box; width is the terminal width.
func dialog(width int, title, body string, lines ...string) string {
	w := min(width-4, 72)
	parts := []string{stHeader.Render(title)}
	if body != "" {
		parts = append(parts, lipgloss.NewStyle().Width(w - 2).Render(body))
	}
	parts = append(parts, lines...)
	return stDialog.Width(w).Render(strings.Join(parts, "\n"))
}
```

- [ ] **Step 6: `model.go`**

Create `internal/tui/model.go`:

```go
package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Ayush2966/Agent-Box/internal/api"
	"github.com/Ayush2966/Agent-Box/internal/client"
)

type mode int

const (
	modeBoxes mode = iota
	modeBox
	modeNew
	modeDoctor
)

type connState int

const (
	connConnecting connState = iota
	connLive
	connReconnecting
)

// Options tune the program; zero values are the defaults.
type Options struct {
	Repo          string                 // repo ID preselected as the filter; "" = all repos
	OpenBrowser   func(url string) error // nil = the platform opener
	Shell         string                 // "" = $SHELL, then sh
	ToastTTL      time.Duration          // 0 = 5s; < 0 = toasts never expire (tests)
	ReconnectBase time.Duration          // 0 = 1s; doubles up to 16×
	NoBlink       bool                   // static cursors in text inputs (tests; dumb terminals)
}

type toast struct {
	text string
	err  bool
}

type Model struct {
	api  API
	ctx  context.Context
	opts Options
	keys keyMap
	help help.Model

	width, height int
	mode          mode
	overlay       overlay
	showHelp      bool

	repos    []api.RepoView
	filter   string // repo ID, "" = all
	query    string
	querying bool
	boxes    map[string]api.BoxView
	steps    map[string]string
	failures map[string]string
	table    table.Model
	rowIDs   []string

	conn    connState
	backoff time.Duration
	events  <-chan client.Event

	// box screen
	cur         string
	tab         boxTab
	logs        []string
	logFollow   bool
	logID       string
	logCh       <-chan string
	logStop     func()
	logView     viewport.Model
	diff        string
	diffLoaded  bool
	diffLoading bool
	diffAgainst string
	diffView    viewport.Model

	form   newForm
	doctor doctorState

	toast    *toast
	toastSeq int
}

func New(ctx context.Context, a API, opts Options) Model {
	if opts.ToastTTL == 0 {
		opts.ToastTTL = 5 * time.Second
	}
	if opts.ReconnectBase == 0 {
		opts.ReconnectBase = time.Second
	}
	if opts.OpenBrowser == nil {
		opts.OpenBrowser = openBrowser
	}
	if opts.Shell == "" {
		opts.Shell = os.Getenv("SHELL")
		if opts.Shell == "" {
			opts.Shell = "sh"
		}
	}
	st := table.DefaultStyles()
	st.Header = st.Header.Bold(true).Foreground(lipgloss.Color("244")).BorderStyle(lipgloss.NormalBorder()).BorderBottom(true).BorderForeground(lipgloss.Color("240"))
	st.Selected = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("238"))
	t := table.New(table.WithFocused(true), table.WithHeight(10), table.WithStyles(st))
	return Model{
		api: a, ctx: ctx, opts: opts, keys: newKeyMap(), help: help.New(),
		filter: opts.Repo,
		boxes: map[string]api.BoxView{}, steps: map[string]string{}, failures: map[string]string{},
		table: t, logFollow: true,
		logView: viewport.New(80, 10), diffView: viewport.New(80, 10),
	}
}

func (m Model) Init() tea.Cmd { return tea.Batch(m.loadAll(), m.subscribe()) }

// bodyHeight is the room between the header and footer lines.
func (m Model) bodyHeight() int { return max(m.height-2, 1) }

func (m *Model) layout() {
	h := m.bodyHeight()
	m.table.SetWidth(m.width)
	m.table.SetHeight(max(h-1, 1))
	m.logView.Width, m.logView.Height = m.width, max(h-2, 1)
	m.diffView.Width, m.diffView.Height = m.width, max(h-2, 1)
	m.refreshTable()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)

	case reposMsg:
		if msg.err != nil {
			return m, m.say(true, describe(msg.err))
		}
		m.repos = msg.repos
		if m.filter != "" && !m.hasRepo(m.filter) {
			m.filter = ""
		}
		m.refreshTable()
		return m, nil
	case boxesMsg:
		if msg.err != nil {
			return m, m.say(true, describe(msg.err))
		}
		m.boxes = map[string]api.BoxView{}
		for _, b := range msg.boxes {
			m.boxes[b.ID] = b
		}
		m.failures = map[string]string{}
		m.refreshTable()
		if m.mode == modeBox {
			if _, ok := m.boxes[m.cur]; !ok {
				m.leaveBox()
			}
		}
		return m, nil
	case boxMsg:
		if msg.err != nil {
			return m, nil // removed meanwhile
		}
		m.boxes[msg.id] = msg.box
		m.refreshTable()
		return m, m.afterBoxUpdate(msg.id)

	case subscribedMsg:
		m.conn, m.backoff, m.events = connLive, 0, msg.ch
		return m, tea.Batch(m.loadAll(), waitEvent(msg.ch))
	case eventMsg:
		cmd := m.applyEvent(msg.ev)
		return m, tea.Batch(cmd, waitEvent(m.events))
	case eventsDownMsg:
		m.conn = connReconnecting
		switch {
		case m.backoff == 0:
			m.backoff = m.opts.ReconnectBase
		case m.backoff < 16*m.opts.ReconnectBase:
			m.backoff *= 2
		}
		return m, reconnectAfter(m.backoff)
	case reconnectMsg:
		return m, m.subscribe()

	case doneMsg:
		if msg.err != nil {
			return m, tea.Batch(m.say(true, describe(msg.err)), m.loadBoxes())
		}
		return m, tea.Batch(m.say(false, msg.what), m.loadBoxes())
	case unmergedMsg:
		b := msg.box
		m.overlay = &confirm{title: "Discard unmerged work?", body: msg.message,
			toggle: &toggle{key: "K", label: "keep the " + b.Branch + " branch", on: msg.keep},
			yes:    func(m *Model, keep bool) tea.Cmd { return m.remove(b, true, keep) }}
		return m, nil
	case toastGoneMsg:
		if msg.seq == m.toastSeq {
			m.toast = nil
		}
		return m, nil
	case toastMsg:
		return m, m.say(msg.err, msg.text)
	case shellDoneMsg:
		if msg.err != nil {
			return m, m.say(true, "shell: "+msg.err.Error())
		}
		return m, m.loadBoxes()

	case createdMsg:
		return m.onCreated(msg)
	case findingsMsg:
		return m.onFindings(msg)
	case diffMsg:
		return m.onDiff(msg)
	case logsOpenMsg:
		return m.onLogsOpen(msg)
	case logLineMsg:
		return m.onLogLine(msg)
	case logsDoneMsg:
		return m.onLogsDone(msg)
	}
	// Anything else (cursor blinks, …) goes to whichever text input is live.
	return m.forwardToInputs(msg)
}

func (m Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.overlay != nil {
		cmd := m.overlay.handle(&m, k)
		return m, cmd
	}
	if m.querying {
		return m.handleQueryKey(k)
	}
	if m.showHelp {
		m.showHelp = false
		return m, nil
	}
	if m.mode == modeNew { // text fields own every key, including q
		return m.updateNew(k)
	}
	if key.Matches(k, m.keys.Quit) {
		m.stopLogs()
		return m, tea.Quit
	}
	if key.Matches(k, m.keys.Help) {
		m.showHelp = true
		return m, nil
	}
	switch m.mode {
	case modeBox:
		return m.updateBox(k)
	case modeDoctor:
		return m.updateDoctor(k)
	}
	return m.updateBoxes(k)
}

func (m Model) handleQueryKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEscape:
		m.querying, m.query = false, ""
	case tea.KeyEnter:
		m.querying = false
	case tea.KeyBackspace:
		if m.query != "" {
			m.query = m.query[:len(m.query)-1]
		}
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyRunes:
		m.query += string(k.Runes)
	}
	m.refreshTable()
	return m, nil
}

// say shows a toast for ToastTTL. Each toast has a sequence number, so an
// older timer can't clear a newer toast.
func (m *Model) say(isErr bool, text string) tea.Cmd {
	m.toastSeq++
	m.toast = &toast{text: text, err: isErr}
	if m.opts.ToastTTL < 0 { // tests: never expire on their own
		return nil
	}
	return toastTimer(m.toastSeq, m.opts.ToastTTL)
}

func (m Model) hasRepo(id string) bool {
	for _, r := range m.repos {
		if r.ID == id {
			return true
		}
	}
	return false
}

func (m Model) repoPath(id string) string {
	for _, r := range m.repos {
		if r.ID == id {
			return r.Path
		}
	}
	return id
}

// --- view ---

func (m Model) View() string {
	if m.width < 60 || m.height < 10 {
		return "terminal too small: agentbox tui needs at least 60×10"
	}
	h := m.bodyHeight()
	var body string
	switch {
	case m.showHelp:
		body = m.help.FullHelpView(m.fullHelp())
	case m.overlay != nil:
		body = lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, m.overlay.view(&m))
	default:
		switch m.mode {
		case modeBox:
			body = m.viewBox(h)
		case modeNew:
			body = m.viewNew(h)
		case modeDoctor:
			body = m.viewDoctor(h)
		default:
			body = m.viewBoxes(h)
		}
	}
	body = lipgloss.NewStyle().Width(m.width).Height(h).MaxHeight(h).Render(body)
	return lipgloss.JoinVertical(lipgloss.Left, m.header(), body, m.footer())
}

func (m Model) header() string {
	repo := "all repos"
	if m.filter != "" {
		repo = repoBase(m.repoPath(m.filter))
	}
	parts := []string{stBrand.Render("agentbox"), connPill(m.conn), stMuted.Render("repo: ") + repo,
		stMuted.Render(fmt.Sprintf("%d boxes", len(m.visibleBoxes())))}
	if m.querying || m.query != "" {
		q := "/" + m.query
		if m.querying {
			q += "▏"
		}
		parts = append(parts, stAccent.Render(q))
	}
	return lipgloss.NewStyle().Width(m.width).MaxHeight(1).Render(strings.Join(parts, "  "))
}

func (m Model) footer() string {
	var s string
	if m.toast != nil {
		if m.toast.err {
			s = stErr.Render(m.toast.text)
		} else {
			s = stOK.Render(m.toast.text)
		}
	} else {
		s = m.help.ShortHelpView(m.shortHelp())
	}
	return lipgloss.NewStyle().Width(m.width).MaxHeight(1).Render(s)
}

// --- shared actions (boxes and box screens) ---

func (m Model) toggleRun(b api.BoxView) tea.Cmd {
	a := m.api
	switch b.State {
	case "running":
		return m.action("stopped "+b.Name, func(ctx context.Context) error { return a.StopBox(ctx, b.Repo, b.Name) })
	case "ready", "stopped":
		return m.action("started "+b.Name, func(ctx context.Context) error { return a.StartBox(ctx, b.Repo, b.Name) })
	}
	return tell(true, fmt.Sprintf("%s is %s; nothing to start or stop", b.Name, b.State))
}

func (m *Model) askRemove(b api.BoxView) {
	m.overlay = &confirm{title: "Remove " + b.Name + "?",
		body:   "Stops the dev server, drops the database copy, frees the ports and deletes the worktree at " + b.Dir + ".",
		toggle: &toggle{key: "K", label: "keep the " + b.Branch + " branch"},
		yes:    func(m *Model, keep bool) tea.Cmd { return m.remove(b, false, keep) }}
}

func (m *Model) askMerge(b api.BoxView) {
	a := m.api
	m.overlay = newPrompt("Merge "+b.Branch+" into…", "the branch checked out in the main checkout",
		&toggle{key: "ctrl+s", label: "squash into one commit"}, m.opts.NoBlink,
		func(m *Model, into string, squash bool) tea.Cmd {
			ctx := m.ctx
			return func() tea.Msg {
				res, err := a.Merge(ctx, b.Repo, b.Name, box.MergeOptions{Into: into, Squash: squash})
				if err != nil {
					return doneMsg{"", err}
				}
				return doneMsg{"merged " + b.Branch + " into " + res.Into + " — remove the box when you are done", nil}
			}
		})
}

func (m *Model) askRefresh(repo string) {
	a := m.api
	m.overlay = &confirm{title: "Refresh the database template of " + repoBase(m.repoPath(repo)) + "?",
		body:   "Re-snapshots the source database so that new boxes start from its current contents. Existing boxes keep their copies.",
		toggle: &toggle{key: "T", label: "terminate stray connections to the template (Postgres)"},
		yes: func(m *Model, terminate bool) tea.Cmd {
			return m.action("database template refreshed", func(ctx context.Context) error { return a.RefreshDB(ctx, repo, terminate) })
		}}
}

func (m Model) openURL(b api.BoxView) tea.Cmd {
	if b.URL == "" {
		return tell(true, b.Name+" has no ports, so no URL")
	}
	open := m.opts.OpenBrowser
	return m.action("opened "+b.URL, func(context.Context) error { return open(b.URL) })
}

func (m Model) shell(b api.BoxView) tea.Cmd {
	c := exec.Command(m.opts.Shell)
	c.Dir = b.Dir
	c.Env = append(os.Environ(), envList(b.Env)...)
	return tea.ExecProcess(c, func(err error) tea.Msg { return shellDoneMsg{err} })
}

func openBrowser(u string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", u).Start()
	}
	return exec.Command("xdg-open", u).Start()
}
```

(`model.go`'s import block must also list `"github.com/Ayush2966/Agent-Box/internal/box"` for `box.MergeOptions`.)

- [ ] **Step 7: `view_boxes.go` and the three stubs**

Create `internal/tui/view_boxes.go`:

```go
package tui

import (
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Ayush2966/Agent-Box/internal/api"
)

// visibleBoxes applies the repo filter and the name query, sorted by repo then name.
func (m Model) visibleBoxes() []api.BoxView {
	out := make([]api.BoxView, 0, len(m.boxes))
	for _, b := range m.boxes {
		if m.filter != "" && b.Repo != m.filter {
			continue
		}
		if m.query != "" && !strings.Contains(b.Name, m.query) {
			continue
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (m Model) stateCell(b api.BoxView) string {
	s := pill(b.State)
	if step, ok := m.steps[b.ID]; ok && step != "" {
		s += " " + stMuted.Render(step+"…")
	} else if b.State == "failed" && b.LastError != "" {
		s += " " + stErr.Render(truncate(b.LastError, 14))
	}
	return s
}

type column struct {
	title string
	width int
	show  bool
	cell  func(b api.BoxView) string
}

func (m Model) columns() []column {
	w := m.width
	return []column{
		{"NAME", 14, true, func(b api.BoxView) string { return b.Name }},
		{"STATE", 22, true, m.stateCell},
		{"AHEAD", 6, true, func(b api.BoxView) string { return strconv.Itoa(b.Ahead) }},
		{"BEHIND", 7, w >= 90, func(b api.BoxView) string { return strconv.Itoa(b.Behind) }},
		{"DIRTY", 6, true, func(b api.BoxView) string { return strconv.Itoa(b.Dirty) }},
		{"URL", 26, w >= 100, func(b api.BoxView) string {
			return strings.TrimSuffix(strings.TrimPrefix(b.URL, "http://"), "/")
		}},
		{"REPO", 18, w >= 120 && m.filter == "", func(b api.BoxView) string { return repoBase(b.RepoPath) }},
		{"PORTS", 24, w >= 145, func(b api.BoxView) string { return formatPorts(b.Ports) }},
	}
}

// refreshTable rebuilds the rows and keeps the cursor on the same box.
func (m *Model) refreshTable() {
	keep, _ := m.selectedID()
	var cols []table.Column
	var shown []column
	for _, c := range m.columns() {
		if c.show {
			cols = append(cols, table.Column{Title: c.title, Width: c.width})
			shown = append(shown, c)
		}
	}
	boxes := m.visibleBoxes()
	rows := make([]table.Row, 0, len(boxes))
	ids := make([]string, 0, len(boxes))
	for _, b := range boxes {
		row := make(table.Row, 0, len(shown))
		for _, c := range shown {
			row = append(row, c.cell(b))
		}
		rows = append(rows, row)
		ids = append(ids, b.ID)
	}
	m.table.SetColumns(cols)
	m.table.SetRows(rows)
	m.rowIDs = ids
	m.table.GotoTop()
	for i, id := range ids {
		if id == keep {
			m.table.MoveDown(i)
			break
		}
	}
}

func (m Model) selectedID() (string, bool) {
	i := m.table.Cursor()
	if i < 0 || i >= len(m.rowIDs) {
		return "", false
	}
	return m.rowIDs[i], true
}

func (m Model) selected() (api.BoxView, bool) {
	id, ok := m.selectedID()
	if !ok {
		return api.BoxView{}, false
	}
	b, ok := m.boxes[id]
	return b, ok
}

// repoFor picks the repo an action without a box should apply to: the
// filter, else the selected box's repo, else the only repo there is.
func (m Model) repoFor(b api.BoxView, ok bool) string {
	switch {
	case m.filter != "":
		return m.filter
	case ok:
		return b.Repo
	case len(m.repos) == 1:
		return m.repos[0].ID
	}
	return ""
}

func (m *Model) cycleRepo() {
	ids := []string{""}
	for _, r := range m.repos {
		ids = append(ids, r.ID)
	}
	next := 0
	for i, id := range ids {
		if id == m.filter {
			next = (i + 1) % len(ids)
			break
		}
	}
	m.filter = ids[next]
	m.refreshTable()
}

func (m Model) updateBoxes(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	b, ok := m.selected()
	keys := m.keys
	switch {
	case key.Matches(k, keys.Open):
		if ok {
			return m, m.openBox(b.ID)
		}
	case key.Matches(k, keys.New):
		return m.startNew()
	case key.Matches(k, keys.StartStop):
		if ok {
			return m, m.toggleRun(b)
		}
	case key.Matches(k, keys.Remove):
		if ok {
			m.askRemove(b)
		}
	case key.Matches(k, keys.Merge):
		if ok {
			m.askMerge(b)
		}
	case key.Matches(k, keys.Doctor):
		if repo := m.repoFor(b, ok); repo != "" {
			return m, m.openDoctor(repo)
		}
		return m, m.say(true, "pick a repo first: press r to filter, or select a box")
	case key.Matches(k, keys.RefreshDB):
		if repo := m.repoFor(b, ok); repo != "" {
			m.askRefresh(repo)
		} else {
			return m, m.say(true, "pick a repo first: press r to filter, or select a box")
		}
	case key.Matches(k, keys.Repo):
		m.cycleRepo()
	case key.Matches(k, keys.OpenURL):
		if ok {
			return m, m.openURL(b)
		}
	case key.Matches(k, keys.Shell):
		if ok {
			return m, m.shell(b)
		}
	case key.Matches(k, keys.Filter):
		m.querying, m.query = true, ""
		m.refreshTable()
	case key.Matches(k, keys.Reload):
		return m, m.loadAll()
	default:
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(k)
		return m, cmd
	}
	return m, nil
}

func (m Model) viewBoxes(h int) string {
	if len(m.rowIDs) == 0 {
		msg := "No boxes yet."
		if m.query != "" {
			msg = "No box matches /" + m.query + "."
		} else if m.filter != "" {
			msg = "No boxes in " + repoBase(m.repoPath(m.filter)) + " yet."
		}
		return "\n  " + msg + "\n\n  " + stMuted.Render("create one with  agentbox new <name>  inside a repo")
	}
	return m.table.View()
}
```

Create the three stubs (Task 3 replaces every line of them):

`internal/tui/view_box.go`:

```go
package tui

import tea "github.com/charmbracelet/bubbletea"

type boxTab int

const (
	tabOverview boxTab = iota
	tabLogs
	tabDiff
)

func (m *Model) openBox(id string) tea.Cmd {
	m.mode, m.cur, m.tab = modeBox, id, tabOverview
	return nil
}

func (m *Model) leaveBox() { m.stopLogs(); m.mode = modeBoxes }

func (m *Model) stopLogs() {
	if m.logStop != nil {
		m.logStop()
		m.logStop = nil
	}
	m.logID, m.logCh = "", nil
}

func (m Model) afterBoxUpdate(string) tea.Cmd                    { return nil }
func (m Model) updateBox(k tea.KeyMsg) (tea.Model, tea.Cmd)       { m.leaveBox(); return m, nil }
func (m Model) viewBox(int) string                                { return "box screen: Task 3" }
func (m Model) onDiff(diffMsg) (tea.Model, tea.Cmd)               { return m, nil }
func (m Model) onLogsOpen(msg logsOpenMsg) (tea.Model, tea.Cmd)   { msg.stop(); return m, nil }
func (m Model) onLogLine(logLineMsg) (tea.Model, tea.Cmd)         { return m, nil }
func (m Model) onLogsDone(logsDoneMsg) (tea.Model, tea.Cmd)       { return m, nil }
```

`internal/tui/view_new.go`:

```go
package tui

import tea "github.com/charmbracelet/bubbletea"

type newForm struct{}

func (m Model) startNew() (tea.Model, tea.Cmd)                 { return m, tell(true, "new box: Task 3") }
func (m Model) updateNew(k tea.KeyMsg) (tea.Model, tea.Cmd)     { m.mode = modeBoxes; return m, nil }
func (m Model) viewNew(int) string                              { return "new box: Task 3" }
func (m Model) onCreated(createdMsg) (tea.Model, tea.Cmd)       { return m, nil }
func (m Model) forwardToInputs(tea.Msg) (tea.Model, tea.Cmd)    { return m, nil }
```

`internal/tui/view_doctor.go`:

```go
package tui

import tea "github.com/charmbracelet/bubbletea"

type doctorState struct{}

func (m *Model) openDoctor(string) tea.Cmd                      { return tell(true, "doctor: Task 3") }
func (m Model) updateDoctor(tea.KeyMsg) (tea.Model, tea.Cmd)    { m.mode = modeBoxes; return m, nil }
func (m Model) viewDoctor(int) string                           { return "doctor: Task 3" }
func (m Model) onFindings(findingsMsg) (tea.Model, tea.Cmd)     { return m, nil }
```

- [ ] **Step 8: Run the tests**

Run: `gofmt -l internal/tui; go vet ./internal/tui/ && go test -race ./internal/tui/`
Expected: PASS for every Task 2 test (16 tests). Known pitfalls if something fails: `table.Model` truncates cells with ANSI-aware width, so a coloured state cell is fine; `key.Matches` compares `KeyMsg.String()`, so `"ctrl+s"` must be produced by `tea.KeyCtrlS`; `lipgloss.Place` needs the body to be shorter than `h` lines or it is clipped by `MaxHeight`.

- [ ] **Step 9: Checkpoint**

Run `go vet ./... && go test -race ./internal/tui/ ./internal/client/ && go build ./...`; record "Task 2 complete" with the `go.mod` go-directive value. Do not commit.

---

### Task 3: Box, new-box and doctor screens

**Files:**
- Replace entirely: `internal/tui/view_box.go`, `internal/tui/view_new.go`, `internal/tui/view_doctor.go`
- Modify: `internal/tui/tui_test.go` (one fake change + new tests)

**Interfaces:**
- Consumes: everything Task 2 exported inside the package (`Model` fields, `m.say`, `tell`, `m.action`, `m.remove`, `m.create`, `m.runDoctor`, `m.loadDiff`, `m.openLogs`, `waitLine`, `newPrompt`, `confirm`, `pill`, `describe`, `envList`, `formatPorts`, `repoBase`, `splitID`, `truncate`, `m.selected`, `m.repoFor`, `m.toggleRun`, `m.askRemove`, `m.askMerge`, `m.openURL`, `m.shell`).
- Produces: the real `openBox`, `leaveBox`, `stopLogs`, `setTab`, `afterBoxUpdate` (now a pointer-receiver method), `updateBox`, `viewBox`, `onDiff`, `onLogsOpen`, `onLogLine`, `onLogsDone`, `newForm`, `startNew`, `updateNew`, `viewNew`, `onCreated`, `forwardToInputs`, `doctorState`, `openDoctor`, `updateDoctor`, `viewDoctor`, `onFindings`, `maxLogLines`.

- [ ] **Step 1: Change the fake's log stream and write the failing tests**

In `internal/tui/tui_test.go`, add a `logs []string` field to `fake` and replace its `LogLines` method so the stream delivers the canned lines and then ends (a closed channel never blocks the test runner):

```go
func (f *fake) LogLines(context.Context, string, string, int) (<-chan string, error) {
	ch := make(chan string, len(f.logs))
	for _, l := range f.logs {
		ch <- l
	}
	close(ch)
	return ch, f.fail["logs"]
}
```

Append the Task 3 tests:

```go
// --- Task 3 tests ---

func TestEnterOpensBoxAndEscReturns(t *testing.T) {
	m := newModel(t, newFake())
	m = press(t, m, "enter")
	if m.mode != modeBox || m.cur != "app-1a2b3c/a" {
		t.Fatalf("mode %v cur %q", m.mode, m.cur)
	}
	mustContain(t, m.View(), "agentbox/a", "/tmp/boxes/a", "1 overview", "AGENTBOX_NAME=a", `cd "/tmp/boxes/a"`, "a.localhost:7777")
	m = press(t, m, "esc")
	if m.mode != modeBoxes {
		t.Fatalf("mode %v", m.mode)
	}
}

func TestBoxScreenActions(t *testing.T) {
	f := newFake()
	m := newModel(t, f)
	m = press(t, m, "enter", "s")
	if !f.called("stop a") {
		t.Fatalf("calls %v", f.calls)
	}
	m = press(t, m, "x")
	mustContain(t, m.View(), "Remove a?")
	m = press(t, m, "n", "o")
	if !f.called("open http://a.localhost:7777/") {
		t.Fatalf("calls %v", f.calls)
	}
	m = press(t, m, "m")
	mustContain(t, m.View(), "Merge agentbox/a into")
}

func TestLogsTabStreamsAndRings(t *testing.T) {
	f := newFake()
	f.logs = []string{"listening on 20000", "GET / 200"}
	m := newModel(t, f)
	m = press(t, m, "enter", "2")
	if m.tab != tabLogs {
		t.Fatalf("tab %v", m.tab)
	}
	mustContain(t, m.View(), "2 logs", "listening on 20000", "GET / 200", "2 lines")
	m.logID = "app-1a2b3c/a" // pretend the stream is still open
	for i := 0; i < maxLogLines+10; i++ {
		m = update(t, m, logLineMsg{id: "app-1a2b3c/a", line: fmt.Sprintf("line %d", i)})
	}
	if len(m.logs) != maxLogLines {
		t.Fatalf("ring holds %d lines, want %d", len(m.logs), maxLogLines)
	}
	mustContain(t, m.View(), fmt.Sprintf("line %d", maxLogLines+9))
	m = press(t, m, "c")
	mustContain(t, m.View(), "0 lines")
	m = press(t, m, "esc")
	if m.logID != "" {
		t.Fatal("leaving the box must stop the stream")
	}
}

func TestStaleLogLinesIgnored(t *testing.T) {
	f := newFake()
	f.boxes["app-1a2b3c/b"] = api.BoxView{ID: "app-1a2b3c/b", Repo: "app-1a2b3c", RepoPath: "/tmp/app", Name: "b", Dir: "/tmp/boxes/b", Branch: "agentbox/b", State: "running"}
	m := newModel(t, f)
	m = press(t, m, "enter", "2")
	m.logID = "app-1a2b3c/a"
	m = press(t, m, "esc", "down", "enter", "2")
	if m.cur != "app-1a2b3c/b" {
		t.Fatalf("cur %q", m.cur)
	}
	m.logID = "app-1a2b3c/b"
	m = update(t, m, logLineMsg{id: "app-1a2b3c/a", line: "from a"})
	m = update(t, m, logLineMsg{id: "app-1a2b3c/b", line: "from b"})
	mustContain(t, m.View(), "from b")
	mustNotContain(t, m.View(), "from a")
}

func TestDiffTabAndAgainstPrompt(t *testing.T) {
	f := newFake()
	m := newModel(t, f)
	m = press(t, m, "enter", "3")
	mustContain(t, m.View(), "3 diff", "+new", "-old", "@@ -1 +1 @@", "against main")
	if !f.called("diff a ") {
		t.Fatalf("calls %v", f.calls)
	}
	m = press(t, m, "a")
	mustContain(t, m.View(), "Diff against")
	m = typeText(t, m, "v1")
	m = press(t, m, "enter")
	if !f.called("diff a v1") {
		t.Fatalf("calls %v", f.calls)
	}
	mustContain(t, m.View(), "against v1")
}

func TestNewBoxFlow(t *testing.T) {
	f := newFake()
	m := newModel(t, f)
	m = press(t, m, "n")
	if m.mode != modeNew {
		t.Fatalf("mode %v", m.mode)
	}
	mustContain(t, m.View(), "New box", "in app", "name", "start from", "[ ]")
	m = typeText(t, m, "Bad")
	mustContain(t, m.View(), "lowercase")
	m = press(t, m, "backspace", "backspace", "backspace")
	m = typeText(t, m, "q-feat") // q must be typed, never quit
	if m.mode != modeNew {
		t.Fatal("typing q quit the program")
	}
	mustNotContain(t, m.View(), "lowercase")
	m = press(t, m, "tab", "tab", " ")
	mustContain(t, m.View(), "[x]")
	m = press(t, m, "enter")
	if !f.called("create q-feat") || m.form.submitted != "app-1a2b3c/q-feat" {
		t.Fatalf("calls %v submitted %q", f.calls, m.form.submitted)
	}
	m = update(t, m, eventMsg{client.Event{Type: "box.step", Box: "app-1a2b3c/q-feat", Data: map[string]any{"step": "setup"}}})
	mustContain(t, m.View(), "Creating", "q-feat", "setup")
	nb := api.BoxView{ID: "app-1a2b3c/q-feat", Repo: "app-1a2b3c", RepoPath: "/tmp/app", Name: "q-feat", State: "running"}
	f.boxes[nb.ID] = nb
	m = update(t, m, boxMsg{id: nb.ID, box: nb})
	if m.mode != modeBox || m.cur != nb.ID {
		t.Fatalf("mode %v cur %q: a ready box must open its screen", m.mode, m.cur)
	}
}

func TestNewBoxFailuresShown(t *testing.T) {
	f := newFake()
	f.fail["create"] = errs.New(errs.Exists, "box a already exists")
	m := newModel(t, f)
	m = press(t, m, "n")
	m = typeText(t, m, "a")
	m = press(t, m, "enter")
	mustContain(t, m.View(), "already exists")
	if m.mode != modeNew || m.form.submitted != "" {
		t.Fatalf("mode %v submitted %q", m.mode, m.form.submitted)
	}
	f.fail["create"] = nil
	m = press(t, m, "enter")
	m = update(t, m, eventMsg{client.Event{Type: "box.removed", Box: "app-1a2b3c/a", Data: map[string]any{"error": "setup failed: npm install exited 1"}}})
	mustContain(t, m.View(), "npm install exited 1")
	m = press(t, m, "esc")
	if m.mode != modeBoxes {
		t.Fatalf("mode %v", m.mode)
	}
}

func TestNewBoxNeedsARepo(t *testing.T) {
	f := newFake()
	f.repos = append(f.repos, api.RepoView{ID: "web-9z9z9z", Path: "/tmp/web"})
	f.boxes = map[string]api.BoxView{}
	m := newModel(t, f)
	m = press(t, m, "n")
	if m.mode != modeBoxes {
		t.Fatal("two repos, no selection, no filter: the form must not open")
	}
	mustContain(t, m.View(), "pick a repo")
	m = press(t, m, "r", "n")
	if m.mode != modeNew {
		t.Fatal("with a repo filter the form must open")
	}
}

func TestRemovedBoxLeavesScreen(t *testing.T) {
	m := newModel(t, newFake())
	m = press(t, m, "enter")
	m = update(t, m, eventMsg{client.Event{Type: "box.removed", Box: "app-1a2b3c/a"}})
	if m.mode != modeBoxes {
		t.Fatalf("mode %v", m.mode)
	}
	mustContain(t, m.View(), "box a was removed")
	mustNotContain(t, m.View(), "a.localhost")
}

func TestDoctorScreen(t *testing.T) {
	f := newFake()
	f.findings = []box.Finding{{Kind: "orphan_branch", Detail: "branch agentbox/old has no box"}}
	m := newModel(t, f)
	m = press(t, m, "d")
	if m.mode != modeDoctor {
		t.Fatalf("mode %v", m.mode)
	}
	mustContain(t, m.View(), "Doctor", "/tmp/app", "orphan_branch", "has no box", "✗")
	m = press(t, m, "f")
	if !f.called("doctor app-1a2b3c fix=true") {
		t.Fatalf("calls %v", f.calls)
	}
	mustContain(t, m.View(), "fixed")
	m = press(t, m, "esc")
	if m.mode != modeBoxes {
		t.Fatalf("mode %v", m.mode)
	}
	f.findings = nil
	m = press(t, m, "d")
	mustContain(t, m.View(), "no problems found")
	m = press(t, m, "r") // re-check, not repo filter, in this mode
	if !f.called("doctor app-1a2b3c fix=false") || m.filter != "" {
		t.Fatalf("calls %v filter %q", f.calls, m.filter)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/tui/ -run 'Enter|BoxScreen|Logs|Stale|Diff|NewBox|Removed|Doctor'`
Expected: FAIL (the stubs return placeholders; e.g. "box screen: Task 3").

- [ ] **Step 3: `view_box.go`**

Replace `internal/tui/view_box.go` with:

```go
package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Ayush2966/Agent-Box/internal/api"
)

type boxTab int

const (
	tabOverview boxTab = iota
	tabLogs
	tabDiff
)

const maxLogLines = 5000

func (m *Model) openBox(id string) tea.Cmd {
	if m.cur != id {
		m.stopLogs()
		m.logs = nil
		m.logView.SetContent("")
		m.diff, m.diffLoaded, m.diffLoading, m.diffAgainst = "", false, false, ""
	}
	m.mode, m.cur, m.tab = modeBox, id, tabOverview
	m.logFollow = true
	return nil
}

func (m *Model) leaveBox() {
	m.stopLogs()
	m.mode = modeBoxes
}

// stopLogs ends the log stream, if any. logID is cleared so the next visit
// to the logs tab opens a fresh stream.
func (m *Model) stopLogs() {
	if m.logStop != nil {
		m.logStop()
		m.logStop = nil
	}
	m.logID, m.logCh = "", nil
}

// setTab switches tabs and starts what the tab needs: the log stream, or the diff.
func (m *Model) setTab(t boxTab) tea.Cmd {
	m.tab = t
	switch t {
	case tabLogs:
		if m.logID != m.cur {
			m.stopLogs()
			m.logs = nil
			m.logFollow = true
			m.logView.SetContent("")
			m.logID = m.cur // claimed now, so a second keypress can't open two streams
			return m.openLogs(m.cur)
		}
	case tabDiff:
		if !m.diffLoaded && !m.diffLoading {
			m.diffLoading = true
			return m.loadDiff(m.cur, m.diffAgainst)
		}
	}
	return nil
}

func (m Model) updateBox(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	b, ok := m.boxes[m.cur]
	if !ok {
		m.leaveBox()
		return m, nil
	}
	keys := m.keys
	switch {
	case key.Matches(k, keys.Back):
		m.leaveBox()
		return m, nil
	case key.Matches(k, keys.Tab):
		cmd := m.setTab((m.tab + 1) % 3)
		return m, cmd
	case key.Matches(k, keys.Tab1):
		cmd := m.setTab(tabOverview)
		return m, cmd
	case key.Matches(k, keys.Tab2):
		cmd := m.setTab(tabLogs)
		return m, cmd
	case key.Matches(k, keys.Tab3):
		cmd := m.setTab(tabDiff)
		return m, cmd
	case key.Matches(k, keys.StartStop):
		return m, m.toggleRun(b)
	case key.Matches(k, keys.Remove):
		m.askRemove(b)
		return m, nil
	case key.Matches(k, keys.Merge):
		m.askMerge(b)
		return m, nil
	case key.Matches(k, keys.OpenURL):
		return m, m.openURL(b)
	case key.Matches(k, keys.Shell):
		return m, m.shell(b)
	}
	switch m.tab {
	case tabLogs:
		switch {
		case key.Matches(k, keys.Follow):
			m.logView.GotoBottom()
			m.logFollow = true
		case key.Matches(k, keys.Clear):
			m.logs = nil
			m.logView.SetContent("")
		default:
			var cmd tea.Cmd
			m.logView, cmd = m.logView.Update(k)
			m.logFollow = m.logView.AtBottom()
			return m, cmd
		}
	case tabDiff:
		if key.Matches(k, keys.Against) {
			m.overlay = newPrompt("Diff against…", m.against(b), nil, m.opts.NoBlink,
				func(m *Model, ref string, _ bool) tea.Cmd {
					m.diffAgainst, m.diff, m.diffLoaded, m.diffLoading = ref, "", false, true
					return m.loadDiff(m.cur, ref)
				})
			return m, nil
		}
		var cmd tea.Cmd
		m.diffView, cmd = m.diffView.Update(k)
		return m, cmd
	}
	return m, nil
}

func (m Model) onLogsOpen(msg logsOpenMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeBox || msg.id != m.logID {
		msg.stop() // the user moved on before the stream opened
		return m, nil
	}
	m.logStop, m.logCh = msg.stop, msg.ch
	return m, waitLine(msg.id, msg.ch)
}

func (m Model) onLogLine(msg logLineMsg) (tea.Model, tea.Cmd) {
	if msg.id != m.logID {
		return m, nil // a stale stream's line
	}
	m.logs = append(m.logs, msg.line)
	if len(m.logs) > maxLogLines {
		m.logs = m.logs[len(m.logs)-maxLogLines:]
	}
	m.logView.SetContent(strings.Join(m.logs, "\n"))
	if m.logFollow {
		m.logView.GotoBottom()
	}
	if m.logCh == nil {
		return m, nil
	}
	return m, waitLine(msg.id, m.logCh)
}

func (m Model) onLogsDone(msg logsDoneMsg) (tea.Model, tea.Cmd) {
	if msg.id != m.logID {
		return m, nil
	}
	m.logStop, m.logCh, m.logID = nil, nil, ""
	if msg.err != nil {
		cmd := m.say(true, "logs: "+describe(msg.err))
		return m, cmd
	}
	return m, nil
}

func (m Model) onDiff(msg diffMsg) (tea.Model, tea.Cmd) {
	if msg.id != m.cur {
		return m, nil
	}
	m.diffLoading = false
	if msg.err != nil {
		cmd := m.say(true, "diff: "+describe(msg.err))
		return m, cmd
	}
	m.diff, m.diffLoaded = msg.text, true
	m.diffView.SetContent(colorDiff(msg.text))
	m.diffView.GotoTop()
	return m, nil
}

func colorDiff(d string) string {
	lines := strings.Split(strings.TrimRight(d, "\n"), "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"), strings.HasPrefix(l, "diff --git"):
			lines[i] = stHeader.Render(l)
		case strings.HasPrefix(l, "@@"):
			lines[i] = stAccent.Render(l)
		case strings.HasPrefix(l, "+"):
			lines[i] = stOK.Render(l)
		case strings.HasPrefix(l, "-"):
			lines[i] = stErr.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

func (m Model) against(b api.BoxView) string {
	switch {
	case m.diffAgainst != "":
		return m.diffAgainst
	case b.Base != "":
		return b.Base
	}
	return "the main checkout's branch"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (m Model) viewBox(h int) string {
	b, ok := m.boxes[m.cur]
	if !ok {
		return ""
	}
	head := stMuted.Render("← boxes") + "  " + stHeader.Render(b.Name) + "  " + pill(b.State)
	if step, ok := m.steps[b.ID]; ok && step != "" {
		head += "  " + stMuted.Render(step+"…")
	}
	var tabs string
	for i, name := range []string{"1 overview", "2 logs", "3 diff"} {
		if boxTab(i) == m.tab {
			tabs += stTabOn.Render(name)
		} else {
			tabs += stTab.Render(name)
		}
	}
	var body string
	switch m.tab {
	case tabLogs:
		status := fmt.Sprintf("%d lines", len(m.logs))
		if !m.logFollow {
			status += " · scrolled up (G to follow)"
		}
		body = stMuted.Render(status) + "\n" + m.logView.View()
	case tabDiff:
		switch {
		case m.diffLoading:
			body = stMuted.Render("loading diff…")
		case m.diffLoaded && strings.TrimSpace(m.diff) == "":
			body = stMuted.Render("No changes against " + m.against(b) + ".")
		default:
			body = stMuted.Render("against "+m.against(b)+"  (a to change)") + "\n" + m.diffView.View()
		}
	default:
		body = m.viewOverview(b)
	}
	return lipgloss.JoinVertical(lipgloss.Left, head, tabs, body)
}

func (m Model) viewOverview(b api.BoxView) string {
	row := func(k, v string) string { return "  " + stMuted.Render(fmt.Sprintf("%-15s", k)) + v }
	created := ""
	if !b.CreatedAt.IsZero() {
		created = b.CreatedAt.Local().Format(time.DateTime)
	}
	lines := []string{
		row("dir", b.Dir),
		row("branch", b.Branch),
		row("base", dash(b.Base)),
		row("ahead / behind", fmt.Sprintf("%d / %d", b.Ahead, b.Behind)),
		row("dirty files", strconv.Itoa(b.Dirty)),
		row("ports", formatPorts(b.Ports)),
		row("url", dash(b.URL)),
		row("database", dash(b.DBName)),
		row("pid", dash(nonZero(b.PID))),
		row("created", dash(created)),
	}
	if b.LastError != "" {
		lines = append(lines, row("last error", stErr.Render(b.LastError)))
	}
	if f, ok := m.failures[b.ID]; ok {
		lines = append(lines, row("failure", stErr.Render(f)))
	}
	lines = append(lines, "", "  "+stHeader.Render("Point your agent at it"), "    cd "+strconv.Quote(b.Dir))
	if len(b.Env) > 0 {
		lines = append(lines, "", "  "+stHeader.Render("Managed env"))
		for _, kv := range envList(b.Env) {
			lines = append(lines, "    "+kv)
		}
	}
	return strings.Join(lines, "\n")
}

func nonZero(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}
```

- [ ] **Step 4: `view_new.go`**

Replace `internal/tui/view_new.go` with:

```go
package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Ayush2966/Agent-Box/internal/api"
	"github.com/Ayush2966/Agent-Box/internal/box"
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

type newForm struct {
	repo      api.RepoView
	name      textinput.Model
	from      textinput.Model
	noDev     bool
	focus     int    // 0 name, 1 from, 2 no-dev
	submitted string // the box id once CreateBox accepted it
	err       string
}

func validateName(s string) string {
	switch {
	case s == "":
		return "a name is required"
	case !nameRe.MatchString(s):
		return "names are lowercase letters, digits and dashes (up to 31, starting with a letter or digit)"
	}
	return ""
}

// newRepo picks the repo a new box goes into: the filter, the selected
// box's repo, or the only repo there is.
func (m Model) newRepo() (api.RepoView, bool) {
	b, ok := m.selected()
	id := m.repoFor(b, ok)
	for _, r := range m.repos {
		if r.ID == id {
			return r, true
		}
	}
	return api.RepoView{}, false
}

func (m Model) startNew() (tea.Model, tea.Cmd) {
	repo, ok := m.newRepo()
	if !ok {
		cmd := m.say(true, "pick a repo first: press r to filter by repo, or select a box in it")
		return m, cmd
	}
	f := newForm{repo: repo, name: textinput.New(), from: textinput.New()}
	f.name.Placeholder, f.name.CharLimit, f.name.Width = "feature-x", 31, 32
	f.from.Placeholder, f.from.CharLimit, f.from.Width = "HEAD (a branch, tag or commit)", 100, 40
	if m.opts.NoBlink {
		f.name.Cursor.SetMode(cursor.CursorStatic)
		f.from.Cursor.SetMode(cursor.CursorStatic)
	}
	cmd := f.name.Focus()
	m.form, m.mode = f, modeNew
	return m, cmd
}

func (m *Model) focusField() tea.Cmd {
	f := &m.form
	f.name.Blur()
	f.from.Blur()
	switch f.focus {
	case 0:
		return f.name.Focus()
	case 1:
		return f.from.Focus()
	}
	return nil
}

func (m Model) updateNew(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := &m.form
	if f.submitted != "" { // progress view
		switch k.String() {
		case "esc", "enter":
			m.mode = modeBoxes
		case "ctrl+c":
			return m, tea.Quit
		}
		return m, nil
	}
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.mode = modeBoxes
		return m, nil
	case "tab", "down":
		f.focus = (f.focus + 1) % 3
		cmd := m.focusField()
		return m, cmd
	case "shift+tab", "up":
		f.focus = (f.focus + 2) % 3
		cmd := m.focusField()
		return m, cmd
	case "enter":
		return m.submitNew()
	case " ":
		if f.focus == 2 {
			f.noDev = !f.noDev
			return m, nil
		}
	}
	var cmd tea.Cmd
	switch f.focus {
	case 0:
		f.name, cmd = f.name.Update(k)
		f.err = validateName(f.name.Value())
	case 1:
		f.from, cmd = f.from.Update(k)
	}
	return m, cmd
}

func (m Model) submitNew() (tea.Model, tea.Cmd) {
	f := &m.form
	name := strings.TrimSpace(f.name.Value())
	if msg := validateName(name); msg != "" {
		f.err = msg
		return m, nil
	}
	f.err = ""
	return m, m.create(box.NewOptions{RepoPath: f.repo.Path, Name: name, From: strings.TrimSpace(f.from.Value()), NoDev: f.noDev})
}

func (m Model) onCreated(msg createdMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeNew {
		return m, nil
	}
	if msg.err != nil {
		m.form.err = describe(msg.err)
		return m, nil
	}
	m.form.submitted = msg.id
	return m, nil
}

// afterBoxUpdate opens the box the new-box screen is waiting on once it is up.
func (m *Model) afterBoxUpdate(id string) tea.Cmd {
	if m.mode != modeNew || m.form.submitted != id {
		return nil
	}
	if b := m.boxes[id]; b.State == "running" || b.State == "ready" {
		return m.openBox(id)
	}
	return nil
}

// forwardToInputs routes non-key messages (cursor blinks, pastes) to the live text input.
func (m Model) forwardToInputs(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if p, ok := m.overlay.(*prompt); ok {
		p.input, cmd = p.input.Update(msg)
		return m, cmd
	}
	if m.mode == modeNew && m.form.submitted == "" {
		switch m.form.focus {
		case 0:
			m.form.name, cmd = m.form.name.Update(msg)
		case 1:
			m.form.from, cmd = m.form.from.Update(msg)
		}
	}
	return m, cmd
}

func (m Model) viewNew(h int) string {
	f := m.form
	title := stHeader.Render("New box") + stMuted.Render(" in "+repoBase(f.repo.Path))
	if f.submitted != "" {
		_, name := splitID(f.submitted)
		lines := []string{title, "", "Creating " + stHeader.Render(name) + "…"}
		if step, ok := m.steps[f.submitted]; ok && step != "" {
			lines = append(lines, stMuted.Render("  "+step+"…"))
		}
		if fail, ok := m.failures[f.submitted]; ok {
			lines = append(lines, "", stErr.Render(fail))
		}
		if b, ok := m.boxes[f.submitted]; ok && b.State == "stopped" && b.LastError != "" {
			lines = append(lines, "", stErr.Render("the dev server keeps crashing: "+b.LastError))
		}
		lines = append(lines, "", stMuted.Render("esc  back to the list"))
		return strings.Join(lines, "\n")
	}
	mark := func(i int) string {
		if f.focus == i {
			return stAccent.Render("› ")
		}
		return "  "
	}
	check := "[ ]"
	if f.noDev {
		check = "[x]"
	}
	lines := []string{title, "",
		mark(0) + stMuted.Render("name        ") + f.name.View(),
		mark(1) + stMuted.Render("start from  ") + f.from.View(),
		mark(2) + check + " don't start the dev server" + stMuted.Render("   (space toggles)"),
	}
	if f.err != "" {
		lines = append(lines, "", stErr.Render(f.err))
	}
	return strings.Join(lines, "\n")
}
```

- [ ] **Step 5: `view_doctor.go`**

Replace `internal/tui/view_doctor.go` with:

```go
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Ayush2966/Agent-Box/internal/box"
)

type doctorState struct {
	repo     string
	findings []box.Finding
	busy     bool
	loaded   bool
	err      string
}

func (m *Model) openDoctor(repo string) tea.Cmd {
	m.mode = modeDoctor
	m.doctor = doctorState{repo: repo, busy: true}
	return m.runDoctor(repo, false)
}

func (m Model) updateDoctor(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := &m.doctor
	switch {
	case key.Matches(k, m.keys.Back):
		m.mode = modeBoxes
	case key.Matches(k, m.keys.Fix):
		if !d.busy {
			d.busy = true
			return m, m.runDoctor(d.repo, true)
		}
	case key.Matches(k, m.keys.Recheck):
		if !d.busy {
			d.busy = true
			return m, m.runDoctor(d.repo, false)
		}
	}
	return m, nil
}

func (m Model) onFindings(msg findingsMsg) (tea.Model, tea.Cmd) {
	if msg.repo != m.doctor.repo {
		return m, nil
	}
	m.doctor.busy, m.doctor.loaded = false, true
	if msg.err != nil {
		m.doctor.err = describe(msg.err)
		return m, nil
	}
	m.doctor.err, m.doctor.findings = "", msg.findings
	return m, nil
}

func (m Model) viewDoctor(h int) string {
	d := m.doctor
	lines := []string{stHeader.Render("Doctor") + stMuted.Render("  "+m.repoPath(d.repo)), ""}
	switch {
	case d.err != "":
		lines = append(lines, stErr.Render(d.err))
	case d.busy && !d.loaded:
		lines = append(lines, stMuted.Render("checking…"))
	case len(d.findings) == 0:
		lines = append(lines, stOK.Render("✓ no problems found"))
	default:
		for _, f := range d.findings {
			mark := stErr.Render("✗")
			if f.Fixed {
				mark = stOK.Render("✓ fixed")
			}
			lines = append(lines, fmt.Sprintf("%s %s %s", mark, stAccent.Render("["+f.Kind+"]"), f.Detail))
		}
	}
	if d.busy && d.loaded {
		lines = append(lines, "", stMuted.Render("working…"))
	}
	return strings.Join(lines, "\n")
}
```

- [ ] **Step 6: Run the tests**

Run: `gofmt -l internal/tui; go vet ./internal/tui/ && go test -race ./internal/tui/`
Expected: PASS — all Task 2 and Task 3 tests (27). If `TestLogsTabStreamsAndRings` reports "0 lines" before the ring loop, the fake's closed channel delivered `logsDoneMsg` which cleared `logID` as designed; the test re-sets `logID` before feeding lines, so check that `onLogLine` compares against `m.logID`.

- [ ] **Step 7: Checkpoint**

Run `go vet ./... && go test -race ./internal/tui/ && go build ./...`; record "Task 3 complete". Do not commit.

---

### Task 4: `Run`, `agentbox tui`, integration test, README

**Files:**
- Create: `internal/tui/tui.go`, `internal/tui/integration_test.go`, `internal/cli/tui_cmd.go`
- Modify: `internal/cli/root.go`, `internal/cli/cli_test.go`, `README.md`

**Interfaces:**
- Consumes: `tui.New/Options/Model` (Tasks 2–3), `client.Client` (satisfies `tui.API`), `daemon.Run/PathsFor/Ping` for the test, `cli.app.connect/repoPath/isTerminal`, `openBrowser` (phase 2, `internal/cli/ui_cmd.go`).
- Produces: `func tui.Run(ctx context.Context, a API, opts Options) error`; `agentbox tui [--all]`.

- [ ] **Step 1: Write the failing tests**

Create `internal/tui/integration_test.go`:

```go
package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Ayush2966/Agent-Box/internal/box"
	"github.com/Ayush2966/Agent-Box/internal/client"
	"github.com/Ayush2966/Agent-Box/internal/daemon"
	"github.com/Ayush2966/Agent-Box/internal/testutil"
)

// TestAgainstRealDaemon runs the model's commands against a live daemon and a
// real client: list, log stream and the event feed.
func TestAgainstRealDaemon(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "abtui") // short path: unix socket limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("api_addr: 127.0.0.1:0\n"), 0o600)
	p := daemon.PathsFor(home)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx, p) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	for deadline := time.Now().Add(5 * time.Second); !daemon.Ping(p.Sock); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("daemon never came up")
		}
	}

	repo := testutil.NewRepo(t, map[string]string{"agentbox.yaml": "version: 1\ndev: echo hello from the box; sleep 30\nports: [APP_PORT]\n"})
	c := client.New(p.Sock)
	rv, err := c.ResolveRepo(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateBox(ctx, box.NewOptions{RepoPath: repo, Name: "a"}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		b, err := c.GetBox(ctx, rv.ID, "a")
		if err == nil && b.State == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("box never ran: %+v %v", b, err)
		}
	}
	t.Cleanup(func() { _ = c.RemoveBox(context.Background(), rv.ID, "a", true, false) })

	m := New(ctx, c, Options{ToastTTL: -1, NoBlink: true, Repo: rv.ID})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = run(t, m, m.loadAll(), 0)
	mustContain(t, m.View(), "running", "repo: "+filepath.Base(repo), "1 boxes")
	if !strings.Contains(m.View(), " a ") && !strings.Contains(m.View(), "a  ") {
		t.Fatalf("box a missing:\n%s", m.View())
	}

	lctx, lcancel := context.WithCancel(ctx)
	defer lcancel()
	lines, err := c.LogLines(lctx, rv.ID, "a", 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for deadline := time.After(10 * time.Second); !seen; {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("log stream closed early")
			}
			seen = strings.Contains(l, "hello from the box")
		case <-deadline:
			t.Fatal("never saw the dev server's output")
		}
	}

	evs, err := c.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.StopBox(ctx, rv.ID, "a"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.After(10 * time.Second); ; {
		select {
		case ev := <-evs:
			if ev.Type == "box.state" && ev.Data["state"] == "stopped" {
				m = update(t, m, eventMsg{ev})
				mustContain(t, m.View(), "stopped")
				return
			}
		case <-deadline:
			t.Fatal("never saw the stopped event")
		}
	}
}
```

Append to `internal/cli/cli_test.go`:

```go
func TestTUIRefusesWithoutATerminal(t *testing.T) {
	t.Setenv("AGENTBOX_HOME", shortTempHome(t))
	code, _, stderr := run("tui")
	if code != 1 || !strings.Contains(stderr, "interactive terminal") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	code, out, _ := run("tui", "--help")
	if code != 0 || !strings.Contains(out, "--all") {
		t.Fatalf("help: exit %d\n%s", code, out)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/tui/ -run RealDaemon; go test ./internal/cli/ -run TUI`
Expected: the integration test PASSES already if Tasks 1–3 are right (it uses `New` and `run`); the CLI test FAILS with `unknown command "tui"`.

- [ ] **Step 3: `tui.go`**

Create `internal/tui/tui.go`:

```go
package tui

import (
	"context"
	"errors"

	tea "github.com/charmbracelet/bubbletea"
)

// Run drives the dashboard until the user quits or ctx is cancelled.
func Run(ctx context.Context, a API, opts Options) error {
	p := tea.NewProgram(New(ctx, a, opts), tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	if err != nil && (errors.Is(err, tea.ErrProgramKilled) || ctx.Err() != nil) {
		return nil // ctrl+c / cancelled: not an error
	}
	return err
}
```

(If `tea.ErrProgramKilled` does not exist in this bubbletea version, drop that clause and keep `ctx.Err() != nil`.)

- [ ] **Step 4: The command**

Create `internal/cli/tui_cmd.go`:

```go
package cli

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/Ayush2966/Agent-Box/internal/tui"
)

func (a *app) tuiCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Open the terminal dashboard",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !isTerminal(os.Stdout) || !isTerminal(os.Stdin) {
				return errors.New("agentbox tui needs an interactive terminal (try `agentbox ls --json` for scripts)")
			}
			if err := a.connect(); err != nil {
				return err
			}
			opts := tui.Options{OpenBrowser: openBrowser}
			if !all {
				if p, err := a.repoPath(); err == nil {
					if rv, err := a.c.ResolveRepo(cmd.Context(), p); err == nil {
						opts.Repo = rv.ID // start on the repo we were launched in; r cycles to the others
					}
				}
			}
			return tui.Run(cmd.Context(), a.c, opts)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "start with every repo shown instead of the current one")
	return cmd
}
```

In `internal/cli/root.go`, add `a.tuiCmd(),` right after `a.uiCmd(),` in `root.AddCommand(…)`.

- [ ] **Step 5: README**

In `README.md`:

1. Quick start: after the `agentbox ui` line add
   ```
       agentbox tui           # the same dashboard in your terminal
   ```
2. Commands table: after the `ui` row add `| `tui [--all]` | terminal dashboard: live boxes, logs, diff, actions, shell into a box |`.
3. In the "Dashboard and box URLs" section, add a paragraph:

```markdown
`agentbox tui` is the terminal version: a live table of every box (`r` cycles repos, `/` filters),
`enter` for a box's overview, logs and diff, `s`/`x`/`m` to start-stop, remove and merge, `e` to open
a shell inside the box with its env, `o` to open its URL, `d` for doctor, `?` for every key.
```

4. Roadmap line: `Docker Compose & MySQL drivers → desktop app.`

- [ ] **Step 6: Run everything**

```bash
gofmt -l internal; go vet ./... && go test -race -count=1 ./...
go test -count=1 -tags e2e ./e2e/
CGO_ENABLED=0 go build -o /tmp/agentbox-tui ./cmd/agentbox && /tmp/agentbox-tui tui --help
```

Expected: all green; the e2e suite unchanged; `tui --help` lists `--all`. Then a manual smoke in a real terminal is the only way to see the alt screen: run `AGENTBOX_HOME=/tmp/abtui2 /tmp/agentbox-tui tui` inside `examples/sqlite` (after `agentbox new a` there) if a TTY is available to you; if not, say so in the report — the integration test covers the model against a live daemon.

- [ ] **Step 7: Checkpoint**

Record "Task 4 complete". Do not commit.

---

## Self-review (done while writing)

- **Spec coverage:** §4 R1 (`tui` subcommand) → T4; R2 (socket via client) → T4 (`a.c`); R3 (all repos, filter defaults to current) → T2 `filter`, T4 `opts.Repo`; R4 (one root model) → T2; R5 (`API` interface) → T2 `api.go`; R6 (deps, go directive) → T2 step 1; R7 (shell, open URL) → T2 `shell`/`openURL`; R8 (no golden tests) → tests assert text. §5 layout → file structure. §6 header/footer → `header`/`footer`; 6.1 columns + every key → `columns`/`updateBoxes`; 6.2 tabs, logs ring 5 000, follow, `a` prompt → T3 `view_box.go`; 6.3 form, validation, progress, auto-open → T3 `view_new.go`; 6.4 doctor → T3; 6.5 overlays with toggles `K`/`T`/`ctrl+s` → T2 `overlay.go`. §7 errors as toasts (`describe`), too-small terminal, non-terminal refusal (T4), removed-box return (`applyEvent`), reconnect backoff → T2. §8 unit list → T2/T3 tests; integration → T4; CLI → T4.
- **Placeholders:** none; the Task 2 stubs are explicit, compile, and are fully replaced in Task 3.
- **Type consistency:** `boxTab` consts, `newForm`, `doctorState` are declared in the stubs with the same names Task 3 keeps; `afterBoxUpdate` changes from a value to a pointer receiver in Task 3 — its only call site (`model.go`, on the addressable `m`) is valid for both; `newPrompt`'s signature is identical in T2 (`askMerge`) and T3 (`Against`); `tell`/`toastMsg` are defined in T2 and used by T2's stubs and helpers; the test helpers `press/run/update/typeText/mustContain` are shared.
- **Review Focus:** 1 → `TestNewBoxFlow` types `q-feat`; 2 → `TestRemovedBoxLeavesScreen`; 3 → `TestStaleLogLinesIgnored`; 4 → `TestReconnectBackoff`; 5 → `TestActionOnVanishedBoxToasts`.
