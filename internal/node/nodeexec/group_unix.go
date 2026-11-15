//go:build !windows

package nodeexec

import (
	"os/exec"
	"syscall"
)

// ownGroup starts the command in a process group of its own, so that its
// end kills what it started too (Nezha's agent, Coder's agentproc).
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills the command's process group.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}

	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
