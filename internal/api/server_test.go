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

// POST /v1/boxes validated and then detached creation into a goroutine, so two
// simultaneous requests for the same name both got 202. Both clients then
// watched the same box ID on the event stream and reported whichever attempt
// published first, while the other attempt quietly went on to create a real
// box the user was told did not exist. Exactly one request may be accepted.
func TestConcurrentCreateSameNameAcceptsOne(t *testing.T) {
	s, repo := newServer(t)
	srv := httptest.NewServer(s.Handler(false))
	defer srv.Close()

	const n = 4
	codes := make(chan int, n)
	start := make(chan struct{})
	for range n {
		go func() {
			<-start
			codes <- call(t, "POST", srv.URL+"/v1/boxes", box.NewOptions{RepoPath: repo, Name: "race", NoDev: true}, nil)
		}()
	}
	close(start)

	accepted, conflicts, other := 0, 0, []int{}
	for range n {
		switch c := <-codes; c {
		case http.StatusAccepted:
			accepted++
		case http.StatusConflict:
			conflicts++
		default:
			other = append(other, c)
		}
	}
	if accepted != 1 || conflicts != n-1 || len(other) > 0 {
		t.Fatalf("got %d accepted, %d conflicts, other %v; want 1 accepted and %d conflicts", accepted, conflicts, other, n-1)
	}
	waitState(t, srv.URL, repoID(t, srv.URL, repo), "race", "ready")

	var boxes []BoxView
	if code := call(t, "GET", srv.URL+"/v1/boxes", nil, &boxes); code != 200 || len(boxes) != 1 {
		t.Fatalf("after the race there are %d boxes (code %d), want exactly 1", len(boxes), code)
	}
}

func repoID(t *testing.T, base, repo string) string {
	t.Helper()
	var rv RepoView
	if code := call(t, "POST", base+"/v1/repos", map[string]string{"path": repo}, &rv); code != 200 {
		t.Fatalf("resolve repo: %d", code)
	}
	return rv.ID
}
