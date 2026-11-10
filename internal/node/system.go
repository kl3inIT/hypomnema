package node

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// DetectSystem describes the machine for the node's hello: its platform, its
// CPUs, its memory and the size of the volume that holds worktreeRoot
// (gopsutil, as Nezha's and Beszel's agents). What cannot be read stays zero.
func DetectSystem(ctx context.Context, worktreeRoot string) nodeproto.SystemInfo {
	out := nodeproto.SystemInfo{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU()}

	if m, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		out.MemoryBytes = clampInt64(m.Total)
	}

	if d, err := disk.UsageWithContext(ctx, existingDir(worktreeRoot)); err == nil {
		out.DiskBytes = clampInt64(d.Total)
	}

	return out
}

// MeasureSystem is the machine's load for one heartbeat: CPU in use since the
// call before, memory in use, the worktree volume's use and the one-minute
// load average. What cannot be read stays zero; nil when nothing can.
func MeasureSystem(ctx context.Context, worktreeRoot string) *nodeproto.SystemStats {
	out := nodeproto.SystemStats{}
	read := false

	// An interval of zero compares with the call before, so it never blocks the heartbeat.
	if p, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(p) == 1 {
		out.CPUPercent = min(max(p[0], 0), 100)
		read = true
	}

	if m, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		out.MemoryUsedBytes = clampInt64(m.Used)
		read = true
	}

	if d, err := disk.UsageWithContext(ctx, existingDir(worktreeRoot)); err == nil {
		out.DiskUsedBytes = clampInt64(d.Used)
		read = true
	}

	if l, err := load.AvgWithContext(ctx); err == nil {
		out.Load1 = max(l.Load1, 0)
	}

	if !read {
		return nil
	}

	return &out
}

// existingDir is path, or its nearest parent that exists: on a new machine
// the worktree root is not made until the first Task, and its volume is the
// one its parent is on.
func existingDir(path string) string {
	for {
		if _, err := os.Stat(path); err == nil {
			return path
		}

		parent := filepath.Dir(path)
		if parent == path {
			return path
		}

		path = parent
	}
}

func clampInt64(v uint64) int64 {
	const maxInt64 = 1<<63 - 1
	if v > maxInt64 {
		return maxInt64
	}

	return int64(v)
}
