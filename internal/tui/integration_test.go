package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/agentbox-dev/agentbox/internal/box"
	"github.com/agentbox-dev/agentbox/internal/client"
	"github.com/agentbox-dev/agentbox/internal/daemon"
	"github.com/agentbox-dev/agentbox/internal/testutil"
)

// TestAgainstRealDaemon runs the model's commands against a live daemon and a
// real client: list, log stream and the event feed.
func TestAgainstRealDaemon(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "abtui") // short path: unix socket limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("api_addr: 127.0.0.1:0\n"), 0o600)
	p := daemon.PathsFor(home)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx, p) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	for deadline := time.Now().Add(5 * time.Second); !daemon.Ping(p.Sock); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("daemon never came up")
		}
	}

	repo := testutil.NewRepo(t, map[string]string{"agentbox.yaml": "version: 1\ndev: echo hello from the box; sleep 30\nports: [APP_PORT]\n"})
	c := client.New(p.Sock)
	rv, err := c.ResolveRepo(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateBox(ctx, box.NewOptions{RepoPath: repo, Name: "a"}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		b, err := c.GetBox(ctx, rv.ID, "a")
		if err == nil && b.State == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("box never ran: %+v %v", b, err)
		}
	}
	t.Cleanup(func() { _ = c.RemoveBox(context.Background(), rv.ID, "a", true, false) })

	m := New(ctx, c, Options{ToastTTL: -1, NoBlink: true, Repo: rv.ID})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = run(t, m, m.loadAll(), 0)
	mustContain(t, m.View(), "running", "repo: "+filepath.Base(repo), "1 boxes")
	if !strings.Contains(m.View(), " a ") && !strings.Contains(m.View(), "a  ") {
		t.Fatalf("box a missing:\n%s", m.View())
	}

	lctx, lcancel := context.WithCancel(ctx)
	defer lcancel()
	lines, err := c.LogLines(lctx, rv.ID, "a", 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for deadline := time.After(10 * time.Second); !seen; {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("log stream closed early")
			}
			seen = strings.Contains(l, "hello from the box")
		case <-deadline:
			t.Fatal("never saw the dev server's output")
		}
	}

	evs, err := c.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.StopBox(ctx, rv.ID, "a"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.After(10 * time.Second); ; {
		select {
		case ev := <-evs:
			if ev.Type == "box.state" && ev.Data["state"] == "stopped" {
				m = update(t, m, eventMsg{ev})
				mustContain(t, m.View(), "stopped")
				return
			}
		case <-deadline:
			t.Fatal("never saw the stopped event")
		}
	}
}
