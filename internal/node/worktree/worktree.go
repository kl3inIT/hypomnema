// Package worktree makes a Dispatch's working copy (issue #32): a git
// worktree on its own branch, from a bare mirror the node keeps per
// repository, as Orca (src/main/git/worktrees/creation/worktree-add.ts) and
// Omnigent (omnigent/host/git_worktree.py) do. The repository is on a git
// host of a kind (internal/shared/githost, issue #44). Git authenticates with
// the host's token the control plane gives the Dispatch, or else the node
// user's own credentials (`gh auth setup-git`).
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// Maker makes worktrees under Root.
type Maker struct {
	// Root holds the mirrors (Root/repos/...); worktrees go where the control plane says.
	Root string
	// URL replaces the host's clone URL in tests: a format with one %s, the repository's path.
	URL string
	// Logger takes a setup script that failed; nil logs nothing.
	Logger *slog.Logger
	// SetupTimeout replaces how long a setup script may run, in tests.
	SetupTimeout time.Duration
}

// Source is the repository a worktree is made from.
type Source struct {
	Repo githost.Repo
	// URL is where git fetches it from.
	URL string
	// Env lets git and the host's CLI act with the host's token; empty when
	// the offer carried none, and the node user's own logins are used.
	Env []string
}

// SourceOf is the repository an offer names. It refuses a host, a kind or a
// path that cannot be one, so nothing an offer carries reaches git unchecked.
func SourceOf(host, kind, path, token string) (Source, error) {
	adapter, ok := githost.For(githost.Kind(kind))
	if !ok {
		return Source{}, fmt.Errorf("this node does not know hosts of kind %q", kind)
	}

	repo, err := githost.Parse("https://" + host + "/" + path)
	if err != nil {
		return Source{}, err
	}

	return Source{Repo: repo, URL: adapter.CloneURL(repo), Env: adapter.Env(repo.Host, token)}, nil
}

// Branch is the branch a Task's worktree works on.
func Branch(taskID string) string { return "hyp/" + taskID }

// Ensure makes the worktree at path on the Task's branch, starting from base,
// or reuses it when it exists (a continued Conversation). A worktree it made
// has had the repository's setup script run in it (setup.go).
func (m Maker) Ensure(ctx context.Context, src Source, base, taskID, path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("the worktree %q is not absolute", path)
	}

	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return nil
	}

	mirror, err := m.mirror(ctx, src)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("making %s: %w", filepath.Dir(path), err)
	}

	branch := Branch(taskID)

	if git(ctx, mirror, nil, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch) == nil {
		// A retry: the branch keeps what the earlier attempt committed.
		err = git(ctx, mirror, nil, "worktree", "add", path, branch)
	} else {
		err = git(ctx, mirror, nil, "worktree", "add", "--no-track", "-b", branch, path, "refs/remotes/origin/"+base)
	}

	if err != nil {
		return err
	}

	m.setup(ctx, path)

	return nil
}

// Remove removes a worktree; its branch stays.
func (m Maker) Remove(ctx context.Context, src Source, path string) error {
	mirror := m.mirrorPath(src.Repo)
	if _, err := os.Stat(mirror); err != nil {
		return os.RemoveAll(path)
	}

	if err := git(ctx, mirror, nil, "worktree", "remove", "--force", path); err != nil {
		return errors.Join(err, os.RemoveAll(path))
	}

	return nil
}

// mirrorPath is where a repository's bare mirror is kept: under its path for
// github.com, as before hosts existed, and under its host for another host.
func (m Maker) mirrorPath(r githost.Repo) string {
	path := filepath.FromSlash(r.Path) + ".git"
	if r.Host == githost.GitHubHost {
		return filepath.Join(m.Root, "repos", path)
	}

	// An owner on GitHub cannot be named "_hosts"; a port's colon is not a file name everywhere.
	return filepath.Join(m.Root, "repos", "_hosts", strings.ReplaceAll(r.Host, ":", "_"), path)
}

// mirror clones the repository bare the first time, then fetches its
// branches as origin/*, so the base is current.
func (m Maker) mirror(ctx context.Context, src Source) (string, error) {
	if src.Repo.Path == "" || src.URL == "" {
		return "", errors.New("the offer names no repository")
	}

	path := m.mirrorPath(src.Repo)

	url := src.URL
	if m.URL != "" {
		url = fmt.Sprintf(m.URL, src.Repo.Path)
	}

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", fmt.Errorf("making %s: %w", filepath.Dir(path), err)
		}

		if err := git(ctx, "", src.Env, "clone", "--bare", "--", url, path); err != nil {
			return "", err
		}
	}

	// Into remote-tracking refs: a branch checked out in a worktree must not be overwritten.
	if err := git(ctx, path, src.Env, "fetch", "--prune", "--", url, "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return "", err
	}

	return path, nil
}

// git runs git in dir with env added to the node's own. The error names the
// arguments and what git printed; a token is in neither, only in env.
func git(ctx context.Context, dir string, env []string, args ...string) error {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}

	var stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // git with this package's own subcommands
	cmd.Stderr = &stderr
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), env...)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}

	return nil
}
