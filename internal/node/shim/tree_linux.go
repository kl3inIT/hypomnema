package shim

import (
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func ownTree(root int) (workerTree, error) {
	return workerTree{root: root, snapshot: processTable, read: readProcess},
		unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
}

func processTable() (map[int]processInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	table := make(map[int]processInfo)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err == nil && pid > 1 {
			if info, ok := readProcess(pid); ok {
				table[pid] = info
			}
		}
	}
	return table, nil
}

func readProcess(pid int) (processInfo, bool) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return processInfo{}, false
	}
	stat := string(raw)
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return processInfo{}, false
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 20 {
		return processInfo{}, false
	}
	parent, parentErr := strconv.Atoi(fields[1])
	started, startErr := strconv.ParseUint(fields[19], 10, 64)
	return processInfo{parent: parent, started: started,
			stopped: fields[0] == "T" || fields[0] == "t", dead: fields[0] == "Z" || fields[0] == "X"},
		parentErr == nil && startErr == nil
}

func reapOrphans(workerPID int) func() {
	done, reaped := make(chan struct{}), make(chan struct{})
	children := make(chan os.Signal, 1)
	signal.Notify(children, unix.SIGCHLD)
	go func() {
		defer close(reaped)
		reapChildren(workerPID)
		for {
			select {
			case <-children:
				reapChildren(workerPID)
			case <-done:
				reapChildren(workerPID)
				return
			}
		}
	}()
	return func() { signal.Stop(children); close(done); <-reaped }
}

func reapChildren(workerPID int) {
	table, _ := processTable()
	for pid, info := range table {
		if info.parent == os.Getpid() && info.dead && pid != workerPID {
			_, _ = unix.Wait4(pid, nil, unix.WNOHANG, nil)
		}
	}
}
