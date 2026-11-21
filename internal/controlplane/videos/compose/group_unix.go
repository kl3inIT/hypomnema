//go:build !windows

package compose

import (
	"os/exec"
	"syscall"
)

// ownGroup starts the command in a process group of its own, so that killing it kills what it started too (as
// internal/node/nodeexec does, after Nezha's agent and Coder's agentproc).
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills the command's process group, or the command alone when the group cannot be killed.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}

	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}

	return nil
}
