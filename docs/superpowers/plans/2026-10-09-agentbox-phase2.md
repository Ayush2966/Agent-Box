# agentbox Phase 2 — Web Dashboard + Reverse Proxy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every box becomes reachable at `http://<box>.localhost:7777/`, and `agentbox ui` opens an embedded React dashboard that shows, creates, controls and inspects boxes live over the phase 1 API.

**Architecture:** The daemon's single TCP listener dispatches on the `Host` header: `*.localhost` hosts go to a new `internal/proxy` package (stdlib `httputil.ReverseProxy` to the box's port, HTML pages for every non-running state), loopback hosts go to the API plus a new `internal/ui` package that serves a Vite build embedded with `go:embed` and handles a one-shot login-code → cookie flow. The API's auth accepts a Bearer token or the session cookie (with Origin/Sec-Fetch-Site rules for writes). The React app (`web/`) is plain React + TypeScript with a small reducer store fed by the existing `/v1/events` SSE stream.

**Tech Stack:** Go 1.24 (stdlib `net/http/httputil`, `html/template`, `embed`, `testing/fstest`), React 18.3.1, TypeScript 5.6, Vite 5.4, Vitest 2.1. No new Go dependencies.

**Spec:** `docs/superpowers/specs/2026-10-09-agentbox-phase2-design.md` — every task below cites the section it implements; read the spec first.

## Global Constraints

