package cli

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/agentbox-dev/agentbox/internal/api"
	"github.com/agentbox-dev/agentbox/internal/daemon"
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

// Unix socket paths are limited to ~104 bytes on macOS, so homes live under /tmp.
func shortTempHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "abcli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	return home
}

func TestDaemonStatusReportsStalePIDFile(t *testing.T) {
	home := shortTempHome(t)
	t.Setenv("AGENTBOX_HOME", home)
	pidFile := filepath.Join(home, "daemon.pid")
	os.WriteFile(pidFile, []byte("999999999\n"), 0o600) // above every OS's pid_max: certainly dead
	code, out, stderr := run("daemon", "status")
	if code != 0 || !strings.Contains(out, "stale") || !strings.Contains(out, "999999999") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, stderr)
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatal("the stale pid file was not removed")
	}
}

func TestDaemonStopSaysWhenNothingWasRunning(t *testing.T) {
	t.Setenv("AGENTBOX_HOME", shortTempHome(t))
	code, out, stderr := run("daemon", "stop")
	if code != 0 || !strings.Contains(out, "not running") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, stderr)
	}
}

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
