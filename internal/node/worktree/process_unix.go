//go:build linux || darwin

package worktree

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	gitSupervisorArg = "--hypomnema-worktree-supervisor"
	gitFailedExit    = 125
)

func (git mirrorGit) runProcess(
	ctx context.Context, dir string, env []string, name string, args ...string,
) (*outputTail, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	read, write, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer func() { _ = read.Close() }()
	defer func() { _ = write.Close() }()

	locked := "no"
	if git.lock != nil {
		locked = "yes"
	}
	launch := append([]string{gitSupervisorArg, locked, git.lockDir, git.stopGrace().String(), name}, args...)
	cmd := exec.CommandContext(context.WithoutCancel(ctx), exe, launch...) //nolint:gosec // reexec this binary
	cmd.Dir, cmd.Env, cmd.Stdin = dir, append(env, "GORACE=atexit_sleep_ms=0"), read
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var output outputTail
	cmd.Stdout, cmd.Stderr = &output, &output
	if git.lock != nil {
		git.lock.inherit(cmd)
	}
	stop := context.AfterFunc(ctx, func() { _ = write.Close() })
	defer stop()
	err = cmd.Run()
	return &output, errors.Join(err, ctx.Err())
}

func init() {
	if len(os.Args) < 6 || os.Args[1] != gitSupervisorArg {
		return
	}
	if os.Args[2] == "yes" {
		unix.CloseOnExec(3)
	}
	grace, graceErr := time.ParseDuration(os.Args[4])
	if graceErr != nil || grace <= 0 {
		os.Exit(gitFailedExit)
	}
	signal.Ignore(syscall.SIGPIPE)
	parentGone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		close(parentGone)
	}()
	os.Exit(supervise(parentGone, grace, os.Args[3], os.Args[5], os.Args[6:]))
}

func supervise(parentGone <-chan struct{}, grace time.Duration, lockDir, name string, args []string) int {
	read, write, err := os.Pipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return gitFailedExit
	}
	defer func() { _ = read.Close() }()
	defer func() { _ = write.Close() }()

	cmd := exec.CommandContext(context.Background(), name, args...) //nolint:gosec // Git or the declared setup shell
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = write, write
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return gitFailedExit
	}
	_ = write.Close()
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_, _ = io.Copy(os.Stdout, read)
	}()
	exited := make(chan error, 1)
	go func() { exited <- gitExited(cmd.Process.Pid) }()

	var recoverLocks func()
	stop := func() {
		recoverLocks = ownedGitLocks(cmd.Process.Pid, lockDir)
		stopGit(cmd.Process.Pid, grace)
	}
	result := watchGit(parentGone, drained, exited, stop)
	err = cmd.Wait()
	if recoverLocks != nil {
		recoverLocks()
	}
	select {
	case <-drained:
	case <-time.After(100 * time.Millisecond):
	}
	if result != 0 || err == nil {
		return result
	}
	if code := cmd.ProcessState.ExitCode(); code >= 0 {
		return code
	}
	fmt.Fprintln(os.Stderr, err)
	return gitFailedExit
}

func watchGit(parentGone, drained <-chan struct{}, exited <-chan error, stop func()) int {
	select {
	case err := <-exited:
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			stop()
			return gitFailedExit
		}
		select {
		case <-drained:
		case <-parentGone:
			stop()
		case <-time.After(100 * time.Millisecond):
		}
		return 0
	case <-parentGone:
		stop()
		return gitFailedExit
	}
}

func stopGit(pid int, grace time.Duration) {
	_ = unix.Kill(-pid, unix.SIGTERM)
	time.Sleep(grace)
	_ = unix.Kill(-pid, unix.SIGKILL)
}
