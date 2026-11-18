package orchestration

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

const ReasonNodesLowDisk = "nodes_low_disk"

type DiskStatus struct {
	nodeproto.DiskSpace
	At    *time.Time
	AgeMS *int64
}

func (s *Service) NodeDisk(ctx context.Context, nodeID string, disk nodeproto.DiskSpace) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		return setNodeDisk(ctx, q, now, nodeID, disk)
	})
}

func setNodeDisk(ctx context.Context, q *store.Queries, now int64, nodeID string, disk nodeproto.DiskSpace) error {
	if !disk.Valid() {
		return fmt.Errorf("%w: disk measurement out of range", ErrInvalid)
	}

	var free, at sql.NullInt64
	if disk.FreeBytes != nil {
		free, at = validInt(*disk.FreeBytes), validInt(now)
	}

	rows, err := q.SetNodeDisk(ctx, store.SetNodeDiskParams{
		ID: nodeID, DiskFreeBytes: free, DiskSampleAt: at,
		DiskFloorBytes: validInt(disk.FloorBytes), DiskTrimBytes: validInt(disk.TrimBytes),
	})

	return oneRow(rows, err, "node", nodeID)
}

func diskStatus(row store.Node, now int64) *DiskStatus {
	if row.DiskPolicy == 0 {
		return nil
	}

	status := &DiskStatus{DiskSpace: nodeproto.DiskSpace{
		FloorBytes: row.DiskFloorBytes.Int64, TrimBytes: row.DiskTrimBytes.Int64,
	}, At: optionalTime(row.DiskSampleAt)}
	if status.At != nil {
		age := max(now-row.DiskSampleAt.Int64, 0)
		status.AgeMS = &age
	}
	if status.At != nil && now >= row.DiskSampleAt.Int64 && now-row.DiskSampleAt.Int64 < NodeOfflineAfter.Milliseconds() &&
		row.DiskFreeBytes.Valid {
		status.FreeBytes = &row.DiskFreeBytes.Int64
	}

	return status
}

func (p *placement) lowDisk(id string) bool {
	if n := p.nodes[id]; n != nil {
		if disk := diskStatus(n.row, p.now); disk != nil {
			return !disk.AllowsWork()
		}
	}

	return false
}

func (p *placement) diskPreventsPlacement(runtime string, host where) bool {
	for _, n := range p.order {
		if n.row.WorktreeRoot.String != "" && p.checkNodeFit(n.row.ID, runtime, host) == nil &&
			!p.lowMemory(n) && p.lowDisk(n.row.ID) {
			return true
		}
	}

	return false
}

func (s *Service) DeclineLowDisk(ctx context.Context, call NodeCall, disk nodeproto.DiskSpace) error {
	if !disk.Valid() || disk.AllowsWork() {
		return fmt.Errorf("%w: expected a disk measurement below the floor or unavailable", ErrInvalid)
	}

	decline := func(q *store.Queries, now int64, d store.Dispatch) error {
		queued := DispatchStatus(d.Status) == DispatchQueued && call.Epoch == 0
		starting := DispatchStatus(d.Status) == DispatchStarting && call.Epoch == int(d.Epoch)
		stopping := DispatchStatus(d.Status) == DispatchStopping && call.Epoch == int(d.Epoch) && !d.StartedAt.Valid
		if !queued && !starting && !stopping {
			return fmt.Errorf("%w: dispatch %s is not queued", ErrConflict, d.ID)
		}

		node, err := q.GetNode(ctx, call.NodeID)
		if err != nil {
			return err
		}
		if !node.DiskSampleAt.Valid || node.DiskSampleAt.Int64 < d.CreatedAt {
			if err := setNodeDisk(ctx, q, now, call.NodeID, disk); err != nil {
				return err
			}
		}
		if stopping {
			return s.settle(ctx, q, now, d, DispatchCanceled, FailureNone, nil, nil, true)
		}
		if err := q.MarkDispatchDeclinedLowDisk(ctx, d.ID); err != nil {
			return err
		}

		d.DeclinedLowDisk = 1
		if err := s.settle(ctx, q, now, d, DispatchExpired, FailureNone, nil, nil, true); err != nil {
			return err
		}

		task, err := q.GetTask(ctx, d.TaskID)
		if err != nil || TaskStatus(task.Status) != TaskReady {
			return err
		}

		return s.wait(ctx, q, now, task, ReasonNodesLowDisk, &Scheduled{})
	}

	return nodeVoid(ctx, s, call, nodeproto.ReportLowDisk, disk, decline)
}
