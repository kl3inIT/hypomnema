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
	stall, _ := git.bounds()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	timer := time.AfterFunc(stall, func() { cancel(context.DeadlineExceeded) })
	defer timer.Stop()
	output := timedOutput{timer: timer, stall: stall}
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // Git or the declared setup shell
	cmd.Dir, cmd.Env, cmd.WaitDelay = dir, env, time.Second
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	return string(output.tail), errors.Join(err, context.Cause(ctx))
}

type timedOutput struct {
	tail  outputTail
	timer *time.Timer
	stall time.Duration
}

func (output *timedOutput) Write(data []byte) (int, error) {
	output.timer.Reset(output.stall)
	return output.tail.Write(data)
}
