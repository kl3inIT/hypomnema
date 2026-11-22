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
	_, err := s.ReportNodeDisk(ctx, nodeID, disk)
	return err
}

// DiskAlertDebounce is how long after the owner was told a node is out of disk a new episode is not told: Coder's
// debounce of its out-of-disk notification (coderd/agentapi/api.go:183).
const DiskAlertDebounce = 30 * time.Minute

// The states of a node's out-of-disk alert (nodes.disk_alert): Coder's OK and NOK, NOK split by whether the owner
// was told, so that he is told of a recovery only when he was told of the episode.
const (
	diskAlertOK   = "ok"
	diskAlertLow  = "low"
	diskAlertTold = "told"
)

// DiskAlert is what the owner is to be told of a node's disk (issue #1017): it is still below its floor after
// trimming every cache it manages, or, after he was told so, it is at or above the floor again.
type DiskAlert struct {
	NodeID     string
	Node       string
	Recovered  bool
	FreeBytes  int64
	FloorBytes int64
}

// ReportNodeDisk keeps a node's heartbeat disk sample and moves its out-of-disk alert, in one write, as Coder's
// monitorVolumes moves a volume monitor (coderd/agentapi/resources_monitoring.go:213,248-252): a node that reports it
// is still below its floor after trimming (Blocked) goes from OK to NOK, and the owner is told when the debounce
// since the last such message has passed; a sample at or above the floor is OK again, and the owner is told the
// node recovered when he was told of the episode; any other sample leaves the state as it was. It returns what to
// tell, or nil.
func (s *Service) ReportNodeDisk(ctx context.Context, nodeID string, disk nodeproto.DiskSpace) (*DiskAlert, error) {
	var alert *DiskAlert

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		alert = nil
		if err := setNodeDisk(ctx, q, now, nodeID, disk); err != nil {
			return err
		}

		node, err := q.GetNode(ctx, nodeID)
		if err != nil {
			return notFound(err, "node", nodeID)
		}

		state, until := node.DiskAlert, node.DiskAlertUntil

		switch {
		case disk.AllowsWork():
			if state == diskAlertTold {
				alert = &DiskAlert{Recovered: true}
			}

			state = diskAlertOK
		case disk.Blocked && disk.FreeBytes != nil && state == diskAlertOK:
			state = diskAlertLow
			if now > until {
				state, until = diskAlertTold, now+DiskAlertDebounce.Milliseconds()
				alert = &DiskAlert{}
			}
		}

		if state == node.DiskAlert && until == node.DiskAlertUntil {
			return nil
		}

		if alert != nil {
			alert.NodeID, alert.Node, alert.FreeBytes, alert.FloorBytes = node.ID, node.Name, *disk.FreeBytes,
				disk.FloorBytes
		}

		return q.SetNodeDiskAlert(ctx, store.SetNodeDiskAlertParams{ID: nodeID, DiskAlert: state, DiskAlertUntil: until})
	})
	if err != nil {
		return nil, err
	}

	return alert, nil
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
		if node.UpdatedAt <= d.CreatedAt && (!node.DiskSampleAt.Valid || node.DiskSampleAt.Int64 < d.CreatedAt) {
			if err := setNodeDisk(ctx, q, now, call.NodeID, disk); err != nil {
				return err
			}
		}
		if err := q.MarkDispatchDeclinedLowDisk(ctx, d.ID); err != nil {
			return err
		}

		d.DeclinedLowDisk = 1
		if stopping {
			return s.settle(ctx, q, now, d, DispatchCanceled, FailureNone, nil, nil, true)
		}
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
