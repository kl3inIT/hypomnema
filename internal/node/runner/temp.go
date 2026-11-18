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

	return err == nil && info.Mode().IsRegular()
}

func (s *Supervisor) CleanTemp(ctx context.Context, id string) error {
	path := filepath.Join(s.dir(id), "tmp")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err := diskguard.CheckTree(ctx, path); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.Stopped(id, false) {
		return nil
	}

	return diskguard.RemoveTree(path)
}
