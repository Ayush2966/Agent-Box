package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentbox-dev/agentbox/internal/box"
	"github.com/agentbox-dev/agentbox/internal/client"
	"github.com/agentbox-dev/agentbox/internal/daemon"
	"github.com/agentbox-dev/agentbox/internal/errs"
	"github.com/agentbox-dev/agentbox/internal/testutil"
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
