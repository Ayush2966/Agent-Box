// Package compose runs one Docker Compose project per box through the docker CLI.
package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/agentbox-dev/agentbox/internal/errs"
	"github.com/agentbox-dev/agentbox/internal/proc"
)

// Executor runs the docker CLI. Docker is the real one; Recorder is for tests.
type Executor interface {
	// Run executes `docker args…` in dir with env, streaming combined output to out.
	Run(ctx context.Context, dir string, env []string, out io.Writer, args ...string) error
	// Output executes `docker args…` and returns its stdout.
	Output(ctx context.Context, args ...string) ([]byte, error)
}

// Docker shells out to the docker binary on PATH.
type Docker struct{}

func (Docker) Run(ctx context.Context, dir string, env []string, out io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 2 * time.Second
	return cmd.Run()
}

func (Docker) Output(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	return out, withStderr(err)
}

// withStderr makes an exec failure say what the tool printed, not just its exit status.
func withStderr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(bytes.TrimSpace(ee.Stderr)) > 0 {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}

// DefaultFiles are the compose file names Docker itself looks for, in order.
var DefaultFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"}

// ProjectName is deterministic so doctor can recognise agentbox's projects.
func ProjectName(repoSlug, box string) string { return "agentbox-" + repoSlug + "-" + box }

const installHint = "install Docker Desktop, or the docker CLI with the compose plugin, and make sure the daemon is running"

// Check verifies docker and its compose plugin answer.
func Check(ctx context.Context, ex Executor) error {
	out, err := ex.Output(ctx, "compose", "version")
	if err != nil {
		return errs.New(errs.Precondition, "docker compose is not available: %v", err).WithHint(installHint)
	}
	if !strings.Contains(strings.ToLower(string(out)), "compose") {
		return errs.New(errs.Precondition, "docker answered but the compose plugin is missing: %s", strings.TrimSpace(string(out))).WithHint(installHint)
	}
	return nil
}

// ResolveFile returns the compose file to use, relative to dir: the configured
// one (which must exist) or the first default name present.
func ResolveFile(dir, configured string) (string, error) {
	if configured != "" {
		if fi, err := os.Stat(filepath.Join(dir, configured)); err != nil || fi.IsDir() {
			return "", errs.New(errs.Precondition, "compose file %s not found in %s", configured, dir)
		}
		return configured, nil
	}
	for _, name := range DefaultFiles {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && !fi.IsDir() {
			return name, nil
		}
	}
	return "", errs.New(errs.Precondition, "no compose file found in %s (looked for %s)", dir, strings.Join(DefaultFiles, ", ")).
		WithHint("set compose.file in agentbox.yaml")
}

// Stack is one box's Compose project.
type Stack struct {
	Project  string        // agentbox-<repo-slug>-<box>
	Dir      string        // the box directory; every command runs here
	File     string        // compose file, relative to Dir
	EnvFile  string        // env file, relative to Dir; ".env" or "" means compose's default
	Services []string      // subset to bring up; nil = all
	Wait     time.Duration // `up --wait` timeout
	Env      []string      // process environment (the box env on top of the daemon's)
	Exec     Executor
	Log      io.Writer // the box log
}

func (s *Stack) base() []string {
	args := []string{"compose", "-p", s.Project, "-f", s.File}
	if s.EnvFile != "" && s.EnvFile != ".env" {
		args = append(args, "--env-file", s.EnvFile)
	}
	return args
}

func (s *Stack) Up(ctx context.Context) error {
	args := append(s.base(), "up", "-d", "--wait", "--wait-timeout", strconv.Itoa(int(s.Wait.Seconds())))
	return s.run(ctx, append(args, s.Services...))
}

func (s *Stack) Stop(ctx context.Context) error { return s.run(ctx, append(s.base(), "stop")) }

func (s *Stack) Down(ctx context.Context) error {
	return s.run(ctx, append(s.base(), "down", "-v", "--remove-orphans"))
}

// run echoes the command into the log, runs it, and on failure reports the
// verb (up/stop/down) with the last lines of output.
func (s *Stack) run(ctx context.Context, args []string) error {
	return runLogged(ctx, s.Exec, s.Dir, s.Env, s.Log, args)
}

func runLogged(ctx context.Context, ex Executor, dir string, env []string, log io.Writer, args []string) error {
	if log == nil {
		log = io.Discard
	}
	fmt.Fprintf(log, "$ docker %s\n", strings.Join(args, " "))
	tail := proc.NewRing(20)
	w := io.MultiWriter(log, tail)
	if err := ex.Run(ctx, dir, env, w, args...); err != nil {
		verb := "compose"
		for i, a := range args {
			if a == "up" || a == "stop" || a == "down" {
				verb = "compose " + args[i]
				break
			}
		}
		return errs.New(errs.Internal, "docker %s failed (%v); last output:\n%s", verb, err, strings.Join(tail.Tail(20), "\n")).
			WithHint("the full output is in the box log (`agentbox logs <name>`)")
	}
	return nil
}

// Projects lists the Compose projects Docker knows (running or not) whose
// names start with prefix.
func Projects(ctx context.Context, ex Executor, prefix string) ([]string, error) {
	out, err := ex.Output(ctx, "compose", "ls", "--all", "--format", "json")
	if err != nil {
		return nil, errs.New(errs.Precondition, "docker compose ls: %v", err).WithHint(installHint)
	}
	var rows []struct {
		Name string `json:"Name"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("could not parse `docker compose ls` output: %w", err)
	}
	var names []string
	for _, r := range rows {
		if strings.HasPrefix(r.Name, prefix) {
			names = append(names, r.Name)
		}
	}
	return names, nil
}

// DownProject removes a project by name alone (Compose finds its containers
// by label), so it works after the box dir or its compose file is gone.
func DownProject(ctx context.Context, ex Executor, project string, log io.Writer) error {
	return runLogged(ctx, ex, "", nil, log, []string{"compose", "-p", project, "down", "-v", "--remove-orphans"})
}

// StopProject stops a project's containers by name, keeping them and their volumes.
func StopProject(ctx context.Context, ex Executor, project string, log io.Writer) error {
	return runLogged(ctx, ex, "", nil, log, []string{"compose", "-p", project, "stop"})
}
