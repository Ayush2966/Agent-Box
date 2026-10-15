package daemon_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Ayush2966/Agent-Box/internal/box"
	"github.com/Ayush2966/Agent-Box/internal/client"
	"github.com/Ayush2966/Agent-Box/internal/daemon"
	"github.com/Ayush2966/Agent-Box/internal/errs"
	"github.com/Ayush2966/Agent-Box/internal/testutil"
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
	if s.BoxesDir != "" {
		t.Fatalf("boxes_dir defaults to the home's boxes dir (empty here), got %q", s.BoxesDir)
	}
	tilde := filepath.Join(dir, "tilde.yaml")
	os.WriteFile(tilde, []byte("boxes_dir: ~/agentbox-boxes\n"), 0o600)
	uh, _ := os.UserHomeDir()
	if s, err := daemon.LoadSettings(tilde); err != nil || s.BoxesDir != filepath.Join(uh, "agentbox-boxes") {
		t.Fatalf("~ not expanded: %q %v", s.BoxesDir, err)
	}
	if !s.Notify() {
		t.Fatal("notifications default on")
	}
	off := filepath.Join(dir, "off.yaml")
	os.WriteFile(off, []byte("notifications: false\n"), 0o600)
	if s, err := daemon.LoadSettings(off); err != nil || s.Notify() {
		t.Fatalf("notifications: false not honoured: %+v %v", s, err)
	}
	rel := filepath.Join(dir, "rel.yaml")
	os.WriteFile(rel, []byte("boxes_dir: boxes/here\n"), 0o600)
	if _, err := daemon.LoadSettings(rel); err == nil || !strings.Contains(err.Error(), "boxes_dir") {
		t.Fatalf("relative boxes_dir accepted: %v", err)
	}
}

