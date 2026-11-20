package shim

import "golang.org/x/sys/unix"

func ownTree(root int) (workerTree, error) {
	return workerTree{root: root, snapshot: processTable, read: readProcess}, nil
}

func processTable() (map[int]processInfo, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	table := make(map[int]processInfo, len(processes))
	for _, process := range processes {
		table[int(process.Proc.P_pid)] = infoOf(&process)
	}
	return table, nil
}

func readProcess(pid int) (processInfo, bool) {
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(process.Proc.P_pid) != pid {
		return processInfo{}, false
	}
	return infoOf(process), true
}

func infoOf(process *unix.KinfoProc) processInfo {
	started := process.Proc.P_starttime
	return processInfo{parent: int(process.Eproc.Ppid),
		started: uint64(started.Sec)*1_000_000 + uint64(started.Usec),
		stopped: process.Proc.P_stat == 4, dead: process.Proc.P_stat == 5}
}

func reapOrphans(int) func() { return func() {} }
