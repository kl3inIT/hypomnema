// Package memlimit puts one memory ceiling over every worker of a node
// (issue #1313). On 2026-11-27 a node's workers filled all the memory of a
// machine without swap that also ran production services; the kernel kept
// reclaiming page cache, never killed anything, and the machine hung until a
// hard reboot.
//
// Every worker, its shim and everything it spawns, runs in a transient
// systemd user scope of its own, hypomnema-worker-<dispatch>.scope, inside
// one slice, nodeproto.WorkerSlice; the node daemon stays outside it, so a
// kill inside the slice never takes the daemon down. This is Buildkite's
// Elastic CI Stack, which runs its agent's jobs in buildkite-agent.slice
// with MemoryHigh, MemoryMax and MemorySwapMax
// (packer/linux/stack/conf/bin/bk-install-elastic-stack.sh:732-777), with
// the slice's MemoryMax set to the node's allocatable memory, which the
// control plane computes (Kubernetes' allocatable, issue #731) and sends at
// hello and with each heartbeat's acknowledgement. MemoryHigh is the same
// value, as Buildkite's defaults give both the same (templates/aws-stack.yml:
// 477-494): a live check found that with MemoryHigh at 90% a worker growing
// past it stays throttled between the two for 15 minutes and more, never
// killed, while with both equal the kernel kills it within a second (the
// owner chose that, 2026-11-28). MemorySwapMax is 0. A worker's own scope gets a MemoryMax
// only when its profile sets limits.memory, as a Kubernetes or Coder
// container's limits are optional (Coder
// examples/templates/kubernetes/main.tf:298-306).
//
// The shim reads its scope's memory.events when its worker exits: an
// oom_kill there is a process of this worker the kernel killed for memory,
// whichever limit it went over, and the Dispatch says oom_killed, as
// Kubernetes says OOMKilled (containerd internal/cri/server/events.go:194-236
// reads the container's memory.events oom_kill the same way). The scope
// keeps OOMPolicy=continue, so systemd does not stop the shim with the
// process the kernel killed, and the shim reads the count before its scope
// is collected (containerd's note 3 there: a scope's cgroup goes with its
// last process).
//
// Without systemd, or where the user manager does not delegate the memory
// controller, the node runs its workers as before, says so once in its log
// and at each hello, and the Machines page shows "Memory not limited".
package memlimit

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Limit is whether the node's workers run in WorkerSlice, and how.
type Limit struct {
	// SystemdRun starts a worker's scope and Systemctl sets the slice's properties; empty SystemdRun: off.
	SystemdRun string
	Systemctl  string
	// OOMPolicy says the user manager takes OOMPolicy= for a scope (systemd 253 and later); an earlier one has
	// no OOM policy for scopes, and leaves a scope running after a kill in it anyway.
	OOMPolicy bool
	// Reason says why it is off.
	Reason string
}

// On says the workers run in WorkerSlice.
func (l Limit) On() bool { return l.SystemdRun != "" }

// Hello is what the node says of it at its hello.
func (l Limit) Hello() *nodeproto.MemoryLimit {
	if l.On() {
		return &nodeproto.MemoryLimit{On: true}
	}

	return &nodeproto.MemoryLimit{Reason: l.Reason}
}

// ScopeName is the transient scope a Dispatch's worker runs in.
func ScopeName(dispatchID string) string { return "hypomnema-worker-" + dispatchID + ".scope" }

// Scope is the command that runs the argv after it in the Dispatch's scope inside WorkerSlice: systemd-run moves
// itself into the new scope and then executes the argv, which keeps its pid. memoryMax, when more than 0, is the
// worker's own MemoryMax, from its profile's limits.memory. Nil when the limit is off.
func (l Limit) Scope(dispatchID string, memoryMax int64) []string {
	if !l.On() {
		return nil
	}

	args := []string{
		l.SystemdRun, "--user", "--scope", "--quiet", "--collect",
		"--slice=" + nodeproto.WorkerSlice, "--unit=" + ScopeName(dispatchID),
	}
	if l.OOMPolicy {
		args = append(args, "--property=OOMPolicy=continue")
	}
	if memoryMax > 0 {
		args = append(args, "--property=MemoryMax="+strconv.FormatInt(memoryMax, 10))
	}

	return append(args, "--")
}

// SliceProperties are WorkerSlice's memory properties for a ceiling of memoryMax bytes: MemoryMax and MemoryHigh at
// it, so a worker over it is killed rather than throttled without end, and no swap. A ceiling of 0 or less is none:
// every property is infinity, as before the limit.
func SliceProperties(memoryMax int64) []string {
	if memoryMax <= 0 {
		return []string{"MemoryMax=infinity", "MemoryHigh=infinity", "MemorySwapMax=infinity"}
	}

	ceiling := strconv.FormatInt(memoryMax, 10)

	return []string{"MemoryMax=" + ceiling, "MemoryHigh=" + ceiling, "MemorySwapMax=0"}
}

// OOMKills is the oom_kill count of a cgroup's memory.events: how many of its processes, and its descendants',
// any OOM killer killed (the kernel's Documentation/admin-guide/cgroup-v2.rst).
func OOMKills(events []byte) (int64, error) {
	sc := bufio.NewScanner(bytes.NewReader(events))
	for sc.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok || key != "oom_kill" {
			continue
		}

		n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("memory.events has oom_kill %q", value)
		}

		return n, nil
	}

	if err := sc.Err(); err != nil {
		return 0, err
	}

	return 0, errors.New("memory.events has no oom_kill")
}

// Ceiling applies the control plane's ceiling to WorkerSlice, once each time it changes.
type Ceiling struct {
	Limit  Limit
	Logger *slog.Logger
	// Run runs a command and returns its combined output; nil: run (exec).
	Run func(ctx context.Context, argv ...string) ([]byte, error)

	mu      sync.Mutex
	applied bool
	max     int64
}

// Apply makes memoryMax WorkerSlice's ceiling, with `systemctl --user set-property`, which applies it to the
// running slice at once and keeps it in a drop-in for the next start (systemctl(1)). A ceiling already applied is
// not applied again; a failure is logged and tried again at the next call.
func (c *Ceiling) Apply(ctx context.Context, memoryMax int64) {
	if c == nil || !c.Limit.On() {
		return
	}

	memoryMax = max(memoryMax, 0)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.applied && c.max == memoryMax {
		return
	}

	run := c.Run
	if run == nil {
		run = runCommand
	}

	argv := append([]string{c.Limit.Systemctl, "--user", "set-property", nodeproto.WorkerSlice},
		SliceProperties(memoryMax)...)

	if out, err := run(ctx, argv...); err != nil {
		c.logger().WarnContext(ctx, "could not set the workers' memory ceiling",
			slog.String("event", "node.memory_limit_failed"), slog.Int64("memory_max", memoryMax),
			slog.String("error", strings.TrimSpace(err.Error()+": "+string(out))))

		return
	}

	c.applied, c.max = true, memoryMax

	c.logger().InfoContext(ctx, "set the workers' memory ceiling",
		slog.String("event", "node.memory_limit"), slog.String("slice", nodeproto.WorkerSlice),
		slog.Int64("memory_max", memoryMax))
}

func (c *Ceiling) logger() *slog.Logger {
	if c.Logger == nil {
		return slog.Default()
	}

	return c.Logger
}

func runCommand(ctx context.Context, argv ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // systemd's own tools, found on PATH
	cmd.WaitDelay = time.Second

	return cmd.CombinedOutput()
}
