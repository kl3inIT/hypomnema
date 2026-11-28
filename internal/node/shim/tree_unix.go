//go:build linux || darwin

package shim

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type processInfo struct {
	parent  int
	started uint64
	stopped bool
	dead    bool
}

type workerTree struct {
	root     int
	snapshot func() (map[int]processInfo, error)
	read     func(int) (processInfo, bool)
}

// Tree is the shim at pid and every process under it, the shim first: the shim is its worker's subreaper, so what
// the worker started stays under it. With no process table, the shim alone.
func Tree(pid int) []int {
	table, err := processTable()
	if err != nil {
		return []int{pid}
	}

	if _, ok := table[pid]; !ok {
		return nil
	}

	return append([]int{pid}, descendants(pid, table)...)
}

func descendants(root int, table map[int]processInfo) []int {
	children := make(map[int][]int)
	for pid, info := range table {
		if pid > 1 {
			children[info.parent] = append(children[info.parent], pid)
		}
	}
	seen := map[int]bool{root: true}
	tree := []int{root}
	for index := 0; index < len(tree); index++ {
		for _, child := range children[tree[index]] {
			if !seen[child] {
				seen[child] = true
				tree = append(tree, child)
			}
		}
	}
	return tree[1:]
}

func (tree workerTree) owns(pid int, table map[int]processInfo) bool {
	for depth := 0; depth <= len(table); depth++ {
		if pid == tree.root {
			return true
		}
		expected, present := table[pid]
		current, found := tree.read(pid)
		if !present || !found || expected.started != current.started || (depth == 0 && current.dead) {
			return false
		}
		pid = current.parent
	}
	return false
}

func (tree workerTree) signal(pid int, table map[int]processInfo, signal syscall.Signal) (bool, error) {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}
	defer func() { _ = process.Release() }()
	if !tree.owns(pid, table) {
		return false, nil
	}
	err = process.Signal(signal)
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return err == nil, err
}

func (tree workerTree) kill(freezeGrace time.Duration) (bool, error) {
	deadline := time.Now().Add(freezeGrace)
	suspended := make(map[int]uint64)
	var table map[int]processInfo
	var snapshotErr error
	stable := false
	for {
		latest, err := tree.snapshot()
		if err != nil {
			snapshotErr = err
			break
		}
		table = latest
		frozen := true
		for _, pid := range descendants(tree.root, table) {
			info := table[pid]
			if info.stopped || info.dead {
				continue
			}
			frozen = false
			if started, ok := suspended[pid]; !ok || started != info.started {
				if sent, _ := tree.signal(pid, table, syscall.SIGSTOP); sent {
					suspended[pid] = info.started
				}
			}
		}
		if (frozen && stable) || !time.Now().Before(deadline) {
			break
		}
		stable = frozen
		time.Sleep(10 * time.Millisecond)
	}

	signaled := false
	owned := descendants(tree.root, table)
	for index := len(owned) - 1; index >= 0; index-- {
		pid := owned[index]
		if !table[pid].dead {
			sent, err := tree.signal(pid, table, syscall.SIGKILL)
			signaled = signaled || sent
			snapshotErr = errors.Join(snapshotErr, err)
		}
	}
	return signaled, snapshotErr
}

type workerStop struct {
	cmd  *exec.Cmd
	tree workerTree
}

func startWorker(cmd *exec.Cmd) (workerStop, error) {
	tree, err := ownTree(os.Getpid())
	if err != nil {
		return workerStop{}, err
	}
	return workerStop{cmd: cmd, tree: tree}, cmd.Start()
}

func (stop workerStop) close() {}

func (stop workerStop) kill() (bool, error) {
	signaled, err := stop.tree.kill(2 * time.Second)
	if err == nil {
		return signaled, nil
	}
	directErr := stop.cmd.Process.Kill()
	directSignaled := directErr == nil
	if errors.Is(directErr, os.ErrProcessDone) {
		directErr = nil
	}
	return signaled || directSignaled, errors.Join(err, directErr)
}
