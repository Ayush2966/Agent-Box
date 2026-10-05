package proc

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var fast = Options{Backoff: []time.Duration{10 * time.Millisecond}, CrashWindow: 5 * time.Second, MaxCrashes: 3, StopTimeout: 2 * time.Second}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func dead(pid int) bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }

func TestRingKeepsLastLinesAndJoinsPartialWrites(t *testing.T) {
	r := NewRing(3)
	r.Write([]byte("one\ntw"))
	r.Write([]byte("o\nthree\nfour\n"))
	if got := r.Tail(10); !slices.Equal(got, []string{"two", "three", "four"}) {
		t.Fatalf("Tail(10) = %v", got)
	}
	if got := r.Tail(1); !slices.Equal(got, []string{"four"}) {
		t.Fatalf("Tail(1) = %v", got)
	}
}

func TestRingSubscribe(t *testing.T) {
	r := NewRing(10)
	ch, cancel := r.Subscribe()
	r.Write([]byte("x\n"))
	if l := <-ch; l != "x" {
		t.Fatalf("got %q", l)
	}
	cancel()
	cancel()
	r.Write([]byte("y\n")) // must not panic
	ch2, _ := r.Subscribe()
	r.CloseSubs()
	if _, ok := <-ch2; ok {
		t.Fatal("CloseSubs should close subscriber channels")
	}
}

func TestRotatingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "box.log")
	w, err := openRotating(path, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	line := append(bytes.Repeat([]byte("x"), 49), '\n')
	for range 10 {
		if _, err := w.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	for _, suffix := range []string{"", ".1", ".2", ".3"} {
		if _, err := os.Stat(path + suffix); err != nil {
			t.Errorf("missing %s: %v", path+suffix, err)
		}
	}
	if _, err := os.Stat(path + ".4"); !os.IsNotExist(err) {
		t.Error(".4 should not exist")
	}
}

func TestLogAndTailFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "a.log")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(l, "hello\nworld\n")
	if got := l.Ring.Tail(5); !slices.Equal(got, []string{"hello", "world"}) {
		t.Fatalf("ring = %v", got)
	}
	l.Close()
	if got, _ := TailFile(path, 1); !slices.Equal(got, []string{"world"}) {
		t.Fatalf("TailFile = %v", got)
	}
	if got, err := TailFile(filepath.Join(t.TempDir(), "missing.log"), 5); got != nil || err != nil {
		t.Fatalf("missing file: %v, %v", got, err)
	}
}

func TestProcCapturesOutputAndStops(t *testing.T) {
	l, _ := OpenLog(filepath.Join(t.TempDir(), "a.log"))
	defer l.Close()
	var mu sync.Mutex
	var last Status
	p := New(Spec{Command: "echo hello; echo oops >&2; sleep 30", Dir: t.TempDir(), Env: os.Environ(), Out: l}, fast,
		func(s Status) { mu.Lock(); last = s; mu.Unlock() })
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	pid := p.Status().PID
	if pid == 0 || !p.Status().Running {
		t.Fatalf("status after start: %+v", p.Status())
	}
	eventually(t, "output", func() bool {
		tail := strings.Join(l.Ring.Tail(10), "\n")
		return strings.Contains(tail, "hello") && strings.Contains(tail, "oops")
	})
	if err := p.Start(); err == nil {
		t.Fatal("second Start should fail")
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if p.Status().Running || last.Running {
		t.Fatalf("still running: %+v / %+v", p.Status(), last)
	}
	eventually(t, "process exit", func() bool { return dead(pid) })
}

func TestStopKillsWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	p := New(Spec{Command: "sleep 300 & echo $! > child.pid; wait", Dir: dir, Env: os.Environ()}, fast, nil)
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	var child int
	eventually(t, "child pid file", func() bool {
		b, err := os.ReadFile(filepath.Join(dir, "child.pid"))
		child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && child > 0
	})
	p.Stop()
	eventually(t, "grandchild to die", func() bool { return dead(child) })
}

func TestCrashLoopGivesUp(t *testing.T) {
	p := New(Spec{Command: "exit 1", Dir: t.TempDir(), Env: os.Environ()}, fast, nil)
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "give up", func() bool { return p.Status().GaveUp })
	st := p.Status()
	if st.Restarts != 2 || !strings.Contains(st.LastError, "crashed 3 times") || st.Running {
		t.Fatalf("status %+v", st)
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestRestartsAfterCrash(t *testing.T) {
	dir := t.TempDir()
	p := New(Spec{Command: "if [ -f marker ]; then sleep 30; else touch marker; exit 1; fi", Dir: dir, Env: os.Environ()}, fast, nil)
	p.Start()
	defer p.Stop()
	eventually(t, "restart", func() bool { s := p.Status(); return s.Running && s.Restarts == 1 })
}

func TestStopBeforeStart(t *testing.T) {
	if err := New(Spec{Command: "true"}, fast, nil).Stop(); err != nil {
		t.Fatal(err)
	}
}
