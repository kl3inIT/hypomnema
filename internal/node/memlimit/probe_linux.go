//go:build linux

package memlimit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// probeTimeout bounds each command of the probe; the node probes once, at its start, often while it is busy.
const probeTimeout = 10 * time.Second

// cgroupRoot is where cgroup v2 is mounted.
const cgroupRoot = "/sys/fs/cgroup"

// Probe finds whether the node can run its workers in WorkerSlice under a memory ceiling: systemd-run and systemctl
// are on PATH, cgroup v2 is mounted, the user's systemd manager has the memory controller (its
// user@<uid>.service/cgroup.controllers, which systemd fills from its Delegate=), and a scope can be started in the
// slice, with OOMPolicy=continue where the manager takes it.
func Probe(ctx context.Context) Limit {
	return probe(ctx, cgroupRoot, os.Getuid(), runCommand)
}

func probe(ctx context.Context, root string, uid int,
	run func(ctx context.Context, argv ...string) ([]byte, error),
) Limit {
	systemdRun, err := exec.LookPath("systemd-run")
	if err != nil {
		return Limit{Reason: "systemd-run is not on PATH: the machine has no systemd"}
	}

	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return Limit{Reason: "systemctl is not on PATH: the machine has no systemd"}
	}

	busctl, err := exec.LookPath("busctl")
	if err != nil {
		return Limit{Reason: "busctl is not on PATH: a worker an earlier node started could not be moved into the slice"}
	}

	if _, err := os.Stat(filepath.Join(root, "cgroup.controllers")); err != nil {
		return Limit{Reason: "cgroup v2 is not mounted at " + root}
	}

	id := strconv.Itoa(uid)
	manager := filepath.Join(root, "user.slice", "user-"+id+".slice", "user@"+id+".service")

	raw, err := os.ReadFile(filepath.Join(manager, "cgroup.controllers")) //nolint:gosec // the kernel's file
	if err != nil {
		return Limit{Reason: "no systemd user manager runs for this user (user@" + id + ".service)"}
	}

	if !strings.Contains(" "+strings.TrimSpace(string(raw))+" ", " memory ") {
		return Limit{Reason: "the systemd user manager does not delegate the memory controller (user@" + id +
			".service has: " + strings.TrimSpace(string(raw)) + ")"}
	}

	l := Limit{SystemdRun: systemdRun, Systemctl: systemctl, Busctl: busctl, OOMPolicy: true}

	// A scope that runs true proves the whole way: the user bus, the slice and the policy.
	scope := func(l Limit) error {
		argv := append(l.Scope("probe-"+strconv.FormatInt(time.Now().UnixNano(), 36), 0), "true")

		ctx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()

		out, err := run(ctx, argv...)
		if err != nil {
			line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
			if line == "" {
				line = err.Error()
			}

			return errors.New(line)
		}

		return nil
	}

	err = scope(l)
	if err == nil {
		return l
	}

	// systemd before 253 takes no OOMPolicy= for a scope; it leaves the scope running after a kill in it anyway.
	l.OOMPolicy = false
	if again := scope(l); again == nil {
		return l
	}

	return Limit{Reason: fmt.Sprintf("no scope could be started in %s: %v", nodeproto.WorkerSlice, err)}
}

// adoptWait bounds the wait for systemd to move an adopted worker into its new scope.
const adoptWait = 2 * time.Second

// Adopt moves a worker an earlier daemon started, the processes of tree (its shim and everything under it), into the
// Dispatch's own scope in WorkerSlice, as Start would have put it (issue #1313, review round 1): a node that updated
// itself while workers ran keeps them under the ceiling. A scope is made for processes that already run with the
// manager's StartTransientUnit and its PIDs property (systemd.scope(5): a scope's processes are started elsewhere and
// handed to it); a process forked while it was made is written into the scope's cgroup.procs after it, which the
// user's manager delegates to the user. It returns the scope's cgroup directory. A worker already in the slice is
// left where it is.
func (l Limit) Adopt(ctx context.Context, dispatchID string, memoryMax int64, tree func() []int) (string, error) {
	if !l.On() {
		return "", errors.New("the workers' memory is not limited")
	}

	pids := tree()
	if len(pids) == 0 {
		return "", errors.New("the worker has no process left")
	}

	if dir, err := cgroupOf(pids[0]); err == nil && inSlice(dir) {
		return dir, nil
	}

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
	argv = append(argv, "0") // no auxiliary units

	callCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	if out, err := runCommand(callCtx, argv...); err != nil {
		return "", fmt.Errorf("making scope %s: %w: %s", ScopeName(dispatchID), err, strings.TrimSpace(string(out)))
	}

	// The scope's job moves the processes; wait for the shim to be in it.
	var dir string

	for deadline := time.Now().Add(adoptWait); ; {
		if got, err := cgroupOf(pids[0]); err == nil && inSlice(got) {
			dir = got

			break
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("the shim %d was not moved into %s", pids[0], ScopeName(dispatchID))
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}

	// A process forked between the listing and the move stayed where it was.
	for _, pid := range tree() {
		if got, err := cgroupOf(pid); err == nil && got != dir {
			_ = os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0o600)
		}
	}

	return dir, nil
}

// cgroupOf is the cgroup directory of a process, under the cgroup v2 mount.
func cgroupOf(pid int) (string, error) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", err
	}

	path, err := unifiedPath(raw)
	if err != nil {
		return "", err
	}

	return filepath.Join(cgroupRoot, path), nil
}

// inSlice says a cgroup directory is a scope inside WorkerSlice.
func inSlice(dir string) bool {
	return strings.Contains(dir, "/"+nodeproto.WorkerSlice+"/")
}

// ScopeOf is the cgroup directory of a worker's scope in WorkerSlice, from its shim's pid, for its memory.events
// (ScopeOOMKills); an error for a shim that is not in the slice.
func ScopeOf(pid int) (string, error) {
	dir, err := cgroupOf(pid)
	if err != nil {
		return "", err
	}

	if !inSlice(dir) {
		return "", fmt.Errorf("the shim %d is not in %s but in %s", pid, nodeproto.WorkerSlice, dir)
	}

	return dir, nil
}

// ScopeOOMKills is the oom_kill count of a scope's cgroup directory.
func ScopeOOMKills(dir string) (int64, error) {
	events, err := os.ReadFile(filepath.Join(dir, "memory.events")) //nolint:gosec // the kernel's file
	if err != nil {
		return 0, err
	}

	return OOMKills(events)
}

// OwnOOMKills is the oom_kill count of the cgroup the calling process is in, from /proc/self/cgroup and its
// memory.events.
func OwnOOMKills() (int64, error) {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return 0, err
	}

	path, err := unifiedPath(raw)
	if err != nil {
		return 0, err
	}

	events, err := os.ReadFile(filepath.Join(cgroupRoot, path, "memory.events")) //nolint:gosec // the kernel's file
	if err != nil {
		return 0, err
	}

	return OOMKills(events)
}

// unifiedPath is the cgroup v2 path in a /proc/<pid>/cgroup, the line "0::<path>".
func unifiedPath(raw []byte) (string, error) {
	for line := range bytes.Lines(raw) {
		if path, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("0::")); ok {
			return filepath.Clean("/" + string(path)), nil
		}
	}

	return "", errors.New("the process is in no cgroup v2")
}
