//go:build unix

package shim

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// detach puts the shim in its own session, so it and its worker survive the
// daemon (Omnara runner_detach_unix.go; spike 1a: killing the daemon's group
// would kill the worker).
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// Alive reports whether the process runs.
func Alive(pid int) bool {
	err := syscall.Kill(pid, 0)

	return err == nil || errors.Is(err, syscall.EPERM)
}

// Kill kills the shim's whole session group: the shim, the worker and what
// the worker started (durable-harness host.ts kills -pgid).
func Kill(pid int) error {
	if pid <= 1 {
		return errors.New("refusing to kill pid 0 or 1")
	}

	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}

	return nil
}

func signalOf(ps *os.ProcessState) string {
	ws, ok := ps.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return ""
	}

	return ws.Signal().String()
}
