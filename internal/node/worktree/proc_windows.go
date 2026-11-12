//go:build windows

package worktree

import "os/exec"

// Nodes run on Linux; on Windows a script works for development and tests
// only, and only the shell itself is ended when its time is up.
func ownGroup(*exec.Cmd) {}
