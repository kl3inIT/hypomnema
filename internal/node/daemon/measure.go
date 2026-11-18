package daemon

import (
	"context"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

const probeLimit = 2 * time.Second

func boundedProbe[T any](ctx context.Context, slot chan struct{}, probe func(context.Context) T, missing T) T {
	ctx, cancel := context.WithTimeout(ctx, probeLimit)
	defer cancel()
	select {
	case slot <- struct{}{}:
	case <-ctx.Done():
		return missing
	}
	result := make(chan T, 1)
	go func() {
		defer func() { <-slot }()
		result <- probe(ctx)
	}()
	select {
	case value := <-result:
		if ctx.Err() != nil {
			return missing
		}
		return value
	case <-ctx.Done():
		return missing
	}
}

func (d *Daemon) measureDisk(ctx context.Context) nodeproto.DiskSpace {
	floor := d.cfg.DiskFloor
	if floor <= 0 {
		floor = 5 << 30
	}
	return boundedProbe(ctx, d.diskProbe, d.cfg.Disk, nodeproto.DiskSpace{FloorBytes: floor, TrimBytes: floor})
}

func (d *Daemon) measureStats(ctx context.Context) *nodeproto.SystemStats {
	if d.cfg.Stats == nil {
		return nil
	}
	return boundedProbe(ctx, d.statsProbe, d.cfg.Stats, nil)
}

func (d *Daemon) heartbeatAsync(ctx context.Context) {
	go func() {
		msg := d.heartbeat(ctx)
		if ctx.Err() == nil {
			d.send(msg)
		}
	}()
}
