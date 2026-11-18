package worktree

import (
	"context"
	"os/exec"

	"github.com/gofrs/flock"
)

type mirrorLock struct{ *flock.Flock }

func openMirrorLock(ctx context.Context, path string) (*mirrorLock, error) {
	lock := flock.New(path)
	if _, err := lock.TryLockContext(ctx, mirrorLockPoll); err != nil {
		_ = lock.Close()
		return nil, err
	}

	return &mirrorLock{lock}, nil
}

// Windows development builds do not support Unix descriptor inheritance.
func (*mirrorLock) inherit(*exec.Cmd) {}
