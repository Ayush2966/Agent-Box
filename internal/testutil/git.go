// Package testutil holds helpers shared by tests.
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var gitEnv = []string{
	"GIT_CONFIG_NOSYSTEM=1",
	"GIT_AUTHOR_NAME=agentbox-test", "GIT_AUTHOR_EMAIL=test@example.com",
	"GIT_COMMITTER_NAME=agentbox-test", "GIT_COMMITTER_EMAIL=test@example.com",
}

// Git runs git in dir and fails the test on error.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// WriteFiles writes name → content pairs under dir, creating parent dirs.
func WriteFiles(t testing.TB, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// InitRepo turns dir into a git repo on branch main with everything in it
// committed, and returns dir with symlinks resolved.
func InitRepo(t testing.TB, dir string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	Git(t, dir, "init", "-q", "-b", "main")
	Git(t, dir, "config", "user.email", "test@example.com")
	Git(t, dir, "config", "user.name", "agentbox-test")
	Git(t, dir, "config", "commit.gpgsign", "false")
	Git(t, dir, "add", "-A")
	Git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

// NewRepo creates a committed repo containing files in a fresh temp dir.
func NewRepo(t testing.TB, files map[string]string) string {
	t.Helper()
	return NewRepoIn(t, t.TempDir(), files)
}

// NewRepoIn creates a committed repo containing files at dir.
func NewRepoIn(t testing.TB, dir string, files map[string]string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	WriteFiles(t, dir, files)
	return InitRepo(t, dir)
}
