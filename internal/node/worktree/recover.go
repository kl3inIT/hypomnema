package worktree

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func markPending(path string) error {
	return os.WriteFile(path, nil, 0o600)
}

func (m Maker) beginMirror(mirror string) (string, error) {
	state, err := m.lockPath("mirror", mirror)
	if err != nil {
		return "", err
	}
	pending := state + ".pending"
	if _, err := os.Stat(pending); err == nil {
		if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
			if err := clearMirrorLocks(mirror); err != nil {
				return "", err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return pending, markPending(pending)
}

func finishMirror(pending string, commandErr error) error {
	if commandErr != nil {
		if errors.Is(commandErr, context.Canceled) || errors.Is(commandErr, context.DeadlineExceeded) {
			return commandErr
		}
		if exit, ok := errors.AsType[*exec.ExitError](commandErr); !ok || exit.ExitCode() < 0 ||
			exit.ExitCode() == gitFailedExit || exit.ExitCode() == gitStalledExit {
			return commandErr
		}
	}
	return errors.Join(commandErr, os.Remove(pending))
}

func clearMirrorLocks(mirror string) error {
	for _, name := range []string{"config.lock", "index.lock", "shallow.lock", "packed-refs.lock"} {
		if err := removeIfPresent(filepath.Join(mirror, name)); err != nil {
			return err
		}
	}
	refs := filepath.Join(mirror, "refs")
	return filepath.WalkDir(refs, func(path string, entry fs.DirEntry, err error) error {
		if path == refs && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".lock") {
			return removeIfPresent(path)
		}
		return nil
	})
}

func removeIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
