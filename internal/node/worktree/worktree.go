// Package worktree makes a Dispatch's working copy (issue #32): a git
// worktree on its own branch, from a bare mirror the node keeps per
// repository, as Orca (src/main/git/worktrees/creation/worktree-add.ts) and
// Omnigent (omnigent/host/git_worktree.py) do. Git authenticates with the
// node user's own credentials (`gh auth setup-git`) until the vault exists.
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitHubURL is the default clone URL for owner/name.
const GitHubURL = "https://github.com/%s.git"

// Maker makes worktrees under Root.
type Maker struct {
	// Root holds the mirrors (Root/repos/owner/name.git); worktrees go where the control plane says.
	Root string
	// URL turns owner/name into a clone URL; GitHubURL when empty.
	URL string
}

// Branch is the branch a Task's worktree works on.
func Branch(taskID string) string { return "hyp/" + taskID }

// Ensure makes the worktree at path on the Task's branch, starting from base,
// or reuses it when it exists (a continued Conversation).
func (m Maker) Ensure(ctx context.Context, repo, base, taskID, path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("the worktree %q is not absolute", path)
	}

	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return nil
	}

	mirror, err := m.mirror(ctx, repo)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("making %s: %w", filepath.Dir(path), err)
	}

	branch := Branch(taskID)

	if git(ctx, mirror, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch) == nil {
		// A retry: the branch keeps what the earlier attempt committed.
		return git(ctx, mirror, "worktree", "add", path, branch)
	}

	return git(ctx, mirror, "worktree", "add", "--no-track", "-b", branch, path, "refs/remotes/origin/"+base)
}

// Remove removes a worktree; its branch stays.
func (m Maker) Remove(ctx context.Context, repo, path string) error {
	mirror := m.mirrorPath(repo)
	if _, err := os.Stat(mirror); err != nil {
		return os.RemoveAll(path)
	}

	if err := git(ctx, mirror, "worktree", "remove", "--force", path); err != nil {
		return errors.Join(err, os.RemoveAll(path))
	}

	return nil
}

func (m Maker) mirrorPath(repo string) string {
	owner, name, _ := strings.Cut(repo, "/")

	return filepath.Join(m.Root, "repos", owner, name+".git")
}

// mirror clones the repository bare the first time, then fetches its
// branches as origin/*, so the base is current.
func (m Maker) mirror(ctx context.Context, repo string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.ContainsAny(repo, " \\") || strings.Contains(name, "/") {
		return "", fmt.Errorf("repository %q is not owner/name", repo)
	}

	path := m.mirrorPath(repo)
	url := fmt.Sprintf(m.urlTemplate(), repo)

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", fmt.Errorf("making %s: %w", filepath.Dir(path), err)
		}

		if err := git(ctx, "", "clone", "--bare", url, path); err != nil {
			return "", err
		}
	}

	// Into remote-tracking refs: a branch checked out in a worktree must not be overwritten.
	if err := git(ctx, path, "fetch", "--prune", url, "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return "", err
	}

	return path, nil
}

func (m Maker) urlTemplate() string {
	if m.URL != "" {
		return m.URL
	}

	return GitHubURL
}

func git(ctx context.Context, dir string, args ...string) error {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}

	var stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // git with this package's own subcommands
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}

	return nil
}
