package shim

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func prepareWorker() error {
	return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
}

// Kill freezes and rescans the shim's process tree before killing it, including Claude's detached Bash sessions.
func Kill(shimPID int) error {
	if shimPID <= 1 || shimPID == os.Getpid() {
		return errors.New("refusing to kill the current process, pid 0 or 1")
	}
	proc, err := os.OpenRoot("/proc")
	if err != nil {
		return err
	}
	defer func() { _ = proc.Close() }()
	if info, found := readProcess(proc, shimPID); found && info.session != shimPID {
		return fmt.Errorf("pid %d is not a shim session leader", shimPID)
	}

	targets := make(map[int]*os.Process)
	var suspended []*os.Process
	defer func() {
		for _, process := range suspended {
			_ = process.Signal(syscall.SIGCONT)
		}
		for _, process := range targets {
			_ = process.Release()
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	previousStable := false
	for {
		tree, err := shimTree(proc, shimPID)
		if err != nil {
			return err
		}
		stable := true
		for pid, info := range tree {
			if info.stopped() {
				continue
			}
			stable = false
			if _, found := targets[pid]; found {
				continue
			}
			process, err := findTarget(proc, pid, info)
			if errors.Is(err, os.ErrProcessDone) {
				continue
			}
			if err != nil {
				return err
			}
			targets[pid] = process
			if err := process.Signal(syscall.SIGSTOP); err != nil && !errors.Is(err, os.ErrProcessDone) {
				return err
			}
			suspended = append(suspended, process)
		}
		if stable && previousStable {
			return killFrozenTree(proc, tree, shimPID)
		}
		previousStable = stable
		if time.Now().After(deadline) {
			return fmt.Errorf("could not freeze shim %d and its descendants", shimPID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type processInfo struct {
	parent  int
	session int
	state   string
	started string
}

func (info processInfo) stopped() bool { return info.state == "T" || info.state == "t" }

func shimTree(proc *os.Root, shimPID int) (map[int]processInfo, error) {
	entries, err := fs.ReadDir(proc.FS(), ".")
	if err != nil {
		return nil, err
	}
	children := make(map[int][]int)
	all := make(map[int]processInfo)
	pending := []int{shimPID}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		info, ok := readProcess(proc, pid)
		if !ok {
			continue
		}
		all[pid] = info
		children[info.parent] = append(children[info.parent], pid)
		if info.session == shimPID && pid != shimPID {
			pending = append(pending, pid)
		}
	}
	tree := make(map[int]processInfo)
	seen := make(map[int]bool)
	for len(pending) > 0 {
		pid := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		pending = append(pending, children[pid]...)
		if info, ok := all[pid]; ok && info.state != "Z" && info.state != "X" {
			tree[pid] = info
		}
	}
	return tree, nil
}

func readProcess(proc *os.Root, pid int) (processInfo, bool) {
	raw, err := proc.ReadFile(filepath.Join(strconv.Itoa(pid), "stat"))
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
	session, sessionErr := strconv.Atoi(fields[3])
	return processInfo{parent: parent, session: session, state: fields[0], started: fields[19]},
		parentErr == nil && sessionErr == nil
}

func findTarget(proc *os.Root, pid int, info processInfo) (*os.Process, error) {
	process, err := os.FindProcess(pid)
	if err != nil {
		return nil, err
	}
	if current, ok := readProcess(proc, pid); !ok || current.started != info.started {
		_ = process.Release()
		return nil, os.ErrProcessDone
	}
	return process, nil
}

func killFrozenTree(proc *os.Root, tree map[int]processInfo, shimPID int) error {
	var killErrors error
	for pid, info := range tree {
		if pid != shimPID {
			killErrors = errors.Join(killErrors, killTarget(proc, pid, info))
		}
	}
	if killErrors != nil {
		return killErrors
	}
	if info, ok := tree[shimPID]; ok {
		return killTarget(proc, shimPID, info)
	}
	return nil
}

func killTarget(proc *os.Root, pid int, info processInfo) error {
	process, err := findTarget(proc, pid, info)
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = process.Release() }()
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
