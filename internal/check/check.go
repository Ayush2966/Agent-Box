// Package check validates a repo's agentbox.yaml without the daemon, and can
// rehearse a box: run setup (and the dev server) in a throwaway worktree.
// It is what `agentbox check` runs, locally and in CI.
package check

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Ayush2966/Agent-Box/internal/config"
	"github.com/Ayush2966/Agent-Box/internal/envfile"
	"github.com/Ayush2966/Agent-Box/internal/worktree"
)

type Finding struct {
	Level string // "error" | "warning" | "ok"
	Msg   string
}

type Options struct {
	Setup bool          // run the setup command in a temporary worktree
	Dev   time.Duration // after setup, start dev and wait this long for the proxy port to answer; 0 = don't
	Out   io.Writer     // setup/dev output; nil = discard
}

// Failed reports whether any finding is an error.
func Failed(fs []Finding) bool {
	for _, f := range fs {
		if f.Level == "error" {
			return true
		}
	}
	return false
}

var (
	containerName = regexp.MustCompile(`(?m)^\s*container_name:`)
	fixedHostPort = regexp.MustCompile(`"?\b(\d{2,5}):(\d{2,5})\b"?`)
)

// Run checks the repo at root and returns what it found, in order.
func Run(ctx context.Context, root string, o Options) []Finding {
	var fs []Finding
	add := func(level, format string, args ...any) { fs = append(fs, Finding{level, fmt.Sprintf(format, args...)}) }

	cfg, err := config.Load(root)
	if err != nil {
		add("error", "%v", err)
		return fs
	}
	add("ok", "%s is valid", config.FileName)

	if cfg.Dev == "" && cfg.Compose == nil {
		add("warning", "no dev command: boxes will be created but nothing will run in them")
	}
	if cfg.Dev != "" {
		switch {
		case len(cfg.Ports) == 0:
			add("warning", "no ports: boxes get no URL; add `ports: [APP_PORT]` and pass it to the dev server")
		case !mentionsAny(cfg.Dev, cfg.Ports):
			add("warning", "dev does not pass any of %v on its command line; the server must read it from the env or two boxes will fight for one port", cfg.Ports)
		default:
			add("ok", "dev passes the port (%s)", cfg.ProxyPort())
		}
	}
	for field, p := range map[string]string{"env.template": cfg.Env.Template} {
		if p != "" {
			if _, err := os.Stat(filepath.Join(root, p)); err != nil {
				add("error", "%s: %s is missing", field, p)
			}
		}
	}
	for _, p := range cfg.Copy {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			add("error", "copy: %s is missing (it must exist in every developer's checkout)", p)
		}
	}
	if tracked(root, cfg.Env.File) {
		add("warning", "env.file %s is tracked by git: the box's copy will show as modified; use a git-ignored file (e.g. env: { template: .env.local, file: .env.local })", cfg.Env.File)
	}
	if cfg.Compose != nil {
		name := cfg.Compose.File
		if name == "" {
			for _, c := range []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"} {
				if _, err := os.Stat(filepath.Join(root, c)); err == nil {
					name = c
					break
				}
			}
		}
		if data, err := os.ReadFile(filepath.Join(root, name)); err != nil {
			add("error", "compose file not found (%s)", name)
		} else {
			if containerName.Match(data) {
				add("warning", "%s sets container_name; Docker needs it unique, so a second box cannot start", name)
			}
			if m := fixedHostPort.FindString(string(data)); m != "" {
				add("warning", "%s publishes a fixed host port (%s); use \"${VAR}:containerPort\" with VAR under ports", name, m)
			}
		}
	}
	for k, spec := range cfg.Links {
		if _, err := config.ParseLink(spec); err == nil {
			add("ok", "link %s → %s (resolved by the daemon when a box is created)", k, spec)
		}
	}
	if !o.Setup && o.Dev == 0 {
		return fs
	}
	fs = append(fs, rehearse(ctx, root, cfg, o)...)
	return fs
}

func mentionsAny(cmd string, vars []string) bool {
	for _, v := range vars {
		if strings.Contains(cmd, "$"+v) || strings.Contains(cmd, "${"+v+"}") {
			return true
		}
	}
	return false
}

func tracked(root, rel string) bool {
	cmd := exec.Command("git", "ls-files", "--error-unmatch", "--", rel)
	cmd.Dir = root
	return cmd.Run() == nil
}