- Go **1.24.0** exactly as pinned in `go.mod`. Never run a bare `go get`; the local toolchain is go1.24.2 and unpinned fetches rewrite the `go` directive upward (see the phase 1 ledger). This plan adds **no** Go dependencies.
- `go test ./...` and `go build ./cmd/agentbox` must pass **without Node installed** and without a dashboard build present (`internal/ui/dist/` holds only `.gitkeep` in git). Tests inject `fstest.MapFS` builds.
- The dashboard build output (`internal/ui/dist/*`) is **not committed**; only `internal/ui/dist/.gitkeep` is.
- `git commit` is **never** run by this plan (the user's standing rule: commit only when asked). Every "Commit" step below is a checkpoint: run the verification command and record the result in the ledger instead.
- Unix socket paths are capped at 103 bytes on macOS: every test that starts a daemon puts its home under `/tmp` (use `os.MkdirTemp("/tmp", …)`), never `t.TempDir()`.
- The TCP listener stays on loopback. The proxy forwards only to `127.0.0.1:<port>`.
- Session cookie: name `agentbox_session`, value = API token, `Path=/; HttpOnly; SameSite=Lax`, no `Secure`, no `Domain` (spec §6.2).
- Box hostnames: `<name>.localhost` and `<name>.<repo-slug>.localhost`, matched case-insensitively, port ignored (spec §5.1).
- Every HTML page the Go side renders goes through `html/template` (spec §13: box names and log lines are escaped).
- Node versions: CI uses Node 22 (already in `ci.yml`); `package.json` pins exact dependency versions.

## Review Focus

Inputs the spec implies but no task's tests would otherwise exercise; each one's pinning test is added to the owning task below.

1. **A box name that is a valid hostname but appears in two repos, requested by bare `<name>.localhost`** — the user expects a page that tells them which qualified host to use, not a silently picked box. *(Task 3: `TestProxyTargetAmbiguousAcrossRepos`; Task 2: `TestAmbiguousPageListsQualifiedHosts`.)*
2. **A `Host` header that merely *contains* `localhost`, such as `evil.localhost.example:7777`** — must be rejected (421), not routed to the proxy. *(Task 6: `TestDispatchRejectsForeignHosts` includes it.)*
3. **A login code pasted twice, or after the daemon restarted** — second use must fail closed with a page saying to run `agentbox ui`, and the browser must never be left half signed in. *(Task 4: `TestLoginCodeIsSingleUseAndExpires`.)*
4. **A dev server that is `running` by state but refusing connections for a moment (crash between supervisor restarts)** — the user expects an explanatory 502 with the last log lines, not a blank browser error. *(Task 2: `TestBadGatewayPageShowsLogTail`.)*
5. **A cookie-authenticated `DELETE`/`POST` arriving without an `Origin` header** (a non-browser client replaying a stolen cookie, or a browser quirk) — must be refused with 403, while the same request with a Bearer token succeeds. *(Task 5: `TestSessionCookieWritesNeedOrigin`.)*

---

## File structure

```
internal/config/config.go            + Proxy field, ProxyPort(), validation           (Task 1)
internal/proxy/proxy.go              Handler, Target, errors, host parsing              (Task 2)
internal/proxy/pages.go              html/template pages (404/300/503/502)             (Task 2)
internal/box/proxy.go                Manager.ProxyTarget                               (Task 3)
internal/ui/ui.go                    Handler, Dist, Built, serveApp                    (Task 4)
internal/ui/codes.go                 LoginCodes                                        (Task 4)
internal/ui/session.go               cookie helpers                                    (Task 4)
internal/ui/pages.go                 403 sign-in page, 503 not-built page              (Task 4)
internal/ui/dist/.gitkeep            embed anchor                                      (Task 4)
internal/api/types.go                + HealthView, LoginCode, BoxView.URL              (Task 5)
internal/api/server.go               auth w/ cookie, health, login-codes, UI mount     (Task 5)
internal/daemon/daemon.go            api_addr file, Dispatch, wiring, UIFS, Version    (Task 6)
internal/client/client.go            + HealthInfo, LoginCode                           (Task 7)
internal/cli/ui_cmd.go               `agentbox ui`                                     (Task 7)
internal/cli/print.go                `url:` line                                       (Task 7)
web/                                 Vite + React app                                  (Tasks 8, 9)
internal/initcfg/initcfg.go          `# proxy:` hint                                   (Task 10)
Makefile, .github/workflows/*.yml, .goreleaser.yaml, README.md, e2e/e2e_test.go      (Task 10)
```

---

### Task 1: `proxy:` config key

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `Config.Proxy string` (yaml `proxy`); `func (c *Config) ProxyPort() string` — the name of the port env var the proxy forwards to: `proxy` if set, else `ports[0]`, else `""`. Task 3 consumes `ProxyPort()`.

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go` (add `"strings"` to its imports if it is not already there):

```go
func TestProxyKey(t *testing.T) {
	c, err := Parse([]byte("version: 1\nports: [APP_PORT, API_PORT]\nproxy: API_PORT\n"))
	if err != nil || c.Proxy != "API_PORT" || c.ProxyPort() != "API_PORT" {
		t.Fatalf("explicit proxy: %+v %v", c, err)
	}
	c, err = Parse([]byte("version: 1\nports: [APP_PORT, API_PORT]\n"))
	if err != nil || c.ProxyPort() != "APP_PORT" {
		t.Fatalf("default proxy should be the first port: %+v %v", c, err)
	}
	c, err = Parse([]byte("version: 1\n"))
	if err != nil || c.ProxyPort() != "" {
		t.Fatalf("no ports: %q %v", c.ProxyPort(), err)
	}
	_, err = Parse([]byte("version: 1\nports: [APP_PORT]\nproxy: WEB_PORT\n"))
	if err == nil || !strings.Contains(err.Error(), "proxy") || !strings.Contains(err.Error(), "WEB_PORT") {
		t.Fatalf("proxy not in ports: %v", err)
	}
	_, err = Parse([]byte("version: 1\nports: [APP_PORT]\nproxi: APP_PORT\n"))
	if err == nil || !strings.Contains(err.Error(), `did you mean "proxy"`) {
		t.Fatalf("typo suggestion: %v", err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/config/ -run TestProxyKey`
Expected: FAIL — `c.Proxy undefined` / `c.ProxyPort undefined` (compile error).

- [ ] **Step 3: Implement**

In `internal/config/config.go`:

1. Add the field to `Config`, between `Ports` and `DB`:

```go
	Ports   []string `yaml:"ports"`
	Proxy   string   `yaml:"proxy"`
	DB      *DB      `yaml:"db"`
```

2. Add the method after `func (d *DB) Path()`:

```go
// ProxyPort names the port env var that http://<box>.localhost forwards to:
// `proxy` when set, else the first entry of `ports`, else "" (no ports).
func (c *Config) ProxyPort() string {
	if c.Proxy != "" {
		return c.Proxy
	}
	if len(c.Ports) > 0 {
		return c.Ports[0]
	}
	return ""
}
```

3. Teach `knownKeys` the key:

```go
	"":    {"version", "setup", "dev", "ports", "proxy", "db", "env", "copy"},
```

4. In `validate()`, right after the `for _, p := range c.Ports { … }` loop:

```go
	if c.Proxy != "" && !seen[c.Proxy] {
		return fmt.Errorf("%s: proxy: %q is not listed in ports", FileName, c.Proxy)
	}
```

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/config/`
Expected: PASS (all, including `TestProxyKey`).

- [ ] **Step 5: Checkpoint**

Run `go vet ./internal/config/ && go test ./internal/config/` and record "Task 1 complete" in the ledger. Do not commit.

---

### Task 2: `internal/proxy` — the reverse proxy and its pages

**Files:**
- Create: `internal/proxy/proxy.go`
- Create: `internal/proxy/pages.go`
- Test: `internal/proxy/proxy_test.go`

**Interfaces:**
- Produces (consumed by Task 3 and Task 6):

```go
package proxy

type Target struct {
	Repo, Name string
	State      string               // running | ready | stopped | failed | creating | removing
	LastError  string
	Port       int                  // the dev server's port on 127.0.0.1
	Tail       func(n int) []string // last n log lines (for the 502 page); may be nil
}
type NoSuchBoxError struct{ Name string; Candidates []string } // Candidates: hosts, e.g. "a.localhost"
type AmbiguousError struct{ Name string; Hosts []string }      // Hosts: "<name>.<slug>.localhost"
type NoPortsError struct{ Name string }
type Options struct {
	Lookup    func(name, repo string) (Target, error) // repo == "" for a bare <name>.localhost
	Dashboard string                                   // e.g. "http://127.0.0.1:7777/ui/"
}
func New(o Options) *Handler            // *Handler implements http.Handler
func IsProxyHost(host string) bool      // true when the Host header ends in ".localhost"
```

- [ ] **Step 1: Write the failing tests**

Create `internal/proxy/proxy_test.go`:

```go
package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// backend answers with the Host and X-Forwarded-Host it saw, and speaks a
// minimal "websocket" (any Upgrade request gets a 101 and a line echo).
func backend(t *testing.T) (*httptest.Server, int) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "no hijack", 500)
				return
			}
			c, buf, err := hj.Hijack()
			if err != nil {
				return
			}
			defer c.Close()
			buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			buf.Flush()
			line, _ := buf.ReadString('\n')
			buf.WriteString("echo: " + line)
			buf.Flush()
			return
		}
		fmt.Fprintf(w, "host=%s xfh=%s path=%s", r.Host, r.Header.Get("X-Forwarded-Host"), r.URL.Path)
	}))
	t.Cleanup(ts.Close)
	_, portStr, _ := net.SplitHostPort(strings.TrimPrefix(ts.URL, "http://"))
	var port int
	fmt.Sscan(portStr, &port)
	return ts, port
}

func handlerFor(targets map[string]Target, errs map[string]error) *Handler {
	return New(Options{
		Dashboard: "http://127.0.0.1:7777/ui/",
		Lookup: func(name, repo string) (Target, error) {
			key := name
			if repo != "" {
				key = name + "." + repo
			}
			if err, ok := errs[key]; ok {
				return Target{}, err
			}
			if t, ok := targets[key]; ok {
				return t, nil
			}
			return Target{}, &NoSuchBoxError{Name: name, Candidates: []string{"a.localhost", "b.localhost"}}
		},
	})
}

func get(t *testing.T, h http.Handler, host, path string) (int, string, http.Header) {
	t.Helper()
	req := httptest.NewRequest("GET", "http://placeholder"+path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), rec.Header()
}

func TestRunningBoxIsProxied(t *testing.T) {
	_, port := backend(t)
	h := handlerFor(map[string]Target{"a": {Repo: "r1", Name: "a", State: "running", Port: port}}, nil)
	code, body, _ := get(t, h, "A.LOCALHOST:7777", "/hello?x=1")
	if code != 200 || !strings.Contains(body, "host=A.LOCALHOST:7777") || !strings.Contains(body, "xfh=A.LOCALHOST:7777") || !strings.Contains(body, "path=/hello") {
		t.Fatalf("%d %q", code, body)
	}
}

func TestQualifiedHostRoutesByRepo(t *testing.T) {
	_, port := backend(t)
	h := handlerFor(map[string]Target{"a.myapp-1a2b3c": {Repo: "myapp-1a2b3c", Name: "a", State: "running", Port: port}}, nil)
	if code, body, _ := get(t, h, "a.myapp-1a2b3c.localhost", "/"); code != 200 || !strings.Contains(body, "path=/") {
		t.Fatalf("%d %q", code, body)
	}
	if code, body, _ := get(t, h, "x.y.z.localhost", "/"); code != 404 || !strings.Contains(body, "not a box address") {
		t.Fatalf("three labels: %d %q", code, body)
	}
}

func TestStoppedBoxGets503Page(t *testing.T) {
	h := handlerFor(map[string]Target{"a": {Repo: "r1", Name: "a", State: "stopped", LastError: "exit status 1", Port: 1}}, nil)
	code, body, hdr := get(t, h, "a.localhost", "/")
	if code != 503 || !strings.Contains(body, "stopped") || !strings.Contains(body, "agentbox start a") || !strings.Contains(body, "exit status 1") {
		t.Fatalf("%d %q", code, body)
	}
	if !strings.Contains(body, "http://127.0.0.1:7777/ui/#/r/r1/b/a") {
		t.Errorf("no dashboard link: %q", body)
	}
	if !strings.Contains(hdr.Get("Content-Type"), "text/html") {
		t.Errorf("content-type %q", hdr.Get("Content-Type"))
	}
}

func TestCreatingBoxRefreshes(t *testing.T) {
	h := handlerFor(map[string]Target{"a": {Repo: "r1", Name: "a", State: "creating"}}, nil)
	code, body, _ := get(t, h, "a.localhost", "/")
	if code != 503 || !strings.Contains(body, "being created") || !strings.Contains(body, `http-equiv="refresh"`) {
		t.Fatalf("%d %q", code, body)
	}
}

func TestUnknownBoxListsCandidates(t *testing.T) {
	h := handlerFor(nil, nil)
	code, body, _ := get(t, h, "nope.localhost", "/")
	if code != 404 || !strings.Contains(body, "no box called") || !strings.Contains(body, `href="http://a.localhost`) || !strings.Contains(body, `href="http://b.localhost`) {
		t.Fatalf("%d %q", code, body)
	}
}

func TestAmbiguousPageListsQualifiedHosts(t *testing.T) {
	h := handlerFor(nil, map[string]error{"a": &AmbiguousError{Name: "a", Hosts: []string{"a.app-111111.localhost", "a.app-222222.localhost"}}})
	code, body, _ := get(t, h, "a.localhost:7777", "/")
	if code != 300 || !strings.Contains(body, `href="http://a.app-111111.localhost:7777/"`) || !strings.Contains(body, `href="http://a.app-222222.localhost:7777/"`) {
		t.Fatalf("%d %q", code, body)
	}
}

func TestNoPortsPage(t *testing.T) {
	h := handlerFor(nil, map[string]error{"a": &NoPortsError{Name: "a"}})
	if code, body, _ := get(t, h, "a.localhost", "/"); code != 404 || !strings.Contains(body, "no ports") {
		t.Fatalf("%d %q", code, body)
	}
}

func TestBadGatewayPageShowsLogTail(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close() // nothing listens here now
	h := handlerFor(map[string]Target{"a": {Repo: "r1", Name: "a", State: "running", Port: port,
		Tail: func(n int) []string { return []string{"Error: listen EADDRINUSE <b>", "crashed"} }}}, nil)
	code, body, _ := get(t, h, "a.localhost", "/")
	if code != 502 || !strings.Contains(body, "crashed") || !strings.Contains(body, "EADDRINUSE &lt;b&gt;") {
		t.Fatalf("%d %q", code, body)
	}
}

func TestPagesEscapeBoxNames(t *testing.T) {
	h := handlerFor(map[string]Target{"a": {Repo: "r1", Name: "<script>", State: "stopped"}}, nil)
	_, body, _ := get(t, h, "a.localhost", "/")
	if strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("unescaped name: %q", body)
	}
}

func TestWebSocketUpgradeIsRelayed(t *testing.T) {
	_, port := backend(t)
	h := handlerFor(map[string]Target{"a": {Repo: "r1", Name: "a", State: "running", Port: port}}, nil)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET /ws HTTP/1.1\r\nHost: a.localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 101 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	fmt.Fprint(conn, "ping\n")
	line, err := br.ReadString('\n')
	if err != nil || line != "echo: ping\n" {
		t.Fatalf("%q %v", line, err)
	}
}

func TestIsProxyHost(t *testing.T) {
	for host, want := range map[string]bool{"a.localhost": true, "A.LOCALHOST:7777": true, "a.repo-1a2b3c.localhost:7777": true,
		"localhost": false, "localhost:7777": false, "127.0.0.1:7777": false, "evil.localhost.example:7777": false, "[::1]:7777": false} {
		if got := IsProxyHost(host); got != want {
			t.Errorf("IsProxyHost(%q) = %v, want %v", host, got, want)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/proxy/`
Expected: FAIL — package does not exist / `New`, `Target` undefined.

- [ ] **Step 3: Implement `proxy.go`**

Create `internal/proxy/proxy.go`:

```go
// Package proxy serves every running box at http://<box>.localhost:<port>/ by
// forwarding to the box's dev server, and explains every other state with a
// small HTML page. It knows nothing about boxes beyond the Lookup function.
package proxy

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

// Target is what Lookup resolves a host to.
type Target struct {
	Repo, Name string
	State      string               // running | ready | stopped | failed | creating | removing
	LastError  string
	Port       int                  // the dev server's port on 127.0.0.1
	Tail       func(n int) []string // last n log lines, for the 502 page; may be nil
}

// NoSuchBoxError: no box matches. Candidates are the hosts that do exist.
type NoSuchBoxError struct {
	Name       string
	Candidates []string
}

func (e *NoSuchBoxError) Error() string { return fmt.Sprintf("no box called %q", e.Name) }

// AmbiguousError: several repos have a box with this name; Hosts are the
// fully qualified <name>.<repo>.localhost addresses.
type AmbiguousError struct {
	Name  string
	Hosts []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%d repos have a box called %q", len(e.Hosts), e.Name)
}

// NoPortsError: the box exists but exposes no port.
type NoPortsError struct{ Name string }

func (e *NoPortsError) Error() string { return fmt.Sprintf("box %q has no ports", e.Name) }

type Options struct {
	// Lookup resolves <name>.localhost (repo == "") or <name>.<repo>.localhost.
	Lookup func(name, repo string) (Target, error)
	// Dashboard is the dashboard's base URL, e.g. "http://127.0.0.1:7777/ui/".
	Dashboard string
}

type Handler struct {
	o         Options
	transport http.RoundTripper
}

func New(o Options) *Handler {
	// One transport for every box, so idle connections are pooled and bounded.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 32
	return &Handler{o: o, transport: tr}
}

// IsProxyHost reports whether a Host header names a box (ends in ".localhost").
func IsProxyHost(host string) bool { return strings.HasSuffix(hostname(host), ".localhost") }

// hostname lowercases a Host header and drops its port and any trailing dot.
func hostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// hostPort returns the ":port" part of a Host header ("" when absent), so the
// pages can build links that keep the port the browser used.
func hostPort(host string) string {
	if _, p, err := net.SplitHostPort(host); err == nil {
		return ":" + p
	}
	return ""
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := hostname(r.Host)
	port := hostPort(r.Host)
	labels := strings.Split(strings.TrimSuffix(host, ".localhost"), ".")
	var name, repo string
	switch len(labels) {
	case 1:
		name = labels[0]
	case 2:
		name, repo = labels[0], labels[1]
	default:
		h.page(w, http.StatusNotFound, pageData{Title: "Not a box address",
			Message: fmt.Sprintf("%s is not a box address. Boxes live at <name>.localhost or <name>.<repo>.localhost.", host)})
		return
	}
	t, err := h.o.Lookup(name, repo)
	if err != nil {
		h.lookupError(w, err, name, port)
		return
	}
	switch t.State {
	case "running":
		h.proxy(t).ServeHTTP(w, r)
	case "creating":
		h.page(w, http.StatusServiceUnavailable, pageData{Title: "Box " + t.Name + " is being created",
			Message: "This page refreshes every two seconds until the box is up.", Refresh: true, Links: h.boxLinks(t)})
	default:
		h.page(w, http.StatusServiceUnavailable, pageData{Title: "Box " + t.Name + " is " + t.State,
			Message: "Its dev server is not running, so there is nothing to show yet.",
			Detail:  t.LastError, Command: "agentbox start " + t.Name, Links: h.boxLinks(t)})
	}
}

func (h *Handler) lookupError(w http.ResponseWriter, err error, name, port string) {
	var nsb *NoSuchBoxError
	var amb *AmbiguousError
	var np *NoPortsError
	switch {
	case errors.As(err, &nsb):
		d := pageData{Title: "No box called " + name, Message: "There is no box called " + name + "."}
		if len(nsb.Candidates) > 0 {
			d.Message += " These boxes exist:"
			for _, c := range nsb.Candidates {
				d.Links = append(d.Links, link{Href: "http://" + c + port + "/", Text: c})
			}
		} else {
			d.Message += " Create one with `agentbox new " + name + "`."
		}
		h.page(w, http.StatusNotFound, d)
	case errors.As(err, &amb):
		d := pageData{Title: "Which " + name + "?", Message: "Several repos have a box called " + name + ". Pick one:"}
		for _, host := range amb.Hosts {
			d.Links = append(d.Links, link{Href: "http://" + host + port + "/", Text: host})
		}
		h.page(w, http.StatusMultipleChoices, d)
	case errors.As(err, &np):
		h.page(w, http.StatusNotFound, pageData{Title: "Box " + name + " has no ports",
			Message: "Its agentbox.yaml lists no `ports`, so there is no dev server to forward to."})
	default:
		h.page(w, http.StatusInternalServerError, pageData{Title: "agentbox error", Message: err.Error()})
	}
}

// proxy builds the reverse proxy for one target. It is per request so the
// 502 page can show that box's logs; the transport (and its pool) is shared.
func (h *Handler) proxy(t Target) *httputil.ReverseProxy {
	target := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(t.Port)}
	return &httputil.ReverseProxy{
		Transport:     h.transport,
		FlushInterval: -1, // dev servers stream (HMR, SSE); never buffer
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			pr.Out.Host = pr.In.Host // the app sees the address the browser used
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			var lines []string
			if t.Tail != nil {
				lines = t.Tail(20)
			}
			h.page(w, http.StatusBadGateway, pageData{Title: "Box " + t.Name + " is not answering",
				Message: fmt.Sprintf("agentbox could not reach the dev server on port %d: %v. It may be restarting; the last log lines:", t.Port, err),
				Lines: lines, Command: "agentbox logs " + t.Name + " -f", Links: h.boxLinks(t)})
		},
	}
}

func (h *Handler) boxLinks(t Target) []link {
	if h.o.Dashboard == "" {
		return nil
	}
	return []link{{Href: h.o.Dashboard + "#/r/" + url.PathEscape(t.Repo) + "/b/" + url.PathEscape(t.Name), Text: "Open " + t.Name + " in the dashboard"}}
}
```

- [ ] **Step 4: Implement `pages.go`**

Create `internal/proxy/pages.go`:

```go
package proxy

import (
	"html/template"
	"net/http"
)

type link struct{ Href, Text string }

type pageData struct {
	Title   string
	Message string
	Detail  string   // e.g. the box's last_error
	Command string   // a CLI command to show
	Links   []link
	Lines   []string // log lines
	Refresh bool     // add <meta http-equiv="refresh" content="2">
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · agentbox</title>{{if .Refresh}}
<meta http-equiv="refresh" content="2">{{end}}
<style>
:root{color-scheme:light dark;font:15px/1.5 system-ui,sans-serif}
body{margin:0;display:grid;place-items:center;min-height:100vh;background:#f6f7f9;color:#1b1f24}
@media(prefers-color-scheme:dark){body{background:#0f1115;color:#e6e8eb}}
main{max-width:40rem;padding:2rem}
.brand{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.85rem;opacity:.6;margin:0 0 .5rem}
h1{font-size:1.4rem;margin:0 0 .75rem}
pre{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.85rem;padding:.75rem;border-radius:6px;background:rgba(127,127,127,.12);overflow:auto;white-space:pre-wrap}
ul{padding-left:1.2rem}a{color:#2563eb}@media(prefers-color-scheme:dark){a{color:#7cb0ff}}
.detail{opacity:.8}
</style></head>
<body><main><p class="brand">agentbox</p><h1>{{.Title}}</h1><p>{{.Message}}</p>
{{if .Detail}}<p class="detail">{{.Detail}}</p>{{end}}
{{if .Command}}<pre>{{.Command}}</pre>{{end}}
{{if .Links}}<ul>{{range .Links}}<li><a href="{{.Href}}">{{.Text}}</a></li>{{end}}</ul>{{end}}
{{if .Lines}}<pre>{{range .Lines}}{{.}}
{{end}}</pre>{{end}}
</main></body></html>
`))

func (h *Handler) page(w http.ResponseWriter, status int, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = pageTmpl.Execute(w, d)
}
```

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/proxy/`
Expected: PASS (11 tests). If `TestRunningBoxIsProxied` reports `xfh=` empty, the stdlib `SetXForwarded` ran before `SetURL` overwrote headers — keep the order shown above (`SetURL`, then `SetXForwarded`).

- [ ] **Step 6: Checkpoint**

Run `go vet ./internal/proxy/ && go test -race ./internal/proxy/`; record "Task 2 complete" in the ledger. Do not commit.

---

### Task 3: `Manager.ProxyTarget`

**Files:**
- Create: `internal/box/proxy.go`
- Test: `internal/box/proxy_test.go`

**Interfaces:**
- Consumes: `proxy.Target`, `proxy.NoSuchBoxError`, `proxy.AmbiguousError`, `proxy.NoPortsError` (Task 2); `config.ProxyPort()` (Task 1); existing `m.d.Store.ListBoxes("")`, `m.d.Store.RepoByID`, `m.LogTail`.
- Produces: `func (m *Manager) ProxyTarget(name, repoSlug string) (proxy.Target, error)` — its signature matches `proxy.Options.Lookup`; Task 6 wires it.

- [ ] **Step 1: Write the failing tests**

Create `internal/box/proxy_test.go`:

```go
package box

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentbox-dev/agentbox/internal/proxy"
	"github.com/agentbox-dev/agentbox/internal/testutil"
)

func lowestPort(ports map[string]int) int {
	lo := 0
	for _, p := range ports {
		if lo == 0 || p < lo {
			lo = p
		}
	}
	return lo
}

func TestProxyTargetDefaultsToFirstPort(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig) // ports: [APP_PORT, API_PORT]
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	tg, err := m.ProxyTarget("a", "")
	if err != nil {
		t.Fatal(err)
	}
	if tg.Port != b.Ports["APP_PORT"] || tg.Repo != b.RepoID || tg.Name != "a" || tg.State != "ready" {
		t.Fatalf("%+v (ports %v)", tg, b.Ports)
	}
	if tg.Tail == nil || !strings.Contains(strings.Join(tg.Tail(50), "\n"), "setting up") {
		t.Errorf("Tail should read the box log: %v", tg.Tail(50))
	}
}

func TestProxyTargetHonoursProxyKeyAndQualifiedRepo(t *testing.T) {
	m := newTestManager(t)
	cfg := strings.Replace(sqliteConfig, "ports: [APP_PORT, API_PORT]\n", "ports: [APP_PORT, API_PORT]\nproxy: API_PORT\n", 1)
	repo := newSQLiteRepo(t, cfg)
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"", b.RepoID} {
		tg, err := m.ProxyTarget("a", slug)
		if err != nil || tg.Port != b.Ports["API_PORT"] {
			t.Fatalf("slug %q: %+v %v (ports %v)", slug, tg, err, b.Ports)
		}
	}
	if _, err := m.ProxyTarget("a", "other-repo-000000"); err == nil {
		t.Fatal("wrong repo slug should not match")
	}
}

func TestProxyTargetFallsBackWhenConfigIsGone(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig)
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(repo, "agentbox.yaml"))
	tg, err := m.ProxyTarget("a", "")
	if err != nil || tg.Port != lowestPort(b.Ports) {
		t.Fatalf("%+v %v (ports %v)", tg, err, b.Ports)
	}
}

func TestProxyTargetAmbiguousAcrossRepos(t *testing.T) {
	m := newTestManager(t)
	var slugs []string
	for i := 0; i < 2; i++ {
		repo := newSQLiteRepo(t, sqliteConfig)
		b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
		if err != nil {
			t.Fatal(err)
		}
		slugs = append(slugs, b.RepoID)
	}
	_, err := m.ProxyTarget("a", "")
	var amb *proxy.AmbiguousError
	if !errors.As(err, &amb) || len(amb.Hosts) != 2 {
		t.Fatalf("want AmbiguousError with 2 hosts, got %v", err)
	}
	for _, s := range slugs {
		if !strings.Contains(strings.Join(amb.Hosts, " "), "a."+s+".localhost") {
			t.Errorf("hosts %v lack %s", amb.Hosts, s)
		}
		if tg, err := m.ProxyTarget("a", s); err != nil || tg.Repo != s {
			t.Errorf("qualified lookup %s: %+v %v", s, tg, err)
		}
	}
}

func TestProxyTargetUnknownListsCandidates(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig)
	if _, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true}); err != nil {
		t.Fatal(err)
	}
	_, err := m.ProxyTarget("zzz", "")
	var nsb *proxy.NoSuchBoxError
	if !errors.As(err, &nsb) || len(nsb.Candidates) != 1 || nsb.Candidates[0] != "a.localhost" {
		t.Fatalf("want NoSuchBoxError{Candidates: [a.localhost]}, got %v", err)
	}
}

func TestProxyTargetNoPorts(t *testing.T) {
	m := newTestManager(t)
	repo := testutil.NewRepo(t, map[string]string{"agentbox.yaml": "version: 1\n"})
	if _, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true}); err != nil {
		t.Fatal(err)
	}
	_, err := m.ProxyTarget("a", "")
	var np *proxy.NoPortsError
	if !errors.As(err, &np) || np.Name != "a" {
		t.Fatalf("want NoPortsError, got %v", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/box/ -run ProxyTarget`
Expected: FAIL — `m.ProxyTarget undefined`.

- [ ] **Step 3: Implement**

Create `internal/box/proxy.go`:

```go
package box

import (
	"slices"

	"github.com/agentbox-dev/agentbox/internal/config"
	"github.com/agentbox-dev/agentbox/internal/proxy"
	"github.com/agentbox-dev/agentbox/internal/state"
)

// ProxyTarget resolves a proxy host to a box: <name>.localhost when repoSlug
// is "" (the name must then be unique across repos), or
// <name>.<repoSlug>.localhost. It is the proxy package's Lookup.
func (m *Manager) ProxyTarget(name, repoSlug string) (proxy.Target, error) {
	boxes, err := m.d.Store.ListBoxes("")
	if err != nil {
		return proxy.Target{}, err
	}
	counts := map[string]int{}
	for _, b := range boxes {
		counts[b.Name]++
	}
	hostOf := func(b state.Box) string {
		if counts[b.Name] == 1 {
			return b.Name + ".localhost"
		}
		return b.Name + "." + b.RepoID + ".localhost"
	}
	var matches []state.Box
	for _, b := range boxes {
		if b.Name == name && (repoSlug == "" || b.RepoID == repoSlug) {
			matches = append(matches, b)
		}
	}
	switch len(matches) {
	case 0:
		cands := make([]string, 0, len(boxes))
		for _, b := range boxes {
			cands = append(cands, hostOf(b))
		}
		slices.Sort(cands)
		return proxy.Target{}, &proxy.NoSuchBoxError{Name: name, Candidates: cands}
	case 1:
	default:
		hosts := make([]string, 0, len(matches))
		for _, b := range matches {
			hosts = append(hosts, b.Name+"."+b.RepoID+".localhost")
		}
		slices.Sort(hosts)
		return proxy.Target{}, &proxy.AmbiguousError{Name: name, Hosts: hosts}
	}
	b := matches[0]
	if len(b.Ports) == 0 {
		return proxy.Target{}, &proxy.NoPortsError{Name: b.Name}
	}
	return proxy.Target{
		Repo: b.RepoID, Name: b.Name, State: string(b.State), LastError: b.LastError,
		Port: m.proxyPort(b),
		Tail: func(n int) []string {
			lines, _ := m.LogTail(b.RepoID, b.Name, n)
			return lines
		},
	}, nil
}

// proxyPort is the port named by the repo's `proxy:` key (default: the first
// of `ports`), or the box's lowest port when the config can't say.
func (m *Manager) proxyPort(b state.Box) int {
	if repo, err := m.d.Store.RepoByID(b.RepoID); err == nil {
		if cfg, err := config.Load(repo.Path); err == nil {
			if p := b.Ports[cfg.ProxyPort()]; p != 0 {
				return p
			}
		}
	}
	lowest := 0
	for _, p := range b.Ports {
		if lowest == 0 || p < lowest {
			lowest = p
		}
	}
	return lowest
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/box/ -run ProxyTarget`
Expected: PASS (6 tests).

- [ ] **Step 5: Checkpoint**

Run `go vet ./internal/box/ && go test -race ./internal/box/`; record "Task 3 complete". Do not commit.

---

### Task 4: `internal/ui` — embedded dashboard, login codes, session cookie

**Files:**
- Create: `internal/ui/ui.go`, `internal/ui/codes.go`, `internal/ui/session.go`, `internal/ui/pages.go`, `internal/ui/dist/.gitkeep`
- Modify: `.gitignore`
- Test: `internal/ui/ui_test.go`

**Interfaces:**
- Produces (consumed by Tasks 5, 6, 7):

```go
package ui
const SessionCookie = "agentbox_session"
func Dist() fs.FS                                   // the embedded build (only .gitkeep in a plain checkout)
func Built(fsys fs.FS) bool                         // true when fsys has index.html
func ValidSession(r *http.Request, token string) bool
func SetSession(w http.ResponseWriter, token string)
func ClearSession(w http.ResponseWriter)
type LoginCodes struct{ TTL time.Duration; Now func() time.Time; /* private */ }
func NewLoginCodes() *LoginCodes                    // TTL 2m
func (c *LoginCodes) Mint() (code string, expiresAt time.Time)
func (c *LoginCodes) Redeem(code string) bool       // single use
type Options struct{ Token string; Codes *LoginCodes; FS fs.FS }
func Handler(o Options) http.Handler                // serves /ui/login (GET+POST), /ui/logout (POST), /ui/* (SPA)
```

- [ ] **Step 1: Create the embed anchor and ignore the build**

```bash
mkdir -p internal/ui/dist && touch internal/ui/dist/.gitkeep
cat >> .gitignore <<'EOF'
/internal/ui/dist/*
!/internal/ui/dist/.gitkeep
/web/node_modules/
EOF
```

- [ ] **Step 2: Write the failing tests**

Create `internal/ui/ui_test.go`:

```go
package ui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func fakeBuild() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>agentbox</title><div id=\"root\"></div>")},
		"assets/app-abc123.js": {Data: []byte("console.log(1)")},
		".gitkeep":             {Data: nil},
	}
}

func newHandler(fsys fstest.MapFS) (http.Handler, *LoginCodes) {
	codes := NewLoginCodes()
	return Handler(Options{Token: "tok", Codes: codes, FS: fsys}), codes
}

func do(h http.Handler, method, path string, form url.Values, cookie string) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", SessionCookie, rec.Header())
	return nil
}

func TestServesAppWithSPAFallbackAndCaching(t *testing.T) {
	h, _ := newHandler(fakeBuild())
	for _, path := range []string{"/ui/", "/ui/r/myapp-1a2b3c/b/a", "/ui/does-not-exist.js"} {
		rec := do(h, "GET", path, nil, "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="root"`) {
			t.Errorf("%s: %d %q", path, rec.Code, rec.Body.String())
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: Cache-Control %q, want no-cache", path, cc)
		}
	}
	rec := do(h, "GET", "/ui/assets/app-abc123.js", nil, "")
	if rec.Code != 200 || rec.Body.String() != "console.log(1)" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "javascript") {
		t.Errorf("asset content-type %q", rec.Header().Get("Content-Type"))
	}
}

func TestNotBuiltExplains(t *testing.T) {
	h, _ := newHandler(fstest.MapFS{".gitkeep": {}})
	rec := do(h, "GET", "/ui/", nil, "")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "built without the dashboard") {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	if Built(fakeBuild()) != true || Built(fstest.MapFS{".gitkeep": {}}) != false {
		t.Fatal("Built() wrong")
	}
}

func TestLoginCodeIsSingleUseAndExpires(t *testing.T) {
	h, codes := newHandler(fakeBuild())
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	codes.Now = func() time.Time { return now }

	code, exp := codes.Mint()
	if len(code) != 64 || !exp.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("mint: %q %v", code, exp)
	}
	rec := do(h, "GET", "/ui/login?code="+code, nil, "")
	if rec.Code != 303 || rec.Header().Get("Location") != "/ui/" {
		t.Fatalf("redeem: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	c := sessionCookie(t, rec)
	if c.Value != "tok" || !c.HttpOnly || c.Path != "/" || c.SameSite != http.SameSiteLaxMode || c.Secure || c.Domain != "" {
		t.Fatalf("cookie attrs: %+v", c)
	}
	if rec := do(h, "GET", "/ui/login?code="+code, nil, ""); rec.Code != 403 || !strings.Contains(rec.Body.String(), "agentbox ui") {
		t.Fatalf("second use: %d %q", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			t.Fatal("a failed login must not set a cookie")
		}
	}
	code2, _ := codes.Mint()
	now = now.Add(2*time.Minute + time.Second)
	if rec := do(h, "GET", "/ui/login?code="+code2, nil, ""); rec.Code != 403 {
		t.Fatalf("expired: %d", rec.Code)
	}
	if rec := do(h, "GET", "/ui/login?code=nope", nil, ""); rec.Code != 403 {
		t.Fatalf("unknown: %d", rec.Code)
	}
	if rec := do(h, "GET", "/ui/login", nil, ""); rec.Code != 403 {
		t.Fatalf("missing: %d", rec.Code)
	}
}

func TestTokenFormAndLogout(t *testing.T) {
	h, _ := newHandler(fakeBuild())
	rec := do(h, "POST", "/ui/login", url.Values{"token": {" tok \n"}}, "")
	if rec.Code != 303 || sessionCookie(t, rec).Value != "tok" {
		t.Fatalf("token form: %d", rec.Code)
	}
	if rec := do(h, "POST", "/ui/login", url.Values{"token": {"wrong"}}, ""); rec.Code != 403 {
		t.Fatalf("wrong token: %d", rec.Code)
	}
	rec = do(h, "POST", "/ui/logout", nil, "tok")
	if rec.Code != 303 || sessionCookie(t, rec).MaxAge >= 0 {
		t.Fatalf("logout: %d %+v", rec.Code, sessionCookie(t, rec))
	}
}

func TestValidSession(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/boxes", nil)
	if ValidSession(req, "tok") {
		t.Fatal("no cookie should be invalid")
	}
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: "tok"})
	if !ValidSession(req, "tok") || ValidSession(req, "other") || ValidSession(req, "") {
		t.Fatal("cookie must equal the token exactly")
	}
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/ui/`
Expected: FAIL — `Handler`, `NewLoginCodes` etc. undefined.

- [ ] **Step 4: Implement `codes.go`**

Create `internal/ui/codes.go`:

```go
package ui

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// LoginCodes are one-shot, short-lived codes that `agentbox ui` turns into a
// browser session without ever putting the API token in a URL.
type LoginCodes struct {
	TTL time.Duration
	Now func() time.Time

	mu    sync.Mutex
	codes map[string]time.Time // code → expiry
}

func NewLoginCodes() *LoginCodes {
	return &LoginCodes{TTL: 2 * time.Minute, Now: time.Now, codes: map[string]time.Time{}}
}

// Mint issues a new code. Expired codes are swept here, so the map stays small.
func (c *LoginCodes) Mint() (string, time.Time) {
	buf := make([]byte, 32)
	rand.Read(buf) // never fails on Go 1.24
	code := hex.EncodeToString(buf)
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now()
	for k, exp := range c.codes {
		if exp.Before(now) {
			delete(c.codes, k)
		}
	}
	exp := now.Add(c.TTL)
	c.codes[code] = exp
	return code, exp
}

// Redeem consumes a code. It is true only once per code, and only before expiry.
func (c *LoginCodes) Redeem(code string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	exp, ok := c.codes[code]
	if !ok {
		return false
	}
	delete(c.codes, code)
	return !exp.Before(c.Now())
}
```

- [ ] **Step 5: Implement `session.go`**

Create `internal/ui/session.go`:

```go
package ui

import (
	"crypto/subtle"
	"net/http"
)

// SessionCookie carries the API token for the browser. HttpOnly keeps page
// scripts (and the box apps, which never see this host anyway) away from it.
const SessionCookie = "agentbox_session"

// ValidSession reports whether the request carries a session cookie equal to the token.
func ValidSession(r *http.Request, token string) bool {
	c, err := r.Cookie(SessionCookie)
	return err == nil && token != "" && subtle.ConstantTimeCompare([]byte(c.Value), []byte(token)) == 1
}

func SetSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: 365 * 24 * 3600})
}

func ClearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: -1})
}
```

- [ ] **Step 6: Implement `pages.go`**

Create `internal/ui/pages.go`:

```go
package ui

import (
	"html/template"
	"net/http"
)

type pageData struct {
	Title, Message string
	ShowTokenForm  bool
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · agentbox</title>
<style>
:root{color-scheme:light dark;font:15px/1.5 system-ui,sans-serif}
body{margin:0;display:grid;place-items:center;min-height:100vh;background:#f6f7f9;color:#1b1f24}
@media(prefers-color-scheme:dark){body{background:#0f1115;color:#e6e8eb}}
main{max-width:34rem;padding:2rem}
.brand{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.85rem;opacity:.6;margin:0 0 .5rem}
h1{font-size:1.4rem;margin:0 0 .75rem}
pre,code,input{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.85rem}
pre{padding:.75rem;border-radius:6px;background:rgba(127,127,127,.12);overflow:auto}
form{margin-top:1.5rem;display:flex;gap:.5rem}input{flex:1;padding:.4rem .5rem}
button{padding:.4rem .8rem}
</style></head>
<body><main><p class="brand">agentbox</p><h1>{{.Title}}</h1><p>{{.Message}}</p>
{{if .ShowTokenForm}}<p>Run this in a terminal to open a signed-in dashboard:</p><pre>agentbox ui</pre>
<p>Or paste the contents of <code>~/.agentbox/token</code>:</p>
<form method="post" action="/ui/login"><input name="token" placeholder="token" autocomplete="off"><button>Sign in</button></form>{{end}}
</main></body></html>
`))

func page(w http.ResponseWriter, status int, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = pageTmpl.Execute(w, d)
}

func forbidden(w http.ResponseWriter, why string) {
	page(w, http.StatusForbidden, pageData{Title: "Sign in to agentbox", Message: why, ShowTokenForm: true})
}

var notBuilt = pageData{Title: "No dashboard in this build",
	Message: "This agentbox binary was built without the dashboard. Install a release build, or run `make ui` (needs Node 20+) and rebuild."}
```

- [ ] **Step 7: Implement `ui.go`**

Create `internal/ui/ui.go`:

```go
// Package ui serves the embedded web dashboard and signs the browser in.
package ui

import (
	"crypto/subtle"
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

// Dist is the dashboard build embedded in this binary. In a plain checkout it
// holds only .gitkeep; `make ui` fills it before `go build`.
func Dist() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Built reports whether fsys holds a dashboard build.
func Built(fsys fs.FS) bool {
	st, err := fs.Stat(fsys, "index.html")
	return err == nil && !st.IsDir()
}

type Options struct {
	Token string      // the API token; a valid session cookie carries it
	Codes *LoginCodes // one-shot codes minted by `agentbox ui`
	FS    fs.FS       // the dashboard build: Dist() in production, a fake in tests
}

// Handler serves everything under /ui/: sign-in, sign-out and the single-page app.
func Handler(o Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui/login", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" || o.Codes == nil || !o.Codes.Redeem(code) {
			forbidden(w, "This sign-in link has expired or was already used.")
			return
		}
		SetSession(w, o.Token)
		http.Redirect(w, r, "/ui/", http.StatusSeeOther)
	})
	mux.HandleFunc("POST /ui/login", func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimSpace(r.FormValue("token"))
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(o.Token)) != 1 {
			forbidden(w, "That is not this daemon's token.")
			return
		}
		SetSession(w, o.Token)
		http.Redirect(w, r, "/ui/", http.StatusSeeOther)
	})
	mux.HandleFunc("POST /ui/logout", func(w http.ResponseWriter, r *http.Request) {
		ClearSession(w)
		http.Redirect(w, r, "/ui/", http.StatusSeeOther)
	})
	mux.HandleFunc("/ui/", func(w http.ResponseWriter, r *http.Request) { serveApp(w, r, o.FS) })
	return mux
}

// serveApp serves a built file when the path names one, otherwise index.html
// so the app's own (hash) router can take over.
func serveApp(w http.ResponseWriter, r *http.Request, fsys fs.FS) {
	if !Built(fsys) {
		page(w, http.StatusServiceUnavailable, notBuilt)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/ui/")
	if name != "" && name != "index.html" {
		if st, err := fs.Stat(fsys, name); err == nil && !st.IsDir() {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeFileFS(w, r, fsys, name)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-cache")
	// ServeFileFS would redirect a path ending in /index.html; serve the bytes ourselves.
	data, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		page(w, http.StatusServiceUnavailable, notBuilt)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
```

- [ ] **Step 8: Run the tests**

Run: `go test -race ./internal/ui/`
Expected: PASS (5 tests). If the asset test fails on `Content-Type`, `http.ServeFileFS` sniffs by extension — `.js` maps to `text/javascript`, which the assertion accepts.

- [ ] **Step 9: Checkpoint**

Run `go vet ./internal/ui/ && go test -race ./internal/ui/ && go build ./...`; confirm `git status` shows `internal/ui/dist/.gitkeep` as a new file and nothing else under `dist/`. Record "Task 4 complete". Do not commit.

---

### Task 5: API — cookie auth, health facts, login codes, dashboard mount, box URL

**Files:**
- Modify: `internal/api/types.go`
- Modify: `internal/api/server.go`
- Test: `internal/api/server_test.go`

**Interfaces:**
- Consumes: `ui.ValidSession`, `ui.LoginCodes` (Task 4).
- Produces (consumed by Tasks 6, 7, 8):

```go
type Server struct {
	M       *box.Manager
	Bus     *events.Bus
	Token   string
	Origins []string
	Codes   *ui.LoginCodes // nil: login codes disabled
	UI      http.Handler   // nil: no /ui/ routes
	APIAddr string         // effective TCP address; "" when the HTTP API is off
	Version string
	UIBuilt bool
}
type HealthView struct{ OK bool; PID int; Version, APIAddr string; UI bool }   // json: ok, pid, version, api_addr, ui
type LoginCode struct{ Code, URL string; ExpiresAt time.Time }                  // json: code, url, expires_at
BoxView.URL string `json:"url,omitempty"`                                       // "http://<name>.localhost:<port>/"
POST /v1/ui/login-codes → 201 LoginCode   (412 when APIAddr == "" or Codes == nil)
GET  /v1/health         → 200 HealthView
```
- `Handler(requireToken bool)`: unix socket (`false`) = API only; TCP (`true`) = auth-wrapped API + `/ui/` + `GET /` → `302 /ui/`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/api/server_test.go` (the file already imports `net/http`, `net/http/httptest`, `strings`, `testing`, `box`, `url`; add `"net"`, `"os"` and `"github.com/agentbox-dev/agentbox/internal/ui"` and `"testing/fstest"` to its import block):

```go
func tcpServer(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	srv, repo := newServer(t)
	srv.Codes = ui.NewLoginCodes()
	srv.UI = ui.Handler(ui.Options{Token: srv.Token, Codes: srv.Codes, FS: fstest.MapFS{"index.html": {Data: []byte("<div id=\"root\"></div>")}}})
	srv.Version = "test"
	srv.UIBuilt = true
	ts := httptest.NewServer(srv.Handler(true))
	t.Cleanup(ts.Close)
	srv.APIAddr = strings.TrimPrefix(ts.URL, "http://")
	srv.Origins = []string{ts.URL}
	return srv, ts, repo
}

func send(t *testing.T, ts *httptest.Server, method, path string, hdr map[string]string, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestHealthReportsDaemonFacts(t *testing.T) {
	srv, ts, _ := tcpServer(t)
	var h HealthView
	req, _ := http.NewRequest("GET", ts.URL+"/v1/health", nil)
	req.Header.Set("Authorization", "Bearer "+srv.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	json.NewDecoder(resp.Body).Decode(&h)
	if !h.OK || h.PID != os.Getpid() || h.Version != "test" || h.APIAddr != srv.APIAddr || !h.UI {
		t.Fatalf("%+v", h)
	}
}

func TestLoginCodesNeedTokenOnTCPButNotOnSocket(t *testing.T) {
	srv, ts, _ := tcpServer(t)
	if code, _ := send(t, ts, "POST", "/v1/ui/login-codes", nil, ""); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	code, body := send(t, ts, "POST", "/v1/ui/login-codes", map[string]string{"Authorization": "Bearer " + srv.Token}, "")
	var lc LoginCode
	json.Unmarshal([]byte(body), &lc)
	if code != 201 || len(lc.Code) != 64 || lc.URL != ts.URL+"/ui/login?code="+lc.Code || lc.ExpiresAt.IsZero() {
		t.Fatalf("%d %s", code, body)
	}
	sock := httptest.NewServer(srv.Handler(false)) // the unix-socket handler: no auth
	t.Cleanup(sock.Close)
	if code, _ := send(t, sock, "POST", "/v1/ui/login-codes", nil, ""); code != 201 {
		t.Fatalf("socket: %d", code)
	}
	if code, _ := send(t, sock, "GET", "/ui/", nil, ""); code != 404 {
		t.Fatalf("the socket handler must not serve the dashboard: %d", code)
	}
	srv.APIAddr = ""
	if code, body := send(t, sock, "POST", "/v1/ui/login-codes", nil, ""); code != 412 || !strings.Contains(body, "disabled") {
		t.Fatalf("HTTP API off: %d %s", code, body)
	}
}

func TestSessionCookieAuth(t *testing.T) {
	srv, ts, repo := tcpServer(t)
	cookie := map[string]string{"Cookie": ui.SessionCookie + "=" + srv.Token}
	if code, _ := send(t, ts, "GET", "/v1/boxes", cookie, ""); code != 200 {
		t.Fatalf("cookie GET: %d", code)
	}
	if code, _ := send(t, ts, "GET", "/v1/boxes", map[string]string{"Cookie": ui.SessionCookie + "=wrong"}, ""); code != 401 {
		t.Fatalf("wrong cookie: %d", code)
	}
	body := `{"path":` + strconv.Quote(repo) + `}`
	withOrigin := map[string]string{"Cookie": cookie["Cookie"], "Origin": ts.URL}
	if code, b := send(t, ts, "POST", "/v1/repos", withOrigin, body); code != 200 {
		t.Fatalf("cookie POST with Origin: %d %s", code, b)
	}
	crossSite := map[string]string{"Cookie": cookie["Cookie"], "Origin": ts.URL, "Sec-Fetch-Site": "cross-site"}
	if code, _ := send(t, ts, "POST", "/v1/repos", crossSite, body); code != 403 {
		t.Fatalf("cross-site POST: %d", code)
	}
	badOrigin := map[string]string{"Cookie": cookie["Cookie"], "Origin": "http://evil.example"}
	if code, _ := send(t, ts, "POST", "/v1/repos", badOrigin, body); code != 403 {
		t.Fatalf("foreign Origin: %d", code)
	}
}

func TestSessionCookieWritesNeedOrigin(t *testing.T) {
	srv, ts, repo := tcpServer(t)
	body := `{"path":` + strconv.Quote(repo) + `}`
	cookieOnly := map[string]string{"Cookie": ui.SessionCookie + "=" + srv.Token}
	if code, b := send(t, ts, "POST", "/v1/repos", cookieOnly, body); code != 403 || !strings.Contains(b, "Origin") {
		t.Fatalf("cookie POST without Origin must be refused: %d %s", code, b)
	}
	if code, _ := send(t, ts, "DELETE", "/v1/repos/x/boxes/y", cookieOnly, ""); code != 403 {
		t.Fatalf("cookie DELETE without Origin: %d", code)
	}
	bearer := map[string]string{"Authorization": "Bearer " + srv.Token}
	if code, _ := send(t, ts, "POST", "/v1/repos", bearer, body); code != 200 {
		t.Fatalf("bearer POST without Origin must work: %d", code)
	}
}

func TestDashboardMountAndRootRedirect(t *testing.T) {
	_, ts, _ := tcpServer(t)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/ui/" {
		t.Fatalf("root: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if code, body := send(t, ts, "GET", "/ui/", nil, ""); code != 200 || !strings.Contains(body, "root") {
		t.Fatalf("dashboard without a cookie must still load: %d %s", code, body)
	}
}

func TestBoxViewCarriesProxyURL(t *testing.T) {
	srv, ts, repo := tcpServer(t)
	bearer := map[string]string{"Authorization": "Bearer " + srv.Token}
	send(t, ts, "POST", "/v1/boxes", bearer, `{"repo_path":`+strconv.Quote(repo)+`,"name":"a","no_dev":true}`)
	_, port, _ := net.SplitHostPort(srv.APIAddr)
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, body := send(t, ts, "GET", "/v1/boxes", bearer, "")
		var list []BoxView
		json.Unmarshal([]byte(body), &list)
		if len(list) == 1 && list[0].State == "ready" {
			if list[0].URL != "http://a.localhost:"+port+"/" {
				t.Fatalf("url = %q", list[0].URL)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("box never ready: %s", body)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
```

Also add `"strconv"` to the imports.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/api/ -run 'Health|LoginCodes|SessionCookie|Dashboard|ProxyURL'`
Expected: FAIL — `srv.Codes undefined`, `HealthView` undefined.

- [ ] **Step 3: Extend `types.go`**

In `internal/api/types.go` add after `RepoView`:

```go
type HealthView struct {
	OK      bool   `json:"ok"`
	PID     int    `json:"pid"`
	Version string `json:"version"`
	APIAddr string `json:"api_addr"` // "" when the HTTP API is disabled
	UI      bool   `json:"ui"`       // whether a dashboard is built into this binary
}

type LoginCode struct {
	Code      string    `json:"code"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}
```

and add to `BoxView`, after `Dirty`:

```go
	URL       string            `json:"url,omitempty"` // http://<name>.localhost:<port>/ when the box has ports
```

- [ ] **Step 4: Rewrite the top of `server.go`**

Replace the `Server` struct, `Handler` and `auth` in `internal/api/server.go` with:

```go
type Server struct {
	M       *box.Manager
	Bus     *events.Bus
	Token   string   // required on the TCP listener (Bearer header or session cookie)
	Origins []string // browser origins allowed on the TCP listener
	Codes   *ui.LoginCodes // nil disables login codes
	UI      http.Handler   // the dashboard; nil disables /ui/
	APIAddr string         // effective TCP address, "" when the HTTP API is disabled
	Version string
	UIBuilt bool
}

// Handler serves the v1 API. requireToken is true for the TCP listener, which
// also gets the dashboard, and false for the Unix socket, which file
// permissions already protect and which serves the API alone.
func (s *Server) Handler(requireToken bool) http.Handler {
	mux := http.NewServeMux()
	const b = "/v1/repos/{repo}/boxes/{name}"
	mux.HandleFunc("GET /v1/health", s.health)
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
	mux.HandleFunc("POST /v1/ui/login-codes", s.loginCode)
	if !requireToken {
		return mux
	}
	root := http.NewServeMux()
	root.Handle("/v1/", s.auth(mux))
	if s.UI != nil {
		root.Handle("/ui/", s.UI)
		root.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/ui/", http.StatusFound)
		})
	}
	return root
}

// auth admits a Bearer token, or a browser session cookie. A cookie may only
// change state from the dashboard's own origin: browsers send Origin on every
// cross-origin request and on same-origin fetch POSTs, and Sec-Fetch-Site
// names a cross-site caller even when Origin looks right.
func (s *Server) auth(next http.Handler) http.Handler {
	want := []byte("Bearer " + s.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && !slices.Contains(s.Origins, origin) {
			writeJSON(w, http.StatusForbidden, ErrorBody{errs.New(errs.Invalid, "origin %q is not allowed", origin)})
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		if !ui.ValidSession(r, s.Token) {
			writeJSON(w, http.StatusUnauthorized, ErrorBody{errs.New(errs.Invalid, "missing or wrong API token").
				WithHint("send `Authorization: Bearer <token>` using the token in ~/.agentbox/token, or sign in with `agentbox ui`")})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin == "" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeJSON(w, http.StatusForbidden, ErrorBody{errs.New(errs.Invalid, "a browser session must send an allowed Origin header to change state")})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, HealthView{OK: true, PID: os.Getpid(), Version: s.Version, APIAddr: s.APIAddr, UI: s.UIBuilt})
}

func (s *Server) loginCode(w http.ResponseWriter, r *http.Request) {
	if s.Codes == nil || s.APIAddr == "" {
		writeError(w, errs.New(errs.Precondition, "the daemon's HTTP API is disabled, so the dashboard cannot be opened").
			WithHint("the port in api_addr (~/.agentbox/config.yaml, default 127.0.0.1:7777) is probably in use; see ~/.agentbox/agentbox.log"))
		return
	}
	code, exp := s.Codes.Mint()
	writeJSON(w, http.StatusCreated, LoginCode{Code: code, URL: "http://" + s.APIAddr + "/ui/login?code=" + code, ExpiresAt: exp})
}

// view adds what only the server knows to a box: its proxy URL.
func (s *Server) view(i box.Info) BoxView {
	v := viewOf(i)
	if len(i.Ports) > 0 && s.APIAddr != "" {
		if _, port, err := net.SplitHostPort(s.APIAddr); err == nil {
			v.URL = "http://" + i.Name + ".localhost:" + port + "/"
		}
	}
	return v
}
```

Then: in `listBoxes` replace `out = append(out, viewOf(i))` with `out = append(out, s.view(i))`; in `getBox` replace `writeJSON(w, 200, viewOf(info))` with `writeJSON(w, 200, s.view(info))`. Delete the old inline `GET /v1/health` closure. Add `"net"`, `"os"` and `"github.com/agentbox-dev/agentbox/internal/ui"` to the imports.

- [ ] **Step 5: Run the whole package**

Run: `go test -race ./internal/api/`
Expected: PASS — including the pre-existing `TestAuth` (Bearer behaviour is unchanged) and the new six.

- [ ] **Step 6: Checkpoint**

Run `go vet ./internal/api/ && go test -race ./internal/api/`; record "Task 5 complete". Do not commit.

---

### Task 6: Daemon — effective address file, Host dispatch, wiring

**Files:**
- Modify: `internal/daemon/daemon.go`
- Test: `internal/daemon/daemon_test.go`

**Interfaces:**
- Consumes: `api.Server` fields (Task 5), `ui.*` (Task 4), `proxy.New`, `proxy.IsProxyHost` (Task 2), `m.ProxyTarget` (Task 3).
- Produces (consumed by Task 7, e2e):
  - `Paths.APIAddr string` — file `<home>/api_addr` holding the effective TCP address (written on start, removed when the HTTP API is disabled).
  - `var Version = "dev"` — set by the CLI before `Run`.
  - `var UIFS fs.FS = ui.Dist()` — the dashboard build the daemon serves; tests replace it.
  - `func Dispatch(proxy, app http.Handler) http.Handler` — the TCP root handler.

- [ ] **Step 1: Write the failing tests**

Append to `internal/daemon/daemon_test.go` (its imports already include `context`, `os`, `path/filepath`, `strings`, `testing`, `time`, `daemon`; add `"net/http"`, `"net/http/httptest"`, `"io"`, `"testing/fstest"`):

```go
func TestDispatchRejectsForeignHosts(t *testing.T) {
	hit := ""
	px := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = "proxy" })
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = "app" })
	h := daemon.Dispatch(px, app)
	cases := map[string]string{
		"a.localhost:7777": "proxy", "A.Repo-1a2b3c.LOCALHOST": "proxy",
		"127.0.0.1:7777": "app", "localhost:7777": "app", "localhost": "app", "[::1]:7777": "app",
		"evil.example:7777": "", "evil.localhost.example:7777": "", "10.0.0.5:7777": "", "": "",
	}
	for host, want := range cases {
		hit = ""
		req := httptest.NewRequest("GET", "http://x/v1/health", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if hit != want {
			t.Errorf("Host %q: routed to %q, want %q", host, hit, want)
		}
		if want == "" && rec.Code != http.StatusMisdirectedRequest {
			t.Errorf("Host %q: status %d, want 421", host, rec.Code)
		}
	}
}

func TestRunWritesEffectiveAPIAddrAndServesDashboard(t *testing.T) {
	daemon.UIFS = fstest.MapFS{"index.html": {Data: []byte("<div id=\"root\"></div>")}}
	t.Cleanup(func() { daemon.UIFS = nil })
	p := daemon.PathsFor(tempHome(t))
	os.WriteFile(p.Config, []byte("api_addr: 127.0.0.1:0\n"), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go daemon.Run(ctx, p)
	deadline := time.Now().Add(5 * time.Second)
	for !daemon.Ping(p.Sock) {
		if time.Now().After(deadline) {
			t.Fatal("daemon never came up")
		}
		time.Sleep(20 * time.Millisecond)
	}
	raw, err := os.ReadFile(p.APIAddr)
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(addr, "127.0.0.1:") || strings.HasSuffix(addr, ":0") {
		t.Fatalf("api_addr file: %q", addr)
	}
	tok, _ := os.ReadFile(p.Token)
	get := func(host, path, bearer string) (int, string) {
		req, _ := http.NewRequest("GET", "http://"+addr+path, nil)
		if host != "" {
			req.Host = host
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("", "/v1/health", strings.TrimSpace(string(tok))); code != 200 || !strings.Contains(body, `"api_addr":"`+addr+`"`) || !strings.Contains(body, `"ui":true`) {
		t.Fatalf("health: %d %s", code, body)
	}
	if code, body := get("", "/ui/", ""); code != 200 || !strings.Contains(body, "root") {
		t.Fatalf("dashboard: %d %s", code, body)
	}
	if code, _ := get("evil.example:7777", "/v1/health", strings.TrimSpace(string(tok))); code != 421 {
		t.Fatalf("foreign host: %d", code)
	}
	if code, body := get("nope.localhost:7777", "/", ""); code != 404 || !strings.Contains(body, "no box called") {
		t.Fatalf("proxy: %d %s", code, body)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/daemon/ -run 'Dispatch|EffectiveAPIAddr'`
Expected: FAIL — `daemon.Dispatch`, `daemon.UIFS`, `p.APIAddr` undefined.

- [ ] **Step 3: Implement**

In `internal/daemon/daemon.go`:

1. Imports: add `"io/fs"`, `"github.com/agentbox-dev/agentbox/internal/proxy"`, `"github.com/agentbox-dev/agentbox/internal/ui"`.

2. `Paths`: add `APIAddr` to the struct and to `PathsFor`:

```go
type Paths struct {
	Home, State, Sock, PID, Token, Log, Logs, Boxes, Config, Out, Lock, APIAddr string
}
```
and in `PathsFor`'s literal add `APIAddr: j("api_addr"),`.

3. Package variables, after `ErrAlreadyRunning`:

```go
// Version is reported by /v1/health; the CLI sets it before Run.
var Version = "dev"

// UIFS is the dashboard build the daemon serves. nil means ui.Dist(), the
// one embedded in this binary; tests substitute a fake build.
var UIFS fs.FS
```

4. `Dispatch`, after `origins`:

```go
// Dispatch is the TCP listener's root: *.localhost hosts go to the box proxy,
// loopback hosts to the API and dashboard, and any other Host is refused.
// A page at evil.example whose DNS now points at 127.0.0.1 still sends
// "Host: evil.example", which is what makes this the rebinding defence.
func Dispatch(px, app http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		switch {
		case proxy.IsProxyHost(host):
			px.ServeHTTP(w, r)
		case host == "127.0.0.1" || host == "localhost" || host == "::1":
			app.ServeHTTP(w, r)
		default:
			http.Error(w, "agentbox answers only to 127.0.0.1, localhost and *.localhost, not to Host "+strconv.Quote(r.Host),
				http.StatusMisdirectedRequest)
		}
	})
}
```

5. `origins` now also allows IPv6 loopback:

```go
func origins(addr string) []string {
	if addr == "" {
		return nil
	}
	out := []string{"http://" + addr}
	if _, port, err := net.SplitHostPort(addr); err == nil {
		out = append(out, "http://localhost:"+port, "http://127.0.0.1:"+port, "http://[::1]:"+port)
	}
	return out
}
```

6. In `Run`, replace everything from `srv := &api.Server{…}` down to (and including) the `if tl, err := net.Listen("tcp", …) { … } else { … }` block with:

```go
	_ = os.Remove(p.Sock) // stale socket from a crashed daemon (the home lock proves nobody owns it)
	ul, err := net.Listen("unix", p.Sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(p.Sock, 0o600); err != nil {
		ul.Close()
		return err
	}
	// Bind TCP before building the handlers: tests bind port 0, and the box
	// URLs and login links need the port that was actually chosen.
	apiAddr := ""
	tl, err := net.Listen("tcp", settings.APIAddr)
	if err != nil {
		slog.Warn("HTTP API disabled", "addr", settings.APIAddr, "err", err)
		_ = os.Remove(p.APIAddr)
	} else {
		apiAddr = tl.Addr().String()
		if err := os.WriteFile(p.APIAddr, []byte(apiAddr+"\n"), 0o600); err != nil {
			ul.Close()
			tl.Close()
			return err
		}
	}
	dist := UIFS
	if dist == nil {
		dist = ui.Dist()
	}
	codes := ui.NewLoginCodes()
	srv := &api.Server{M: m, Bus: bus, Token: token, Origins: origins(apiAddr), Codes: codes,
		UI: ui.Handler(ui.Options{Token: token, Codes: codes, FS: dist}), APIAddr: apiAddr, Version: Version, UIBuilt: ui.Built(dist)}
	px := proxy.New(proxy.Options{Lookup: m.ProxyTarget, Dashboard: "http://" + apiAddr + "/ui/"})
	base := func(net.Listener) context.Context { return ctx } // cancels SSE streams on shutdown
	servers := []*http.Server{{Handler: srv.Handler(false), BaseContext: base}}
	listeners := []net.Listener{ul}
	if tl != nil {
		servers = append(servers, &http.Server{Handler: Dispatch(px, srv.Handler(true)), BaseContext: base})
		listeners = append(listeners, tl)
	}
```

(The old `base := …`, `servers := …`, `listeners := …` lines are part of what is replaced; keep the `os.WriteFile(p.PID, …)` that follows.) In the shutdown section, after `_ = os.Remove(p.PID)` add `_ = os.Remove(p.APIAddr)`. Update the `slog.Info("daemon started", …)` call to log `"api", apiAddr`.

- [ ] **Step 4: Run the daemon tests**

Run: `go test -race ./internal/daemon/`
Expected: PASS (all, including the pre-existing `TestRunServesAndShutsDown`).

- [ ] **Step 5: Checkpoint**

Run `go vet ./... && go test -race ./internal/daemon/ ./internal/api/`; record "Task 6 complete". Do not commit.

---

### Task 7: Client methods, `agentbox ui`, `url:` in `new`

**Files:**
- Modify: `internal/client/client.go`
- Create: `internal/cli/ui_cmd.go`
- Modify: `internal/cli/root.go` (register the command), `internal/cli/print.go`, `internal/cli/admin_cmds.go` (set `daemon.Version`)
- Test: `internal/cli/cli_test.go`

**Interfaces:**
- Consumes: `api.HealthView`, `api.LoginCode` (Task 5); `daemon.Version`, `daemon.UIFS`, `Paths.APIAddr` (Task 6).
- Produces: `client.HealthInfo(ctx) (api.HealthView, error)`, `client.LoginCode(ctx) (api.LoginCode, error)`; CLI `agentbox ui [--no-open]`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/cli/cli_test.go` (imports: add `"context"`, `"net/http"`, `"testing/fstest"`, `"time"`, `"github.com/agentbox-dev/agentbox/internal/daemon"`):

```go
func TestUIPrintsALoginLinkThatSignsIn(t *testing.T) {
	daemon.UIFS = fstest.MapFS{"index.html": {Data: []byte("<div id=\"root\"></div>")}}
	t.Cleanup(func() { daemon.UIFS = nil })
	home := shortTempHome(t)
	t.Setenv("AGENTBOX_HOME", home)
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("api_addr: 127.0.0.1:0\n"), 0o600)
	p := daemon.PathsFor(home)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go daemon.Run(ctx, p)
	deadline := time.Now().Add(5 * time.Second)
	for !daemon.Ping(p.Sock) {
		if time.Now().After(deadline) {
			t.Fatal("daemon never came up")
		}
		time.Sleep(20 * time.Millisecond)
	}

	code, out, stderr := run("ui", "--no-open")
	if code != 0 || !strings.HasPrefix(out, "dashboard: http://127.0.0.1:") || !strings.Contains(out, "/ui/login?code=") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, stderr)
	}
	link := strings.TrimSpace(strings.TrimPrefix(out, "dashboard: "))
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/ui/" {
		t.Fatalf("login link: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "agentbox_session" {
			session = c
		}
	}
	if session == nil {
		t.Fatal("no session cookie")
	}
	req, _ := http.NewRequest("GET", strings.Split(link, "/ui/login")[0]+"/v1/boxes", nil)
	req.AddCookie(session)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("cookie did not authenticate: %d", resp2.StatusCode)
	}
}

func TestUIExplainsAMissingDashboard(t *testing.T) {
	daemon.UIFS = fstest.MapFS{".gitkeep": {}}
	t.Cleanup(func() { daemon.UIFS = nil })
	home := shortTempHome(t)
	t.Setenv("AGENTBOX_HOME", home)
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("api_addr: 127.0.0.1:0\n"), 0o600)
	p := daemon.PathsFor(home)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go daemon.Run(ctx, p)
	for deadline := time.Now().Add(5 * time.Second); !daemon.Ping(p.Sock); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("daemon never came up")
		}
	}
	code, _, stderr := run("ui", "--no-open")
	if code != 1 || !strings.Contains(stderr, "built without the dashboard") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

func TestPrintBoxShowsURL(t *testing.T) {
	var b bytes.Buffer
	printBox(&b, api.BoxView{Name: "a", State: "running", Dir: "/tmp/x", Branch: "agentbox/a", Ports: map[string]int{"APP_PORT": 20000}, URL: "http://a.localhost:7777/"})
	if !strings.Contains(b.String(), "url     http://a.localhost:7777/") {
		t.Fatalf("no url line:\n%s", b.String())
	}
	b.Reset()
	printBox(&b, api.BoxView{Name: "a", State: "ready"})
	if strings.Contains(b.String(), "url") {
		t.Fatalf("url line without a URL:\n%s", b.String())
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/cli/ -run 'TestUI|TestPrintBoxShowsURL'`
Expected: FAIL — `unknown command "ui"` (exit 2) and `BoxView` has no field `URL` compiles fine after Task 5, so only the `ui` tests fail.

- [ ] **Step 3: Client methods**

Append to `internal/client/client.go`:

```go
func (c *Client) HealthInfo(ctx context.Context) (api.HealthView, error) {
	var h api.HealthView
	return h, c.do(ctx, "GET", "/v1/health", nil, &h)
}

// LoginCode mints a one-shot dashboard sign-in link.
func (c *Client) LoginCode(ctx context.Context) (api.LoginCode, error) {
	var lc api.LoginCode
	return lc, c.do(ctx, "POST", "/v1/ui/login-codes", nil, &lc)
}
```

- [ ] **Step 4: The `ui` command**

Create `internal/cli/ui_cmd.go`:

```go
package cli

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"
)

func (a *app) uiCmd() *cobra.Command {
	var noOpen bool
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Open the web dashboard in your browser",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if err := a.connect(); err != nil {
				return err
			}
			h, err := a.c.HealthInfo(ctx)
			if err != nil {
				return err
			}
			if h.APIAddr == "" {
				return fmt.Errorf("the daemon's HTTP API is disabled (its port is probably in use); see %s and api_addr in %s", a.paths.Log, a.paths.Config)
			}
			if !h.UI {
				return errors.New("this agentbox binary was built without the dashboard; install a release build, or run `make ui` (needs Node 20+) and rebuild")
			}
			lc, err := a.c.LoginCode(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "dashboard: %s\n", lc.URL)
			if noOpen {
				return nil
			}
			if err := openBrowser(lc.URL); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "could not open a browser (%v); open the link above yourself\n", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "print the sign-in link instead of opening a browser")
	return cmd
}

func openBrowser(u string) error {
	var c *exec.Cmd
	if runtime.GOOS == "darwin" {
		c = exec.Command("open", u)
	} else {
		c = exec.Command("xdg-open", u)
	}
	return c.Start()
}
```

Register it in `internal/cli/root.go`: in `root.AddCommand(…)` add `a.uiCmd(),` after `a.doctorCmd(),`.

- [ ] **Step 5: `url:` line and daemon version**

In `internal/cli/print.go`, in `printBox`, after the `ports` line:

```go
	if b.URL != "" {
		fmt.Fprintf(out, "  url     %s\n", b.URL)
	}
```

In `internal/cli/admin_cmds.go`, in the `run` subcommand's `RunE`, before `return daemon.Run(ctx, p)`:

```go
			daemon.Version = Version
```

- [ ] **Step 6: Run the tests**

Run: `go test -race ./internal/cli/ ./internal/client/`
Expected: PASS.

- [ ] **Step 7: Checkpoint**

Run `go vet ./... && go test -race ./...` (everything so far); record "Task 7 complete". Do not commit.

---

### Task 8: `web/` — Vite scaffold, API client, SSE helper, stores, Vitest

**Files:**
- Create: `web/package.json`, `web/tsconfig.json`, `web/vite.config.ts`, `web/index.html`, `web/README.md`
- Create: `web/src/api/types.ts`, `web/src/api/client.ts`, `web/src/api/sse.ts`
- Create: `web/src/store/store.ts`, `web/src/store/boxes.ts`, `web/src/store/route.ts`, `web/src/store/toasts.ts`
- Test: `web/src/store/boxes.test.ts`, `web/src/store/route.test.ts`, `web/src/api/client.test.ts`

**Interfaces:**
- Produces (consumed by Task 9): everything exported below. Task 9 adds `main.tsx` and the components; this task's `npm run check` must pass on its own (no `vite build` yet — `index.html` points at a `main.tsx` that Task 9 creates).
- Node ≥ 20 and npm are required for this task and Task 9 only.

- [ ] **Step 1: Scaffold**

Create `web/package.json`:

```json
{
  "name": "agentbox-dashboard",
  "private": true,
  "version": "0.0.0",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "tsc --noEmit && vite build",
    "check": "tsc --noEmit && vitest run",
    "test": "vitest run"
  },
  "dependencies": {
    "react": "18.3.1",
    "react-dom": "18.3.1"
  },
  "devDependencies": {
    "@types/react": "18.3.12",
    "@types/react-dom": "18.3.1",
    "@vitejs/plugin-react": "4.3.4",
    "typescript": "5.6.3",
    "vite": "5.4.11",
    "vitest": "2.1.8"
  }
}
```

Create `web/tsconfig.json`:

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "moduleResolution": "bundler",
    "jsx": "react-jsx",
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "noFallthroughCasesInSwitch": true,
    "skipLibCheck": true,
    "isolatedModules": true,
    "noEmit": true,
    "types": ["vite/client"]
  },
  "include": ["src"]
}
```

Create `web/vite.config.ts`:

```ts
import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

const outDir = resolve(__dirname, '../internal/ui/dist')

export default defineConfig({
  base: '/ui/',
  plugins: [
    react(),
    {
      // `emptyOutDir` wipes the embed anchor that keeps go:embed valid in a
      // plain checkout; put it back after every build.
      name: 'agentbox-keep-gitkeep',
      closeBundle() {
        writeFileSync(resolve(outDir, '.gitkeep'), '')
      },
    },
  ],
  build: { outDir, emptyOutDir: true },
  server: {
    // Cookies are host-only on 127.0.0.1 and ignore the port, so proxying
    // the API from the Vite dev server keeps the session working.
    proxy: {
      '/v1': 'http://127.0.0.1:7777',
      '/ui/login': 'http://127.0.0.1:7777',
      '/ui/logout': 'http://127.0.0.1:7777',
    },
  },
  test: { environment: 'node', include: ['src/**/*.test.ts'] },
})
```

Create `web/index.html`:

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <meta name="color-scheme" content="light dark" />
    <title>agentbox</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

Create `web/README.md`:

```markdown
# agentbox dashboard

