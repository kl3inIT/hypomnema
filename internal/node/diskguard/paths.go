package diskguard

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func RealDir(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return false
	}

	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

func SameVolume(first, second string) bool {
	firstVolume, err := volume(first)
	if err != nil {
		return false
	}

	secondVolume, err := volume(second)
	return err == nil && firstVolume == secondVolume
}

func CheckTree(ctx context.Context, path string) error {
	return checkTree(ctx, path, false)
}

func CheckCacheTree(ctx context.Context, path string) error {
	return checkTree(ctx, path, true)
}

func checkTree(ctx context.Context, path string, rejectLinks bool) error {
	if !RealDir(path) {
		return errors.New("not a real directory")
	}

	base, err := volume(filepath.Dir(path))
	if err != nil {
		return err
	}

	return filepath.WalkDir(path, func(name string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if rejectLinks {
				return fmt.Errorf("%s is a symlink", name)
			}
			return nil
		}

		mount, err := volume(name)
		if err != nil {
			return fmt.Errorf("checking mount of %s: %w", name, err)
		}

		if mount != base {
			return fmt.Errorf("%s crosses a mount boundary", name)
		}

		return nil
	})
}

func OpenRealDir(path string) (*os.Root, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("not an absolute clean path: %s", path)
	}
	parent := filepath.Dir(path)
	if parent == path {
		return os.OpenRoot(path)
	}
	root, err := OpenRealDir(parent)
	if err != nil {
		return nil, err
	}
	defer root.Close() //nolint:errcheck // closing a directory
	return openChild(root, filepath.Base(path))
}

func OpenDirFile(path string) (*os.File, error) {
	root, err := OpenRealDir(path)
	if err != nil {
		return nil, err
	}
	defer root.Close() //nolint:errcheck // closing a directory
	return root.Open(".")
}

func RemoveEmptyChild(ctx context.Context, path, home, allowed string) error {
	root, err := OpenRealDir(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // directory handle
	name := filepath.Base(path)
	mount, err := volumeAt(root, ".")
	if err != nil {
		return err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a real directory: %s", path)
	}
	if err := checkMountAt(root, name, mount); err != nil {
		return err
	}
	if err := Protect(ctx, path, home, allowed); err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return fmt.Errorf("directory changed: %s", path)
	}
	return root.Remove(name)
}

func openChild(root *os.Root, name string) (*os.Root, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("not a real directory: %s", name)
	}
	child, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := child.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		_ = child.Close()
		return nil, fmt.Errorf("directory changed: %s", name)
	}
	return child, nil
}

func RemoveTree(ctx context.Context, path string, stopped func() bool) error {
	return RemoveManagedTree(ctx, path, "", "", stopped)
}

func RemoveManagedTree(ctx context.Context, path, home, allowed string, stopped func() bool) error {
	root, err := OpenRealDir(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // closing a directory
	name := filepath.Base(path)
	before, err := root.Lstat(name)
	if err != nil {
		return fmt.Errorf("cannot identify directory %s: %w", path, err)
	}
	if !before.IsDir() {
		return fmt.Errorf("not a real directory: %s", path)
	}
	protected, err := deletionProtection(ctx, path, home, allowed)
	if err != nil {
		return err
	}
	if err := CheckTree(ctx, path); err != nil {
		return err
	}
	if !stopped() || ctx.Err() != nil {
		return ctx.Err()
	}
	current, err := os.Lstat(path)
	if err != nil || !RealDir(path) || !os.SameFile(before, current) {
		return fmt.Errorf("directory changed: %s", path)
	}
	mount, err := volumeAt(root, ".")
	if err != nil {
		return err
	}
	return removeAt(ctx, root, name, mount, before, protected)
}

func removeAt(ctx context.Context, parent *os.Root, name string, mount mountID, expected os.FileInfo,
	protected []os.FileInfo,
) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	info, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if expected != nil && !os.SameFile(expected, info) {
		return fmt.Errorf("entry changed: %s", name)
	}
	if sameIdentity(info, protected) {
		return fmt.Errorf("deletion encountered a protected filesystem identity: %s", filepath.Join(parent.Name(), name))
	}
	if info.Mode()&os.ModeSymlink == 0 {
		if err := checkMountAt(parent, name, mount); err != nil {
			return err
		}
	}
	if info.IsDir() {
		dir, err := openChild(parent, name)
		if err != nil {
			return err
		}
		defer dir.Close() //nolint:errcheck // directory handle
		opened, err := dir.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			return fmt.Errorf("directory changed: %s", name)
		}
		if err := checkMountAt(dir, ".", mount); err != nil {
			return err
		}
		entries, err := fs.ReadDir(dir.FS(), ".")
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := removeAt(ctx, dir, entry.Name(), mount, nil, protected); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	current, err := parent.Lstat(name)
	if err != nil || !os.SameFile(info, current) {
		return fmt.Errorf("entry changed: %s", name)
	}
	return parent.Remove(name)
}

func checkMountAt(root *os.Root, name string, expected mountID) error {
	mount, err := volumeAt(root, name)
	if err != nil {
		return fmt.Errorf("checking mount of %s: %w", filepath.Join(root.Name(), name), err)
	}
	if mount != expected {
		return fmt.Errorf("%s crosses a mount boundary", filepath.Join(root.Name(), name))
	}
	return nil
}
