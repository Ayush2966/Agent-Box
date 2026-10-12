# agentbox Phase 5 — Desktop app (Wails) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `Agentbox.app` opens the phase 2 dashboard in a native window, signed in, live, and starts the daemon itself.

**Architecture:** The pure Go pieces live untagged in `internal/desktop` (daemon bootstrap, an authenticating reverse proxy that is the Wails asset handler, and an event/log bridge that pumps the daemon's streams into Wails events). `cmd/agentbox-desktop` (build tag `desktop`, CGO) wires `wails.Run` with that proxy and bridge and doubles as the CLI when given arguments, so `daemon.EnsureRunning` can spawn `<self> daemon run`. The React app gains a Wails branch in its streaming helper and an `openExternal` helper; nothing else changes.

**Tech Stack:** Go 1.24 (directive stays `1.24.2`), `github.com/wailsapp/wails/v2 v2.12.0` (pinned; needs CGO + macOS Command Line Tools + `CGO_LDFLAGS="-framework UniformTypeIdentifiers"`), React 18 / Vitest as before.

**Spec:** `docs/superpowers/specs/2026-10-10-agentbox-phase5-design.md` — read it first (§1.1 holds the spike findings this plan relies on).

## Global Constraints

- Dependencies only via `go get github.com/wailsapp/wails/v2@v2.12.0` (Task 3, first use). **Never `go mod tidy`**, never a bare `go get`. After the `go get`, `grep '^go ' go.mod` must still print `go 1.24.2`.
- `git commit` is never run (standing rule). "Checkpoint" = run the verification, record it in the ledger.
- `go build ./...`, `go vet ./...` and `go test ./...` must stay CGO-free and unaffected: every file in `cmd/agentbox-desktop/` carries `//go:build desktop`; `internal/desktop` has no build tag and imports neither Wails nor cgo.
- The proxy forwards only to a loopback `api_addr`; it injects `Authorization: Bearer <token>` and drops `Origin` and `Cookie`; it sets the outgoing `Host` to the api address (the daemon's Host allowlist).
- Wails event names: `agentbox:event` (daemon events, same JSON shape as SSE), `agentbox:conn` (`"live"` / `"reconnecting"`), `agentbox:log:<id>` (one string per line), `agentbox:log:<id>:end`.
- Bound struct: `App` in package `main` with `StartLogs(repo, name string) (string, error)`, `StopLogs(id string)` and `ConnState() string` (frontend calls `window.go.main.App.StartLogs` / `ConnState`).
- macOS is the tested target; the `.app` is built by `make desktop` and by a `macos-latest` CI job, not by GoReleaser.

## Review Focus

1. **The daemon is already running from an older `agentbox` binary** — the app must not fight it: `EnsureRunning` pings the socket first and only spawns when nothing answers. *(Task 1: `TestBootstrapUsesRunningDaemon`.)*
2. **The daemon's HTTP API is disabled (port in use)** — `api_addr` never appears; the app must fail with a message naming the log file, not hang. *(Task 1: `TestBootstrapFailsWhenAPIDisabled`.)*
3. **A second `StartLogs` for the same box while the first streams** — the frontend closes the old one; the bridge must keep both ids distinct and stop only the one asked. *(Task 1: `TestBridgeLogStreamsAreIndependent`.)*
4. **The dashboard's `Sign out` form inside the app** — it would clear a cookie that does not exist and reload into a proxied page; hide it in the app. *(Task 2: `TopBar` renders no sign-out form when `wails()` is truthy — covered by the vitest helper test on `wails()` plus a code check in review.)*
5. **An `api_addr` that is not loopback** (someone edited the file) — the proxy must refuse at construction. *(Task 1: `TestProxyRefusesNonLoopback`.)*

---

## File structure

```
internal/desktop/bootstrap.go       Bootstrap(home, ensure) → apiAddr, token        (Task 1)
internal/desktop/proxy.go           NewProxy(apiAddr, token) http.Handler            (Task 1)
internal/desktop/bridge.go          Bridge: Run, StartLogs, StopLogs, Close          (Task 1)
internal/desktop/desktop_test.go                                                      (Task 1)
web/src/api/wails.ts                wails() runtime detection + types                (Task 2)
web/src/api/sse.ts                  Wails branch                                     (Task 2)
web/src/api/sse.test.ts                                                               (Task 2)
web/src/ui/bits.tsx, BoxTable.tsx, BoxPage.tsx, TopBar.tsx   openExternal, no sign-out in app   (Task 2)
cmd/agentbox-desktop/main.go        //go:build desktop                               (Task 3)
desktop/Info.plist.tmpl, desktop/mkapp.sh                                             (Task 3)
Makefile, .gitignore, .github/workflows/ci.yml, README.md                             (Task 3)
```

---

### Task 1: `internal/desktop` — bootstrap, proxy, bridge

**Files:**
- Create: `internal/desktop/bootstrap.go`, `proxy.go`, `bridge.go`
- Test: `internal/desktop/desktop_test.go`

**Interfaces:**
- Consumes: `daemon.Paths`, `daemon.PathsFor`, `daemon.EnsureRunning`, `client.Event`, `client.Client.Events/LogLines` (phase 3).
- Produces (consumed by Task 3):

```go
package desktop
func Bootstrap(home string, ensure func(daemon.Paths) error) (apiAddr, token string, err error)
func NewProxy(apiAddr, token string) (http.Handler, error)
type API interface {
	Events(ctx context.Context) (<-chan client.Event, error)
	LogLines(ctx context.Context, repoID, name string, tail int) (<-chan string, error)
}
type Emitter func(name string, data any)
type Bridge struct{ /* private */ }
func NewBridge(api API, emit Emitter) *Bridge   // reconnect base 1s (tests set b.base)
func (b *Bridge) Run(ctx context.Context)       // blocks until ctx is done
func (b *Bridge) StartLogs(repo, name string) (string, error)
func (b *Bridge) StopLogs(id string)
func (b *Bridge) Close()
```

- [ ] **Step 1: Write the failing tests**

Create `internal/desktop/desktop_test.go`:

```go
package desktop

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ayush2966/Agent-Box/internal/client"
	"github.com/Ayush2966/Agent-Box/internal/daemon"
)

func TestBootstrapUsesRunningDaemon(t *testing.T) {
	home := t.TempDir()
	p := daemon.PathsFor(home)
	os.WriteFile(p.APIAddr, []byte("127.0.0.1:7777\n"), 0o600)
	os.WriteFile(p.Token, []byte("tok\n"), 0o600)
	calls := 0
	addr, tok, err := Bootstrap(home, func(daemon.Paths) error { calls++; return nil })
	if err != nil || addr != "127.0.0.1:7777" || tok != "tok" || calls != 1 {
		t.Fatalf("%q %q %v calls=%d", addr, tok, err, calls)
	}
}

func TestBootstrapWaitsForFilesWrittenByTheDaemon(t *testing.T) {
	home := t.TempDir()
	p := daemon.PathsFor(home)
	ensure := func(daemon.Paths) error {
		go func() {
			time.Sleep(150 * time.Millisecond)
			os.WriteFile(p.Token, []byte("tok"), 0o600)
			os.WriteFile(p.APIAddr, []byte("127.0.0.1:50000"), 0o600)
		}()
		return nil
	}
	addr, tok, err := Bootstrap(home, ensure)
	if err != nil || addr != "127.0.0.1:50000" || tok != "tok" {
		t.Fatalf("%q %q %v", addr, tok, err)
	}
}

func TestBootstrapFailsWhenAPIDisabled(t *testing.T) {
	home := t.TempDir()
	p := daemon.PathsFor(home)
	os.WriteFile(p.Token, []byte("tok"), 0o600) // daemon up, but no api_addr: HTTP API disabled
	start := time.Now()
	_, _, err := Bootstrap(home, func(daemon.Paths) error { return nil })
	if err == nil || !strings.Contains(err.Error(), p.Log) || !strings.Contains(err.Error(), "api_addr") {
		t.Fatalf("want an error naming the log and api_addr, got %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("bootstrap must give up within its deadline")
	}
	if _, _, err := Bootstrap(home, func(daemon.Paths) error { return errors.New("spawn failed") }); err == nil || !strings.Contains(err.Error(), "spawn failed") {
		t.Fatalf("ensure errors must surface: %v", err)
	}
}

func TestProxyInjectsTokenAndRewritesRoot(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, fmt.Sprintf("%s %s auth=%q origin=%q cookie=%q host=%s", r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get("Origin"), r.Header.Get("Cookie"), r.Host))
		mu.Unlock()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.WriteHeader(201)
		fmt.Fprint(w, "ok")
	}))
	defer backend.Close()
	addr := strings.TrimPrefix(backend.URL, "http://")
	h, err := NewProxy(addr, "secret")
	if err != nil {
		t.Fatal(err)
	}
	app := httptest.NewServer(h)
	defer app.Close()
	for _, path := range []string{"/", "/index.html", "/v1/boxes?repo=x", "/ui/assets/a.js"} {
		req, _ := http.NewRequest("POST", app.URL+path, strings.NewReader("{}"))
		req.Header.Set("Origin", "wails://wails")
		req.Header.Set("Cookie", "agentbox_session=stale")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 201 || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Frame-Options") != "DENY" {
			t.Fatalf("%s: %d %v", path, resp.StatusCode, resp.Header)
		}
		resp.Body.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		`POST /ui/ auth="Bearer secret" origin="" cookie="" host=` + addr,
		`POST /ui/ auth="Bearer secret" origin="" cookie="" host=` + addr,
		`POST /v1/boxes?repo=x auth="Bearer secret" origin="" cookie="" host=` + addr,
		`POST /ui/assets/a.js auth="Bearer secret" origin="" cookie="" host=` + addr,
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("request %d:\n got %s\nwant %s", i, seen[i], want[i])
		}
	}
}

func TestProxyRefusesNonLoopback(t *testing.T) {
	for _, bad := range []string{"10.0.0.5:7777", "example.com:7777", ":7777", "nonsense"} {
		if _, err := NewProxy(bad, "t"); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, ok := range []string{"127.0.0.1:7777", "localhost:7777", "[::1]:7777"} {
		if _, err := NewProxy(ok, "t"); err != nil {
			t.Errorf("refused %q: %v", ok, err)
		}
	}
}

// fakeAPI hands out events channels and log channels the test controls.
type fakeAPI struct {
	mu     sync.Mutex
	events []chan client.Event
	logs   map[string]chan string
	err    error
}

func (f *fakeAPI) Events(context.Context) (<-chan client.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan client.Event, 8)
	f.events = append(f.events, ch)
	return ch, nil
}

func (f *fakeAPI) LogLines(ctx context.Context, repo, name string, _ int) (<-chan string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	src := make(chan string, 8)
	f.logs[repo+"/"+name] = src
	out := make(chan string)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case l, ok := <-src:
				if !ok {
					return
				}
				select {
				case out <- l:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

type recorder struct {
	mu    sync.Mutex
	items []string
}

func (r *recorder) emit(name string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, fmt.Sprintf("%s=%v", name, data))
}

func (r *recorder) wait(t *testing.T, want string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		r.mu.Lock()
		for _, it := range r.items {
			if strings.Contains(it, want) {
				r.mu.Unlock()
				return
			}
		}
		r.mu.Unlock()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Fatalf("never saw %q in %v", want, r.items)
}

func (r *recorder) count(prefix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, it := range r.items {
		if strings.HasPrefix(it, prefix) {
			n++
		}
	}
	return n
}

func TestBridgePumpsEventsAndReconnects(t *testing.T) {
	f := &fakeAPI{logs: map[string]chan string{}}
	rec := &recorder{}
	b := NewBridge(f, rec.emit)
	b.base = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)
	rec.wait(t, "agentbox:conn=live")
	f.mu.Lock()
	first := f.events[0]
	f.mu.Unlock()
	first <- client.Event{Type: "box.state", Box: "r/a", Data: map[string]any{"state": "running"}}
	rec.wait(t, "agentbox:event={box.state r/a map[state:running]}")
	close(first) // the daemon went away
	rec.wait(t, "agentbox:conn=reconnecting")
	rec.wait(t, "agentbox:conn=live") // and came back
	if rec.count("agentbox:conn=live") < 2 {
		t.Fatalf("expected a second live after reconnect: %v", rec.items)
	}
	cancel()
}

func TestBridgeLogStreamsAreIndependent(t *testing.T) {
	f := &fakeAPI{logs: map[string]chan string{}}
	rec := &recorder{}
	b := NewBridge(f, rec.emit)
	id1, err := b.StartLogs("r", "a")
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	src1 := f.logs["r/a"]
	f.mu.Unlock()
	src1 <- "first stream line"
	rec.wait(t, "agentbox:log:"+id1+"=first stream line")
	id2, _ := b.StartLogs("r", "a") // the frontend reopened the tab
	if id2 == id1 {
		t.Fatal("ids must differ")
	}
	f.mu.Lock()
	src2 := f.logs["r/a"]
	f.mu.Unlock()
	b.StopLogs(id1)
	src2 <- "second stream line"
	rec.wait(t, "agentbox:log:"+id2+"=second stream line")
	rec.wait(t, "agentbox:log:"+id1+":end")
	if rec.count("agentbox:log:"+id1+"=") != 1 {
		t.Fatalf("the stopped stream must emit nothing more: %v", rec.items)
	}
	b.Close()
	rec.wait(t, "agentbox:log:"+id2+":end")
}
```

(`f.logs[...]` is replaced by the second `StartLogs`; the test reads it between the calls on purpose.)

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/desktop/`
Expected: FAIL — package has no non-test Go files / symbols undefined.

- [ ] **Step 3: `bootstrap.go`**

```go
// Package desktop holds the CGO-free parts of the desktop app: starting the
// daemon, proxying the dashboard with the API token, and bridging the
// daemon's streams into Wails events.
package desktop

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Ayush2966/Agent-Box/internal/daemon"
)

// Bootstrap makes sure a daemon is serving home (ensure is daemon.EnsureRunning
// in production) and returns its HTTP API address and token. It waits a few
// seconds for a freshly spawned daemon to write them.
func Bootstrap(home string, ensure func(daemon.Paths) error) (apiAddr, token string, err error) {
	p := daemon.PathsFor(home)
	if err := ensure(p); err != nil {
		return "", "", err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		a, aerr := os.ReadFile(p.APIAddr)
		t, terr := os.ReadFile(p.Token)
		apiAddr, token = strings.TrimSpace(string(a)), strings.TrimSpace(string(t))
		if aerr == nil && terr == nil && apiAddr != "" && token != "" {
			return apiAddr, token, nil
		}
		if time.Now().After(deadline) {
			return "", "", fmt.Errorf("the agentbox daemon is running but its HTTP API is not reachable: %s is missing — the port in api_addr (%s) is probably in use; see %s",
				p.APIAddr, p.Config, p.Log)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
```

- [ ] **Step 4: `proxy.go`**

```go
package desktop

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// NewProxy is the Wails asset handler: every request from the window goes to
// the daemon's HTTP API with the token injected, so the dashboard loads
// signed in and its relative fetches just work. The window's own origin
// (wails://) is dropped so the daemon's Origin allowlist is satisfied by the
// Bearer path, and the outgoing Host is the daemon's address for its Host
// allowlist.
func NewProxy(apiAddr, token string) (http.Handler, error) {
	host, _, err := net.SplitHostPort(apiAddr)
	if err != nil || host == "" {
		return nil, fmt.Errorf("bad api_addr %q: %v", apiAddr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("refusing to proxy to a non-loopback address %q", apiAddr)
	}
	target := &url.URL{Scheme: "http", Host: apiAddr}
	return &httputil.ReverseProxy{
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = apiAddr
			pr.Out.Header.Del("Origin")
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Set("Authorization", "Bearer "+token)
			if pr.Out.URL.Path == "/" || pr.Out.URL.Path == "/index.html" {
				pr.Out.URL.Path, pr.Out.URL.RawPath = "/ui/", ""
			}
		},
	}, nil
}
```

- [ ] **Step 5: `bridge.go`**

```go
package desktop

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Ayush2966/Agent-Box/internal/client"
)

// API is the slice of the daemon client the bridge streams from.
type API interface {
	Events(ctx context.Context) (<-chan client.Event, error)
	LogLines(ctx context.Context, repoID, name string, tail int) (<-chan string, error)
}

// Emitter delivers an event to the window (runtime.EventsEmit in production).
type Emitter func(name string, data any)

// Bridge turns the daemon's SSE streams into Wails events, because the Wails
// asset server cannot stream a response (spec §1.1).
type Bridge struct {
	api  API
	emit Emitter
	base time.Duration

	mu   sync.Mutex
	seq  int
	logs map[string]context.CancelFunc
}

func NewBridge(api API, emit Emitter) *Bridge {
	return &Bridge{api: api, emit: emit, base: time.Second, logs: map[string]context.CancelFunc{}}
}

// Run pumps daemon events to "agentbox:event" until ctx is done, reconnecting
// with 1s→16s backoff and reporting the connection as "agentbox:conn".
func (b *Bridge) Run(ctx context.Context) {
	backoff := b.base
	for {
		ch, err := b.api.Events(ctx)
		if err == nil {
			b.emit("agentbox:conn", "live")
			backoff = b.base
			for ev := range ch {
				b.emit("agentbox:event", ev)
			}
		}
		if ctx.Err() != nil {
			return
		}
		b.emit("agentbox:conn", "reconnecting")
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 16*b.base {
			backoff *= 2
		}
	}
}

// StartLogs follows a box's log (tail 500) as "agentbox:log:<id>" events, one
// line each, then "agentbox:log:<id>:end". Each call gets its own id.
func (b *Bridge) StartLogs(repo, name string) (string, error) {
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := b.api.LogLines(ctx, repo, name, 500)
	if err != nil {
		cancel()
		return "", err
	}
	b.mu.Lock()
	b.seq++
	id := fmt.Sprintf("%s/%s#%d", repo, name, b.seq)
	b.logs[id] = cancel
	b.mu.Unlock()
	go func() {
		for line := range ch {
			b.emit("agentbox:log:"+id, line)
		}
		b.emit("agentbox:log:"+id+":end", nil)
		b.StopLogs(id)
	}()
	return id, nil
}

func (b *Bridge) StopLogs(id string) {
	b.mu.Lock()
	cancel, ok := b.logs[id]
	delete(b.logs, id)
	b.mu.Unlock()
	if ok {
		cancel()
	}
}

// Close stops every log stream.
func (b *Bridge) Close() {
	b.mu.Lock()
	ids := make([]string, 0, len(b.logs))
	for id := range b.logs {
		ids = append(ids, id)
	}
	b.mu.Unlock()
	for _, id := range ids {
		b.StopLogs(id)
	}
}
```

- [ ] **Step 6: Run the tests**

Run: `gofmt -l internal/desktop; go vet ./internal/desktop/ && go test -race ./internal/desktop/`
Expected: PASS (7 tests). The bridge tests use real goroutines and 1 ms backoff; if `TestBridgePumpsEventsAndReconnects` is flaky, the recorder's `wait` has a 5 s window — raise it rather than weaken the assertions.

- [ ] **Step 7: Checkpoint**

Record "Task 1 complete". Do not commit.

---

### Task 2: Frontend — Wails branch for streams, external links, no sign-out in the app

**Files:**
- Create: `web/src/api/wails.ts`, `web/src/api/sse.test.ts`
- Modify: `web/src/api/sse.ts`, `web/src/ui/bits.tsx`, `web/src/ui/BoxTable.tsx`, `web/src/ui/BoxPage.tsx`, `web/src/ui/TopBar.tsx`

**Interfaces:**
- Consumes the Wails runtime the app injects: `window.runtime.EventsOn(name, cb) → unsubscribe`, `window.runtime.EventsOff(name)`, `window.runtime.BrowserOpenURL(url)`, `window.go.main.App.StartLogs(repo, name) → Promise<string>`, `window.go.main.App.StopLogs(id) → Promise<void>`; event names from Global Constraints.
- Produces: `wails()` detection; `connectSSE` works in both worlds with the same signature; `openExternal(url)`.

- [ ] **Step 1: Write the failing test**

Create `web/src/api/sse.test.ts`:

```ts
import { afterEach, describe, expect, it, vi } from 'vitest'
import { connectSSE } from './sse'
import { wails } from './wails'

type Handler = (data: unknown) => void

function fakeWails() {
  const handlers = new Map<string, Handler>()
  const calls: string[] = []
  const runtime = {
    EventsOn(name: string, cb: Handler) {
      handlers.set(name, cb)
      return () => handlers.delete(name)
    },
    EventsOff(name: string) {
      handlers.delete(name)
    },
    BrowserOpenURL(url: string) {
      calls.push('open ' + url)
    },
  }
  const app = {
    StartLogs: async (repo: string, name: string) => {
      calls.push(`start ${repo} ${name}`)
      return `${repo}/${name}#1`
    },
    StopLogs: async (id: string) => {
      calls.push('stop ' + id)
    },
  }
  ;(globalThis as { window?: unknown }).window = { runtime, go: { main: { App: app } } }
  return { handlers, calls, fire: (name: string, data: unknown) => handlers.get(name)?.(data) }
}

afterEach(() => {
  delete (globalThis as { window?: unknown }).window
})

describe('wails()', () => {
  it('is null outside the app and non-null inside', () => {
    expect(wails()).toBeNull()
    fakeWails()
    expect(wails()).not.toBeNull()
  })
})

describe('connectSSE in the app', () => {
  it('maps the events stream to agentbox:event and agentbox:conn', () => {
    const w = fakeWails()
    const got: string[] = []
    const states: string[] = []
    let reconnects = 0
    const close = connectSSE({
      url: '/v1/events',
      events: ['box.state'],
      onEvent: (type, data) => got.push(type + ':' + JSON.stringify(data)),
      onState: (s) => states.push(s),
      onReconnect: () => reconnects++,
    })
    w.fire('agentbox:conn', 'live')
    w.fire('agentbox:event', { type: 'box.state', box: 'r/a', data: { state: 'running' } })
    w.fire('agentbox:conn', 'reconnecting')
    w.fire('agentbox:conn', 'live')
    expect(got).toEqual(['box.state:{"type":"box.state","box":"r/a","data":{"state":"running"}}'])
    expect(states).toEqual(['connecting', 'open', 'reconnecting', 'open'])
    expect(reconnects).toBe(1)
    close()
    expect(w.handlers.size).toBe(0)
  })

  it('maps a logs url to StartLogs/StopLogs and per-line events', async () => {
    const w = fakeWails()
    const lines: string[] = []
    const close = connectSSE({
      url: '/v1/repos/app-1a2b3c/boxes/feature-x/logs?follow=true&tail=500',
      events: ['line'],
      onEvent: (_t, data) => lines.push(String(data)),
    })
    await vi.waitFor(() => expect(w.calls).toContain('start app-1a2b3c feature-x'))
    w.fire('agentbox:log:app-1a2b3c/feature-x#1', 'listening on 20000')
    expect(lines).toEqual(['listening on 20000'])
    close()
    await vi.waitFor(() => expect(w.calls).toContain('stop app-1a2b3c/feature-x#1'))
    expect(w.handlers.size).toBe(0)
  })
})
```

Run: `cd web && npm run check` — Expected: FAIL (`./wails` cannot be resolved).

- [ ] **Step 2: `wails.ts` and the `sse.ts` branch**

Create `web/src/api/wails.ts`:

```ts
// Detection of the Wails desktop runtime, which the app injects into the window.
export interface WailsRuntime {
  EventsOn(name: string, cb: (data: unknown) => void): () => void
  EventsOff(name: string): void
  BrowserOpenURL(url: string): void
}

export interface WailsApp {
  StartLogs(repo: string, name: string): Promise<string>
  StopLogs(id: string): Promise<void>
}

declare global {
  interface Window {
    runtime?: WailsRuntime
    go?: { main?: { App?: WailsApp } }
  }
}

/** The Wails runtime and bound app, or null in a plain browser. */
export function wails(): { runtime: WailsRuntime; app: WailsApp } | null {
  if (typeof window === 'undefined') return null
  const runtime = window.runtime
  const app = window.go?.main?.App
  return runtime && app ? { runtime, app } : null
}
```

In `web/src/api/sse.ts`, add at the top `import { wails } from './wails'` and, as the first lines of `connectSSE`'s body:

```ts
  const w = wails()
  if (w) return connectWails(w, o)
```

and append:

```ts
const EVENTS_URL = '/v1/events'
const LOGS_URL = /^\/v1\/repos\/([^/]+)\/boxes\/([^/]+)\/logs(\?|$)/

// In the desktop app the asset server cannot stream, so the Go side pumps the
// daemon's streams into Wails events (spec phase 5 §1.1, R4).
function connectWails(w: NonNullable<ReturnType<typeof wails>>, o: SSEOptions): () => void {
  o.onState?.('connecting')
  if (o.url === EVENTS_URL) {
    let wasLive = false
    const offEvent = w.runtime.EventsOn('agentbox:event', (data) => {
      const type = (data as { type?: string })?.type
      if (type && o.events.includes(type)) o.onEvent(type, data)
    })
    const offConn = w.runtime.EventsOn('agentbox:conn', (state) => {
      if (state === 'live') {
        o.onState?.('open')
        if (wasLive) o.onReconnect?.()
        wasLive = true
      } else {
        o.onState?.('reconnecting')
      }
    })
    return () => {
      offEvent()
      offConn()
    }
  }
  const m = LOGS_URL.exec(o.url)
  if (!m) throw new Error('unsupported stream: ' + o.url)
  const repo = decodeURIComponent(m[1])
  const name = decodeURIComponent(m[2])
  let closed = false
  let id: string | null = null
  let off: (() => void) | null = null
  w.app
    .StartLogs(repo, name)
    .then((streamId) => {
      if (closed) {
        void w.app.StopLogs(streamId)
        return
      }
      id = streamId
      o.onState?.('open')
      off = w.runtime.EventsOn('agentbox:log:' + streamId, (line) => o.onEvent('line', line))
    })
    .catch(() => o.onState?.('reconnecting'))
  return () => {
    closed = true
    off?.()
    if (id) void w.app.StopLogs(id)
  }
}
```

- [ ] **Step 3: External links and no sign-out in the app**

In `web/src/ui/bits.tsx` add:

```tsx
import { wails } from '../api/wails'

/** Opens a URL in the user's browser: via Wails inside the app, a new tab otherwise. */
export function openExternal(url: string) {
  const w = wails()
  if (w) w.runtime.BrowserOpenURL(url)
  else window.open(url, '_blank', 'noopener')
}

/** An anchor that opens outside the app window. */
export function ExternalLink({ href, className, children }: { href: string; className?: string; children: ReactNode }) {
  return (
    <a
      className={className}
      href={href}
      target="_blank"
      rel="noreferrer"
      onClick={(e) => {
        e.preventDefault()
        openExternal(href)
      }}
    >
      {children}
    </a>
  )
}
```

Replace every `<a … target="_blank" rel="noreferrer">` in `BoxTable.tsx` (the URL cell) and `BoxPage.tsx` (the "Open app ↗" button and the overview `url` row) with `<ExternalLink href={…} className={…}>…</ExternalLink>` (keep the `button` class on the header one).

In `web/src/ui/TopBar.tsx`, import `wails` and render the sign-out form only when not in the app: `{!wails() && (<form method="post" action="/ui/logout">…</form>)}`.

- [ ] **Step 4: Check and build**

Run: `cd web && npm run check && npm run build`
Expected: tsc clean; Vitest 4 files / 16 tests (13 + 3) pass; the build writes `internal/ui/dist`. Then `go test -race ./internal/ui/` still passes (the embedded build changed, tests use a fake FS).

- [ ] **Step 5: Checkpoint**

Record "Task 2 complete". Do not commit.

---

### Task 3: The Wails app, packaging, CI, README, smoke

**Files:**
- Create: `cmd/agentbox-desktop/main.go`, `desktop/Info.plist.tmpl`, `desktop/mkapp.sh`
- Modify: `Makefile`, `.gitignore`, `.github/workflows/ci.yml`, `README.md`, `go.mod`/`go.sum` (the pinned `go get`)

**Interfaces:**
- Consumes Task 1's `desktop.Bootstrap/NewProxy/NewBridge`, `daemon.DefaultHome/PathsFor/EnsureRunning`, `client.New`, `cli.Execute`; Wails v2.12.0 (`wails.Run`, `options.App`, `assetserver.Options`, `runtime.EventsEmit`, `mac.Options`).

- [ ] **Step 1: Dependency**

```bash
go get github.com/wailsapp/wails/v2@v2.12.0
grep '^go ' go.mod          # go 1.24.2
go build ./... && go vet ./... && go test ./internal/desktop/   # still CGO-free and green
```

(If `go get` pulls a version of `golang.org/x/net`/`x/text` and the directive still reads 1.24.2, fine. If anything moved the directive, STOP and report.)

- [ ] **Step 2: The app**

Create `cmd/agentbox-desktop/main.go`:

```go
//go:build desktop

// agentbox-desktop is the agentbox CLI plus a native window for the dashboard.
// With arguments it is the CLI (so it can spawn itself as the daemon); with
// none it opens the window.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/Ayush2966/Agent-Box/internal/cli"
	"github.com/Ayush2966/Agent-Box/internal/client"
	"github.com/Ayush2966/Agent-Box/internal/daemon"
	"github.com/Ayush2966/Agent-Box/internal/desktop"
)

// App is bound into the window as window.go.main.App.
type App struct{ bridge *desktop.Bridge }

func (a *App) StartLogs(repo, name string) (string, error) {
	if a.bridge == nil {
		return "", fmt.Errorf("not connected yet")
	}
	return a.bridge.StartLogs(repo, name)
}

func (a *App) StopLogs(id string) {
	if a.bridge != nil {
		a.bridge.StopLogs(id)
	}
}

// ConnState lets the window learn the bridge's state when it subscribes,
// since conn events are only emitted on transitions.
func (a *App) ConnState() string {
	if a.bridge == nil {
		return "connecting"
	}
	return a.bridge.State()
}

func main() {
	if len(os.Args) > 1 {
		os.Exit(cli.Execute())
	}
	home, err := daemon.DefaultHome()
	if err != nil {
		fatal(err)
	}
	apiAddr, token, err := desktop.Bootstrap(home, daemon.EnsureRunning)
	if err != nil {
		fatal(err)
	}
	proxy, err := desktop.NewProxy(apiAddr, token)
	if err != nil {
		fatal(err)
	}
	c := client.New(daemon.PathsFor(home).Sock)
	app := &App{}
	err = wails.Run(&options.App{
		Title:     "agentbox",
		Width:     1200,
		Height:    800,
		MinWidth:  900,
		MinHeight: 600,
		AssetServer: &assetserver.Options{Handler: proxy},
		Bind:        []interface{}{app},
		OnStartup: func(ctx context.Context) {
			app.bridge = desktop.NewBridge(c, func(name string, data any) { runtime.EventsEmit(ctx, name, data) })
			go app.bridge.Run(ctx)
		},
		OnShutdown: func(context.Context) {
			if app.bridge != nil {
				app.bridge.Close()
			}
		},
		Mac: &mac.Options{About: &mac.AboutInfo{Title: "agentbox", Message: "Isolated dev environments for parallel AI coding agents"}},
	})
	if err != nil {
		fatal(err)
	}
}

// fatal reports a startup failure where a desktop user can see it, then exits.
func fatal(err error) {
	fmt.Fprintln(os.Stderr, "agentbox-desktop:", err)
	_ = exec.Command("osascript", "-e", fmt.Sprintf(`display dialog %q buttons {"OK"} default button 1 with title "agentbox" with icon stop`, err.Error())).Run()
	os.Exit(1)
}
```

- [ ] **Step 3: Packaging**

Create `desktop/Info.plist.tmpl`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>agentbox</string>
  <key>CFBundleDisplayName</key><string>agentbox</string>
  <key>CFBundleIdentifier</key><string>dev.agentbox.desktop</string>
  <key>CFBundleExecutable</key><string>agentbox-desktop</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleVersion</key><string>__VERSION__</string>
  <key>CFBundleShortVersionString</key><string>__VERSION__</string>
  <key>LSMinimumSystemVersion</key><string>12.0</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>NSHumanReadableCopyright</key><string>MIT</string>
</dict>
</plist>
```

Create `desktop/mkapp.sh`:

```sh
#!/bin/sh
# usage: mkapp.sh <binary> <out.app> [version]  — assembles a minimal macOS bundle.
set -eu
bin=$1; app=$2; version=${3:-dev}
rm -rf "$app"
mkdir -p "$app/Contents/MacOS"
cp "$bin" "$app/Contents/MacOS/agentbox-desktop"
chmod +x "$app/Contents/MacOS/agentbox-desktop"
sed "s/__VERSION__/$version/g" "$(dirname "$0")/Info.plist.tmpl" > "$app/Contents/Info.plist"
echo "built $app"
```

`chmod +x desktop/mkapp.sh`.

In `Makefile` add:

```make
## desktop: build build/Agentbox.app (macOS only; needs Node 20+ and the Xcode Command Line Tools)
desktop: ui
	mkdir -p build
	CGO_ENABLED=1 CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -tags desktop,production -ldflags "-s -w -X github.com/Ayush2966/Agent-Box/internal/cli.Version=$$(git describe --tags --always 2>/dev/null || echo dev)" -o build/agentbox-desktop ./cmd/agentbox-desktop
	sh desktop/mkapp.sh build/agentbox-desktop build/Agentbox.app "$$(git describe --tags --always 2>/dev/null || echo dev)"
```

(add `desktop` to `.PHONY`). In `.gitignore` add `/build/`.

- [ ] **Step 4: CI and README**

In `.github/workflows/ci.yml` add a job:

```yaml
  desktop:
    runs-on: macos-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.24' }
      - uses: actions/setup-node@v4
        with: { node-version: '22', cache: npm, cache-dependency-path: web/package-lock.json }
      - run: go test -race ./internal/desktop/...
      - run: make desktop
      - run: CGO_ENABLED=1 CGO_LDFLAGS="-framework UniformTypeIdentifiers" go vet -tags desktop,production ./cmd/agentbox-desktop/
      - run: cd build && zip -qr Agentbox.app.zip Agentbox.app
      - uses: actions/upload-artifact@v4
        with: { name: Agentbox.app, path: build/Agentbox.app.zip }
```

Validate with `ruby -ryaml -e 'YAML.load_file(".github/workflows/ci.yml")'`.

In `README.md` add, after the Docker Compose section:

```markdown
## Desktop app

`make desktop` builds `build/Agentbox.app` (macOS; needs Node 20+ and the Xcode Command Line Tools —
the app is a native window around the same dashboard, so no second UI to maintain). It starts the
daemon itself and opens signed in; box links open in your browser. The app binary is also the full
`agentbox` CLI (`Agentbox.app/Contents/MacOS/agentbox-desktop ls` works). It is unsigned: on first
launch, right-click → Open. CI attaches the `.app` zip to every run; releases do not include it yet.
```

and change the Roadmap line to: `All five phases are in: core + CLI, web dashboard + proxy, terminal UI, Compose + MySQL, desktop app.`

- [ ] **Step 5: Build, verify, smoke**

```bash
gofmt -l internal cmd; go vet ./... && go test -race -count=1 ./...        # CGO-free suite unchanged
make desktop                                                                # builds build/Agentbox.app
CGO_ENABLED=1 CGO_LDFLAGS="-framework UniformTypeIdentifiers" go vet -tags desktop,production ./cmd/agentbox-desktop/
build/agentbox-desktop version                                               # the CLI entry: prints the version
```

Smoke (spec R7), against a throwaway home:

```bash
export AGENTBOX_HOME=/tmp/abdesk && rm -rf $AGENTBOX_HOME
( build/agentbox-desktop & echo $! > /tmp/abdesk.pid )      # opens a window on this Mac for a few seconds
for i in $(seq 1 60); do [ -f $AGENTBOX_HOME/api_addr ] && break; sleep 0.5; done
cat $AGENTBOX_HOME/api_addr                                   # the daemon the app spawned
TOKEN=$(cat $AGENTBOX_HOME/token); curl -s -H "Authorization: Bearer $TOKEN" http://$(cat $AGENTBOX_HOME/api_addr)/v1/health
sleep 5; kill $(cat /tmp/abdesk.pid) 2>/dev/null; sleep 1
build/agentbox-desktop daemon stop; rm -rf $AGENTBOX_HOME
```

Expected: `api_addr` appears within seconds (the app spawned `agentbox-desktop daemon run`), health answers `{"ok":true,…,"ui":true}`, the app process exits on kill, the daemon stops. The proxy and bridge were unit-tested in Task 1; if the window itself cannot be observed (headless session), say so.

- [ ] **Step 6: Checkpoint**

Record "Task 3 complete" with the build size, the smoke transcript and the go directive. Do not commit.

---

## Self-review (done while writing)

- **Spec coverage:** §4 R1 (v2.12.0, no CLI) → T3 step 1 + Makefile; R2 (tagged cmd, untagged internal) → T1/T3; R3 (proxy as asset handler) → T1 `NewProxy`, T3 `AssetServer`; R4 (events bridge) → T1 `Bridge`, T2 `connectWails`; R5 (binary is the CLI) → T3 `main`; R6 (macOS CI artifact) → T3 CI; R7 (launch smoke) → T3 step 5. §6 proxy/bridge/frontend behaviours → T1/T2; §7 build/packaging → T3; §8 tests → T1 (7), T2 (3 vitest), T3 (vet + smoke).
- **Placeholders:** none.
- **Type consistency:** `desktop.Bootstrap(home, ensure)`, `NewProxy`, `NewBridge/Run/StartLogs/StopLogs/Close` (T1) used by T3 with the same signatures; event names identical in T1 `bridge.go`, T2 `sse.ts` and the Global Constraints; `App` method names match `window.go.main.App.StartLogs/StopLogs` in T2's `wails.ts`.
- **Review Focus:** 1 → `TestBootstrapUsesRunningDaemon`; 2 → `TestBootstrapFailsWhenAPIDisabled`; 3 → `TestBridgeLogStreamsAreIndependent`; 4 → `wails()` test + TopBar change reviewed; 5 → `TestProxyRefusesNonLoopback`.
