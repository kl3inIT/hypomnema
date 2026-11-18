//go:build unix && !linux

package shim

import (
	"errors"
	"syscall"
)

func prepareWorker() error { return nil }

// Kill stops the shim's process group on non-production Unix platforms.
func Kill(pid int) error {
	if pid <= 1 {
		return errors.New("refusing to kill pid 0 or 1")
	}

	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}

	return nil
}
