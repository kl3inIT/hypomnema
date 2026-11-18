// Package worktree makes a Dispatch's working copy (issue #32): a git
// worktree on its own branch, from a bare mirror the node keeps per
// repository, as Orca (src/main/git/worktrees/creation/worktree-add.ts) and
// Omnigent (omnigent/host/git_worktree.py) do. The repository is on a git
// host of a kind (internal/shared/githost, issue #44). Git authenticates with
// the host's token the control plane gives the Dispatch, or else the node
// user's own credentials (`gh auth setup-git`); a repository registered with
// an SSH address is cloned over SSH with the control plane's key instead
// (issue #625, internal/node/gitssh).
package worktree

import (
	"context"
	"crypto/sha256"
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

const (
	mirrorGitGrace = time.Second
	mirrorLockPoll = 100 * time.Millisecond
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
	// GitTimeout is an optional total limit for each Git command. Zero means no limit.
	GitTimeout time.Duration
	// GitGrace overrides the termination grace period.
	GitGrace time.Duration
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

// OverSSH is the source cloned and fetched from an SSH address instead, with
// env added to git's: the helper that gives ssh the control plane's key. The
// host's token stays in the environment for its CLI, which opens the pull
// request over HTTPS.
func (src Source) OverSSH(url string, env []string) (Source, error) {
	if !githost.ValidSSHURL(url) {
		return Source{}, fmt.Errorf("%q is not an SSH address of a repository", url)
	}

	src.URL = url
	src.Env = append(append([]string{}, src.Env...), env...)

	return src, nil
}

// Branch is the branch a Task's worktree works on.
func Branch(taskID string) string { return "hyp/" + taskID }

// Ensure makes the worktree at path on the Task's branch, starting from base,
// or reuses it when it exists (a continued Conversation). Setup runs on a new
// checkout only, after it is marked complete so a retry preserves its changes.
func (m Maker) Ensure(ctx context.Context, src Source, base, taskID, path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("the worktree %q is not absolute", path)
	}

	state, err := m.lockPath("worktree", path)
	if err != nil {
		return err
	}
	lock, err := openMirrorLock(ctx, state)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()

	pending := state + ".pending"
	_, err = os.Stat(pending)
	recovering := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !recovering && worktreeExists(path) {
		return nil
	}
	if !recovering {
		entries, err := os.ReadDir(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if len(entries) != 0 {
			return fmt.Errorf("worktree path %s is not empty", path)
		}
		if err := markPending(pending); err != nil {
			return err
		}
	}

	if err := m.add(ctx, src, base, taskID, path, recovering); err != nil {
		return err
	}
	if err := os.Remove(pending); err != nil {
		return err
	}
	m.setup(ctx, path, lock)
	return ctx.Err()
}

func (m Maker) add(ctx context.Context, src Source, base, taskID, path string, recovering bool) error {
	if src.Repo.Path == "" || src.URL == "" {
		return errors.New("the offer names no repository")
	}

	mirror := m.mirrorPath(src.Repo)
	lock, err := m.lockMirror(ctx, mirror)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()

	if err := os.MkdirAll(filepath.Dir(mirror), 0o700); err != nil {
		return fmt.Errorf("making %s: %w", filepath.Dir(mirror), err)
	}

	git := m.git(lock)
	if err := m.updateMirror(ctx, git, src, mirror); err != nil {
		return err
	}
	if recovering {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		if err := git.removeRegistration(ctx, mirror, path); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("making %s: %w", filepath.Dir(path), err)
	}

	branch := Branch(taskID)
	args := []string{"worktree", "add"}

	err = git.run(ctx, mirror, nil, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 ||
			errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return err
		}
		args = append(args, "--no-track", "-b", branch, path, "refs/remotes/origin/"+base)
	} else {
		args = append(args, path, branch)
	}

	return git.run(ctx, mirror, nil, args...)
}

func worktreeExists(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

// Remove removes a worktree; its branch stays.
func (m Maker) Remove(ctx context.Context, src Source, path string) error {
	state, err := m.lockPath("worktree", path)
	if err != nil {
		return err
	}
	worktree, err := openMirrorLock(ctx, state)
	if err != nil {
		return err
	}
	defer func() { _ = worktree.Close() }()

	mirror := m.mirrorPath(src.Repo)
	lock, err := m.lockMirror(ctx, mirror)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()

	if _, err := os.Stat(mirror); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		return removeIfPresent(state + ".pending")
	}

	git := m.git(lock)
	if err := git.run(ctx, mirror, nil, "worktree", "remove", "--force", path); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return errors.Join(err, os.RemoveAll(path))
	}

	return removeIfPresent(state + ".pending")
}

