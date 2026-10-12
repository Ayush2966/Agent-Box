package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/agentbox-dev/agentbox/internal/box"
	"github.com/agentbox-dev/agentbox/internal/proc"
	"github.com/agentbox-dev/agentbox/internal/state"
	"github.com/agentbox-dev/agentbox/internal/testutil"
	"github.com/agentbox-dev/agentbox/internal/ui"
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
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", resp.Header.Get("Cache-Control"))
	}
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

func TestDashboardRoutesEnforceOrigin(t *testing.T) {
	_, ts, _ := tcpServer(t)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(origin string) int {
		req, _ := http.NewRequest("POST", ts.URL+"/ui/logout", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("http://evil.example"); code != 403 {
		t.Fatalf("cross-site logout: %d, want 403", code)
	}
	if code := post(ts.URL); code != 303 {
		t.Fatalf("same-origin logout: %d, want 303", code)
	}
	if code := post(""); code != 303 {
		t.Fatalf("no Origin (non-browser) logout: %d, want 303", code)
	}
	if code, _ := send(t, ts, "GET", "/ui/", map[string]string{"Origin": "http://evil.example"}, ""); code != 403 {
		t.Fatalf("cross-origin GET /ui/: %d, want 403", code)
	}
}
