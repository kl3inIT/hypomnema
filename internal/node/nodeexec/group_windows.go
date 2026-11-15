//go:build windows

package nodeexec

import "os/exec"

// ownGroup does nothing on Windows: the node is released for Linux and
// macOS, and a Windows build kills the shell alone.
func ownGroup(*exec.Cmd) {}

// killGroup kills the shell.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
