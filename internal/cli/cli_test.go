package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentbox-dev/agentbox/internal/api"
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
