// Package daemon runs agentboxd: the long-lived process that owns boxes and serves the API.
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/agentbox-dev/agentbox/internal/api"
	"github.com/agentbox-dev/agentbox/internal/box"
	"github.com/agentbox-dev/agentbox/internal/events"
	"github.com/agentbox-dev/agentbox/internal/ports"
	"github.com/agentbox-dev/agentbox/internal/state"
)

type Paths struct {
	Home, State, Sock, PID, Token, Log, Logs, Boxes, Config, Out, Lock string
}

func PathsFor(home string) Paths {
	j := func(n string) string { return filepath.Join(home, n) }
	return Paths{
		Home: home, State: j("state.db"), Sock: j("daemon.sock"), PID: j("daemon.pid"), Token: j("token"),
		Log: j("agentbox.log"), Logs: j("logs"), Boxes: j("boxes"), Config: j("config.yaml"), Out: j("daemon.out"),
		Lock: j("daemon.lock"),
	}
}

func DefaultHome() (string, error) {
	if h := os.Getenv("AGENTBOX_HOME"); h != "" {
		return filepath.Abs(h)
	}
	uh, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(uh, ".agentbox"), nil
}

// ErrAlreadyRunning means another daemon already owns this agentbox home.
var ErrAlreadyRunning = errors.New("agentbox daemon is already running")

// lockHome takes an exclusive, process-lifetime lock on the agentbox home so
// that exactly one daemon owns it, even when several CLI invocations auto-start
// one at the same instant. The lock is released when the file is closed (or
// when the process dies), so a crashed daemon never blocks the next one.
func lockHome(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return f, nil
}

type Settings struct {
	PortRange []int  `yaml:"port_range"`
	APIAddr   string `yaml:"api_addr"`
}

func LoadSettings(path string) (Settings, error) {
	s := Settings{PortRange: []int{ports.DefaultLo, ports.DefaultHi}, APIAddr: "127.0.0.1:7777"}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := yaml.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}
	if len(s.PortRange) != 2 || s.PortRange[0] < 1024 || s.PortRange[1] > 65535 || s.PortRange[0] > s.PortRange[1] {
		return s, fmt.Errorf("%s: port_range must be [low, high] within 1024-65535 (got %v)", path, s.PortRange)
	}
	return s, nil
}

func Ping(sock string) bool {
	c, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func ReadPID(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

func ensureToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return strings.TrimSpace(string(b)), nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	return tok, os.WriteFile(path, []byte(tok+"\n"), 0o600)
}

// Run is the daemon's main loop. It returns when ctx is cancelled, after
// stopping every dev server.
func Run(ctx context.Context, p Paths) error {
	for _, d := range []string{p.Home, p.Logs, p.Boxes} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if Ping(p.Sock) {
		return ErrAlreadyRunning
	}
	// Ping is only advisory: several CLIs auto-starting at once all see no
	// socket and race into Listen. The home lock is the real guard, and
	// holding it is what makes removing a stale socket below safe.
	lock, err := lockHome(p.Lock)
	if err != nil {
		return err
	}
	defer lock.Close()

	logf, err := os.OpenFile(p.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	settings, err := LoadSettings(p.Config)
	if err != nil {
		return err
	}
	token, err := ensureToken(p.Token)
	if err != nil {
		return err
	}
	store, err := state.Open(p.State)
	if err != nil {
		return err
	}
	defer store.Close()

	bus := events.NewBus()
	m := box.NewManager(box.Deps{Store: store, BoxesDir: p.Boxes, LogsDir: p.Logs,
		PortLo: settings.PortRange[0], PortHi: settings.PortRange[1], Bus: bus})
	if err := m.Recover(); err != nil {
		return err
	}
	srv := &api.Server{M: m, Bus: bus, Token: token, Origins: origins(settings.APIAddr)}

	_ = os.Remove(p.Sock) // stale socket from a crashed daemon (the home lock proves nobody owns it)
	ul, err := net.Listen("unix", p.Sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(p.Sock, 0o600); err != nil {
		ul.Close()
		return err
	}
	base := func(net.Listener) context.Context { return ctx } // cancels SSE streams on shutdown
	servers := []*http.Server{{Handler: srv.Handler(false), BaseContext: base}}
	listeners := []net.Listener{ul}
	if tl, err := net.Listen("tcp", settings.APIAddr); err != nil {
		slog.Warn("HTTP API disabled", "addr", settings.APIAddr, "err", err)
	} else {
		servers = append(servers, &http.Server{Handler: srv.Handler(true), BaseContext: base})
		listeners = append(listeners, tl)
	}
	if err := os.WriteFile(p.PID, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return err
	}

	errc := make(chan error, len(servers))
	for i, s := range servers {
		go func() {
			if err := s.Serve(listeners[i]); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}
	slog.Info("daemon started", "pid", os.Getpid(), "sock", p.Sock, "api", settings.APIAddr)

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errc:
	}
	m.Shutdown()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(sctx)
	}
	_ = os.Remove(p.Sock)
	_ = os.Remove(p.PID)
	slog.Info("daemon stopped")
	return runErr
}

func origins(addr string) []string {
	out := []string{"http://" + addr}
	if _, port, err := net.SplitHostPort(addr); err == nil {
		out = append(out, "http://localhost:"+port, "http://127.0.0.1:"+port)
	}
	return out
}

// EnsureRunning starts the daemon in the background if it isn't answering.
func EnsureRunning(p Paths) error {
	if Ping(p.Sock) {
		return nil
	}
	if err := os.MkdirAll(p.Home, 0o700); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(p.Out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	cmd := exec.Command(exe, "daemon", "run")
	cmd.Env = append(os.Environ(), "AGENTBOX_HOME="+p.Home)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start agentbox daemon: %w", err)
	}
	_ = cmd.Process.Release()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if Ping(p.Sock) {
			return nil
		}
	}
	return fmt.Errorf("agentbox daemon did not start within 3s; see %s", p.Out)
}

// Stop asks the running daemon to exit (SIGTERM) and waits for its socket to go away.
func Stop(p Paths, timeout time.Duration) error {
	if !Ping(p.Sock) {
		return nil
	}
	pid, err := ReadPID(p.PID)
	if err != nil {
		return fmt.Errorf("daemon is running but %s is unreadable: %w", p.PID, err)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal daemon (pid %d): %w", pid, err)
	}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if !Ping(p.Sock) {
			return nil
		}
	}
	return fmt.Errorf("daemon (pid %d) did not stop within %s", pid, timeout)
}
