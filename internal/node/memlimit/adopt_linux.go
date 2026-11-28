//go:build linux

package memlimit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// adoptWait bounds the wait for systemd to move an adopted worker into its new scope, and then for every process
// the worker forked meanwhile to be moved after it.
const adoptWait = 5 * time.Second

// cgroupOps is how Adopt reads and changes cgroups: the host's, or a test's.
type cgroupOps struct {
	run      func(ctx context.Context, argv ...string) ([]byte, error)
	cgroupOf func(pid int) (string, error)
	// moveInto writes a process into a cgroup's cgroup.procs; freeze writes its cgroup.freeze.
	moveInto func(dir string, pid int) error
	freeze   func(dir string, frozen bool) error
}

var hostCgroups = cgroupOps{
	run: runCommand, cgroupOf: cgroupOf,
	moveInto: func(dir string, pid int) error {
		return os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0o600)
	},
	freeze: func(dir string, frozen bool) error {
		value := "0"
		if frozen {
			value = "1"
		}

		return os.WriteFile(filepath.Join(dir, "cgroup.freeze"), []byte(value), 0o600)
	},
}

// Adopt moves a worker an earlier daemon started, the processes tree lists (its shim, everything under it, and the
// holder that keeps its scope until its end is read, Holder), into the Dispatch's own scope in WorkerSlice, as Start
// would have put it (issue #1313, review rounds 1 and 2): a node that updated itself while workers ran keeps them
// under the ceiling. It returns the scope's cgroup directory. A worker already in the slice is left where it is.
//
// The scope is made for processes that already run with the manager's StartTransientUnit and its PIDs property
// (systemd.scope(5): a scope's processes are started elsewhere and handed to it). A process the worker forked between
// the listing and the move stays behind, and may fork again. The worker shares the daemon's cgroup, which cannot be
// frozen without freezing the daemon, so the new scope is frozen instead (cgroup.freeze), as runc freezes a
// container's cgroup before it lists its processes so that none forks in between (libcontainer/init_linux.go:699-
// 702): what is moved in after it is frozen as it enters, and can fork no more. The tree is listed again and what is
// outside is moved, until a listing finds every process inside; the scope is thawed then. Memory a process had
// already used stays charged to the cgroup it was in until it is freed (the kernel's cgroup-v2.rst, "Memory
// Ownership"): the ceiling covers what the worker uses after the move.
func (l Limit) Adopt(ctx context.Context, dispatchID string, memoryMax int64, tree func() []int) (string, error) {
	return l.adopt(ctx, hostCgroups, dispatchID, memoryMax, tree)
}

func (l Limit) adopt(ctx context.Context, ops cgroupOps, dispatchID string, memoryMax int64, tree func() []int,
) (string, error) {
	if !l.On() {
		return "", errors.New("the workers' memory is not limited")
	}

	pids := tree()
	if len(pids) == 0 {
		return "", errors.New("the worker has no process left")
	}

	if dir, err := ops.cgroupOf(pids[0]); err == nil && inSlice(dir) {
		return dir, nil
	}

	callCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	if out, err := ops.run(callCtx, l.startScope(dispatchID, memoryMax, pids)...); err != nil {
		return "", fmt.Errorf("making scope %s: %w: %s", ScopeName(dispatchID), err, strings.TrimSpace(string(out)))
	}

	deadline := time.Now().Add(adoptWait)

	// The scope's job moves the processes; wait for the shim to be in it.
	dir := ""
	for dir == "" {
		if got, err := ops.cgroupOf(pids[0]); err == nil && inSlice(got) {
			dir = got

			break
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("the shim %d was not moved into %s", pids[0], ScopeName(dispatchID))
		}

		if err := pause(ctx); err != nil {
			return "", err
		}
	}

	if err := ops.freeze(dir, true); err != nil {
		return dir, fmt.Errorf("freezing %s: %w", ScopeName(dispatchID), err)
	}

	defer func() { _ = ops.freeze(dir, false) }()

	for {
		outside := 0

		for _, pid := range tree() {
			got, err := ops.cgroupOf(pid)
			if err != nil || got == dir {
				continue // gone, or inside
			}

			outside++

			if err := ops.moveInto(dir, pid); err != nil && !errors.Is(err, syscall.ESRCH) {
				return dir, fmt.Errorf("moving process %d into %s: %w", pid, ScopeName(dispatchID), err)
			}
		}

		if outside == 0 {
			return dir, nil
		}

		if time.Now().After(deadline) {
			return dir, fmt.Errorf("%d processes of the worker were still outside %s", outside, ScopeName(dispatchID))
		}
	}
}

