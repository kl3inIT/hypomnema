//go:build !linux && !darwin

package worktree

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

func (git mirrorGit) runProcess(
	ctx context.Context, dir string, env []string, name string, args ...string,
) (string, error) {
	var output outputTail
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // Git or the declared setup shell
	cmd.Dir, cmd.Env, cmd.WaitDelay = dir, env, time.Second
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	return string(output), errors.Join(err, ctx.Err())
}
