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

const gitSupervisorArg = "--hypomnema-worktree-supervisor"

func (git mirrorGit) runProcess(
	ctx context.Context, dir string, env []string, name string, args ...string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	read, write, err := os.Pipe()
	if err != nil {
		return "", err
	}
	defer func() { _ = read.Close() }()
	defer func() { _ = write.Close() }()

	stall, grace := git.bounds()
	locked := "no"
	if git.lock != nil {
		locked = "yes"
	}
	launch := append([]string{gitSupervisorArg, locked, stall.String(), grace.String(), name}, args...)
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
	if err != nil {
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		} else if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == gitStalledExit {
			err = fmt.Errorf("no output for %s: %w", stall, context.DeadlineExceeded)
		}
	}
	return string(output), err
}

func init() {
	if len(os.Args) < 6 || os.Args[1] != gitSupervisorArg {
		return
	}
	if os.Args[2] == "yes" {
		unix.CloseOnExec(3)
	}
	stall, stallErr := time.ParseDuration(os.Args[3])
	grace, graceErr := time.ParseDuration(os.Args[4])
	if stallErr != nil || graceErr != nil || stall <= 0 || grace <= 0 {
		os.Exit(gitFailedExit)
	}
	signal.Ignore(syscall.SIGPIPE)
	parentGone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		close(parentGone)
	}()
	os.Exit(supervise(parentGone, stall, grace, os.Args[5], os.Args[6:]))
}

func supervise(parentGone <-chan struct{}, stall, grace time.Duration, name string, args []string) int {
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
	activity, drained := watchOutput(read)
	exited := make(chan error, 1)
	go func() { exited <- gitExited(cmd.Process.Pid) }()

	result := watchGit(parentGone, activity, drained, exited, cmd.Process.Pid, stall, grace)
	err = cmd.Wait()
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

func watchOutput(read *os.File) (<-chan struct{}, <-chan struct{}) {
	activity, drained := make(chan struct{}, 1), make(chan struct{})
	go func() {
		defer close(drained)
		buf := make([]byte, 4096)
		for {
			size, err := read.Read(buf)
			if size != 0 {
				select {
				case activity <- struct{}{}:
				default:
				}
				_, _ = os.Stdout.Write(buf[:size])
			}
			if err != nil {
				return
			}
		}
	}()
	return activity, drained
}

func watchGit(
	parentGone, activity, drained <-chan struct{}, exited <-chan error, pid int, stall, grace time.Duration,
) int {
	timer := time.NewTimer(stall)
	defer timer.Stop()
	for {
		select {
		case err := <-exited:
			if err == nil {
				select {
				case <-drained:
				case <-parentGone:
					stopGit(pid, grace)
				case <-time.After(100 * time.Millisecond):
					stopGit(pid, grace)
				}
				return 0
			}
			fmt.Fprintln(os.Stderr, err)
			stopGit(pid, grace)
			return gitFailedExit
		case <-activity:
			timer.Reset(stall)
		case <-parentGone:
			stopGit(pid, grace)
			return gitFailedExit
		case <-timer.C:
			select {
			case <-activity:
				timer.Reset(stall)
			default:
				stopGit(pid, grace)
				return gitStalledExit
			}
		}
	}
}

func stopGit(pid int, grace time.Duration) {
	_ = unix.Kill(-pid, unix.SIGTERM)
	time.Sleep(grace)
	_ = unix.Kill(-pid, unix.SIGKILL)
}
