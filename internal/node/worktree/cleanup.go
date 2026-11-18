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

	if err := ctx.Err(); err != nil {
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
	if !diskguard.ScopeAvailable() {
		return errors.New("Git worktree cleanup needs filesystem confinement")
	}
	worktrees, err := diskguard.OpenDirFile(filepath.Join(mirror, "worktrees"))
	if err != nil {
		return err
	}
	defer worktrees.Close() //nolint:errcheck // directory handle
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	remove, err := diskguard.ScopedCommand(ctx, []*os.File{worktrees}, gitPath, "-C", mirror, "worktree", "remove", "--", path)
	if err != nil {
		return err
	}
	var refused error
	removed := false
	if err := diskguard.RemoveTree(ctx, path, func() bool {
		if !stopped() {
			return false
		}
		if checked, err := m.managedMirror(path); err != nil || checked != mirror {
			refused = errors.New("the worktree or its mirror changed")
			return false
		}
		refused = cleanStatus(ctx, gitPath, path)
		removed = refused == nil
		return removed
	}); err != nil {
		return err
	}
	if !removed {
		return refused
	}
	if output, err := remove.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove: %w: %s", err, output)
	}
	return nil
}

func cleanStatus(ctx context.Context, gitPath, path string) error {
	status, err := exec.CommandContext(ctx, gitPath, "--no-optional-locks", "-C", path, "status", "--porcelain=v1", //nolint:gosec // checked worktree
		"--untracked-files=all", "--ignored=matching", "--ignore-submodules=none").Output()
	if err != nil {
		return err
	}
	if len(status) != 0 {
		return errors.New("worktree contains tracked, untracked, or ignored changes")
	}
	index, err := exec.CommandContext(ctx, gitPath, "-C", path, "ls-files", "--stage", "-z").Output() //nolint:gosec // checked worktree
	if err != nil {
		return err
	}
	if bytes.HasPrefix(index, []byte("160000 ")) || bytes.Contains(index, []byte("\x00160000 ")) {
		return errors.New("worktree contains a submodule")
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
