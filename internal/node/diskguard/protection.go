package diskguard

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type protectionKey struct{}

func WithProtection(ctx context.Context, paths ...string) context.Context {
	previous, _ := ctx.Value(protectionKey{}).([]string)
	return context.WithValue(ctx, protectionKey{}, append(slices.Clone(previous), paths...))
}

type mountedPath struct {
	path  string
	alias bool
}

func containsPath(parent, child string) bool {
	separator := string(filepath.Separator)
	return parent == child || strings.HasPrefix(child, strings.TrimSuffix(parent, separator)+separator)
}

func resolvedPath(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("not an absolute clean path: %s", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if !errors.Is(err, os.ErrNotExist) || filepath.Dir(path) == path {
		return resolved, err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("cannot resolve protected path: %s", path)
	}
	parent, err := resolvedPath(filepath.Dir(path))
	return filepath.Join(parent, filepath.Base(path)), err
}

func Protect(ctx context.Context, target, home, allowed string) error {
	_, err := deletionProtection(ctx, target, home, allowed)
	return err
}

func deletionProtection(ctx context.Context, target, home, allowed string) ([]os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(target); err != nil || resolved != target {
		return nil, fmt.Errorf("cannot identify deletion target: %s", target)
	}
	paths, _ := ctx.Value(protectionKey{}).([]string)
	var homeIdentity os.FileInfo
	if home != "" {
		resolved, err := resolvedPath(home)
		if err != nil {
			return nil, err
		}
		homeIdentity, err = os.Stat(resolved)
		if err != nil || !homeIdentity.IsDir() {
			return nil, fmt.Errorf("cannot identify the node home: %s", home)
		}
		paths = slices.Clone(paths)
		retained := []string{
			"state.db", "state.db-wal", "state.db-shm", "state.db-journal", "config.json", "accounts", "conversations",
		}
		for _, name := range retained {
			paths = append(paths, filepath.Join(resolved, name))
		}
		if allowed == "" || !containsPath(resolved, allowed) || !containsPath(allowed, target) || target == resolved {
			paths = append(paths, resolved)
		}
	}
	mounts, err := mountedPaths()
	if err != nil {
		return nil, err
	}
	return checkedIdentities(ctx, target, paths, mounts, homeIdentity)
}

func checkedIdentities(ctx context.Context, target string, paths []string, mounts []mountedPath,
	homeIdentity os.FileInfo,
) ([]os.FileInfo, error) {
	for _, mount := range mounts {
		if mount.alias && containsPath(mount.path, target) {
			return nil, fmt.Errorf("cannot establish original ancestry of mounted deletion target: %s", target)
		}
	}
	roots, ancestors, err := protectedIdentities(ctx, target, paths, mounts)
	if err != nil {
		return nil, err
	}
	if len(ancestors) == 0 && homeIdentity == nil {
		return nil, nil
	}
	for dir := target; ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err != nil {
			return nil, err
		}
		if sameIdentity(info, roots) || (dir == target && sameIdentity(info, ancestors)) {
			return nil, fmt.Errorf("deletion overlaps a protected filesystem identity: %s", dir)
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	if homeIdentity != nil {
		roots = append(roots, homeIdentity)
	}
	err = filepath.WalkDir(target, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if sameIdentity(info, roots) {
			return fmt.Errorf("deletion contains a protected filesystem identity: %s", path)
		}
		return nil
	})
	return roots, err
}

func protectedIdentities(ctx context.Context, target string, paths []string, mounts []mountedPath) (
	roots, ancestors []os.FileInfo, err error,
) {
	for _, path := range paths {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		resolved, err := resolvedPath(path)
		if err != nil {
			return nil, nil, err
		}
		if containsPath(target, resolved) || containsPath(resolved, target) {
			return nil, nil, fmt.Errorf("deletion overlaps protected path %s", path)
		}
		for dir := resolved; ; dir = filepath.Dir(dir) {
			info, err := os.Stat(dir)
			if err == nil {
				ancestors = append(ancestors, info)
				if dir == resolved {
					roots = append(roots, info)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, nil, err
			}
			if dir == filepath.Dir(dir) {
				break
			}
		}
		for _, mount := range mounts {
			if containsPath(resolved, mount.path) {
				info, err := os.Stat(mount.path)
				if err != nil {
					return nil, nil, err
				}
				roots = append(roots, info)
			}
		}
	}
	return roots, ancestors, nil
}

func sameIdentity(info os.FileInfo, identities []os.FileInfo) bool {
	return slices.ContainsFunc(identities, func(identity os.FileInfo) bool { return os.SameFile(info, identity) })
}