func (m Maker) lockMirror(ctx context.Context, mirror string) (*mirrorLock, error) {
	path, err := m.lockPath("mirror", mirror)
	if err != nil {
		return nil, err
	}
	lock, err := openMirrorLock(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("locking mirror %s: %w", mirror, err)
	}
	return lock, nil
}

func (m Maker) lockPath(kind, target string) (string, error) {
	dir := filepath.Join(m.Root, "mirror-locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("making %s: %w", dir, err)
	}
	if kind == "mirror" {
		rel, err := filepath.Rel(m.Root, target)
		if err != nil {
			return "", fmt.Errorf("finding mirror relative to node home: %w", err)
		}
		return filepath.Join(dir, fmt.Sprintf("%x.lock", sha256.Sum256([]byte(rel)))), nil
	}
	return filepath.Join(dir, fmt.Sprintf("worktree-%x.lock", sha256.Sum256([]byte(filepath.Clean(target))))), nil
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

// updateMirror clones the repository bare the first time, then fetches its
// branches as origin/*, so the base is current.
func (m Maker) updateMirror(ctx context.Context, git mirrorGit, src Source, path string) error {
	url := src.URL
	if m.URL != "" {
		url = fmt.Sprintf(m.URL, src.Repo.Path)
	}

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		key := sha256.Sum256([]byte(filepath.Base(path)))
		staging := filepath.Join(filepath.Dir(path), fmt.Sprintf(".hypomnema-clone-%x", key))
		if err := os.RemoveAll(staging); err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(staging) }()
		if err := git.run(ctx, "", src.Env, "clone", "--progress", "--bare", "--", url, staging); err != nil {
			return err
		}
		if err := os.Rename(staging, path); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// The worker pushes to origin: it goes where the mirror is fetched from, over SSH once the repository is
	// registered with an SSH address.
	if err := git.run(ctx, path, nil, "remote", "set-url", "origin", url); err != nil {
		return err
	}

	// Into remote-tracking refs: a branch checked out in a worktree must not be overwritten.
	return git.fetch(ctx, path, url, src.Env)
}

type mirrorGit struct {
	lock    *mirrorLock
	timeout time.Duration
	grace   time.Duration
	lockDir string
}

func (m Maker) git(lock *mirrorLock) mirrorGit {
	return mirrorGit{lock: lock, timeout: m.GitTimeout, grace: m.GitGrace}
}

func (git mirrorGit) removeRegistration(ctx context.Context, mirror, path string) error {
	err := git.run(ctx, mirror, []string{"LC_ALL=C"}, "worktree", "remove", "--force", "--force", path)
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 128 &&
		strings.Contains(err.Error(), "is not a working tree") && ctx.Err() == nil &&
		!errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return err
}

func (git mirrorGit) fetch(ctx context.Context, path, url string, env []string) error {
	env = append(append([]string{}, env...), "LC_ALL=C")
	args := []string{"fetch", "--progress", "--no-auto-gc", "--prune", "--", url, "+refs/heads/*:refs/remotes/origin/*"}
	err := git.run(ctx, path, env, args...)
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.DeadlineExceeded) &&
		strings.Contains(err.Error(), "error: cannot lock ref '") {
		return git.run(ctx, path, env, args...)
	}

	return err
}

// run runs git in dir with env added to the node's own. The error names the
// arguments and what git printed; a token is in neither, only in env.
func (git mirrorGit) run(ctx context.Context, dir string, env []string, args ...string) error {
	if git.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, git.timeout)
		defer cancel()
	}
	if git.lock != nil && (args[0] == "fetch" || args[0] == "remote") {
		git.lockDir = dir
	}
	withHooks := args[0] == "worktree"
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	if !withHooks {
		args = append([]string{"-c", "core.hooksPath=" + os.DevNull}, args...)
	}

	output, err := git.runProcess(ctx, "", append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), env...), "git", args...)
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(output))
	}

	return nil
}
