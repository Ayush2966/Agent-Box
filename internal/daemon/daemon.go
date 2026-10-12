// Package daemon runs agentboxd: the long-lived process that owns boxes and serves the API.
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Ayush2966/Agent-Box/internal/api"
	"github.com/Ayush2966/Agent-Box/internal/box"
	"github.com/Ayush2966/Agent-Box/internal/events"
	"github.com/Ayush2966/Agent-Box/internal/ports"
	"github.com/Ayush2966/Agent-Box/internal/proxy"
	"github.com/Ayush2966/Agent-Box/internal/state"
	"github.com/Ayush2966/Agent-Box/internal/ui"
)

type Paths struct {
	Home, State, Sock, PID, Token, Log, Logs, Boxes, Config, Out, Lock, APIAddr string
}

func PathsFor(home string) Paths {
	j := func(n string) string { return filepath.Join(home, n) }
	return Paths{
		Home: home, State: j("state.db"), Sock: j("daemon.sock"), PID: j("daemon.pid"), Token: j("token"),
		Log: j("agentbox.log"), Logs: j("logs"), Boxes: j("boxes"), Config: j("config.yaml"), Out: j("daemon.out"),
		Lock: j("daemon.lock"), APIAddr: j("api_addr"),
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

// Version is reported by /v1/health; the CLI sets it before Run.
var Version = "dev"

// UIFS is the dashboard build the daemon serves. nil means ui.Dist(), the
// one embedded in this binary; tests substitute a fake build.
var UIFS fs.FS

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
	host, _, err := net.SplitHostPort(s.APIAddr)
	if err != nil || !isLoopbackHost(host) {
		return s, fmt.Errorf("%s: api_addr must be a loopback address such as 127.0.0.1:7777 (got %q); remote access is not supported", path, s.APIAddr)
	}
	return s, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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

// StalePID reports a pid file left behind by a daemon that is no longer
// running (it crashed or was killed before it could clean up).
func StalePID(p Paths) (pid int, stale bool) {
	pid, err := ReadPID(p.PID)
	if err != nil || pid <= 0 {
		return 0, false
	}
	if err := syscall.Kill(pid, 0); err == nil || errors.Is(err, syscall.EPERM) {
		return pid, false
	}
	return pid, true
}

// maxSockPath is the longest unix socket path the OS accepts (sun_path minus
// the terminating NUL): 104 bytes on macOS and the BSDs, 108 on Linux.
func maxSockPath() int {
	if runtime.GOOS == "linux" {
		return 107
	}
	return 103
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
	if max := maxSockPath(); len(p.Sock) > max {
		return fmt.Errorf("agentbox home %s is too long: its unix socket path is %d bytes and this OS allows %d; set AGENTBOX_HOME to a shorter path",
			p.Home, len(p.Sock), max)
	}
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
	_ = os.Remove(p.Sock) // stale socket from a crashed daemon (the home lock proves nobody owns it)
	ul, err := net.Listen("unix", p.Sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(p.Sock, 0o600); err != nil {
		ul.Close()
		return err
	}
	// Bind TCP before building the handlers: tests bind port 0, and the box
	// URLs and login links need the port that was actually chosen.
	apiAddr := ""
	tl, err := net.Listen("tcp", settings.APIAddr)
	if err != nil {
		slog.Warn("HTTP API disabled", "addr", settings.APIAddr, "err", err)
		_ = os.Remove(p.APIAddr)
	} else {
		apiAddr = tl.Addr().String()
		if err := os.WriteFile(p.APIAddr, []byte(apiAddr+"\n"), 0o600); err != nil {
			ul.Close()
			tl.Close()
			return err
		}
	}
	dist := UIFS
	if dist == nil {
		dist = ui.Dist()
	}
	codes := ui.NewLoginCodes()
	srv := &api.Server{M: m, Bus: bus, Token: token, Origins: origins(apiAddr), Codes: codes,
		UI: ui.Handler(ui.Options{Token: token, Codes: codes, FS: dist}), APIAddr: apiAddr, Version: Version, UIBuilt: ui.Built(dist)}
	px := proxy.New(proxy.Options{Lookup: m.ProxyTarget, Dashboard: "http://" + apiAddr + "/ui/"})
	base := func(net.Listener) context.Context { return ctx } // cancels SSE streams on shutdown
	servers := []*http.Server{{Handler: srv.Handler(false), BaseContext: base}}
	listeners := []net.Listener{ul}
	if tl != nil {
		servers = append(servers, &http.Server{Handler: Dispatch(px, srv.Handler(true)), BaseContext: base})
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
	slog.Info("daemon started", "pid", os.Getpid(), "sock", p.Sock, "api", apiAddr)

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
	_ = os.Remove(p.APIAddr)
	slog.Info("daemon stopped")
	return runErr
}

func origins(addr string) []string {
	if addr == "" {
		return nil
	}
	out := []string{"http://" + addr}
	if _, port, err := net.SplitHostPort(addr); err == nil {
		out = append(out, "http://localhost:"+port, "http://127.0.0.1:"+port, "http://[::1]:"+port)
	}
	return out
}

// Dispatch is the TCP listener's root: *.localhost hosts go to the box proxy,
// loopback hosts to the API and dashboard, and any other Host is refused.
// A page at evil.example whose DNS now points at 127.0.0.1 still sends
// "Host: evil.example", which is what makes this the rebinding defence.
func Dispatch(px, app http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		switch {
		case proxy.IsProxyHost(host):
			px.ServeHTTP(w, r)
		case host == "127.0.0.1" || host == "localhost" || host == "::1":
			app.ServeHTTP(w, r)
		default:
			http.Error(w, "agentbox answers only to 127.0.0.1, localhost and *.localhost, not to Host "+strconv.Quote(r.Host),
				http.StatusMisdirectedRequest)
		}
	})
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
	cmd.Dir = p.Home
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