React + TypeScript + Vite. Built into the `agentbox` binary with `go:embed`.

    npm ci
    npm run check   # tsc + vitest
    npm run build   # writes ../internal/ui/dist (then `go build ./cmd/agentbox` embeds it)
    npm run dev     # http://127.0.0.1:5173/ui/ with /v1 proxied to a running daemon on :7777

Sign in during development: run `agentbox ui --no-open`, open the printed link once (it sets the
session cookie on 127.0.0.1, which the Vite dev server shares), then open the dev URL.
```

Run: `cd web && npm install` (this writes `package-lock.json`; keep it — CI uses `npm ci`).
Expected: installs without errors. If a pinned version is not on the registry, pick the closest patch release of the same minor and update `package.json`.

- [ ] **Step 2: Write the failing tests**

Create `web/src/store/boxes.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import type { BoxView } from '../api/types'
import { boxesReducer, initialBoxes, selectBoxes, splitId } from './boxes'

const box = (over: Partial<BoxView>): BoxView => ({
  id: 'r/a', repo: 'r', repo_path: '/r', name: 'a', dir: '/d', branch: 'agentbox/a', state: 'ready',
  ports: {}, ahead: 0, behind: 0, dirty: 0, created_at: '2026-10-09T00:00:00Z', ...over,
})

