//go:build unix

package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

type mirrorLock struct{ *os.File }

func openMirrorLock(ctx context.Context, path string) (*mirrorLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // the hashed lock path under node home
	if err != nil {
		return nil, err
	}

	poll := time.NewTicker(mirrorLockPoll)
	defer poll.Stop()

	for ctx.Err() == nil {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &mirrorLock{file}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			_ = file.Close()
			return nil, err
		}

		select {
		case <-ctx.Done():
		case <-poll.C:
		}
	}

	_ = file.Close()
	return nil, ctx.Err()
}

// Close, not LOCK_UN, releases this reference without unlocking the supervisor's descriptor.
func (lock *mirrorLock) inherit(cmd *exec.Cmd) {
	cmd.ExtraFiles = append(cmd.ExtraFiles, lock.File)
}
