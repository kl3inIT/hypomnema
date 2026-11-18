package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/node/diskguard"
	"github.com/kl3inIT/hypomnema/internal/node/shim"
)

const neverStartedFile = "never-started"

func tempEnv(env []string, dir string) []string {
	env = slices.DeleteFunc(env, func(kv string) bool {
		key, _, _ := strings.Cut(kv, "=")
		return key == "TMPDIR" || key == "TEMP" || key == "TMP"
	})

	return append(env, "TMPDIR="+dir, "TEMP="+dir, "TMP="+dir)
}

func (s *Supervisor) Stopped(id string, missingOK bool) bool {
	if s.get(id) != nil {
		return false
	}

	dir := s.dir(id)
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		parent := filepath.Dir(dir)
		_, parentErr := os.Lstat(parent)

		return missingOK && (diskguard.RealDir(parent) || errors.Is(parentErr, os.ErrNotExist))
	}

	if !diskguard.RealDir(dir) {
		return false
	}

	pidFile := filepath.Join(dir, shim.PIDFile)
	if info, err := os.Lstat(pidFile); err == nil && info.Mode().IsRegular() {
		pid, err := shim.PID(dir)

		return err == nil && shim.GroupGone(pid)
	} else if !errors.Is(err, os.ErrNotExist) {
		return false
	}

	info, err := os.Lstat(filepath.Join(dir, neverStartedFile))
	if err == nil && info.Mode().IsRegular() {
		return true
	}
	for _, name := range []string{shim.LaunchingFile, shim.ClaimedFile} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			return false
		}
	}
	info, err = os.Lstat(filepath.Join(dir, shim.PreparingFile))

	return err == nil && info.Mode().IsRegular()
}

func (s *Supervisor) CleanTemp(ctx context.Context, id string) error {
	path := filepath.Join(s.dir(id), "tmp")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}

	return diskguard.RemoveManagedTree(ctx, path, s.Home, path, func() bool {
		dir, err := diskguard.OpenRealDir(s.dir(id))
		if err != nil {
			return false
		}
		defer dir.Close() //nolint:errcheck // directory handle
		if info, err := dir.Lstat(shim.PreparingFile); err == nil && info.Mode().IsRegular() && s.get(id) == nil {
			if err := dir.Remove(shim.LaunchingFile); err != nil && !errors.Is(err, os.ErrNotExist) {
				return false
			}
		}
		return s.Stopped(id, false)
	})
}
