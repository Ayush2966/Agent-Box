package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/agentbox-dev/agentbox/internal/api"
	"github.com/agentbox-dev/agentbox/internal/box"
	"github.com/agentbox-dev/agentbox/internal/client"
	"github.com/agentbox-dev/agentbox/internal/errs"
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
	logs     []string
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

func (f *fake) Repos(context.Context) ([]api.RepoView, error) {
	f.rec("repos")
	return f.repos, f.fail["repos"]
}

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

func (f *fake) StartBox(_ context.Context, _, name string) error {
	f.rec("start " + name)
	return f.fail["start"]
}
func (f *fake) StopBox(_ context.Context, _, name string) error {
	f.rec("stop " + name)
	return f.fail["stop"]
}

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
	ch := make(chan string, len(f.logs))
	for _, l := range f.logs {
		ch <- l
	}
	close(ch)
	return ch, f.fail["logs"]
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
	m = press(t, m, "enter")
	mustContain(t, m.View(), "Discard unmerged work?")
	if f.called("remove a force=true keep=false") {
		t.Fatalf("enter must not confirm the discard: %v", f.calls)
	}
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
	ov := press(t, m, "x")
	if _, cmd := ov.Update(keyMsg("ctrl+c")); cmd == nil || cmd() != tea.Quit() {
		t.Fatal("ctrl+c must quit while an overlay is open")
	}
	hp := m
	hp.showHelp = true
	if _, cmd := hp.Update(keyMsg("ctrl+c")); cmd == nil || cmd() != tea.Quit() {
		t.Fatal("ctrl+c must quit while help is shown")
	}
}

func TestStateCellIsPlainText(t *testing.T) {
	f := newFake()
	m := newModel(t, f)
	b := f.boxes["app-1a2b3c/a"]
	b.State = "creating"
	m = update(t, m, boxesMsg{boxes: []api.BoxView{b}})
	m = update(t, m, eventMsg{client.Event{Type: "box.step", Box: b.ID, Data: map[string]any{"step": "database"}}})
	if cell := m.stateCell(m.boxes[b.ID]); strings.Contains(cell, "\x1b") || cell != "creating database…" {
		t.Fatalf("state cell must be plain text: %q", cell)
	}
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	mustContain(t, m.View(), "creating database")
}

func TestHelpNeverHidesAnOverlay(t *testing.T) {
	m := newModel(t, newFake())
	m = press(t, m, "x")
	m.showHelp = true
	mustContain(t, m.View(), "Remove a?")
	mustNotContain(t, m.View(), "next tab") // full help only; the footer short help lacks it
	m = press(t, m, "n")
	m.showHelp = true
	m = update(t, m, unmergedMsg{box: m.boxes["app-1a2b3c/a"], message: "2 unmerged commits"})
	if m.showHelp {
		t.Fatal("a new confirm must dismiss the help screen")
	}
	mustContain(t, m.View(), "Discard unmerged work?")
}

func TestReloadAndEscClearQuery(t *testing.T) {
	f := newFake()
	f.boxes["app-1a2b3c/feature-b"] = api.BoxView{ID: "app-1a2b3c/feature-b", Repo: "app-1a2b3c", RepoPath: "/tmp/app", Name: "feature-b", State: "ready"}
	m := newModel(t, f)
	m = press(t, m, "/")
	m = typeText(t, m, "feat")
	m = press(t, m, "enter")
	mustContain(t, m.View(), "1 boxes")
	m = press(t, m, "esc")
	mustContain(t, m.View(), "2 boxes")
	before := len(f.calls)
	m = press(t, m, "/")
	m = typeText(t, m, "zzz")
	m = press(t, m, "enter") // no rows now
	m = press(t, m, "g")
	if !strings.Contains(strings.Join(f.calls[before:], " "), "repos") {
		t.Fatalf("g with no row must reload: %v", f.calls[before:])
	}
}

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
		m = update(t, m, logLineMsg{id: "app-1a2b3c/a", seq: m.logSeq, line: fmt.Sprintf("line %d", i)})
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
	m = update(t, m, logLineMsg{id: "app-1a2b3c/a", seq: m.logSeq, line: "from a"})
	m = update(t, m, logLineMsg{id: "app-1a2b3c/b", seq: m.logSeq, line: "from b"})
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

