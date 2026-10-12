package compose

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentbox-dev/agentbox/internal/errs"
)

var ctx = context.Background()

func stack(rec *Recorder, log *bytes.Buffer) *Stack {
	return &Stack{Project: "agentbox-app-1a2b3c-a", Dir: "/tmp/boxes/a", File: "compose.yaml", EnvFile: ".env",
		Services: []string{"redis"}, Wait: 90 * time.Second, Env: []string{"REDIS_PORT=20001"}, Exec: rec, Log: log}
}

func TestProjectName(t *testing.T) {
	if got := ProjectName("app-1a2b3c", "feature-x"); got != "agentbox-app-1a2b3c-feature-x" {
		t.Fatal(got)
	}
}

func TestUpStopDownArgv(t *testing.T) {
	rec, log := &Recorder{}, &bytes.Buffer{}
	s := stack(rec, log)
	if err := s.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Down(ctx); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"compose", "-p", "agentbox-app-1a2b3c-a", "-f", "compose.yaml", "up", "-d", "--wait", "--wait-timeout", "90", "redis"},
		{"compose", "-p", "agentbox-app-1a2b3c-a", "-f", "compose.yaml", "stop"},
		{"compose", "-p", "agentbox-app-1a2b3c-a", "-f", "compose.yaml", "down", "-v", "--remove-orphans"},
	}
	if len(rec.Calls) != 3 {
		t.Fatalf("calls %v", rec.Calls)
	}
	for i := range want {
		if strings.Join(rec.Calls[i], " ") != strings.Join(want[i], " ") {
			t.Errorf("call %d = %v, want %v", i, rec.Calls[i], want[i])
		}
		if rec.Dirs[i] != "/tmp/boxes/a" || !contains(rec.Envs[i], "REDIS_PORT=20001") {
			t.Errorf("call %d ran in %q with env %v", i, rec.Dirs[i], rec.Envs[i])
		}
	}
	if !strings.Contains(log.String(), "$ docker compose -p agentbox-app-1a2b3c-a -f compose.yaml up") {
		t.Errorf("commands are echoed to the log:\n%s", log.String())
	}
}

func TestCustomEnvFileAndNoServices(t *testing.T) {
	rec := &Recorder{}
	s := stack(rec, &bytes.Buffer{})
	s.EnvFile, s.Services = ".env.development", nil
	if err := s.Up(ctx); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(rec.Calls[0], " ")
	if !strings.Contains(got, "--env-file .env.development up") || strings.HasSuffix(got, " redis") {
		t.Fatalf("%s", got)
	}
}

func TestUpFailureCarriesOutput(t *testing.T) {
	rec := &Recorder{Fail: map[string]error{"up": errors.New("exit status 1")}, Out: "Error response from daemon: port is already allocated\n"}
	s := stack(rec, &bytes.Buffer{})
	err := s.Up(ctx)
	e := errs.From(err)
	if e == nil || !strings.Contains(e.Message, "docker compose up failed") || !strings.Contains(e.Message, "port is already allocated") {
		t.Fatalf("%v", err)
	}
}

func TestResolveFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := ResolveFile(dir, ""); err == nil || !strings.Contains(err.Error(), "compose.yaml") {
		t.Fatalf("no file: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}\n"), 0o644)
	if f, err := ResolveFile(dir, ""); err != nil || f != "docker-compose.yml" {
		t.Fatalf("%q %v", f, err)
	}
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0o644)
	if f, _ := ResolveFile(dir, ""); f != "compose.yaml" {
		t.Fatalf("precedence: %q", f)
	}
	if _, err := ResolveFile(dir, "deploy/compose.yaml"); err == nil {
		t.Fatal("configured file that does not exist must error")
	}
	os.MkdirAll(filepath.Join(dir, "deploy"), 0o755)
	os.WriteFile(filepath.Join(dir, "deploy", "compose.yaml"), []byte("services: {}\n"), 0o644)
	if f, err := ResolveFile(dir, "deploy/compose.yaml"); err != nil || f != "deploy/compose.yaml" {
		t.Fatalf("%q %v", f, err)
	}

	// a directory named like a compose file is not a compose file
	dirOnly := t.TempDir()
	os.Mkdir(filepath.Join(dirOnly, "compose.yml"), 0o755)
	os.WriteFile(filepath.Join(dirOnly, "docker-compose.yaml"), []byte("services: {}\n"), 0o644)
	if f, err := ResolveFile(dirOnly, ""); err != nil || f != "docker-compose.yaml" {
		t.Fatalf("directory must be skipped: %q %v", f, err)
	}
	os.Mkdir(filepath.Join(dirOnly, "deploy-dir"), 0o755)
	if _, err := ResolveFile(dirOnly, "deploy-dir"); err == nil {
		t.Fatal("a configured directory must error")
	}
}

func TestProjectsAndDownProject(t *testing.T) {
	rec := &Recorder{Stdout: map[string]string{"ls": `[{"Name":"agentbox-app-1a2b3c-a","Status":"running(1)"},{"Name":"other","Status":"exited(1)"},{"Name":"agentbox-app-1a2b3c-ghost","Status":"exited(1)"},{"Name":"agentbox-web-9z9z9z-a","Status":"running(2)"}]`}}
	got, err := Projects(ctx, rec, "agentbox-app-1a2b3c-")
	if err != nil || strings.Join(got, ",") != "agentbox-app-1a2b3c-a,agentbox-app-1a2b3c-ghost" {
		t.Fatalf("%v %v", got, err)
	}
	if strings.Join(rec.Calls[0], " ") != "compose ls --all --format json" {
		t.Fatalf("%v", rec.Calls[0])
	}
	log := &bytes.Buffer{}
	if err := DownProject(ctx, rec, "agentbox-app-1a2b3c-ghost", log); err != nil {
		t.Fatal(err)
	}
	if strings.Join(rec.Calls[1], " ") != "compose -p agentbox-app-1a2b3c-ghost down -v --remove-orphans" {
		t.Fatalf("%v", rec.Calls[1])
	}
	if err := StopProject(ctx, rec, "agentbox-app-1a2b3c-a", log); err != nil {
		t.Fatal(err)
	}
	if strings.Join(rec.Calls[2], " ") != "compose -p agentbox-app-1a2b3c-a stop" {
		t.Fatalf("%v", rec.Calls[2])
	}
	bad := &Recorder{Stdout: map[string]string{"ls": "not json"}}
	if _, err := Projects(ctx, bad, "agentbox-"); err == nil {
		t.Fatal("unparsable ls output must error")
	}
}

func TestCheck(t *testing.T) {
	ok := &Recorder{Stdout: map[string]string{"version": "Docker Compose version v2.29.0\n"}}
	if err := Check(ctx, ok); err != nil {
		t.Fatal(err)
	}
	missing := &Recorder{Fail: map[string]error{"version": errors.New(`exec: "docker": executable file not found in $PATH`)}}
	err := Check(ctx, missing)
	if e := errs.From(err); e == nil || e.Code != errs.Precondition || !strings.Contains(e.Hint, "Docker") {
		t.Fatalf("%v", err)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestWithStderrKeepsTheToolsMessage(t *testing.T) {
	_, err := exec.Command("sh", "-c", "echo 'Cannot connect to the Docker daemon' >&2; exit 1").Output()
	if err == nil {
		t.Fatal("expected a failure")
	}
	got := withStderr(err).Error()
	if !strings.Contains(got, "exit status 1") || !strings.Contains(got, "Cannot connect to the Docker daemon") {
		t.Fatalf("%q", got)
	}
	if withStderr(nil) != nil || withStderr(errors.New("plain")).Error() != "plain" {
		t.Fatal("non-exit errors must pass through")
	}
}
