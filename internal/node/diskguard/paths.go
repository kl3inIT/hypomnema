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
		if err != nil || entry.Type()&os.ModeSymlink != 0 {
			return err
		}

		if !entry.IsDir() && !entry.Type().IsRegular() {
			return nil
		}

		mount, err := volume(name)
		if err != nil {
			return err
		}

		if mount != base {
			return fmt.Errorf("%s crosses a mount boundary", name)
		}

		return nil
	})
}

func RemoveTree(path string) error {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // closing a directory

	return root.RemoveAll(filepath.Base(path))
}