describe('boxesReducer', () => {
  it('loads a list and selects per repo, sorted by name', () => {
    const s = boxesReducer(initialBoxes, { type: 'load', boxes: [box({ id: 'r/b', name: 'b' }), box({}), box({ id: 'q/z', repo: 'q', name: 'z' })] })
    expect(selectBoxes(s, 'r').map((b) => b.name)).toEqual(['a', 'b'])
    expect(selectBoxes(s, '').map((b) => b.name)).toEqual(['a', 'b', 'z'])
  })

  it('applies box.state to a known box and clears its step on a terminal state', () => {
    let s = boxesReducer(initialBoxes, { type: 'load', boxes: [box({ state: 'creating' })] })
    s = boxesReducer(s, { type: 'event', event: { type: 'box.step', box: 'r/a', data: { step: 'setup' } } })
    expect(s.steps['r/a']).toBe('setup')
    s = boxesReducer(s, { type: 'event', event: { type: 'box.state', box: 'r/a', data: { state: 'running' } } })
    expect(s.byId['r/a'].state).toBe('running')
    expect(s.steps['r/a']).toBeUndefined()
  })

  it('keeps steps and failures for boxes it has not loaded yet', () => {
    let s = boxesReducer(initialBoxes, { type: 'event', event: { type: 'box.step', box: 'r/n', data: { step: 'worktree' } } })
    expect(s.steps['r/n']).toBe('worktree')
    s = boxesReducer(s, { type: 'event', event: { type: 'box.state', box: 'r/n', data: { state: 'failed', error: 'setup failed: boom' } } })
    expect(s.byId['r/n']).toBeUndefined()
    expect(s.steps['r/n']).toBeUndefined() // failed is terminal: the step is done
    expect(s.failures['r/n']).toBe('setup failed: boom')
  })

  it('removes a box on box.removed but remembers why', () => {
    let s = boxesReducer(initialBoxes, { type: 'load', boxes: [box({})] })
    s = boxesReducer(s, { type: 'event', event: { type: 'box.removed', box: 'r/a', data: { error: 'rolled back' } } })
    expect(s.byId['r/a']).toBeUndefined()
    expect(s.failures['r/a']).toBe('rolled back')
    const again = boxesReducer(s, { type: 'load', boxes: [] })
    expect(again.failures).toEqual({})
  })

  it('upsert replaces a box and returns the same state for unknown event types', () => {
    let s = boxesReducer(initialBoxes, { type: 'load', boxes: [box({})] })
    s = boxesReducer(s, { type: 'upsert', box: box({ state: 'stopped', dirty: 3 }) })
    expect(s.byId['r/a']).toMatchObject({ state: 'stopped', dirty: 3 })
    expect(boxesReducer(s, { type: 'event', event: { type: 'nope' as never, box: 'r/a' } })).toBe(s)
  })
})