// startScope is the busctl call that makes a Dispatch's scope with these processes.
func (l Limit) startScope(dispatchID string, memoryMax int64, pids []int) []string {
	pidsValue := []string{strconv.Itoa(len(pids))}
	for _, pid := range pids {
		pidsValue = append(pidsValue, strconv.Itoa(pid))
	}

	props := [][]string{
		append([]string{"PIDs", "au"}, pidsValue...),
		{"Slice", "s", nodeproto.WorkerSlice},
		{"CollectMode", "s", "inactive-or-failed"},
	}
	if l.OOMPolicy {
		props = append(props, []string{"OOMPolicy", "s", "continue"})
	}
	if memoryMax > 0 {
		props = append(props, []string{"MemoryMax", "t", strconv.FormatInt(memoryMax, 10)})
	}

	argv := []string{
		l.Busctl, "--user", "call", "org.freedesktop.systemd1", "/org/freedesktop/systemd1",
		"org.freedesktop.systemd1.Manager", "StartTransientUnit", "ssa(sv)a(sa(sv))", ScopeName(dispatchID), "fail",
		strconv.Itoa(len(props)),
	}
	for _, prop := range props {
		argv = append(argv, prop...)
	}

	return append(argv, "0") // no auxiliary units
}

func pause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(20 * time.Millisecond):
		return nil
	}
}

// Holder is a process that does nothing, put in an adopted worker's scope so that the scope, and the memory.events
// that counts its kills for memory, outlive the worker until the Supervisor has read them (issue #1313, review round
// 2): a shim an earlier daemon started does not count them itself, and systemd removes a scope with its last process.
// It is kept in a file of the worker's run directory, with its start time, so a later daemon stops it and no other
// process that took its pid.
type Holder struct {
	PID   int    `json:"pid"`
	Start uint64 `json:"start"`
}

// holderFile is where a worker's holder is kept, in its run directory.
const holderFile = "scope-holder.json"

// StartHolder starts a holder for the worker whose run directory is dir and records it there.
func StartHolder(dir string) (Holder, error) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		return Holder{}, err
	}

	cmd := exec.Command(sleep, "infinity") //nolint:noctx,gosec // it outlives the call, stopped by StopHolder
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return Holder{}, fmt.Errorf("starting the scope's holder: %w", err)
	}

	go func() { _ = cmd.Wait() }()

	h := Holder{PID: cmd.Process.Pid}
	if h.Start, err = startTime(h.PID); err != nil {
		_ = cmd.Process.Kill()

		return Holder{}, err
	}

	raw, err := json.Marshal(h)
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, holderFile), raw, 0o600)
	}

	if err != nil {
		_ = cmd.Process.Kill()

		return Holder{}, fmt.Errorf("recording the scope's holder: %w", err)
	}

	return h, nil
}

// HeldScope is the scope a worker's holder is in, from its run directory; empty when it has none in the slice.
func HeldScope(dir string) string {
	h, ok := readHolder(dir)
	if !ok {
		return ""
	}

	scope, err := cgroupOf(h.PID)
	if err != nil || !inSlice(scope) {
		return ""
	}

	return scope
}

// StopHolder stops the worker's holder, if it still runs, and forgets it; its scope then goes with its last process.
func StopHolder(dir string) {
	if h, ok := readHolder(dir); ok {
		_ = syscall.Kill(h.PID, syscall.SIGKILL)
	}

	_ = os.Remove(filepath.Join(dir, holderFile))
}

// readHolder is the holder recorded in dir, when that process still runs: the same pid and start time.
func readHolder(dir string) (Holder, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, holderFile)) //nolint:gosec // the daemon's own run directory
	if err != nil {
		return Holder{}, false
	}

	var h Holder
	if json.Unmarshal(raw, &h) != nil || h.PID <= 1 {
		return Holder{}, false
	}

	start, err := startTime(h.PID)
	if err != nil || start != h.Start {
		return Holder{}, false
	}

	return h, true
}

// startTime is a process's start time, in clock ticks since boot (proc(5), /proc/<pid>/stat field 22).
func startTime(pid int) (uint64, error) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}

	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return 0, errors.New("unreadable stat")
	}

	fields := strings.Fields(string(raw)[end+1:])
	if len(fields) < 20 {
		return 0, errors.New("short stat")
	}

	return strconv.ParseUint(fields[19], 10, 64)
}
