//go:build unix && !linux && !darwin

package shim

import (
	"os/exec"
)

type workerStop struct{ cmd *exec.Cmd }

func startWorker(cmd *exec.Cmd) (workerStop, error) {
	return workerStop{cmd: cmd}, cmd.Start()
}

func (stop workerStop) close() {}

func (stop workerStop) kill() (bool, error) {
	err := stop.cmd.Process.Kill()
	return err == nil, err
}

func reapOrphans(int) func() { return func() {} }

// Tree is the shim alone: this system has no process table the node reads.
func Tree(pid int) []int { return []int{pid} }