describe('splitId', () => {
  it('splits on the first slash only', () => {
    expect(splitId('myapp-1a2b3c/feature-x')).toEqual({ repo: 'myapp-1a2b3c', name: 'feature-x' })
    expect(splitId('solo')).toEqual({ repo: '', name: 'solo' })
  })
})
```

Create `web/src/store/route.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { formatRoute, parseRoute, type Route } from './route'

describe('routes', () => {
  it('round-trips every route kind', () => {
    const routes: Route[] = [
      { kind: 'home' },
      { kind: 'repo', repo: 'myapp-1a2b3c' },
      { kind: 'box', repo: 'myapp-1a2b3c', name: 'feature-x' },
    ]
    for (const r of routes) expect(parseRoute(formatRoute(r))).toEqual(r)
  })

  it('tolerates missing hashes and junk', () => {
    expect(parseRoute('')).toEqual({ kind: 'home' })
    expect(parseRoute('#')).toEqual({ kind: 'home' })
    expect(parseRoute('#/r')).toEqual({ kind: 'home' })
    expect(parseRoute('#/r/x/b')).toEqual({ kind: 'repo', repo: 'x' })
    expect(parseRoute('#/what/ever')).toEqual({ kind: 'home' })
  })

  it('decodes encoded segments', () => {
    expect(parseRoute('#/r/a%2Fb/b/c')).toEqual({ kind: 'box', repo: 'a/b', name: 'c' })
    expect(formatRoute({ kind: 'repo', repo: 'a/b' })).toBe('#/r/a%2Fb')
  })
})
```

Create `web/src/api/client.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { ApiError, parseError } from './client'

describe('parseError', () => {
  it('reads the daemon error shape', () => {
    const e = parseError(409, { error: { code: 'unmerged_work', message: 'box a has 2 unmerged commits', hint: 'merge first' } })
    expect(e).toBeInstanceOf(ApiError)
    expect(e.status).toBe(409)
    expect(e.code).toBe('unmerged_work')
    expect(e.message).toBe('box a has 2 unmerged commits')
    expect(e.hint).toBe('merge first')
    expect(e.title).toBe('request failed')
  })

  it('names the step in the title', () => {
    expect(parseError(500, { error: { code: 'internal', message: 'x', step: 'db clone' } }).title).toBe('db clone failed')
  })

  it('falls back when the body is not the error shape', () => {
    const e = parseError(502, 'Bad Gateway')
    expect(e.code).toBe('internal')
    expect(e.message).toBe('HTTP 502')
  })
})
```

- [ ] **Step 3: Run them to verify they fail**

Run: `cd web && npm run check`
Expected: FAIL — `tsc` cannot resolve `./boxes`, `./route`, `./client`.

- [ ] **Step 4: Types and API client**

Create `web/src/api/types.ts` (mirrors `internal/api/types.go` and `internal/box`):

```ts
export type BoxState = 'creating' | 'ready' | 'running' | 'stopped' | 'removing' | 'failed'

export interface RepoView {
  id: string
  path: string
}

export interface BoxView {
  id: string
  repo: string
  repo_path: string
  name: string
  dir: string
  branch: string
  state: BoxState
  ports: Record<string, number>
  db_name?: string
  db_url?: string
  pid?: number
  last_step?: string
  last_error?: string
  base?: string
  ahead: number
  behind: number
  dirty: number
  env?: Record<string, string>
  created_at: string
  url?: string
}

export interface Created {
  id: string
  repo: string
  name: string
}

export interface MergeResult {
  into: string
}

export interface Finding {
  kind: string
  detail: string
  fixed: boolean
}

export interface Health {
  ok: boolean
  pid: number
  version: string
  api_addr: string
  ui: boolean
}

export interface ApiErrorBody {
  error: { code: string; message: string; step?: string; hint?: string }
}

export type BoxEventType = 'box.created' | 'box.state' | 'box.step' | 'box.removed'

export interface BoxEvent {
  type: BoxEventType
  box: string // "<repo>/<name>"
  data?: Record<string, unknown>
}
```

Create `web/src/api/client.ts`:

```ts
import type { ApiErrorBody, BoxView, Created, Finding, Health, MergeResult, RepoView } from './types'

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public step?: string,
    public hint?: string,
  ) {
    super(message)
  }

  get title(): string {
    return this.step ? `${this.step} failed` : 'request failed'
  }
}

export class SignedOut extends Error {
  constructor() {
    super('signed out')
  }
}

export function parseError(status: number, body: unknown): ApiError {
  const e = (body as Partial<ApiErrorBody> | null)?.error
  if (e && typeof e.message === 'string') return new ApiError(status, e.code ?? 'internal', e.message, e.step, e.hint)
  return new ApiError(status, 'internal', `HTTP ${status}`)
}

let signedOutHandler: () => void = () => {}
/** The app registers what to do when the daemon answers 401 (show the sign-in screen). */
export function onSignedOut(fn: () => void) {
  signedOutHandler = fn
}

type As = 'json' | 'text' | 'none'

async function request<T>(method: string, path: string, body?: unknown, as: As = 'json'): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body !== undefined ? { 'content-type': 'application/json' } : {},
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: 'same-origin',
  })
  if (res.status === 401) {
    signedOutHandler()
    throw new SignedOut()
  }
  if (!res.ok) {
    let parsed: unknown = null
    try {
      parsed = await res.json()
    } catch {
      /* not JSON */
    }
    throw parseError(res.status, parsed)
  }
  if (as === 'none' || res.status === 204) return undefined as T
  if (as === 'text') return (await res.text()) as T
  return (await res.json()) as T
}

const enc = encodeURIComponent
const boxPath = (repo: string, name: string) => `/v1/repos/${enc(repo)}/boxes/${enc(name)}`

export const api = {
  health: () => request<Health>('GET', '/v1/health'),
  repos: () => request<RepoView[]>('GET', '/v1/repos'),
  boxes: (repo = '') => request<BoxView[]>('GET', `/v1/boxes?repo=${enc(repo)}`),
  box: (repo: string, name: string) => request<BoxView>('GET', boxPath(repo, name)),
  create: (opts: { repo_path: string; name: string; from?: string; no_dev?: boolean }) =>
    request<Created>('POST', '/v1/boxes', opts),
  start: (repo: string, name: string) => request<void>('POST', `${boxPath(repo, name)}/start`, undefined, 'none'),
  stop: (repo: string, name: string) => request<void>('POST', `${boxPath(repo, name)}/stop`, undefined, 'none'),
  merge: (repo: string, name: string, opts: { into?: string; squash?: boolean }) =>
    request<MergeResult>('POST', `${boxPath(repo, name)}/merge`, opts),
  remove: (repo: string, name: string, opts: { force?: boolean; keep_branch?: boolean }) =>
    request<void>(
      'DELETE',
      `${boxPath(repo, name)}?force=${opts.force ? 'true' : 'false'}&keep_branch=${opts.keep_branch ? 'true' : 'false'}`,
      undefined,
      'none',
    ),
  diff: (repo: string, name: string, against: string) =>
    request<string>('GET', `${boxPath(repo, name)}/diff?against=${enc(against)}`, undefined, 'text'),
  doctor: (repo: string, fix: boolean) => request<Finding[]>(fix ? 'POST' : 'GET', `/v1/repos/${enc(repo)}/doctor`),
  refreshDB: (repo: string, terminate: boolean) =>
    request<void>('POST', `/v1/repos/${enc(repo)}/db/refresh?terminate=${terminate ? 'true' : 'false'}`, undefined, 'none'),
  logsURL: (repo: string, name: string, tail: number) => `${boxPath(repo, name)}/logs?follow=true&tail=${tail}`,
  eventsURL: '/v1/events',
}

/** A one-line description of any thrown value, for toasts and inline errors. */
export function describe(e: unknown): string {
  if (e instanceof ApiError) return e.hint ? `${e.message} — ${e.hint}` : e.message
  if (e instanceof Error) return e.message
  return String(e)
}
```

Create `web/src/api/sse.ts`:

```ts
export type SSEState = 'connecting' | 'open' | 'reconnecting'

export interface SSEOptions {
  url: string
  events: string[]
  onEvent: (type: string, data: unknown) => void
  onState?: (s: SSEState) => void
  /** Called after a dropped stream comes back; callers reload what they may have missed. */
  onReconnect?: () => void
}

/** Opens an EventSource that reconnects with 1s→16s backoff. Returns a close function. */
export function connectSSE(o: SSEOptions): () => void {
  let es: EventSource | null = null
  let closed = false
  let attempt = 0
  let everOpened = false
  let timer: ReturnType<typeof setTimeout> | undefined

  const open = () => {
    if (closed) return
    o.onState?.(attempt === 0 ? 'connecting' : 'reconnecting')
    es = new EventSource(o.url)
    es.onopen = () => {
      const was = everOpened
      everOpened = true
      attempt = 0
      o.onState?.('open')
      if (was) o.onReconnect?.()
    }
    for (const t of o.events) {
      es.addEventListener(t, (ev) => {
        try {
          o.onEvent(t, JSON.parse((ev as MessageEvent).data))
        } catch {
          /* malformed frame: ignore */
        }
      })
    }
    es.onerror = () => {
      es?.close()
      es = null
      if (closed) return
      const delay = Math.min(16_000, 1_000 * 2 ** attempt)
      attempt++
      o.onState?.('reconnecting')
      timer = setTimeout(open, delay)
    }
  }
  open()
  return () => {
    closed = true
    clearTimeout(timer)
    es?.close()
  }
}
```

- [ ] **Step 5: Stores**

Create `web/src/store/store.ts`:

```ts
import { useSyncExternalStore } from 'react'

type Listener = () => void

/** A tiny reducer store with a React hook. */
export function createStore<S, A>(reducer: (s: S, a: A) => S, initial: S) {
  let state = initial
  const listeners = new Set<Listener>()
  const get = () => state
  const subscribe = (l: Listener) => {
    listeners.add(l)
    return () => {
      listeners.delete(l)
    }
  }
  const dispatch = (a: A) => {
    const next = reducer(state, a)
    if (next !== state) {
      state = next
      listeners.forEach((l) => l())
    }
  }
  const use = () => useSyncExternalStore(subscribe, get, get)
  return { get, subscribe, dispatch, use }
}
```

Create `web/src/store/boxes.ts`:

```ts
import type { BoxEvent, BoxState, BoxView } from '../api/types'
import { createStore } from './store'

export interface BoxesState {
  byId: Record<string, BoxView>
  steps: Record<string, string> // box id → current `new` step, while creating
  failures: Record<string, string> // box id → error text from a failed/rolled-back creation
}

export type BoxesAction =
  | { type: 'load'; boxes: BoxView[] }
  | { type: 'upsert'; box: BoxView }
  | { type: 'event'; event: BoxEvent }

export const initialBoxes: BoxesState = { byId: {}, steps: {}, failures: {} }

const terminal = new Set<string>(['ready', 'running', 'stopped', 'failed'])

function without<T>(m: Record<string, T>, key: string): Record<string, T> {
  if (!(key in m)) return m
  const copy = { ...m }
  delete copy[key]
  return copy
}

export function boxesReducer(s: BoxesState, a: BoxesAction): BoxesState {
  switch (a.type) {
    case 'load': {
      const byId: Record<string, BoxView> = {}
      for (const b of a.boxes) byId[b.id] = b
      return { byId, steps: s.steps, failures: {} }
    }
    case 'upsert':
      return { ...s, byId: { ...s.byId, [a.box.id]: a.box } }
    case 'event': {
      const { type, box: id, data } = a.event
      const error = typeof data?.error === 'string' ? data.error : undefined
      switch (type) {
        case 'box.step':
          return { ...s, steps: { ...s.steps, [id]: String(data?.step ?? '') } }
        case 'box.state': {
          const state = String(data?.state ?? '') as BoxState
          const steps = terminal.has(state) ? without(s.steps, id) : s.steps
          const failures = error ? { ...s.failures, [id]: error } : s.failures
          const cur = s.byId[id]
          if (!cur) return { ...s, steps, failures }
          const last_error = typeof data?.last_error === 'string' ? data.last_error : error ?? cur.last_error
          return { byId: { ...s.byId, [id]: { ...cur, state, last_error } }, steps, failures }
        }
        case 'box.removed':
          return { byId: without(s.byId, id), steps: without(s.steps, id), failures: error ? { ...s.failures, [id]: error } : s.failures }
        case 'box.created':
          return s
        default:
          return s
      }
    }
  }
}

export const boxes = createStore(boxesReducer, initialBoxes)
export const useBoxes = boxes.use

