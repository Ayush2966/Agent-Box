package worktree

import (
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
