package worktree

import (
	"errors"
	"os"
)

func markPending(path string) error {
	return os.WriteFile(path, nil, 0o600)
}

func removeIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
