//go:build windows

package compose

import "os/exec"

// ownGroup does nothing on Windows, as internal/node/nodeexec: the control plane is released for Linux and macOS,
// and a Windows build kills ffmpeg alone.
func ownGroup(*exec.Cmd) {}

// killGroup kills the command.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}

	return cmd.Process.Kill()
}
