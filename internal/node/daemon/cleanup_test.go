package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

func TestReconnectBackfillsOnlyConfirmedStoppedWorktrees(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	db, err := statedb.Open(t.Context(), filepath.Join(root, "state.db"), time.Now)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	run, task, dispatch := platform.NewID("run"), platform.NewID("tsk"), platform.NewID("dsp")
	worktrees := filepath.Join(root, "worktrees")
	worktree := filepath.Join(worktrees, run, task)
	unknown := filepath.Join(worktrees, platform.NewID("run"), platform.NewID("tsk"))
	require.NoError(t, os.MkdirAll(worktree, 0o700))
	require.NoError(t, os.MkdirAll(unknown, 0o700))
	require.NoError(t, os.Symlink(unknown, filepath.Join(filepath.Dir(worktree), platform.NewID("tsk"))))

	ctx := t.Context()
	stopped := false
	d := New(Config{WorktreeRoot: worktrees, Maintenance: &Maintenance{
		ShimRoot: filepath.Join(root, "run"),
		Stopped:  func(id string, _ bool) bool { return id == dispatch && stopped },
		CleanWorktree: func(_ context.Context, path string, _ bool, recheck func() bool) error {
			if recheck() {
				return os.Remove(path)
			}
			return nil
		},
		TrimCaches: func(context.Context, func(func())) {},
	}}, db, nil, slog.New(slog.DiscardHandler))

	for _, shouldStop := range []bool{false, true} {
		stopped = shouldStop
		conn, cancel := context.WithCancel(ctx)
		out, acks := make(chan nodeproto.Message, 1), make(chan nodeproto.CleanupAck, 1)
		var session sync.WaitGroup
		session.Go(func() { d.maintain(conn, out, acks) })
		t.Cleanup(func() {
			cancel()
			session.Wait()
		})
		select {
		case msg := <-out:
			assert.Equal(t, nodeproto.TypeCleanupCheck, msg.Type)
			var check nodeproto.CleanupCheck
			require.NoError(t, json.Unmarshal(msg.Payload, &check))
			assert.Len(t, check.Paths, 2, "a symlink is not a cleanup candidate")
			acks <- nodeproto.CleanupAck{ID: check.ID, Worktrees: []nodeproto.CleanupWorktree{{
				WorktreePath: nodeproto.WorktreePath{RunID: run, TaskID: task, Path: worktree},
				Dispatches:   []string{dispatch}, Accepted: []string{dispatch},
			}}}
			if shouldStop {
				assert.Eventually(t, func() bool {
					_, err := os.Lstat(worktree)
					return os.IsNotExist(err)
				}, 3*time.Second, 10*time.Millisecond)
			}
		case <-time.After(3 * time.Second):
			t.Error("no cleanup request after connecting")
		}
		cancel()
		session.Wait()
		if !shouldStop {
			assert.DirExists(t, worktree, "a still-running shim keeps its worktree")
		}
	}
	assert.DirExists(t, unknown)
}

func TestOfferMeasuresDiskBeforeJournalingAccept(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db, err := statedb.Open(ctx, filepath.Join(t.TempDir(), "state.db"), time.Now)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	const floor = 5 << 30
	free := int64(0)
	d := New(Config{Disk: func(context.Context) nodeproto.DiskSpace {
		return nodeproto.DiskSpace{FreeBytes: &free, FloorBytes: floor, TrimBytes: 15 << 30}
	}}, db, nil, slog.New(slog.DiscardHandler))
	low, recovered := platform.NewID("dsp"), platform.NewID("dsp")
	require.NoError(t, d.offered(ctx, nodeproto.Offer{DispatchID: low}))
	free = floor
	require.NoError(t, d.offered(ctx, nodeproto.Offer{DispatchID: recovered}))
	reports, err := db.Pending(ctx)
	require.NoError(t, err)
	assert.Len(t, reports, 2)
	for _, report := range reports {
		if report.DispatchID == low {
			assert.Equal(t, nodeproto.ReportLowDisk, report.Kind)
			require.NoError(t, d.acked(ctx, nodeproto.ReportAck{ReportID: report.ReportID, Status: nodeproto.AckCommitted}))
		} else {
			assert.Equal(t, nodeproto.ReportAccept, report.Kind)
		}
	}
	_, err = db.Get(ctx, low)
	require.ErrorIs(t, err, statedb.ErrNotFound)
}
