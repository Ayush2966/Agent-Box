// Package worktree wraps the git CLI for worktree, branch, status and merge operations.
package worktree

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var ErrBranchExists = errors.New("branch already exists")

// ConflictError reports a merge that was aborted because of conflicts.
type ConflictError struct{ Files []string }

func (e *ConflictError) Error() string { return "merge conflict in: " + strings.Join(e.Files, ", ") }

func gitRaw(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	slog.Debug("git", "dir", dir, "args", args)
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

func git(dir string, args ...string) (string, error) {
	out, err := gitRaw(dir, args...)
	return strings.TrimSpace(out), err
}

// MainWorktree returns the root of the main checkout for any directory inside
// the repo or inside one of its linked worktrees (such as a box).
func MainWorktree(dir string) (string, error) {
	common, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git repository", dir)
	}
	if filepath.Base(common) != ".git" {
		return "", fmt.Errorf("%s: bare repositories and custom git dirs are not supported", dir)
	}
	return filepath.EvalSymlinks(filepath.Dir(common))
}

func BranchExists(repo, branch string) bool {
	_, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// Add creates a worktree at dir on a new branch started from `from` (HEAD if empty).
func Add(repo, dir, branch, from string) error {
	if BranchExists(repo, branch) {
		return fmt.Errorf("%w: %s", ErrBranchExists, branch)
	}
	if from == "" {
		from = "HEAD"
	}
	if err := checkRef(repo, from); err != nil {
		return err
	}
	_, err := git(repo, "worktree", "add", "-q", "-b", branch, dir, from)
	return err
}

// checkRef rejects user-supplied refs that git would otherwise parse as
// options ("--force" would silently mean HEAD) and refs that don't exist.
func checkRef(dir, ref string) error {
	if _, err := git(dir, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}"); err != nil {
		return fmt.Errorf("%q is not a branch, tag or commit", ref)
	}
	return nil
}

// Remove deletes the worktree at dir, discarding its changes. A worktree
// whose directory is already gone is pruned instead.
func Remove(repo, dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return Prune(repo)
	}
	_, err := git(repo, "worktree", "remove", "--force", "--force", dir)
	return err
}

func Prune(repo string) error {
	_, err := git(repo, "worktree", "prune")
	return err
}

func DeleteBranch(repo, branch string) error {
	_, err := git(repo, "branch", "-D", branch)
	return err
}

func CurrentBranch(dir string) (string, error) {
	return git(dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// Changes lists changed paths relative to the worktree root. Untracked files
// are listed individually when untracked is true.
func Changes(dir string, untracked bool) ([]string, error) {
	mode := "--untracked-files=no"
	if untracked {
		mode = "--untracked-files=all"
	}
	out, err := gitRaw(dir, "status", "--porcelain", "-z", mode)
	if err != nil {
		return nil, err
	}
	var paths []string
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		paths = append(paths, e[3:])
		if e[0] == 'R' || e[0] == 'C' {
			i++ // skip the rename/copy source path
		}
	}
	return paths, nil
}

// AheadBehind counts commits on branch that base lacks (ahead) and vice versa.
func AheadBehind(repo, base, branch string) (ahead, behind int, err error) {
	if err := checkRef(repo, base); err != nil {
		return 0, 0, err
	}
	out, err := git(repo, "rev-list", "--left-right", "--count", base+"..."+branch)
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output %q", out)
	}
	behind, _ = strconv.Atoi(f[0])
	ahead, _ = strconv.Atoi(f[1])
	return ahead, behind, nil
}

// Merge merges branch into the branch checked out in repo. On conflict the
// merge is aborted, the checkout is left as it was, and *ConflictError is returned.
func Merge(repo, branch string, squash bool, message string) error {
	var err error
	if squash {
		_, err = git(repo, "merge", "--squash", branch)
	} else {
		_, err = git(repo, "merge", "--no-edit", branch)
	}
	if err != nil {
		files, _ := git(repo, "diff", "--name-only", "--diff-filter=U")
		_, _ = git(repo, "reset", "--merge")
		if files != "" {
			return &ConflictError{Files: strings.Split(files, "\n")}
		}
		return err
	}
	if !squash {
		return nil
	}
	if _, err := git(repo, "diff", "--cached", "--quiet"); err == nil {
		return nil // nothing to commit
	}
	if _, err := git(repo, "commit", "-q", "-m", message); err != nil {
		_, _ = git(repo, "reset", "--merge")
		return err
	}
	return nil
}

// Diff shows tracked changes in dir (committed or not) since it diverged from base.
func Diff(dir, base string) (string, error) {
	if err := checkRef(dir, base); err != nil {
		return "", err
	}
	mb, err := git(dir, "merge-base", base, "HEAD")
	if err != nil {
		return "", err
	}
	return gitRaw(dir, "diff", mb)
}

func ListBranches(repo, prefix string) ([]string, error) {
	out, err := git(repo, "for-each-ref", "--format=%(refname:short)", "refs/heads/"+prefix)
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// MergeIsNoop reports whether merging branch into base would change nothing,
// e.g. because the branch was already squash-merged.
func MergeIsNoop(repo, base, branch string) bool {
	if checkRef(repo, base) != nil {
		return false
	}
	out, err := git(repo, "merge-tree", "--write-tree", base, branch)
	if err != nil {
		return false // conflicts (exit 1) or an old git
	}
	tree, _, _ := strings.Cut(out, "\n")
	baseTree, err := git(repo, "rev-parse", base+"^{tree}")
	return err == nil && tree == baseTree
}

// List returns the paths of all worktrees of repo, the main checkout first.
func List(repo string) ([]string, error) {
	out, err := git(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			paths = append(paths, p)
		}
	}
	return paths, nil
}
