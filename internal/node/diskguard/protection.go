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
	path   string
	root   string
	device string
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
	return deletionIdentities(ctx, target, home, allowed, true)
}

func deletionIdentities(ctx context.Context, target, home, allowed string, inspect bool) ([]os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(target); err != nil || resolved != target {
		return nil, fmt.Errorf("cannot identify deletion target: %s", target)
	}
	paths, _ := ctx.Value(protectionKey{}).([]string)
	var homeIdentity os.FileInfo
	var managedAncestors []os.FileInfo
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
		for _, name := range []string{"repos", "run"} {
			path := filepath.Join(resolved, name)
			if allowed != "" && containsPath(path, allowed) && containsPath(allowed, target) && target != path && RealDir(path) {
				info, err := os.Lstat(path)
				if err != nil || !info.IsDir() {
					return nil, fmt.Errorf("cannot identify managed directory: %s", path)
				}
				managedAncestors = append(managedAncestors, info)
			} else {
				paths = append(paths, path)
			}
		}
		if allowed == "" || !containsPath(resolved, allowed) || !containsPath(allowed, target) || target == resolved {
			paths = append(paths, resolved)
		}
	}
	mounts, err := mountedPaths()
	if err != nil {
		return nil, err
	}
	return checkedIdentities(ctx, target, paths, mounts, homeIdentity, inspect, managedAncestors...)
}

func checkedIdentities(ctx context.Context, target string, paths []string, mounts []mountedPath,
	homeIdentity os.FileInfo, inspect bool, managedAncestors ...os.FileInfo,
) ([]os.FileInfo, error) {
	roots, ancestors, err := protectedIdentities(ctx, target, paths, mounts)
	if err != nil {
		return nil, err
	}
	if len(ancestors) == 0 && homeIdentity == nil && len(managedAncestors) == 0 {
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
	roots = append(roots, managedAncestors...)
	if !inspect {
		return roots, nil
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
	targetLocations := mountLocations(target, mounts)
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
		for _, protected := range mountLocations(resolved, mounts) {
			if overlapsMount(targetLocations, protected) {
				return nil, nil, fmt.Errorf("deletion overlaps protected mount path %s", path)
			}
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
				if mount.device != "" && overlapsMount(targetLocations, mountLocation{mount.device, mount.root}) {
					return nil, nil, fmt.Errorf("deletion overlaps protected mount path %s", mount.path)
				}
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

type mountLocation struct {
	device string
	path   string
}

func mountLocations(path string, mounts []mountedPath) []mountLocation {
	var locations []mountLocation
	deepest := 0
	for _, mount := range mounts {
		if mount.device == "" || len(mount.path) < deepest || !containsPath(mount.path, path) {
			continue
		}
		if len(mount.path) > deepest {
			locations = locations[:0]
			deepest = len(mount.path)
		}
		inside := filepath.Join(mount.root, strings.TrimPrefix(path, mount.path))
		locations = append(locations, mountLocation{mount.device, inside})
	}
	return locations
}

func overlapsMount(locations []mountLocation, protected mountLocation) bool {
	return slices.ContainsFunc(locations, func(target mountLocation) bool {
		return target.device == protected.device &&
			(containsPath(target.path, protected.path) || containsPath(protected.path, target.path))
	})
}

func sameIdentity(info os.FileInfo, identities []os.FileInfo) bool {
	return slices.ContainsFunc(identities, func(identity os.FileInfo) bool { return os.SameFile(info, identity) })
}
