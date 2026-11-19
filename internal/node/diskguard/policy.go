package diskguard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/shirou/gopsutil/v4/disk"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

type Policy struct {
	FloorBytes      int64 `json:"floor_bytes"`
	TrimPercent     int   `json:"trim_percent"`
	TargetPercent   int   `json:"target_percent"`
	GoCacheMaxBytes int64 `json:"go_cache_max_bytes"`
	TempMaxAgeHours int   `json:"temp_max_age_hours"`
}

func DefaultPolicy() Policy {
	return Policy{FloorBytes: 5 << 30, TrimPercent: 15, TargetPercent: 20, GoCacheMaxBytes: 8 << 30, TempMaxAgeHours: 6}
}

func (p Policy) Validate() error {
	if p.FloorBytes <= 0 || p.TrimPercent <= 0 || p.TargetPercent <= p.TrimPercent || p.TargetPercent > 100 {
		return errors.New("disk policy needs floor_bytes > 0 and 0 < trim_percent < target_percent <= 100")
	}
	if p.GoCacheMaxBytes <= 0 || p.TempMaxAgeHours <= 0 || p.TempMaxAgeHours > int((1<<63-1)/time.Hour) {
		return errors.New("disk policy needs go_cache_max_bytes > 0 and a positive, representable temp_max_age_hours")
	}

	return nil
}

func (p Policy) Measure(ctx context.Context, path string) (nodeproto.DiskSpace, int64) {
	space := nodeproto.DiskSpace{FloorBytes: p.FloorBytes, TrimBytes: p.FloorBytes}
	usage, err := disk.UsageWithContext(ctx, ExistingDir(path))
	if err != nil || usage.Total == 0 {
		return space, p.FloorBytes
	}

	total := int64(min(usage.Total, 1<<63-1))
	free := int64(min(usage.Free, usage.Total, 1<<63-1))
	space.FreeBytes = &free
	space.SampleAt = time.Now().UnixMilli()
	space.TrimBytes = max(p.FloorBytes, share(total, p.TrimPercent))

	return space, max(p.FloorBytes, share(total, p.TargetPercent))
}

func share(total int64, percent int) int64 {
	return total/100*int64(percent) + (total%100*int64(percent)+99)/100
}

func ExistingDir(path string) string {
	for {
		_, err := os.Stat(path)
		if !errors.Is(err, os.ErrNotExist) {
			return path
		}

		parent := filepath.Dir(path)
		if parent == path {
			return path
		}

		path = parent
	}
}
