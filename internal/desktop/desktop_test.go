package desktop

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ayush2966/Agent-Box/internal/api"
	"github.com/Ayush2966/Agent-Box/internal/client"
	"github.com/Ayush2966/Agent-Box/internal/daemon"
)

func shortDeadline(t *testing.T) {
	old := bootstrapDeadline
	bootstrapDeadline = 200 * time.Millisecond
	t.Cleanup(func() { bootstrapDeadline = old })
}

func healthOK(addr string) func(context.Context, string) (api.HealthView, error) {
	return func(context.Context, string) (api.HealthView, error) {
		return api.HealthView{OK: true, APIAddr: addr}, nil
	}
}

func TestBootstrapUsesRunningDaemon(t *testing.T) {
	home := t.TempDir()
	p := daemon.PathsFor(home)
	os.WriteFile(p.Token, []byte("tok\n"), 0o600)
	calls := 0
	addr, tok, err := Bootstrap(home, func(daemon.Paths) error { calls++; return nil }, healthOK("127.0.0.1:7777"))
	if err != nil || addr != "127.0.0.1:7777" || tok != "tok" || calls != 1 {
		t.Fatalf("%q %q %v calls=%d", addr, tok, err, calls)
	}
}

func TestBootstrapWaitsForTheDaemonToAnswer(t *testing.T) {
	home := t.TempDir()
	p := daemon.PathsFor(home)
	ensure := func(daemon.Paths) error { return os.WriteFile(p.Token, []byte("tok"), 0o600) }
	n := 0
	health := func(context.Context, string) (api.HealthView, error) {
		n++
		if n <= 3 {
			return api.HealthView{}, errors.New("not up yet")
		}
		return api.HealthView{OK: true, APIAddr: "127.0.0.1:50000"}, nil
	}
	addr, tok, err := Bootstrap(home, ensure, health)
	if err != nil || addr != "127.0.0.1:50000" || tok != "tok" || n != 4 {
		t.Fatalf("%q %q %v n=%d", addr, tok, err, n)
	}
}

func TestBootstrapFailsWhenAPIDisabled(t *testing.T) {
	home := t.TempDir()
	p := daemon.PathsFor(home)
	os.WriteFile(p.Token, []byte("tok"), 0o600)
	start := time.Now()
	_, _, err := Bootstrap(home, func(daemon.Paths) error { return nil }, healthOK(""))
	if err == nil || !strings.Contains(err.Error(), p.Log) || !strings.Contains(err.Error(), "api_addr") || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("want an error naming the log, api_addr and disabled, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("a disabled API must fail immediately, not wait out the deadline")
	}
	if _, _, err := Bootstrap(home, func(daemon.Paths) error { return errors.New("spawn failed") }, healthOK("x")); err == nil || !strings.Contains(err.Error(), "spawn failed") {
		t.Fatalf("ensure errors must surface: %v", err)
	}
}

func TestBootstrapReportsTheRealMissingPiece(t *testing.T) {
	home := t.TempDir()
	p := daemon.PathsFor(home) // no token file
	_, _, err := Bootstrap(home, func(daemon.Paths) error { return nil }, healthOK("127.0.0.1:7777"))
	if err == nil || !strings.Contains(err.Error(), p.Token) {
		t.Fatalf("want an error naming the token path %s, got %v", p.Token, err)
	}
}

func TestBootstrapGivesUp(t *testing.T) {
	shortDeadline(t)
	home := t.TempDir()
	p := daemon.PathsFor(home)
	health := func(context.Context, string) (api.HealthView, error) { return api.HealthView{}, errors.New("down") }
	start := time.Now()
	_, _, err := Bootstrap(home, func(daemon.Paths) error { return nil }, health)
	if err == nil || !strings.Contains(err.Error(), p.Sock) || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("want the deadline error naming the socket, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("took too long")
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

func TestProxyPinsHostToTheAPI(t *testing.T) {
	var host string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { host = r.Host }))
	defer backend.Close()
	addr := strings.TrimPrefix(backend.URL, "http://")
	h, err := NewProxy(addr, "secret")
	if err != nil {
		t.Fatal(err)
	}
	app := httptest.NewServer(h)
	defer app.Close()
	req, _ := http.NewRequest("GET", app.URL+"/", nil)
	req.Host = "feature-x.localhost:7777"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if host != addr {
		t.Fatalf("backend saw Host %q, want %q", host, addr)
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
	// wait() matches the first live too, so poll for the second one.
	for deadline := time.Now().Add(5 * time.Second); rec.count("agentbox:conn=live") < 2 && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
	}
	if rec.count("agentbox:conn=live") < 2 {
		t.Fatalf("expected a second live after reconnect: %v", rec.items)
	}
	cancel()
}

func TestBridgeLogStreamsAreIndependent(t *testing.T) {
	f := &fakeAPI{logs: map[string]chan string{}}
	rec := &recorder{}
	b := NewBridge(f, rec.emit)
	id1, id2 := "r/a#1-x", "r/a#2-y" // the window picks the ids
	if err := b.StartLogs(id1, "r", "a"); err != nil {
		t.Fatal(err)
	}
	if err := b.StartLogs(id1, "r", "a"); err == nil {
		t.Fatal("a second stream with the same id must be refused")
	}
	if err := b.StartLogs("", "r", "a"); err == nil {
		t.Fatal("an empty id must be refused")
	}
	f.mu.Lock()
	src1 := f.logs["r/a"]
	f.mu.Unlock()
	src1 <- "first stream line"
	rec.wait(t, "agentbox:log:"+id1+"=first stream line")
	if err := b.StartLogs(id2, "r", "a"); err != nil { // the frontend reopened the tab
		t.Fatal(err)
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

func TestBridgeStateTracksConnection(t *testing.T) {
	f := &fakeAPI{logs: map[string]chan string{}}
	rec := &recorder{}
	b := NewBridge(f, rec.emit)
	b.base = time.Millisecond
	if got := b.State(); got != "connecting" {
		t.Fatalf("fresh bridge state = %q", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)
	rec.wait(t, "agentbox:conn=live")
	if got := b.State(); got != "live" {
		t.Fatalf("after connect state = %q", got)
	}
	f.mu.Lock()
	first := f.events[0]
	f.err = errors.New("daemon down") // keep it down so reconnecting is observable
	f.mu.Unlock()
	close(first)
	rec.wait(t, "agentbox:conn=reconnecting")
	if got := b.State(); got != "reconnecting" {
		t.Fatalf("after drop state = %q", got)
	}
	time.Sleep(50 * time.Millisecond) // several failed retries at 1ms base
	if n := rec.count("agentbox:conn=reconnecting"); n != 1 {
		t.Fatalf("reconnecting must be announced once per outage, saw %d", n)
	}
	f.mu.Lock()
	f.err = nil
	f.mu.Unlock()
	for deadline := time.Now().Add(5 * time.Second); b.State() != "live" && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
	}
	if got := b.State(); got != "live" {
		t.Fatalf("after reconnect state = %q", got)
	}
}
