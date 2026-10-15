package worktree

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// PullRef is the remote ref that holds a pull request's head: GitHub, Gitea
// and Forgejo publish refs/pull/<n>/head, GitLab refs/merge-requests/<n>/head.
func PullRef(remoteURL string, n int) string {
	if strings.Contains(strings.ToLower(remoteURL), "gitlab") {
		return fmt.Sprintf("refs/merge-requests/%d/head", n)
	}
	return fmt.Sprintf("refs/pull/%d/head", n)
}

var scpLike = regexp.MustCompile(`^(?:[^@]+@)?([^:/]+):(.+?)(?:\.git)?/?$`)

// PullRequestURL turns a remote URL and a number into the web page of the pull
// request, or "" when the remote is not a known forge URL.
func PullRequestURL(remoteURL string, n int) string {
	host, path := splitRemote(remoteURL)
	if host == "" || strings.Count(path, "/") != 1 {
		return ""
	}
	if strings.Contains(strings.ToLower(host), "gitlab") {
		return fmt.Sprintf("https://%s/%s/-/merge_requests/%d", host, path, n)
	}
	return fmt.Sprintf("https://%s/%s/pull/%d", host, path, n)
}

// splitRemote returns host and "owner/repo" for https://, ssh:// and
// git@host:owner/repo.git remotes.
func splitRemote(remote string) (host, path string) {
	remote = strings.TrimSpace(remote)
	if u, err := url.Parse(remote); err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http" || u.Scheme == "ssh" || u.Scheme == "git") {
		return u.Hostname(), strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	}
	if m := scpLike.FindStringSubmatch(remote); m != nil {
		return m[1], strings.Trim(m[2], "/")
	}
	return "", ""
}

// FetchPullRequest fetches pull request n from origin into repo and returns
// the commit it points at, plus the web URL of the request when known. The
// commit is then usable as the `from` of a new box.
func FetchPullRequest(repo string, n int) (sha, webURL string, err error) {
	remote, err := git(repo, "remote", "get-url", "origin")
	if err != nil {
		return "", "", fmt.Errorf("this repo has no origin remote: %w", err)
	}
	ref := PullRef(remote, n)
	if _, err := git(repo, "fetch", "--no-tags", "origin", ref); err != nil {
		return "", "", fmt.Errorf("pull request %d not found on origin (%s): %w", n, ref, err)
	}
	sha, err = git(repo, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return "", "", err
	}
	return sha, PullRequestURL(remote, n), nil
}
