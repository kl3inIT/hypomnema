package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/node/diskguard"
)

func (m Maker) Clean(ctx context.Context, path string, hasRepo bool, stopped func() bool) error {
	if err := diskguard.CheckTree(ctx, path); err != nil {
		return err
	}

	if !hasRepo {
		if _, err := os.Lstat(filepath.Join(path, ".git")); !errors.Is(err, os.ErrNotExist) {
			return errors.New("the disposable worktree has a .git entry")
		}

		var refused error
		err := diskguard.RemoveTree(ctx, path, func() bool {
			if !stopped() {
				return false
			}
			if _, err := os.Lstat(filepath.Join(path, ".git")); !errors.Is(err, os.ErrNotExist) {
				refused = errors.New("the disposable worktree has a .git entry")
				return false
			}
			return true
		})
		return errors.Join(err, refused)
	}

	mirror, err := m.managedMirror(path)
	if err != nil {
		return err
	}

	if !diskguard.ScopeAvailable() {
		return errors.New("git worktree cleanup needs filesystem confinement")
	}
	dirs := []string{path, filepath.Dir(path), filepath.Join(mirror, "worktrees")}
	scopes := make([]diskguard.Scope, 0, len(dirs))
	for index, dir := range dirs {
		handle, err := diskguard.OpenDirFile(dir)
		if err != nil {
			return err
		}
		defer handle.Close() //nolint:errcheck // directory handle
		scopes = append(scopes, diskguard.Scope{Dir: handle, OnlyRmdir: index == 1})
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	if !stopped() {
		return nil
	}
	if err := cleanStatus(ctx, gitPath, path); err != nil {
		return err
	}
	if !stopped() {
		return nil
	}
	if err := diskguard.CheckTree(ctx, path); err != nil {
		return err
	}
	if checked, err := m.managedMirror(path); err != nil || checked != mirror {
		return errors.New("the worktree or its mirror changed")
	}
	for index, dir := range dirs {
		opened, err := scopes[index].Dir.Stat()
		current, pathErr := os.Lstat(dir)
		if err != nil || pathErr != nil || !diskguard.RealDir(dir) || !os.SameFile(opened, current) {
			return fmt.Errorf("the cleanup directory changed: %s", dir)
		}
	}
	remove, err := diskguard.ScopedCommand(ctx, scopes, gitPath,
		"-c", "status.showUntrackedFiles=all", "-c", "core.fsmonitor=false", "-C", mirror, "worktree", "remove", "--", path)
	if err != nil {
		return err
	}
	if output, err := remove.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove: %w: %s", err, output)
	}
	return nil
}

func cleanStatus(ctx context.Context, gitPath, path string) error {
	args := []string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-C", path, "status", "--porcelain=v1",
		"--untracked-files=all", "--ignored=matching", "--ignore-submodules=none"}
	status, err := exec.CommandContext(ctx, gitPath, args...).Output() //nolint:gosec // checked worktree
	if err != nil {
		return err
	}
	if len(status) != 0 {
		return errors.New("worktree contains tracked, untracked, or ignored changes")
	}
	return nil
}

func (m Maker) managedMirror(path string) (string, error) {
	marker := filepath.Join(path, ".git")
	file, err := readLinkFile(marker)
	if err != nil {
		return "", err
	}

	admin, ok := strings.CutPrefix(file, "gitdir: ")
	if ok && !filepath.IsAbs(admin) {
		admin = filepath.Join(filepath.Dir(marker), admin)
	}
	worktrees := filepath.Dir(admin)
	mirror := filepath.Dir(worktrees)
	repos := filepath.Join(m.Root, "repos")
	if !ok || !strings.HasPrefix(mirror, repos+string(filepath.Separator)) || !strings.HasSuffix(mirror, ".git") ||
		filepath.Base(worktrees) != "worktrees" || !diskguard.RealDir(admin) {
		return "", errors.New("not a linked worktree of a managed mirror")
	}

	backlink, err := readLinkFile(filepath.Join(admin, "gitdir"))
	if err == nil && !filepath.IsAbs(backlink) {
		backlink = filepath.Join(admin, backlink)
	}
	resolved, resolveErr := filepath.EvalSymlinks(backlink)
	if err != nil || resolveErr != nil || resolved != marker || backlink != marker {
		return "", errors.New("the managed mirror does not point back to this worktree")
	}
	if _, err := os.Lstat(filepath.Join(admin, "locked")); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("the Git worktree is locked or its lock could not be checked")
	}

	return mirror, nil
}

func readLinkFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8192 {
		return "", errors.New("missing or invalid Git worktree metadata")
	}

	raw, err := os.ReadFile(path) //nolint:gosec // the checked worktree metadata file
	return strings.TrimSpace(string(raw)), err
}