// rehearse runs setup (and dev) the way a box would, in a temporary worktree
// of HEAD with free ports in the env, then removes every trace of it.
func rehearse(ctx context.Context, root string, cfg *config.Config, o Options) []Finding {
	var fs []Finding
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	dir, err := os.MkdirTemp("", "agentbox-check-")
	if err != nil {
		return []Finding{{"error", err.Error()}}
	}
	dir = filepath.Join(dir, "box")
	branch := "agentbox/check-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := worktree.Add(root, dir, branch, ""); err != nil {
		return []Finding{{"error", "temporary worktree: " + err.Error()}}
	}
	defer func() {
		_ = worktree.Remove(root, dir)
		_ = worktree.DeleteBranch(root, branch)
		_ = os.RemoveAll(filepath.Dir(dir))
	}()

	// The env a box would get, minus the database copy (the daemon's job).
	var vars []envfile.KV
	for _, k := range cfg.Ports {
		p, err := freePort()
		if err != nil {
			return []Finding{{"error", err.Error()}}
		}
		vars = append(vars, envfile.KV{Key: k, Value: strconv.Itoa(p)})
	}
	for k, v := range cfg.Env.Set {
		vars = append(vars, envfile.KV{Key: k, Value: v})
	}
	vars = append(vars, envfile.KV{Key: "AGENTBOX_NAME", Value: "check"}, envfile.KV{Key: "AGENTBOX_ROOT", Value: dir})
	for _, rel := range cfg.Copy {
		if data, err := os.ReadFile(filepath.Join(root, rel)); err == nil {
			_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755)
			_ = os.WriteFile(filepath.Join(dir, rel), data, 0o600)
		}
	}
	var tmpl []byte
	if cfg.Env.Template != "" {
		tmpl, _ = os.ReadFile(filepath.Join(root, cfg.Env.Template))
	}
	_ = os.WriteFile(filepath.Join(dir, cfg.Env.File), envfile.Render(tmpl, vars), 0o600)
	env := append(os.Environ(), envfile.Pairs(vars)...)

	if cfg.Setup != "" {
		start := time.Now()
		if err := runSetup(ctx, cfg.Setup, dir, env, out); err != nil {
			fs = append(fs, Finding{"error", "setup: " + err.Error()})
			return fs
		}
		fs = append(fs, Finding{"ok", fmt.Sprintf("setup ran in %s", time.Since(start).Round(time.Second))})
	}
	if o.Dev <= 0 || cfg.Dev == "" {
		return fs
	}
	port := 0
	for _, kv := range vars {
		if kv.Key == cfg.ProxyPort() {
			port, _ = strconv.Atoi(kv.Value)
		}
	}
	start := time.Now()
	ok, lastOut, err := runDev(ctx, cfg.Dev, dir, env, out, port, o.Dev)
	switch {
	case err != nil:
		fs = append(fs, Finding{"error", "dev: " + err.Error()})
	case !ok:
		fs = append(fs, Finding{"error", fmt.Sprintf("dev did not answer on port %d within %s; last output:\n%s", port, o.Dev, lastOut)})
	default:
		fs = append(fs, Finding{"ok", fmt.Sprintf("dev answered on port %d after %s", port, time.Since(start).Round(time.Second))})
	}
	return fs
}

func runSetup(ctx context.Context, command, dir string, env []string, out io.Writer) error {
	tail := &ring{n: 20}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir, cmd.Env = dir, env
	w := io.MultiWriter(out, tail)
	cmd.Stdout, cmd.Stderr = w, w
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%q failed (%v); last output:\n%s", command, err, tail.String())
	}
	return nil
}

// runDev starts the dev command in its own process group, waits up to
// timeout for port to accept a connection (on 127.0.0.1 or [::1]), then
// kills the whole group.
func runDev(ctx context.Context, command, dir string, env []string, out io.Writer, port int, timeout time.Duration) (bool, string, error) {
	tail := &ring{n: 20}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir, cmd.Env = dir, env
	w := io.MultiWriter(out, tail)
	cmd.Stdout, cmd.Stderr = w, w
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return false, "", err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-exited
		}
	}()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			exited <- err
			return false, tail.String(), fmt.Errorf("dev exited early (%v); last output:\n%s", err, tail.String())
		case <-ctx.Done():
			return false, tail.String(), ctx.Err()
		default:
		}
		if portAnswers(port) {
			return true, tail.String(), nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false, tail.String(), nil
}

func portAnswers(port int) bool {
	for _, host := range []string{"127.0.0.1", "[::1]"} {
		c, err := net.DialTimeout("tcp", host+":"+strconv.Itoa(port), 300*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
	}
	return false
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// ring keeps the last n lines written to it.
type ring struct {
	n     int
	lines []string
	part  string
}

func (r *ring) Write(p []byte) (int, error) {
	s := r.part + string(p)
	parts := strings.Split(s, "\n")
	r.part = parts[len(parts)-1]
	for _, l := range parts[:len(parts)-1] {
		r.lines = append(r.lines, l)
		if len(r.lines) > r.n {
			r.lines = r.lines[1:]
		}
	}
	return len(p), nil
}

func (r *ring) String() string {
	lines := r.lines
	if r.part != "" {
		lines = append(append([]string{}, lines...), r.part)
	}
	return strings.Join(lines, "\n")
}
