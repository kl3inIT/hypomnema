//go:build unix

package agenttool

import (
	"os"
	"syscall"
)

// lockFile waits for the lock on path and returns what lets it go: Hermes's
// lock_fd(wait=True) on the environment's .install.lock (pm/filesystem.py).
// The lock goes with the process, so a builder that dies holds nothing.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // the environment's own lock
	if err != nil {
		return nil, err
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()

		return nil, err
	}

	return func() { _ = f.Close() }, nil
}

// syncDisk flushes every file to the disk, Hermes's os.sync() before a
// selection is written (pm/environments.py, flush_before_selecting).
func syncDisk() { syscall.Sync() }