export function splitId(id: string): { repo: string; name: string } {
  const i = id.indexOf('/')
  return i < 0 ? { repo: '', name: id } : { repo: id.slice(0, i), name: id.slice(i + 1) }
}

export function selectBoxes(s: BoxesState, repo: string): BoxView[] {
  return Object.values(s.byId)
    .filter((b) => !repo || b.repo === repo)
    .sort((a, b) => a.name.localeCompare(b.name))
}
```

Create `web/src/store/route.ts`:

```ts
import { useMemo, useSyncExternalStore } from 'react'

export type Route = { kind: 'home' } | { kind: 'repo'; repo: string } | { kind: 'box'; repo: string; name: string }

export function parseRoute(hash: string): Route {
  const parts = hash
    .replace(/^#\/?/, '')
    .split('/')
    .filter(Boolean)
    .map(decodeURIComponent)
  if (parts[0] === 'r' && parts[1]) {
    if (parts[2] === 'b' && parts[3]) return { kind: 'box', repo: parts[1], name: parts[3] }
    return { kind: 'repo', repo: parts[1] }
  }
  return { kind: 'home' }
}

export function formatRoute(r: Route): string {
  switch (r.kind) {
    case 'home':
      return '#/'
    case 'repo':
      return `#/r/${encodeURIComponent(r.repo)}`
    case 'box':
      return `#/r/${encodeURIComponent(r.repo)}/b/${encodeURIComponent(r.name)}`
  }
}

export function navigate(r: Route) {
  location.hash = formatRoute(r)
}

const subscribe = (l: () => void) => {
  addEventListener('hashchange', l)
  return () => removeEventListener('hashchange', l)
}
const getHash = () => location.hash
const getServerHash = () => ''

export function useRoute(): Route {
  const hash = useSyncExternalStore(subscribe, getHash, getServerHash)
  return useMemo(() => parseRoute(hash), [hash])
}
```

Create `web/src/store/toasts.ts`:

```ts
import { ApiError, SignedOut } from '../api/client'
import { createStore } from './store'

export interface Toast {
  id: number
  kind: 'error' | 'ok'
  title: string
  message?: string
  hint?: string
}

type Action = { type: 'add'; toast: Toast } | { type: 'dismiss'; id: number }

function reducer(s: Toast[], a: Action): Toast[] {
  switch (a.type) {
    case 'add':
      return [...s.slice(-4), a.toast]
    case 'dismiss':
      return s.filter((t) => t.id !== a.id)
  }
}

const store = createStore(reducer, [] as Toast[])
let seq = 0

function add(t: Omit<Toast, 'id'>, ttlMs: number) {
  const id = ++seq
  store.dispatch({ type: 'add', toast: { ...t, id } })
  setTimeout(() => store.dispatch({ type: 'dismiss', id }), ttlMs)
}

export const toasts = {
  use: store.use,
  dismiss: (id: number) => store.dispatch({ type: 'dismiss', id }),
  ok: (title: string, message?: string) => add({ kind: 'ok', title, message }, 3_000),
  /** Shows any thrown value; a 401 is handled by the sign-in screen and not shown. */
  error: (e: unknown) => {
    if (e instanceof SignedOut) return
    if (e instanceof ApiError) add({ kind: 'error', title: e.title, message: e.message, hint: e.hint }, 8_000)
    else add({ kind: 'error', title: 'error', message: e instanceof Error ? e.message : String(e) }, 8_000)
  },
}
```

- [ ] **Step 6: Run the checks**

Run: `cd web && npm run check`
Expected: `tsc` clean; Vitest: 3 files, 11 tests pass.

- [ ] **Step 7: Checkpoint**

Record "Task 8 complete" with the Vitest summary line. Confirm `git status` shows `web/` (including `package-lock.json`) and no `node_modules`. Do not commit.

---

### Task 9: `web/` — the dashboard UI, styles, build into the binary

**Files:**
- Create: `web/src/main.tsx`, `web/src/styles.css`
- Create: `web/src/ui/bits.tsx`, `web/src/ui/confirm.tsx`, `web/src/ui/Toasts.tsx`, `web/src/ui/SignIn.tsx`, `web/src/ui/TopBar.tsx`, `web/src/ui/RunButtons.tsx`, `web/src/ui/BoxTable.tsx`, `web/src/ui/NewBoxDialog.tsx`, `web/src/ui/LogView.tsx`, `web/src/ui/DiffView.tsx`, `web/src/ui/DoctorPanel.tsx`, `web/src/ui/BoxPage.tsx`, `web/src/ui/App.tsx`

**Interfaces:**
- Consumes everything from Task 8 (`api`, `ApiError`, `describe`, `onSignedOut`, `connectSSE`, `boxes`/`useBoxes`/`selectBoxes`/`splitId`, `useRoute`/`navigate`/`formatRoute`, `toasts`).
- Produces: the built dashboard in `internal/ui/dist/` (gitignored) that `ui.Dist()` embeds.

This task has no unit tests of its own (the logic was tested in Task 8); its gate is `npm run build` succeeding, the Go suite still passing, and the smoke test in Step 5.

- [ ] **Step 1: Shared bits, toasts, confirm, sign-in**

Create `web/src/ui/bits.tsx`:

```tsx
import { useEffect, type ReactNode } from 'react'

export function StatePill({ state }: { state: string }) {
  return <span className={`pill pill-${state}`}>{state}</span>
}

export function formatPorts(p: Record<string, number>): string {
  const keys = Object.keys(p).sort()
  return keys.length ? keys.map((k) => `${k}=${p[k]}`).join(' ') : '—'
}

export function basename(p: string): string {
  const parts = p.split('/').filter(Boolean)
  return parts[parts.length - 1] ?? p
}

/** Runs fn when `key` is pressed while no text field has focus. */
export function useKey(key: string, fn: () => void, enabled = true) {
  useEffect(() => {
    if (!enabled) return
    const h = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null
      const typing = !!t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)
      if (e.key === key && !typing && !e.metaKey && !e.ctrlKey && !e.altKey) {
        e.preventDefault()
        fn()
      }
    }
    addEventListener('keydown', h)
    return () => removeEventListener('keydown', h)
  }, [key, fn, enabled])
}

export function Modal({ title, onClose, children, wide }: { title: string; onClose: () => void; children: ReactNode; wide?: boolean }) {
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    addEventListener('keydown', h)
    return () => removeEventListener('keydown', h)
  }, [onClose])
  return (
    <div
      className="backdrop"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div className={wide ? 'modal wide' : 'modal'} role="dialog" aria-modal="true" aria-label={title}>
        <header className="modal-head">
          <h2>{title}</h2>
          <button className="icon" aria-label="Close" onClick={onClose}>
            ×
          </button>
        </header>
        <div className="modal-body">{children}</div>
      </div>
    </div>
  )
}
```

Create `web/src/ui/confirm.tsx`:

```tsx
import type { ReactNode } from 'react'
import { Modal } from './bits'

export function ConfirmDialog({
  title,
  children,
  confirmLabel = 'Confirm',
  danger,
  busy,
  onConfirm,
  onCancel,
}: {
  title: string
  children: ReactNode
  confirmLabel?: string
  danger?: boolean
  busy?: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <Modal title={title} onClose={onCancel}>
      <div className="stack">{children}</div>
      <div className="row end">
        <button onClick={onCancel} disabled={busy}>
          Cancel
        </button>
        <button className={danger ? 'danger' : 'primary'} onClick={onConfirm} disabled={busy} autoFocus>
          {confirmLabel}
        </button>
      </div>
    </Modal>
  )
}
```

Create `web/src/ui/Toasts.tsx`:

```tsx
import { toasts } from '../store/toasts'

export function Toasts() {
  const list = toasts.use()
  return (
    <div className="toasts" aria-live="polite">
      {list.map((t) => (
        <div key={t.id} className={`toast toast-${t.kind}`}>
          <div>
            <strong>{t.title}</strong>
            {t.message && <div>{t.message}</div>}
            {t.hint && <div className="muted">hint: {t.hint}</div>}
          </div>
          <button className="icon" aria-label="Dismiss" onClick={() => toasts.dismiss(t.id)}>
            ×
          </button>
        </div>
      ))}
    </div>
  )
}
```

Create `web/src/ui/SignIn.tsx` (a plain form: the daemon sets the cookie and redirects, no script needed):

```tsx
export function SignIn() {
  return (
    <div className="signin">
      <p className="brand">agentbox</p>
      <h1>Sign in</h1>
      <p>
        Run <code>agentbox ui</code> in a terminal to open a signed-in dashboard.
      </p>
      <p className="muted">
        Or paste the contents of <code>~/.agentbox/token</code>:
      </p>
      <form method="post" action="/ui/login" className="row">
        <input name="token" placeholder="token" autoComplete="off" spellCheck={false} />
        <button className="primary">Sign in</button>
      </form>
    </div>
  )
}
```

- [ ] **Step 2: Top bar, run buttons, box table, new-box dialog**

Create `web/src/ui/TopBar.tsx`:

```tsx
import type { SSEState } from '../api/sse'
import type { Health, RepoView } from '../api/types'
import { basename } from './bits'

interface Props {
  health: Health | null
  sse: SSEState
  repos: RepoView[]
  repo: string
  onRepo: (id: string) => void
  onNew: () => void
  onDoctor: () => void
  onRefreshDB: () => void
}

export function TopBar(p: Props) {
  const conn = p.sse === 'open' ? 'live' : p.sse
  return (
    <header className="topbar">
      <a className="brand" href="#/">
        agentbox
      </a>
      <span className={`pill pill-conn-${p.sse}`} title={p.health ? `daemon pid ${p.health.pid} · ${p.health.version}` : ''}>
        {conn}
      </span>
      <label className="repo-select">
        <span className="muted">repo</span>
        <select value={p.repo} onChange={(e) => p.onRepo(e.target.value)} disabled={p.repos.length === 0}>
          {p.repos.length === 0 && <option value="">no repos yet</option>}
          {p.repos.map((r) => (
            <option key={r.id} value={r.id} title={r.path}>
              {basename(r.path)}
            </option>
          ))}
        </select>
      </label>
      <span className="spacer" />
      <button onClick={p.onDoctor} disabled={!p.repo}>
        Doctor
      </button>
      <button onClick={p.onRefreshDB} disabled={!p.repo}>
        Refresh DB
      </button>
      <button className="primary" onClick={p.onNew} disabled={!p.repo}>
        New box <kbd>n</kbd>
      </button>
      <form method="post" action="/ui/logout">
        <button className="quiet">Sign out</button>
      </form>
    </header>
  )
}
```

Create `web/src/ui/RunButtons.tsx`:

```tsx
import { useState } from 'react'
import { api } from '../api/client'
import type { BoxView } from '../api/types'
import { toasts } from '../store/toasts'

/** Start/Stop for one box, with an in-flight guard. */
export function RunButtons({ box }: { box: BoxView }) {
  const [busy, setBusy] = useState(false)
  const act = async (fn: () => Promise<unknown>, done: string) => {
    setBusy(true)
    try {
      await fn()
      toasts.ok(done)
    } catch (e) {
      toasts.error(e)
    } finally {
      setBusy(false)
    }
  }
  if (box.state === 'running') {
    return (
      <button disabled={busy} onClick={() => act(() => api.stop(box.repo, box.name), `stopped ${box.name}`)}>
        Stop
      </button>
    )
  }
  if (box.state === 'ready' || box.state === 'stopped') {
    return (
      <button disabled={busy} onClick={() => act(() => api.start(box.repo, box.name), `started ${box.name}`)}>
        Start
      </button>
    )
  }
  return null
}
```

Create `web/src/ui/BoxTable.tsx`:

```tsx
import type { BoxView, RepoView } from '../api/types'
import { formatRoute } from '../store/route'
import { RunButtons } from './RunButtons'
import { StatePill, formatPorts } from './bits'

interface Props {
  boxes: BoxView[]
  steps: Record<string, string>
  repo: RepoView
  onOpen: (b: BoxView) => void
}

