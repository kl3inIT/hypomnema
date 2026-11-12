//go:build unix

package worktree

import (
	"os/exec"
	"syscall"
)

// ownGroup puts a command in its own process group and ends the whole group
// when the command is cancelled, so what a script started does not outlive it.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
