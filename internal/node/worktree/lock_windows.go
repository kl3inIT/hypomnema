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

// inherit does nothing: Windows has no confined cleanup (diskguard.ScopeAvailable is false), so Clean
// refuses before it would start Git, and node Git runs without the Unix supervisor.
func (*mirrorLock) inherit(*exec.Cmd) {}
