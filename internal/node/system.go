package node

import (
	"context"
	"runtime"

	"github.com/shirou/gopsutil/v4/disk"
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

	if d, err := disk.UsageWithContext(ctx, worktreeRoot); err == nil {
		out.DiskBytes = clampInt64(d.Total)
	}

	return out
}

func clampInt64(v uint64) int64 {
	const maxInt64 = 1<<63 - 1
	if v > maxInt64 {
		return maxInt64
	}

	return int64(v)
}
