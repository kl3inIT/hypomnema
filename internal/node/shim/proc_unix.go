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

func killSignal() string { return syscall.SIGKILL.String() }

func signalOf(ps *os.ProcessState) string {
	ws, ok := ps.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return ""
	}

	return ws.Signal().String()
}