export function BoxTable({ boxes, steps, repo, onOpen }: Props) {
  if (boxes.length === 0) {
    return (
      <div className="empty">
        <p>No boxes in this repo yet.</p>
        <pre className="copy">
          cd {repo.path}
          {'\n'}agentbox new feature-x
        </pre>
      </div>
    )
  }
  return (
    <div className="table-wrap">
      <p className="muted path">{repo.path}</p>
      <table className="boxes">
        <thead>
          <tr>
            <th>Name</th>
            <th>State</th>
            <th>URL</th>
            <th className="secondary">Ports</th>
            <th className="num">Ahead</th>
            <th className="num secondary">Behind</th>
            <th className="num">Dirty</th>
            <th aria-label="Actions"></th>
          </tr>
        </thead>
        <tbody>
          {boxes.map((b) => (
            <tr key={b.id}>
              <td>
                <a className="mono strong" href={formatRoute({ kind: 'box', repo: b.repo, name: b.name })}>
                  {b.name}
                </a>
              </td>
              <td>
                <StatePill state={b.state} />
                {steps[b.id] && <span className="muted step"> {steps[b.id]}…</span>}
                {b.state === 'failed' && b.last_error && <span className="error step"> {b.last_error}</span>}
              </td>
              <td>
                {b.url ? (
                  <a className="mono" href={b.url} target="_blank" rel="noreferrer">
                    {b.url.replace(/^http:\/\//, '').replace(/\/$/, '')}
                  </a>
                ) : (
                  <span className="muted">—</span>
                )}
              </td>
              <td className="mono secondary">{formatPorts(b.ports)}</td>
              <td className="num">{b.ahead}</td>
              <td className="num secondary">{b.behind}</td>
              <td className="num">{b.dirty}</td>
              <td className="actions">
                <RunButtons box={b} />
                <button onClick={() => onOpen(b)}>Open</button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
```

Create `web/src/ui/NewBoxDialog.tsx`:

```tsx
import { useEffect, useState, type FormEvent } from 'react'
import { api, describe } from '../api/client'
import type { RepoView } from '../api/types'
import { useBoxes } from '../store/boxes'
import { navigate } from '../store/route'
import { toasts } from '../store/toasts'
import { Modal } from './bits'

export function NewBoxDialog({ repo, onClose }: { repo: RepoView; onClose: () => void }) {
  const [name, setName] = useState('')
  const [from, setFrom] = useState('')
  const [noDev, setNoDev] = useState(false)
  const [id, setId] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const state = useBoxes()
  const box = id ? state.byId[id] : undefined
  const step = id ? state.steps[id] : undefined
  const failure = id ? state.failures[id] : undefined

  useEffect(() => {
    if (box && (box.state === 'running' || box.state === 'ready')) {
      toasts.ok(`box ${box.name} is ${box.state}`)
      navigate({ kind: 'box', repo: box.repo, name: box.name })
      onClose()
    }
  }, [box, onClose])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError(null)
    try {
      const c = await api.create({ repo_path: repo.path, name: name.trim(), from: from.trim() || undefined, no_dev: noDev || undefined })
      setId(c.id)
    } catch (err) {
      setError(describe(err))
    }
  }

  return (
    <Modal title="New box" onClose={onClose}>
      {!id ? (
        <form onSubmit={submit} className="form">
          <label>
            Name
            <input autoFocus value={name} onChange={(e) => setName(e.target.value)} pattern="[a-z0-9][a-z0-9-]{0,30}" required placeholder="feature-x" spellCheck={false} />
          </label>
          <label>
            Start from
            <input value={from} onChange={(e) => setFrom(e.target.value)} placeholder="HEAD (a branch, tag or commit)" spellCheck={false} />
          </label>
          <label className="check">
            <input type="checkbox" checked={noDev} onChange={(e) => setNoDev(e.target.checked)} /> Don't start the dev server
          </label>
          {error && <p className="error">{error}</p>}
          <div className="row end">
            <button type="button" onClick={onClose}>
              Cancel
            </button>
            <button className="primary" type="submit">
              Create
            </button>
          </div>
        </form>
      ) : (
        <div className="stack">
          <p>
            Creating <span className="mono strong">{name}</span>
            {!failure && !box?.last_error && <span className="muted"> · {step ?? 'starting'}…</span>}
          </p>
          {failure && <p className="error">{failure}</p>}
          {!failure && box?.state === 'failed' && <p className="error">{box.last_error}</p>}
          {!failure && box?.state === 'stopped' && <p className="error">The dev server keeps crashing: {box.last_error}</p>}
          <div className="row end">
            <button onClick={onClose}>Close</button>
          </div>
        </div>
      )}
    </Modal>
  )
}
```

- [ ] **Step 3: Logs, diff, doctor, box page**

Create `web/src/ui/LogView.tsx`:

```tsx
import { useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import { connectSSE, type SSEState } from '../api/sse'

const MAX_LINES = 5_000

export function LogView({ repo, name }: { repo: string; name: string }) {
  const [lines, setLines] = useState<string[]>([])
  const [conn, setConn] = useState<SSEState>('connecting')
  const [follow, setFollow] = useState(true)
  const pre = useRef<HTMLPreElement>(null)

  useEffect(() => {
    setLines([])
    return connectSSE({
      url: api.logsURL(repo, name, 500),
      events: ['line'],
      onState: setConn,
      onEvent: (_t, data) =>
        setLines((ls) => {
          const next = ls.length >= MAX_LINES ? ls.slice(ls.length - MAX_LINES + 1) : ls.slice()
          next.push(String(data))
          return next
        }),
    })
  }, [repo, name])

  useEffect(() => {
    if (follow && pre.current) pre.current.scrollTop = pre.current.scrollHeight
  }, [lines, follow])

  const onScroll = () => {
    const el = pre.current
    if (!el) return
    setFollow(el.scrollHeight - el.scrollTop - el.clientHeight < 8)
  }

  return (
    <div className="logs">
      <div className="row">
        <span className="muted">
          {lines.length} lines · {conn === 'open' ? 'streaming' : conn}
        </span>
        <span className="spacer" />
        {!follow && <button onClick={() => setFollow(true)}>Follow</button>}
        <button onClick={() => setLines([])}>Clear</button>
      </div>
      <pre ref={pre} onScroll={onScroll} className="log">
        {lines.join('\n')}
      </pre>
    </div>
  )
}
```

Create `web/src/ui/DiffView.tsx`:

```tsx
import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { api, describe } from '../api/client'

const MAX_BYTES = 1_000_000

export function DiffView({ repo, name, base }: { repo: string; name: string; base?: string }) {
  const [against, setAgainst] = useState(base ?? '')
  const [diff, setDiff] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const load = useCallback(
    async (a: string) => {
      setLoading(true)
      setError(null)
      try {
        setDiff(await api.diff(repo, name, a))
      } catch (e) {
        setDiff(null)
        setError(describe(e))
      } finally {
        setLoading(false)
      }
    },
    [repo, name],
  )

  useEffect(() => {
    void load(base ?? '')
  }, [load, base])

  const submit = (e: FormEvent) => {
    e.preventDefault()
    void load(against.trim())
  }

  const truncated = diff !== null && diff.length > MAX_BYTES
  const shown = truncated ? diff!.slice(0, MAX_BYTES) : diff

  return (
    <div className="diff">
      <form onSubmit={submit} className="row">
        <label className="inline">
          against
          <input value={against} onChange={(e) => setAgainst(e.target.value)} placeholder={base || 'main checkout branch'} spellCheck={false} />
        </label>
        <button disabled={loading}>Compare</button>
      </form>
      {error && <p className="error">{error}</p>}
      {shown === '' && <p className="muted">No changes.</p>}
      {truncated && <p className="muted">Diff truncated to 1 MB — use <code>agentbox exec {name} -- git diff</code> for the rest.</p>}
      {shown && (
        <pre className="diff-body">
          {shown.split('\n').map((l, i) => (
            <span key={i} className={lineClass(l)}>
              {l}
              {'\n'}
            </span>
          ))}
        </pre>
      )}
    </div>
  )
}

function lineClass(l: string): string {
  if (l.startsWith('+++') || l.startsWith('---')) return 'd-file'
  if (l.startsWith('diff --git')) return 'd-head'
  if (l.startsWith('@@')) return 'd-hunk'
  if (l.startsWith('+')) return 'd-add'
  if (l.startsWith('-')) return 'd-del'
  return ''
}
```

Create `web/src/ui/DoctorPanel.tsx`:

```tsx
import { useCallback, useEffect, useState } from 'react'
import { api, describe } from '../api/client'
import type { Finding, RepoView } from '../api/types'
import { Modal } from './bits'

export function DoctorPanel({ repo, onClose }: { repo: RepoView; onClose: () => void }) {
  const [findings, setFindings] = useState<Finding[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const run = useCallback(
    async (fix: boolean) => {
      setBusy(true)
      setError(null)
      try {
        setFindings(await api.doctor(repo.id, fix))
      } catch (e) {
        setError(describe(e))
      } finally {
        setBusy(false)
      }
    },
    [repo.id],
  )

  useEffect(() => {
    void run(false)
  }, [run])

  const unfixed = findings?.filter((f) => !f.fixed).length ?? 0

  return (
    <Modal title="Doctor" onClose={onClose} wide>
      <p className="muted">{repo.path}</p>
      {error && <p className="error">{error}</p>}
      {findings === null && !error && <p className="muted">Checking…</p>}
      {findings?.length === 0 && <p className="ok">✓ No problems found.</p>}
      {findings && findings.length > 0 && (
        <ul className="findings">
          {findings.map((f, i) => (
            <li key={i} className={f.fixed ? 'fixed' : ''}>
              <span className="pill pill-kind">{f.kind}</span> {f.fixed ? '✓ fixed · ' : ''}
              {f.detail}
            </li>
          ))}
        </ul>
      )}
      <div className="row end">
        <button onClick={() => run(false)} disabled={busy}>
          Re-check
        </button>
        <button className="primary" onClick={() => run(true)} disabled={busy || unfixed === 0}>
          Fix what is safe
        </button>
      </div>
    </Modal>
  )
}
```

Create `web/src/ui/BoxPage.tsx`:

```tsx
import { Fragment, useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { ApiError, api } from '../api/client'
import type { BoxView } from '../api/types'
import { boxes, useBoxes } from '../store/boxes'
import { toasts } from '../store/toasts'
import { DiffView } from './DiffView'
import { LogView } from './LogView'
import { RunButtons } from './RunButtons'
import { StatePill, formatPorts } from './bits'
import { ConfirmDialog } from './confirm'

type Tab = 'overview' | 'logs' | 'diff' | 'danger'
const TABS: Tab[] = ['overview', 'logs', 'diff', 'danger']

export function BoxPage({ repo, name, onBack }: { repo: string; name: string; onBack: () => void }) {
  const state = useBoxes()
  const id = `${repo}/${name}`
  const box = state.byId[id]
  const [tab, setTab] = useState<Tab>('overview')
  const [missing, setMissing] = useState(false)

  useEffect(() => {
    if (box) return
    api
      .box(repo, name)
      .then((b) => boxes.dispatch({ type: 'upsert', box: b }))
      .catch(() => setMissing(true))
  }, [box, repo, name])

  if (!box) {
    return (
      <section className="page">
        <p className="muted">{missing ? `No box called ${name} in this repo.` : 'Loading…'}</p>
        <button onClick={onBack}>Back to boxes</button>
      </section>
    )
  }

  return (
    <section className="page">
      <header className="page-head">
        <button className="quiet" onClick={onBack}>
          ← boxes
        </button>
        <h1 className="mono">{box.name}</h1>
        <StatePill state={box.state} />
        {state.steps[id] && <span className="muted">{state.steps[id]}…</span>}
        <span className="spacer" />
        {box.url && (
          <a className="button" href={box.url} target="_blank" rel="noreferrer">
            Open app ↗
          </a>
        )}
        <RunButtons box={box} />
      </header>
      <nav className="tabs" role="tablist">
        {TABS.map((t) => (
          <button key={t} role="tab" aria-selected={tab === t} className={tab === t ? 'tab active' : 'tab'} onClick={() => setTab(t)}>
            {t}
          </button>
        ))}
      </nav>
      {tab === 'overview' && <Overview box={box} />}
      {tab === 'logs' && <LogView repo={repo} name={name} />}
      {tab === 'diff' && <DiffView repo={repo} name={name} base={box.base} />}
      {tab === 'danger' && <Danger box={box} onRemoved={onBack} />}
    </section>
  )
}

function Overview({ box }: { box: BoxView }) {
  const rows: [string, ReactNode][] = [
    ['dir', <code>{box.dir}</code>],
    ['branch', <code>{box.branch}</code>],
    ['base', box.base ?? '—'],
    ['ahead / behind', `${box.ahead} / ${box.behind}`],
    ['dirty files', box.dirty],
    ['ports', <code>{formatPorts(box.ports)}</code>],
    [
      'url',
      box.url ? (
        <a href={box.url} target="_blank" rel="noreferrer">
          {box.url}
        </a>
      ) : (
        '—'
      ),
    ],
    ['database', box.db_name ?? '—'],
    ['pid', box.pid ?? '—'],
    ['created', new Date(box.created_at).toLocaleString()],
  ]
  if (box.last_error) rows.push(['last error', <span className="error">{box.last_error}</span>])
  const env = Object.entries(box.env ?? {}).sort(([a], [b]) => a.localeCompare(b))
  return (
    <div className="stack">
      <dl className="facts">
        {rows.map(([k, v]) => (
          <Fragment key={k}>
            <dt>{k}</dt>
            <dd>{v}</dd>
          </Fragment>
        ))}
      </dl>
      <h3>Point your agent at it</h3>
      <pre className="copy">cd {JSON.stringify(box.dir)}</pre>
      {env.length > 0 && (
        <>
          <h3>Managed env</h3>
          <table className="env">
            <tbody>
              {env.map(([k, v]) => (
                <tr key={k}>
                  <th className="mono">{k}</th>
                  <td>
                    <code>{v}</code>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </div>
  )
}

function Danger({ box, onRemoved }: { box: BoxView; onRemoved: () => void }) {
  const [into, setInto] = useState('')
  const [squash, setSquash] = useState(false)
  const [keepBranch, setKeepBranch] = useState(false)
  const [busy, setBusy] = useState(false)
  const [confirm, setConfirm] = useState<null | { force: boolean; message?: string }>(null)

  const merge = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try {
      const r = await api.merge(box.repo, box.name, { into: into.trim() || undefined, squash: squash || undefined })
      toasts.ok(`merged ${box.branch} into ${r.into}`, 'remove the box when you are done with it')
    } catch (err) {
      toasts.error(err)
    } finally {
      setBusy(false)
    }
  }

  const remove = async (force: boolean) => {
    setBusy(true)
    try {
      await api.remove(box.repo, box.name, { force, keep_branch: keepBranch })
      toasts.ok(`removed ${box.name}`)
      setConfirm(null)
      onRemoved()
    } catch (err) {
      if (!force && err instanceof ApiError && err.code === 'unmerged_work') {
        setConfirm({ force: true, message: err.message })
      } else {
        toasts.error(err)
        setConfirm(null)
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="stack">
      <section className="card">
        <h3>Merge</h3>
        <p className="muted">
          Merges <code>{box.branch}</code> into the main checkout's current branch. It refuses when either side has uncommitted
          changes and never switches branches for you.
        </p>
        <form onSubmit={merge} className="form">
          <label>
            Into
            <input value={into} onChange={(e) => setInto(e.target.value)} placeholder="the branch checked out in the main checkout" spellCheck={false} />
          </label>
          <label className="check">
            <input type="checkbox" checked={squash} onChange={(e) => setSquash(e.target.checked)} /> Squash into one commit
          </label>
          <div className="row end">
            <button className="primary" disabled={busy}>
              Merge
            </button>
          </div>
        </form>
      </section>
      <section className="card danger-zone">
        <h3>Remove</h3>
        <p className="muted">Stops the dev server, drops the database copy, frees the ports and deletes the worktree. It asks first if there is unmerged work.</p>
        <label className="check">
          <input type="checkbox" checked={keepBranch} onChange={(e) => setKeepBranch(e.target.checked)} /> Keep the <code>{box.branch}</code> branch
        </label>
        <div className="row end">
          <button className="danger" disabled={busy} onClick={() => setConfirm({ force: false })}>
            Remove box
          </button>
        </div>
      </section>
      {confirm && (
        <ConfirmDialog
          title={confirm.force ? 'Discard unmerged work?' : `Remove ${box.name}?`}
          confirmLabel={confirm.force ? 'Remove anyway' : 'Remove'}
          danger
          busy={busy}
          onCancel={() => setConfirm(null)}
          onConfirm={() => void remove(confirm.force)}
        >
          {confirm.message ? (
            <p>{confirm.message}</p>
          ) : (
            <p>
              This deletes the worktree at <code>{box.dir}</code>.
            </p>
          )}
        </ConfirmDialog>
      )}
    </div>
  )
}
```

- [ ] **Step 4: App shell, entry point, styles**

Create `web/src/ui/App.tsx`:

```tsx
import { useCallback, useEffect, useState } from 'react'
import { SignedOut, api, onSignedOut } from '../api/client'
import { connectSSE, type SSEState } from '../api/sse'
import type { BoxEvent, BoxEventType, Health, RepoView } from '../api/types'
import { boxes, selectBoxes, splitId, useBoxes } from '../store/boxes'
import { navigate, useRoute } from '../store/route'
import { toasts } from '../store/toasts'
import { BoxPage } from './BoxPage'
import { BoxTable } from './BoxTable'
import { DoctorPanel } from './DoctorPanel'
import { NewBoxDialog } from './NewBoxDialog'
import { SignIn } from './SignIn'
import { Toasts } from './Toasts'
import { TopBar } from './TopBar'
import { useKey } from './bits'
import { ConfirmDialog } from './confirm'

type Auth = 'checking' | 'in' | 'out'
type Dialog = 'none' | 'new' | 'doctor' | 'refresh'
const EVENTS: BoxEventType[] = ['box.created', 'box.state', 'box.step', 'box.removed']

export function App() {
  const [auth, setAuth] = useState<Auth>('checking')
  const [health, setHealth] = useState<Health | null>(null)
  const [repos, setRepos] = useState<RepoView[]>([])
  const [sse, setSse] = useState<SSEState>('connecting')
  const [dialog, setDialog] = useState<Dialog>('none')
  const route = useRoute()
  const state = useBoxes()

  const load = useCallback(async () => {
    try {
      const [h, rs, bs] = await Promise.all([api.health(), api.repos(), api.boxes()])
      setHealth(h)
      setRepos(rs)
      boxes.dispatch({ type: 'load', boxes: bs })
      setAuth('in')
    } catch (e) {
      if (e instanceof SignedOut) setAuth('out')
      else toasts.error(e)
    }
  }, [])

  useEffect(() => {
    onSignedOut(() => setAuth('out'))
    void load()
  }, [load])

  useEffect(() => {
    if (auth !== 'in') return
    return connectSSE({
      url: api.eventsURL,
      events: EVENTS,
      onState: setSse,
      onReconnect: () => void load(),
      onEvent: (type, data) => {
        const ev = { ...(data as BoxEvent), type: type as BoxEventType }
        boxes.dispatch({ type: 'event', event: ev })
        if (type === 'box.created' || type === 'box.state') {
          const { repo, name } = splitId(ev.box)
          api
            .box(repo, name)
            .then((b) => boxes.dispatch({ type: 'upsert', box: b }))
            .catch(() => {
              /* removed in the meantime */
            })
        }
        if (type === 'box.created') {
          api.repos().then(setRepos).catch(() => {})
        }
      },
    })
  }, [auth, load])

  const repoId = route.kind === 'home' ? (repos[0]?.id ?? '') : route.repo
  const repo = repos.find((r) => r.id === repoId)
  const closeDialog = useCallback(() => setDialog('none'), [])
  useKey('n', () => setDialog('new'), auth === 'in' && dialog === 'none' && !!repo)

  if (auth === 'out') return <SignIn />

  return (
    <div className="app">
      <TopBar
        health={health}
        sse={sse}
        repos={repos}
        repo={repoId}
        onRepo={(id) => navigate({ kind: 'repo', repo: id })}
        onNew={() => setDialog('new')}
        onDoctor={() => setDialog('doctor')}
        onRefreshDB={() => setDialog('refresh')}
      />
      <main className="main">
        {auth === 'checking' && <p className="muted">Loading…</p>}
        {auth === 'in' && repos.length === 0 && (
          <div className="empty">
            <p>agentbox doesn't know any repos yet.</p>
            <pre className="copy">cd your-repo{'\n'}agentbox init{'\n'}agentbox new feature-x</pre>
          </div>
        )}
        {auth === 'in' && repos.length > 0 && !repo && <p className="muted">Unknown repo. Pick one above.</p>}
        {auth === 'in' && repo && route.kind !== 'box' && (
          <BoxTable boxes={selectBoxes(state, repo.id)} steps={state.steps} repo={repo} onOpen={(b) => navigate({ kind: 'box', repo: b.repo, name: b.name })} />
        )}
        {auth === 'in' && route.kind === 'box' && (
          <BoxPage repo={route.repo} name={route.name} onBack={() => navigate({ kind: 'repo', repo: route.repo })} />
        )}
      </main>
      {dialog === 'new' && repo && <NewBoxDialog repo={repo} onClose={closeDialog} />}
      {dialog === 'doctor' && repo && <DoctorPanel repo={repo} onClose={closeDialog} />}
      {dialog === 'refresh' && repo && <RefreshDialog repo={repo} onClose={closeDialog} />}
      <Toasts />
    </div>
  )
}

function RefreshDialog({ repo, onClose }: { repo: RepoView; onClose: () => void }) {
  const [terminate, setTerminate] = useState(false)
  const [busy, setBusy] = useState(false)
  const go = async () => {
    setBusy(true)
    try {
      await api.refreshDB(repo.id, terminate)
      toasts.ok('database template refreshed', 'new boxes start from the current source database')
      onClose()
    } catch (e) {
      toasts.error(e)
      setBusy(false)
    }
  }
  return (
    <ConfirmDialog title="Refresh the database template?" confirmLabel="Refresh" busy={busy} onCancel={onClose} onConfirm={() => void go()}>
      <p>Re-snapshots the source database so that new boxes start from its current contents. Existing boxes keep their copies.</p>
      <label className="check">
        <input type="checkbox" checked={terminate} onChange={(e) => setTerminate(e.target.checked)} /> Terminate stray connections to the template (Postgres)
      </label>
    </ConfirmDialog>
  )
}
```

Create `web/src/main.tsx`:

```tsx
import React from 'react'
import { createRoot } from 'react-dom/client'
import './styles.css'
import { App } from './ui/App'

createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
```

Create `web/src/styles.css`:

```css
:root {
  color-scheme: light dark;
  --bg: #f6f7f9;
  --surface: #ffffff;
  --surface-2: #eef0f3;
  --text: #1b1f24;
  --muted: #6b7280;
  --line: #d9dde3;
  --accent: #2563eb;
  --accent-text: #ffffff;
  --danger: #b91c1c;
  --ok: #15803d;
  --mono: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  --sans: system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
  --radius: 6px;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #0f1115;
    --surface: #171a21;
    --surface-2: #1f232c;
    --text: #e6e8eb;
    --muted: #9aa3ae;
    --line: #2b303a;
    --accent: #7cb0ff;
    --accent-text: #0b1020;
    --danger: #f87171;
    --ok: #4ade80;
  }
}

* { box-sizing: border-box; }
html, body, #root { height: 100%; }
body { margin: 0; background: var(--bg); color: var(--text); font: 14px/1.5 var(--sans); }
a { color: var(--accent); text-decoration: none; }
a:hover { text-decoration: underline; }
code, pre, .mono, kbd { font-family: var(--mono); font-size: 0.92em; }
h1 { font-size: 1.25rem; margin: 0; }
h2 { font-size: 1.05rem; margin: 0; }
h3 { font-size: 0.95rem; margin: 0 0 0.5rem; }
p { margin: 0 0 0.75rem; }
.muted { color: var(--muted); }
.error { color: var(--danger); }
.ok { color: var(--ok); }
.strong { font-weight: 600; }
.spacer { flex: 1; }
.row { display: flex; gap: 0.5rem; align-items: center; flex-wrap: wrap; }
.row.end { justify-content: flex-end; margin-top: 0.75rem; }
.stack { display: flex; flex-direction: column; gap: 1rem; }
.brand { font-family: var(--mono); font-weight: 600; letter-spacing: 0.02em; color: var(--text); }

button, .button, input, select {
  font: inherit; color: var(--text); background: var(--surface); border: 1px solid var(--line);
  border-radius: var(--radius); padding: 0.35rem 0.7rem;
}
button, .button { cursor: pointer; display: inline-flex; align-items: center; gap: 0.35rem; }
button:hover:not(:disabled), .button:hover { background: var(--surface-2); text-decoration: none; }
button:disabled { opacity: 0.5; cursor: default; }
button.primary { background: var(--accent); color: var(--accent-text); border-color: transparent; }
button.primary:hover:not(:disabled) { filter: brightness(1.08); background: var(--accent); }
button.danger { color: var(--danger); border-color: var(--danger); }
button.danger:hover:not(:disabled) { background: var(--danger); color: #fff; }
button.quiet { border-color: transparent; background: transparent; color: var(--muted); }
button.icon { border: none; background: transparent; font-size: 1.2rem; line-height: 1; padding: 0.1rem 0.4rem; color: var(--muted); }
button:focus-visible, input:focus-visible, select:focus-visible, a:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
kbd { border: 1px solid var(--line); border-radius: 3px; padding: 0 0.3rem; font-size: 0.75em; opacity: 0.8; }
input, select { min-width: 0; }
input:invalid { border-color: var(--danger); }

.app { display: flex; flex-direction: column; min-height: 100%; }
.topbar { display: flex; align-items: center; gap: 0.75rem; padding: 0.6rem 1rem; border-bottom: 1px solid var(--line); background: var(--surface); position: sticky; top: 0; z-index: 5; }
.repo-select { display: flex; align-items: center; gap: 0.4rem; }
.main { padding: 1rem; max-width: 1200px; width: 100%; margin: 0 auto; }

.pill { display: inline-block; padding: 0.05rem 0.5rem; border-radius: 999px; font-size: 0.78rem; font-weight: 600; line-height: 1.5; background: var(--surface-2); color: var(--muted); text-transform: lowercase; }
.pill-running { background: color-mix(in srgb, var(--ok) 18%, transparent); color: var(--ok); }
.pill-failed { background: color-mix(in srgb, var(--danger) 18%, transparent); color: var(--danger); }
.pill-creating { background: color-mix(in srgb, var(--accent) 18%, transparent); color: var(--accent); }
.pill-removing { background: color-mix(in srgb, #d97706 20%, transparent); color: #d97706; }
.pill-conn-open { background: color-mix(in srgb, var(--ok) 18%, transparent); color: var(--ok); }
.pill-conn-reconnecting { background: color-mix(in srgb, #d97706 20%, transparent); color: #d97706; }
.pill-kind { text-transform: none; }

.table-wrap { background: var(--surface); border: 1px solid var(--line); border-radius: var(--radius); overflow: auto; }
.table-wrap .path { padding: 0.5rem 0.75rem 0; margin: 0; font-family: var(--mono); font-size: 0.8rem; }
table.boxes { width: 100%; border-collapse: collapse; }
table.boxes th, table.boxes td { padding: 0.5rem 0.75rem; text-align: left; border-top: 1px solid var(--line); white-space: nowrap; }
table.boxes thead th { border-top: none; font-size: 0.78rem; text-transform: uppercase; letter-spacing: 0.04em; color: var(--muted); font-weight: 600; }
table.boxes .num { text-align: right; font-variant-numeric: tabular-nums; }
table.boxes .actions { text-align: right; }
table.boxes .actions button { margin-left: 0.35rem; }
.step { font-size: 0.85rem; }
@media (max-width: 900px) { .secondary { display: none; } }

.empty { text-align: center; padding: 3rem 1rem; color: var(--muted); }
pre.copy { display: inline-block; text-align: left; background: var(--surface-2); padding: 0.75rem 1rem; border-radius: var(--radius); user-select: all; }

.page-head { display: flex; align-items: center; gap: 0.75rem; margin-bottom: 0.75rem; flex-wrap: wrap; }
.tabs { display: flex; gap: 0.25rem; border-bottom: 1px solid var(--line); margin-bottom: 1rem; }
.tab { border: none; background: transparent; border-bottom: 2px solid transparent; border-radius: 0; padding: 0.4rem 0.75rem; color: var(--muted); text-transform: capitalize; }
.tab.active { color: var(--text); border-bottom-color: var(--accent); }
.tab:hover { background: transparent; color: var(--text); }

dl.facts { display: grid; grid-template-columns: max-content 1fr; gap: 0.3rem 1rem; margin: 0; }
dl.facts dt { color: var(--muted); }
dl.facts dd { margin: 0; overflow-wrap: anywhere; }
table.env { border-collapse: collapse; }
table.env th, table.env td { text-align: left; padding: 0.2rem 0.75rem 0.2rem 0; vertical-align: top; overflow-wrap: anywhere; }
table.env th { color: var(--muted); font-weight: 500; }
.card { background: var(--surface); border: 1px solid var(--line); border-radius: var(--radius); padding: 1rem; }
.danger-zone { border-color: color-mix(in srgb, var(--danger) 50%, var(--line)); }

.form { display: flex; flex-direction: column; gap: 0.75rem; }
.form label { display: flex; flex-direction: column; gap: 0.25rem; font-weight: 500; }
.form label.check, label.check { flex-direction: row; align-items: center; gap: 0.5rem; font-weight: 400; }
label.inline { display: inline-flex; align-items: center; gap: 0.4rem; }

.logs .row { margin-bottom: 0.5rem; }
pre.log { margin: 0; height: 60vh; overflow: auto; background: #0b0d12; color: #d7dbe0; padding: 0.75rem; border-radius: var(--radius); font-size: 0.82rem; white-space: pre-wrap; overflow-wrap: anywhere; }
pre.diff-body { margin: 0.75rem 0 0; max-height: 70vh; overflow: auto; background: var(--surface); border: 1px solid var(--line); border-radius: var(--radius); padding: 0.5rem 0; font-size: 0.82rem; }
pre.diff-body span { display: block; padding: 0 0.75rem; white-space: pre-wrap; overflow-wrap: anywhere; }
.d-add { background: color-mix(in srgb, var(--ok) 14%, transparent); }
.d-del { background: color-mix(in srgb, var(--danger) 14%, transparent); }
.d-hunk { color: var(--accent); }
.d-head, .d-file { color: var(--muted); font-weight: 600; }

.backdrop { position: fixed; inset: 0; background: rgba(0, 0, 0, 0.45); display: grid; place-items: center; z-index: 20; padding: 1rem; }
.modal { background: var(--surface); border: 1px solid var(--line); border-radius: 10px; width: min(32rem, 100%); max-height: 90vh; display: flex; flex-direction: column; box-shadow: 0 20px 60px rgba(0, 0, 0, 0.35); }
.modal.wide { width: min(48rem, 100%); }
.modal-head { display: flex; align-items: center; justify-content: space-between; padding: 0.75rem 1rem; border-bottom: 1px solid var(--line); }
.modal-body { padding: 1rem; overflow: auto; }
ul.findings { list-style: none; padding: 0; margin: 0 0 0.5rem; display: flex; flex-direction: column; gap: 0.4rem; }
ul.findings li.fixed { color: var(--muted); }

.toasts { position: fixed; right: 1rem; bottom: 1rem; display: flex; flex-direction: column; gap: 0.5rem; z-index: 30; max-width: min(28rem, calc(100% - 2rem)); }
.toast { display: flex; gap: 0.75rem; align-items: flex-start; background: var(--surface); border: 1px solid var(--line); border-left: 4px solid var(--accent); border-radius: var(--radius); padding: 0.6rem 0.75rem; box-shadow: 0 8px 24px rgba(0, 0, 0, 0.25); }
.toast-error { border-left-color: var(--danger); }
.toast-ok { border-left-color: var(--ok); }
.toast strong { display: block; }

.signin { max-width: 26rem; margin: 15vh auto; padding: 0 1rem; }
.signin input { flex: 1; }
```

- [ ] **Step 5: Build, embed, smoke-test**

Run:

```bash
cd web && npm run check && npm run build && cd ..
ls internal/ui/dist            # index.html, assets/, .gitkeep
git status --short internal/ui # only .gitkeep is tracked; nothing else listed
go vet ./... && go test -race ./internal/ui/ ./internal/daemon/ ./internal/cli/
CGO_ENABLED=0 go build -o /tmp/agentbox-ui ./cmd/agentbox
```

Expected: `tsc` clean, Vitest green, `vite build` writes `internal/ui/dist/index.html` plus `assets/*.js|css`, `.gitkeep` still present, Go green.

Smoke test the real binary (nothing is left behind: a throwaway home under `/tmp`):

```bash
export AGENTBOX_HOME=/tmp/abui && rm -rf $AGENTBOX_HOME
/tmp/agentbox-ui daemon status                 # auto-starts nothing yet
/tmp/agentbox-ui ui --no-open                  # prints: dashboard: http://127.0.0.1:7777/ui/login?code=…
curl -si "$(/tmp/agentbox-ui ui --no-open | sed 's/^dashboard: //')" | head -5   # HTTP/1.1 303, Set-Cookie: agentbox_session=…
curl -s http://127.0.0.1:7777/ui/ | grep -o '<script[^>]*>'                        # the hashed bundle
curl -si -H 'Host: nothing.localhost' http://127.0.0.1:7777/ | head -1             # HTTP/1.1 404 (proxy page)
/tmp/agentbox-ui daemon stop && rm -rf $AGENTBOX_HOME
```

Then open the dashboard in a browser against the `examples/sqlite` app (`agentbox new a` there) and check: the row appears live, `a.localhost:7777` link serves the example app's JSON, Logs tab streams `listening on …`, Diff tab says "No changes", Stop/Start work, Remove asks for confirmation. Fix anything that is off before moving on.

- [ ] **Step 6: Checkpoint**

Record "Task 9 complete" with the build output's bundle sizes. Do not commit.

---

### Task 10: `init` hint, Makefile, CI/release, README, e2e

**Files:**
- Modify: `internal/initcfg/initcfg.go`, `internal/initcfg/initcfg_test.go`
- Create: `Makefile`
- Modify: `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `.goreleaser.yaml`, `README.md`
- Modify: `e2e/e2e_test.go`

- [ ] **Step 1: Failing test for the `init` hint**

Append to `internal/initcfg/initcfg_test.go`:

```go
func TestDetectHintsProxyWhenSeveralPorts(t *testing.T) {
	res, _ := detect(t, map[string]string{
		"package.json": `{"scripts": {"dev": "node server.js"}}`,
		".env.example": "PORT=3000\nAPI_PORT=4000\n",
	})
	if !strings.Contains(res.YAML, "# proxy: PORT") {
		t.Fatalf("no proxy hint:\n%s", res.YAML)
	}
	res, _ = detect(t, map[string]string{
		"package.json": `{"scripts": {"dev": "node server.js"}}`,
		".env.example": "PORT=3000\n",
	})
	if strings.Contains(res.YAML, "proxy") {
		t.Fatalf("proxy hint with one port:\n%s", res.YAML)
	}
}
```

Run: `go test ./internal/initcfg/ -run TestDetectHintsProxy` — Expected: FAIL (no hint).

- [ ] **Step 2: Implement the hint**

In `internal/initcfg/initcfg.go`, replace

```go
	if len(portKeys) > 0 {
		fmt.Fprintf(&b, "ports: [%s]\n", strings.Join(portKeys, ", "))
	} else {
```

with

```go
	if len(portKeys) > 0 {
		fmt.Fprintf(&b, "ports: [%s]\n", strings.Join(portKeys, ", "))
		if len(portKeys) > 1 {
			fmt.Fprintf(&b, "# proxy: %s  # which port http://<box>.localhost:7777 serves (default: the first)\n", portKeys[0])
		}
	} else {
```

Run: `go test ./internal/initcfg/` — Expected: PASS.

- [ ] **Step 3: Makefile**

Create `Makefile`:

```make
.PHONY: ui build test e2e

## ui: build the dashboard into internal/ui/dist (needs Node 20+)
ui:
	cd web && npm ci && npm run build

## build: build ./agentbox with the dashboard embedded
build: ui
	CGO_ENABLED=0 go build -ldflags "-s -w" -o agentbox ./cmd/agentbox

## test: vet + race tests (no Node needed)
test:
	go vet ./... && go test -race ./...

## e2e: real boxes on the example apps (needs node; build the UI first for the dashboard test)
e2e:
	go test -tags e2e ./e2e/...
```

(Use a real tab before each recipe line.) Run `make test` — Expected: green.

- [ ] **Step 4: CI and release**

Replace `.github/workflows/ci.yml` with:

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
        with: { node-version: '22', cache: npm, cache-dependency-path: web/package-lock.json }
      - run: cd web && npm ci && npm run check && npm run build
      - run: go vet ./...
      - run: go test -race ./...
      - run: go test -tags e2e ./e2e/...

  # The Go side must build and pass without a dashboard build present.
  go-without-ui:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.24' }
      - run: test ! -f internal/ui/dist/index.html
      - run: go vet ./... && go test -race ./... && CGO_ENABLED=0 go build ./cmd/agentbox

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

In `.github/workflows/release.yml`, add after the `setup-go` step:

```yaml
      - uses: actions/setup-node@v4
        with: { node-version: '22', cache: npm, cache-dependency-path: web/package-lock.json }
```

In `.goreleaser.yaml`, add at the top (after `version: 2`):

```yaml
before:
  hooks:
    - sh -c "cd web && npm ci && npm run build"
```

- [ ] **Step 5: README**

In `README.md`:

1. In **Quick start**, after `agentbox new feature-x # prints the box dir: point your agent there`, add:

```
    agentbox ui            # web dashboard; the box also answers at http://feature-x.localhost:7777/
```

2. In the **Configuration** YAML, after `ports: [APP_PORT, API_PORT]   # each gets a free port per box`, add:

```yaml
proxy: APP_PORT               # which port http://<box>.localhost:7777 forwards to (default: the first)
```

3. In the **Commands** table, add a row after `doctor`:

```
| `ui [--no-open]` | open the web dashboard (signed in) |
```

4. Add a section before **How it works**:

```markdown
## Dashboard and box URLs

`agentbox ui` opens `http://127.0.0.1:7777/ui/`, signed in via a one-time link, and shows every repo
and box live: state, logs, diff, doctor, and the same create/start/stop/merge/remove actions as the CLI.

Every box with ports is also served at **`http://<box>.localhost:7777/`** (or
`http://<box>.<repo-slug>.localhost:7777/` when two repos share a box name). The daemon forwards to the
port named by `proxy:` in `agentbox.yaml` (default: the first of `ports`), WebSockets included. A box that
is stopped or still being created gets an explanatory page instead of a connection error.

Chrome, Edge and Firefox resolve `*.localhost` on their own. Safari and `curl` on macOS use the system
resolver, which does not: use `curl -H 'Host: a.localhost' http://127.0.0.1:7777/`, or add
`127.0.0.1 a.localhost` to `/etc/hosts`. Your app should use relative URLs (or honour `X-Forwarded-Host`)
rather than redirecting to `http://localhost:3000`.
```

5. In **Install**, after the `brew` line, add:

```
`go install` builds without the dashboard (everything else works; `agentbox ui` says so). Release
binaries include it. From a checkout: `make build` (needs Node 20+).
```

6. Change the **Roadmap** line to: `Terminal UI → Docker Compose & MySQL drivers → desktop app.`

- [ ] **Step 6: e2e test**

In `e2e/e2e_test.go`:

1. Add `home string` to the `env` struct and set it in `newEnv`: `e := &env{t: t, home: home, vars: …}`.
2. Add `"io"` to the imports.
3. Append:

```go
// runErr is run without the fatal: it returns combined output and the error.
func (e *env) runErr(dir string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir, cmd.Env = dir, e.vars
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestProxyAndDashboard(t *testing.T) {
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
	e.run(repo, "new", "a")
	t.Cleanup(func() { e.run(repo, "rm", "--force", "a") })

	raw, err := os.ReadFile(filepath.Join(e.home, "api_addr"))
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimSpace(string(raw))
	viaProxy := func(host string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", "http://"+addr+"/", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		code, body := viaProxy("a.localhost")
		if code == 200 && strings.Contains(body, `"box":"a"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("proxy never served box a: %d %s", code, body)
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.run(repo, "stop", "a")
	if code, body := viaProxy("a.localhost"); code != 503 || !strings.Contains(body, "stopped") || !strings.Contains(body, "agentbox start a") {
		t.Fatalf("stopped box via proxy: %d %s", code, body)
	}
	if code, _ := viaProxy("nope.localhost"); code != 404 {
		t.Fatalf("unknown box via proxy: %d", code)
	}

	out, err := e.runErr(repo, "ui", "--no-open")
	if err != nil && strings.Contains(out, "built without the dashboard") {
		t.Skip("dashboard not built into this binary (run `make ui` first)")
	}
	if err != nil || !strings.HasPrefix(out, "dashboard: ") {
		t.Fatalf("ui --no-open: %v\n%s", err, out)
	}
	link := strings.TrimSpace(strings.TrimPrefix(out, "dashboard: "))
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var session *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == "agentbox_session" {
			session = ck
		}
	}
	if resp.StatusCode != 303 || session == nil {
		t.Fatalf("login link: %d, cookie %v", resp.StatusCode, session)
	}
	base := "http://" + addr
	req, _ := http.NewRequest("GET", base+"/v1/boxes", nil)
	req.AddCookie(session)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var boxes []api.BoxView
	json.NewDecoder(resp2.Body).Decode(&boxes)
	resp2.Body.Close()
	_, port, _ := net.SplitHostPort(addr)
	if resp2.StatusCode != 200 || len(boxes) != 1 || boxes[0].URL != "http://a.localhost:"+port+"/" {
		t.Fatalf("boxes with cookie: %d %+v", resp2.StatusCode, boxes)
	}
	start, _ := http.NewRequest("POST", base+"/v1/repos/"+boxes[0].Repo+"/boxes/a/start", nil)
	start.AddCookie(session)
	start.Header.Set("Origin", base)
	resp3, err := http.DefaultClient.Do(start)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != 204 {
		t.Fatalf("start with cookie+Origin: %d", resp3.StatusCode)
	}
	if code, body := viaProxy("a.localhost"); code != 200 && code != 502 {
		t.Fatalf("after start: %d %s", code, body)
	}
}
```

Add `"net"` to the imports.

- [ ] **Step 7: Run everything**

```bash
go vet ./... && go test -race ./...
cd web && npm run check && npm run build && cd ..
go test -tags e2e ./e2e/
```

Expected: all green; `TestProxyAndDashboard` PASS (not skipped, since the UI was just built). Also run `go test -tags e2e ./e2e/ -run TestProxyAndDashboard` once after `rm -rf internal/ui/dist/* && touch internal/ui/dist/.gitkeep` to see it **skip** cleanly, then rebuild the UI.

- [ ] **Step 8: Checkpoint**

Record "Task 10 complete" with the three command results. `git status` should list: modified Go/YAML/MD files, new `internal/proxy`, `internal/ui` (only `.gitkeep` under `dist`), `web/` without `node_modules`, `Makefile`. Do not commit.

---

## Self-review (done while writing)

- **Spec coverage:** §4 one listener/dispatch → T6. §5.1–5.3 hosts, target port, state pages → T2, T3. §5.4 `url` + `new` output → T5, T7. §5.5 docs → T10. §6.1 login codes → T4, T5. §6.2 cookie attrs → T4. §6.3 rules 1–5 → T6 (Host), T5 (Origin, auth, Sec-Fetch-Site), T4 (public /ui/). §7 ui package incl. not-built page and `agentbox ui` detection → T4, T7. §8 stack, modules, behaviour → T8, T9. §9 CLI → T7. §10 daemon → T6. §11 config + init hint → T1, T10. §12 API → T5. §13 security → T2 (escaping), T5, T6. §14 tests → every task; e2e → T10. §15 risks → README in T10.
- **Placeholders:** none; every step has its code.
- **Type consistency:** `proxy.Target`/errors (T2) used by T3 and T6 with the same names; `api.HealthView`/`LoginCode`/`BoxView.URL` (T5) used by T7's client and T10's e2e; `daemon.UIFS`/`Version`/`Paths.APIAddr`/`Dispatch` (T6) used by T7's tests; web `api`, stores (T8) used by T9 with the same exports.
- **Review Focus:** each of the five lines has its test named in the owning task.
