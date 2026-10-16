package worktree

import (
	"strings"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Ayush2966/Agent-Box/internal/testutil"
)

func TestPullRequestURLAndRef(t *testing.T) {
	cases := []struct{ remote, url, ref string }{
		{"git@github.com:Ayush2966/Agent-Box.git", "https://github.com/Ayush2966/Agent-Box/pull/7", "refs/pull/7/head"},
		{"https://github.com/Ayush2966/Agent-Box.git", "https://github.com/Ayush2966/Agent-Box/pull/7", "refs/pull/7/head"},
		{"https://github.com/Ayush2966/Agent-Box", "https://github.com/Ayush2966/Agent-Box/pull/7", "refs/pull/7/head"},
		{"ssh://git@github.com/Ayush2966/Agent-Box.git", "https://github.com/Ayush2966/Agent-Box/pull/7", "refs/pull/7/head"},
		{"git@gitlab.com:group/project.git", "https://gitlab.com/group/project/-/merge_requests/7", "refs/merge-requests/7/head"},
		{"/tmp/some/bare.git", "", "refs/pull/7/head"},
	}
	for _, c := range cases {
		if got := PullRequestURL(c.remote, 7); got != c.url {
			t.Errorf("PullRequestURL(%q) = %q, want %q", c.remote, got, c.url)
		}
		if got := PullRef(c.remote, 7); got != c.ref {
			t.Errorf("PullRef(%q) = %q, want %q", c.remote, got, c.ref)
		}
	}
}

// A bare "origin" that publishes refs/pull/<n>/head, the way GitHub does.
func TestFetchPullRequest(t *testing.T) {
	repo := testutil.NewRepo(t, map[string]string{"README.md": "hi\n"})
	origin := filepath.Join(t.TempDir(), "origin.git")
	mustGit(t, "", "init", "--bare", "-q", origin)
	mustGit(t, repo, "remote", "add", "origin", origin)
	mustGit(t, repo, "push", "-q", "origin", "HEAD:refs/heads/main")
	// The "pull request": a commit that exists only on origin.
	mustGit(t, repo, "checkout", "-q", "-b", "feature")
	testutil.WriteFiles(t, repo, map[string]string{"new.txt": "pr\n"})
	mustGit(t, repo, "add", "new.txt")
	mustGit(t, repo, "commit", "-q", "-m", "pr commit")
	want := mustGit(t, repo, "rev-parse", "HEAD")
	mustGit(t, repo, "push", "-q", "origin", "HEAD:refs/pull/7/head")
	mustGit(t, repo, "checkout", "-q", "-")
	mustGit(t, repo, "branch", "-q", "-D", "feature")

	sha, webURL, err := FetchPullRequest(repo, 7)
	if err != nil || sha != want {
		t.Fatalf("sha %q (want %q), url %q, err %v", sha, want, webURL, err)
	}
	if webURL != "" {
		t.Fatalf("a path remote has no web URL, got %q", webURL)
	}
	if _, _, err := FetchPullRequest(repo, 8); err == nil {
		t.Fatal("a missing pull request must fail")
	}
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(trimNL(out))
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// ResolveStart falls back to a remote-tracking branch when a bare name has no
// local match, which is what "start from release/x" means to a developer.
func TestResolveStart(t *testing.T) {
	repo := testutil.NewRepo(t, map[string]string{"README.md": "hi\n"})
	origin := filepath.Join(t.TempDir(), "origin.git")
	mustGit(t, "", "init", "--bare", "-q", origin)
	mustGit(t, repo, "remote", "add", "origin", origin)
	mustGit(t, repo, "push", "-q", "origin", "HEAD:refs/heads/main")
	// A release branch that exists only on origin.
	mustGit(t, repo, "checkout", "-q", "-b", "release/en-16")
	mustGit(t, repo, "commit", "-q", "--allow-empty", "-m", "rel")
	want := mustGit(t, repo, "rev-parse", "release/en-16")
	mustGit(t, repo, "push", "-q", "origin", "release/en-16")
	mustGit(t, repo, "checkout", "-q", "main")
	mustGit(t, repo, "branch", "-q", "-D", "release/en-16")
	mustGit(t, repo, "fetch", "-q", "origin")

	// Bare remote-only name resolves to origin/<name>.
	got, err := ResolveStart(repo, "release/en-16")
	if err != nil || got != "origin/release/en-16" {
		t.Fatalf("ResolveStart = %q, %v; want origin/release/en-16", got, err)
	}
	if sha := mustGit(t, repo, "rev-parse", got); sha != want {
		t.Fatalf("resolved ref points at %s, want %s", sha, want)
	}
	// A local ref is returned unchanged.
	if got, _ := ResolveStart(repo, "main"); got != "main" {
		t.Fatalf("local ref changed: %q", got)
	}
	// Add actually uses it: a box from the bare name lands on the release commit.
	dir := filepath.Join(t.TempDir(), "box")
	if err := Add(repo, dir, "agentbox/sri", "release/en-16"); err != nil {
		t.Fatal(err)
	}
	if sha := mustGit(t, dir, "rev-parse", "HEAD"); sha != want {
		t.Fatalf("box HEAD %s, want the release commit %s", sha, want)
	}
	// A name on no remote is a clear error that mentions fetch.
	if _, err := ResolveStart(repo, "nope-xyz"); err == nil || !strings.Contains(err.Error(), "fetch") {
		t.Fatalf("missing ref error = %v", err)
	}
}
