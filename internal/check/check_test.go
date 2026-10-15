package check

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ayush2966/Agent-Box/internal/testutil"
)

var ctx = context.Background()

func has(fs []Finding, level, substr string) bool {
	for _, f := range fs {
		if f.Level == level && strings.Contains(f.Msg, substr) {
			return true
		}
	}
	return false
}

func TestStaticFindings(t *testing.T) {
	bad := testutil.NewRepo(t, map[string]string{"agentbox.yaml": "version: 2\n"})
	if fs := Run(ctx, bad, Options{}); !Failed(fs) || !has(fs, "error", "version must be 1") {
		t.Fatalf("invalid config: %+v", fs)
	}

	missing := testutil.NewRepo(t, map[string]string{"agentbox.yaml": "version: 1\ndev: node s.js\nports: [APP_PORT]\nenv: {template: .env.example}\ncopy: [.env.local]\n"})
	fs := Run(ctx, missing, Options{})
	if !has(fs, "error", "env.template: .env.example is missing") || !has(fs, "error", "copy: .env.local is missing") {
		t.Fatalf("missing files: %+v", fs)
	}
	if !has(fs, "warning", "dev does not pass any of [APP_PORT]") {
		t.Fatalf("port warning: %+v", fs)
	}

	// A tracked env file (committed .env) would show as modified in every box.
	tracked := testutil.NewRepo(t, map[string]string{"agentbox.yaml": "version: 1\ndev: vite --port \"$APP_PORT\"\nports: [APP_PORT]\n", ".env": "A=1\n"})
	fs = Run(ctx, tracked, Options{})
	if Failed(fs) || !has(fs, "warning", "env.file .env is tracked by git") || !has(fs, "ok", "dev passes the port") {
		t.Fatalf("tracked env: %+v", fs)
	}

	fine := testutil.NewRepo(t, map[string]string{"agentbox.yaml": "version: 1\ndev: vite --port \"$APP_PORT\"\nports: [APP_PORT]\nlinks: {API_URL: api}\n", ".gitignore": ".env\n"})
	fs = Run(ctx, fine, Options{})
	if Failed(fs) || has(fs, "warning", "") || !has(fs, "ok", "link API_URL") {
		t.Fatalf("clean repo: %+v", fs)
	}
}

func TestRehearsalRunsSetupInAThrowawayWorktree(t *testing.T) {
	repo := testutil.NewRepo(t, map[string]string{
		"agentbox.yaml": "version: 1\nsetup: echo \"port=$APP_PORT name=$AGENTBOX_NAME\" > setup.out && cat .env\ndev: sleep 30\nports: [APP_PORT]\nenv: {template: .env.example}\n",
		".env.example":  "KEEP=1\n",
		".gitignore":    ".env\nsetup.out\n",
	})
	var out bytes.Buffer
	fs := Run(ctx, repo, Options{Setup: true, Out: &out})
	if Failed(fs) || !has(fs, "ok", "setup ran") {
		t.Fatalf("findings %+v\n%s", fs, out.String())
	}
	if !strings.Contains(out.String(), "KEEP=1") || !strings.Contains(out.String(), "APP_PORT=") {
		t.Fatalf("setup did not see the rendered env file:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(repo, "setup.out")); err == nil {
		t.Fatal("setup ran in the main checkout, not in a worktree")
	}
	branches, _ := exec.Command("git", "-C", repo, "branch", "--list", "agentbox/check-*").Output()
	if strings.TrimSpace(string(branches)) != "" {
		t.Fatalf("temporary branch left behind: %s", branches)
	}
	wts, _ := exec.Command("git", "-C", repo, "worktree", "list").Output()
	if strings.Count(string(wts), "\n") != 1 {
		t.Fatalf("temporary worktree left behind:\n%s", wts)
	}

	broken := testutil.NewRepo(t, map[string]string{"agentbox.yaml": "version: 1\nsetup: echo nope >&2; exit 3\nports: [APP_PORT]\n", ".gitignore": ".env\n"})
	fs = Run(ctx, broken, Options{Setup: true})
	if !Failed(fs) || !has(fs, "error", "setup:") || !has(fs, "error", "nope") {
		t.Fatalf("failing setup: %+v", fs)
	}
}

func TestRehearsalWaitsForTheDevServer(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	listen := `python3 -c "import os,socket,time; s=socket.socket(); s.bind(('127.0.0.1', int(os.environ['APP_PORT']))); s.listen(1); time.sleep(30)"`
	repo := testutil.NewRepo(t, map[string]string{"agentbox.yaml": "version: 1\ndev: " + listen + "\nports: [APP_PORT]\n", ".gitignore": ".env\n"})
	fs := Run(ctx, repo, Options{Dev: 10 * time.Second})
	if Failed(fs) || !has(fs, "ok", "dev answered on port") {
		t.Fatalf("listening dev: %+v", fs)
	}
	crash := testutil.NewRepo(t, map[string]string{"agentbox.yaml": "version: 1\ndev: echo boom; exit 1\nports: [APP_PORT]\n", ".gitignore": ".env\n"})
	fs = Run(ctx, crash, Options{Dev: 5 * time.Second})
	if !Failed(fs) || !has(fs, "error", "dev exited early") || !has(fs, "error", "boom") {
		t.Fatalf("crashing dev: %+v", fs)
	}
	silent := testutil.NewRepo(t, map[string]string{"agentbox.yaml": "version: 1\ndev: sleep 30\nports: [APP_PORT]\n", ".gitignore": ".env\n"})
	fs = Run(ctx, silent, Options{Dev: 1 * time.Second})
	if !Failed(fs) || !has(fs, "error", "did not answer on port") {
		t.Fatalf("silent dev: %+v", fs)
	}
}