// boxes_dir in config.yaml moves the worktrees (and only them) out of the
// agentbox home, e.g. somewhere visible like ~/Desktop.
func TestRunPutsBoxesInBoxesDir(t *testing.T) {
	home := tempHome(t)
	p := daemon.PathsFor(home)
	boxes := filepath.Join(home, "elsewhere", "my boxes")
	os.WriteFile(p.Config, []byte("api_addr: 127.0.0.1:0\nboxes_dir: "+boxes+"\n"), 0o600)
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
	c := client.New(p.Sock)
	repo := testutil.NewRepo(t, map[string]string{"agentbox.yaml": "version: 1\n"})
	rv, err := c.ResolveRepo(ctx, repo)
	if err != nil {
		t.Fatal(err)
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
	bv, err := c.GetBox(ctx, rv.ID, "a")
	if err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(boxes) // the manager keeps paths symlink-resolved (/tmp → /private/tmp)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(real, rv.ID, "a")
	if bv.Dir != want {
		t.Fatalf("box dir %q, want %q", bv.Dir, want)
	}
	if _, err := os.Stat(filepath.Join(want, "agentbox.yaml")); err != nil {
		t.Fatalf("worktree not at boxes_dir: %v", err)
	}
	if _, err := os.Stat(p.Boxes); !os.IsNotExist(err) {
		t.Fatalf("the home's boxes dir was still created: %v", err)
	}
	if err := c.RemoveBox(ctx, rv.ID, "a", false, false); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

// Two daemons racing to own the same AGENTBOX_HOME must not both bind: the
// loser used to os.Remove the winner's live socket and rebind, leaving an
// unreachable, unkillable daemon behind (and a daemon.pid naming only one).
func TestRunRefusesSecondDaemonInSameHome(t *testing.T) {
	p := daemon.PathsFor(tempHome(t))
	os.WriteFile(p.Config, []byte("api_addr: 127.0.0.1:0\n"), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const n = 4
	errc := make(chan error, n)
	start := make(chan struct{})
	for range n {
		go func() {
			<-start // release them together, into the Ping→Listen window
			errc <- daemon.Run(ctx, p)
		}()
	}
	close(start)

	// Exactly n-1 must refuse; the last one keeps serving until ctx is cancelled.
	refused := 0
	for range n - 1 {
		select {
		case err := <-errc:
			if err == nil || !strings.Contains(err.Error(), "already running") {
				t.Fatalf("loser returned %v, want an 'already running' error", err)
			}
			refused++
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of %d daemons refused; the rest are all still bound", refused, n-1)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for !daemon.Ping(p.Sock) {
		if time.Now().After(deadline) {
			t.Fatal("the surviving daemon is not reachable")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The pid file must name the daemon that actually owns the socket.
	pid, err := daemon.ReadPID(p.PID)
	if err != nil || pid != os.Getpid() {
		t.Fatalf("pid file: %d %v", pid, err)
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("survivor returned %v", err)
	}
}

func TestRunExplainsAnOverlongSocketPath(t *testing.T) {
	home := filepath.Join(tempHome(t), strings.Repeat("x", 120))
	err := daemon.Run(context.Background(), daemon.PathsFor(home))
	if err == nil || !strings.Contains(err.Error(), "too long") || !strings.Contains(err.Error(), "AGENTBOX_HOME") {
		t.Fatalf("got %v, want an error naming the socket-path limit and AGENTBOX_HOME", err)
	}
}

func TestDispatchRejectsForeignHosts(t *testing.T) {
	hit := ""
	px := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = "proxy" })
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = "app" })
	h := daemon.Dispatch(px, app)
	cases := map[string]string{
		"a.localhost:7777": "proxy", "A.Repo-1a2b3c.LOCALHOST": "proxy",
		"127.0.0.1:7777": "app", "localhost:7777": "app", "localhost": "app", "[::1]:7777": "app", "[::1]": "app",
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

func TestLoadSettingsRejectsNonLoopbackAPIAddr(t *testing.T) {
	cases := []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:7777", true}, {"localhost:7777", true}, {"[::1]:7777", true}, {"127.0.0.1:0", true},
		{"0.0.0.0:7777", false}, {":7777", false}, {"10.0.0.5:7777", false}, {"example.com:7777", false}, {"nonsense", false},
	}
	for _, c := range cases {
		path := filepath.Join(t.TempDir(), "config.yaml")
		os.WriteFile(path, []byte("api_addr: \""+c.addr+"\"\n"), 0o600)
		_, err := daemon.LoadSettings(path)
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error %v", c.addr, err)
		}
		if !c.ok && (err == nil || !strings.Contains(err.Error(), "loopback")) {
			t.Errorf("%s: accepted or wrong error: %v", c.addr, err)
		}
	}
}

// A `url` link points at the port the daemon actually bound (api_addr may say :0).
func TestRunResolvesURLLinksToTheBoundAPIPort(t *testing.T) {
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
	c := client.New(p.Sock)
	h, err := c.HealthInfo(ctx)
	if err != nil || h.APIAddr == "" {
		t.Fatalf("health: %+v %v", h, err)
	}
	backend := testutil.NewRepo(t, map[string]string{"agentbox.yaml": "version: 1\n"})
	br, err := c.ResolveRepo(ctx, backend)
	if err != nil {
		t.Fatal(err)
	}
	frontend := testutil.NewRepo(t, map[string]string{"agentbox.yaml": "version: 1\nlinks:\n  API_URL: " + filepath.Base(backend) + "\n"})
	fr, err := c.ResolveRepo(ctx, frontend)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := c.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateBox(ctx, box.NewOptions{RepoPath: frontend, Name: "a", NoDev: true}); err != nil {
		t.Fatal(err)
	}
	for ev := range evs {
		if ev.Type == "box.state" && ev.Data["state"] == "ready" {
			break
		}
	}
	bv, err := c.GetBox(ctx, fr.ID, "a")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := strings.Cut(h.APIAddr, ":")
	if want := "http://a." + br.ID + ".localhost:" + port + "/"; bv.Env["API_URL"] != want {
		t.Fatalf("API_URL = %q, want %q", bv.Env["API_URL"], want)
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}