func TestLastLinesStayVisibleAtSmallHeight(t *testing.T) {
	m := newModelWith(t, newFake(), 100, 12)
	m = press(t, m, "enter", "2")
	m.logID = "app-1a2b3c/a"
	for i := 0; i < 20; i++ {
		m = update(t, m, logLineMsg{id: "app-1a2b3c/a", seq: m.logSeq, line: fmt.Sprintf("line %d", i)})
	}
	mustContain(t, m.View(), "line 19")
	m = press(t, m, "3")
	m = update(t, m, diffMsg{id: "app-1a2b3c/a", text: strings.Repeat("+x\n", 30) + "+last\n"})
	for i := 0; i < 6; i++ { // the viewport keymap has no G/End binding; page down to the end
		m = update(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	}
	mustContain(t, m.View(), "+last")
}

func TestStaleStreamOpenIsCancelled(t *testing.T) {
	m := newModel(t, newFake())
	m = press(t, m, "enter")
	m = update(t, m, keyMsg("2")) // claims seq 1 without running the open cmd
	stopped := 0
	m = update(t, m, logsOpenMsg{id: "app-1a2b3c/a", seq: m.logSeq - 1, ch: make(chan string), stop: func() { stopped++ }})
	if stopped != 1 || m.logCh != nil {
		t.Fatalf("a stale open must be stopped and ignored: stopped=%d logCh=%v", stopped, m.logCh)
	}
	ch := make(chan string)
	m = update(t, m, logsOpenMsg{id: "app-1a2b3c/a", seq: m.logSeq, ch: ch, stop: func() { stopped++ }})
	if m.logCh == nil {
		t.Fatal("the current open must be accepted")
	}
	m = press(t, m, "esc")
	if stopped != 2 || m.logCh != nil {
		t.Fatalf("leaving must stop the live stream: stopped=%d", stopped)
	}
}

func TestFooterHelpFitsAt80Cols(t *testing.T) {
	m := newModelWith(t, newFake(), 80, 24)
	lines := strings.Split(m.View(), "\n")
	footer := lines[len(lines)-1]
	for _, want := range []string{"? help", "q quit", "n new", "enter open"} {
		if !strings.Contains(footer, want) {
			t.Fatalf("footer at 80 cols lacks %q: %q", want, footer)
		}
	}
	if lipgloss.Width(footer) > 80 {
		t.Fatalf("footer wider than the terminal: %d", lipgloss.Width(footer))
	}
}

func TestLogStreamEndIsShownAndReopenedOnReconnect(t *testing.T) {
	f := newFake()
	m := newModel(t, f)
	m = press(t, m, "enter", "2") // the fake stream ends at once → logID cleared
	mustContain(t, m.View(), "stream ended")
	before := len(f.calls)
	m = update(t, m, subscribedMsg{ch: make(chan client.Event)}) // returned cmd not run: it includes waitEvent
	if m.logID == "" {
		t.Fatal("resubscribing while on the logs tab must reopen the stream")
	}
	_ = before
}

func TestOverviewScrollsInsteadOfClipping(t *testing.T) {
	f := newFake()
	b := f.boxes["app-1a2b3c/a"]
	b.Env = map[string]string{}
	for i := 0; i < 12; i++ {
		b.Env[fmt.Sprintf("VAR_%02d", i)] = "v"
	}
	f.boxes[b.ID] = b
	m := newModelWith(t, f, 80, 16)
	m = press(t, m, "enter")
	mustNotContain(t, m.View(), "VAR_11=v")
	for i := 0; i < 20; i++ {
		m = press(t, m, "j")
	}
	mustContain(t, m.View(), "VAR_11=v")
}

func TestReloadPrunesStaleSteps(t *testing.T) {
	f := newFake()
	m := newModel(t, f)
	m = update(t, m, eventMsg{client.Event{Type: "box.step", Box: "app-1a2b3c/a", Data: map[string]any{"step": "dev"}}})
	m = update(t, m, eventMsg{client.Event{Type: "box.step", Box: "app-1a2b3c/gone", Data: map[string]any{"step": "setup"}}})
	bs, _ := f.ListBoxes(context.Background(), "")
	m = update(t, m, boxesMsg{boxes: bs})
	if len(m.steps) != 0 {
		t.Fatalf("stale steps survived the reload: %v", m.steps)
	}
	mustNotContain(t, m.View(), "dev…")
}
