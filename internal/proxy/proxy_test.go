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

func TestRemovingBoxPage(t *testing.T) {
	h := handlerFor(map[string]Target{"a": {Repo: "r1", Name: "a", State: "removing"}}, nil)
	code, body, _ := get(t, h, "a.localhost", "/")
	if code != 503 || !strings.Contains(body, "being removed") || strings.Contains(body, "agentbox start") {
		t.Fatalf("%d %q", code, body)
	}
}

// A dev server that binds only [::1] (what Vite does for "localhost" on recent
// Node) is still reached, because the proxy falls back from 127.0.0.1 to ::1.
func TestProxyReachesAnIPv6OnlyDevServer(t *testing.T) {
	l, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback:", err)
	}
	ts := &httptest.Server{Listener: l, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "v6 says hi")
	})}}
	ts.Start()
	t.Cleanup(ts.Close)
	port := l.Addr().(*net.TCPAddr).Port
	h := handlerFor(map[string]Target{"a": {Repo: "r1", Name: "a", State: "running", Port: port}}, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://a.localhost:7777/", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "v6 says hi") {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
}

func TestProxyStillReports502WhenNothingListens(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close() // nothing on v4 or v6 now
	h := handlerFor(map[string]Target{"a": {Repo: "r1", Name: "a", State: "running", Port: port}}, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://a.localhost:7777/", nil))
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "not answering") {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String()[:min(200, len(rec.Body.String()))])
	}
}
