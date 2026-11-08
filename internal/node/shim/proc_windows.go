//go:build windows

package shim

import (
	"errors"
	"os"
	"os/exec"
)

// Nodes run on Linux; on Windows the shim works for development and tests
// only, without its own session.
func detach(*exec.Cmd) {}

// Alive reports whether the process runs.
func Alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	_ = p.Release()

	return true
}

// Kill kills the shim; on Windows its worker is not in a group.
func Kill(pid int) error {
	if pid <= 0 {
		return errors.New("refusing to kill pid 0")
	}

	p, err := os.FindProcess(pid)
	if err != nil {
		return nil //nolint:nilerr // a process that cannot be found is already gone
	}

	if err := p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}

	return nil
}

func signalOf(*os.ProcessState) string { return "" }
