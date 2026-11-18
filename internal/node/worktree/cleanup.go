package worktree

import (
	"context"
	"errors"
	"os"
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

		if err := ctx.Err(); err != nil {
			return err
		}
		if !stopped() {
			return nil
		}

		return diskguard.RemoveTree(path)
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

	return git(ctx, mirror, nil, "worktree", "remove", "--", path)
}

func (m Maker) managedMirror(path string) (string, error) {
	marker := filepath.Join(path, ".git")
	file, err := readLinkFile(marker)
	if err != nil {
		return "", err
	}

	admin, ok := strings.CutPrefix(file, "gitdir: ")
	worktrees := filepath.Dir(admin)
	mirror := filepath.Dir(worktrees)
	repos := filepath.Join(m.Root, "repos")
	if !ok || !strings.HasPrefix(mirror, repos+string(filepath.Separator)) || !strings.HasSuffix(mirror, ".git") ||
		filepath.Base(worktrees) != "worktrees" || !diskguard.RealDir(admin) {
		return "", errors.New("not a linked worktree of a managed mirror")
	}

	backlink, err := readLinkFile(filepath.Join(admin, "gitdir"))
	if err != nil || backlink != marker {
		return "", errors.New("the managed mirror does not point back to this worktree")
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
