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

	"github.com/kl3inIT/hypomnema/internal/node/diskguard"
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
	alias := filepath.Join(root, "an-open-runs-custom-worktree")
	require.NoError(t, os.Symlink(worktree, alias))

	ctx := t.Context()
	stopped := false
	swept := make(chan struct{}, 1)
	d := New(Config{WorktreeRoot: worktrees, Maintenance: &Maintenance{
		ShimRoot: filepath.Join(root, "run"),
		Stopped:  func(id string, _ bool) bool { return id == dispatch && stopped },
		CleanWorktree: func(ctx context.Context, path string, _ bool, recheck func() bool) error {
			return diskguard.RemoveTree(ctx, path, recheck)
		},
		TrimCaches: func(context.Context, func(func())) { swept <- struct{}{} },
	}}, db, nil, slog.New(slog.DiscardHandler))

	for round := range 3 {
		stopped = round != 0
		protected := []string{}
		if round == 1 {
			protected = append(protected, alias)
		}
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
			acks <- nodeproto.CleanupAck{ID: check.ID, Protected: &protected, Worktrees: []nodeproto.CleanupWorktree{{
				WorktreePath: nodeproto.WorktreePath{RunID: run, TaskID: task, Path: worktree},
				Dispatches:   []string{dispatch}, Accepted: []string{dispatch},
			}}}
			if round == 2 {
				assert.Eventually(t, func() bool {
					_, err := os.Lstat(worktree)
					return os.IsNotExist(err)
				}, 3*time.Second, 10*time.Millisecond)
			}
		case <-time.After(3 * time.Second):
			t.Error("no cleanup request after connecting")
		}
		select {
		case msg := <-out:
			var check nodeproto.CleanupCheck
			require.NoError(t, json.Unmarshal(msg.Payload, &check))
			assert.Empty(t, check.Paths, "temp and cache pruning fetch the same protections")
			acks <- nodeproto.CleanupAck{ID: check.ID, Protected: &protected}
		case <-time.After(3 * time.Second):
			t.Error("no protection request for temp and caches")
		}
		select {
		case <-swept:
		case <-time.After(3 * time.Second):
			t.Error("cleanup sweep did not finish")
		}
		cancel()
		session.Wait()
		if round < 2 {
			assert.DirExists(t, worktree, "a live shim or an open Run's alias keeps the worktree")
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

type startProbeRunner struct {
	Runner
	started chan string
	stopped chan string
}

func (r startProbeRunner) Start(_ context.Context, dispatch statedb.Dispatch, _ Reporter) error {
	r.started <- dispatch.ID
	return nil
}

func (r startProbeRunner) Stop(id string) { r.stopped <- id }

func TestReplayedAcceptAndStopBeforeStart(t *testing.T) {
	t.Parallel()
	for _, stop := range []bool{false, true} {
		name := "replayed accept rechecks disk"
		if stop {
			name = "stop cancels start waiting for prune"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			db, err := statedb.Open(ctx, filepath.Join(t.TempDir(), "state.db"), time.Now)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, db.Close()) })
			id := platform.NewID("dsp")
			accept := nodeproto.Report{ReportID: platform.NewID("rep"), DispatchID: id, Kind: nodeproto.ReportAccept}
			_, err = db.AddOffer(ctx, nodeproto.Offer{DispatchID: id}, accept)
			require.NoError(t, err)
			runner := startProbeRunner{started: make(chan string, 1), stopped: make(chan string, 1)}
			free := int64(0)
			if stop {
				free = 5 << 30
			}
			d := New(Config{Disk: func(context.Context) nodeproto.DiskSpace {
				return nodeproto.DiskSpace{FreeBytes: &free, FloorBytes: 5 << 30, TrimBytes: 15 << 30}
			}}, db, runner, slog.New(slog.DiscardHandler))
			d.base = ctx
			d.diskWorkers.Lock()
			ack := nodeproto.ReportAck{ReportID: accept.ReportID, Status: nodeproto.AckCommitted,
				Response: mustJSON(nodeproto.Accepted{Epoch: 1})}
			err = d.acked(ctx, ack)
			if stop {
				d.stop(id)
			}
			d.diskWorkers.Unlock()
			require.NoError(t, err)
			d.starting.Wait()
			assert.Empty(t, runner.started)
			reports, err := db.Pending(ctx)
			require.NoError(t, err)
			require.Len(t, reports, 1)
			want := nodeproto.ReportLowDisk
			if stop {
				want = nodeproto.ReportStopped
			}
			assert.Equal(t, want, reports[0].Kind)
		})
	}
}

func TestBlockedDiskProbeDoesNotHoldNodeMessages(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db, err := statedb.Open(ctx, filepath.Join(t.TempDir(), "state.db"), time.Now)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	blocked, entered := make(chan struct{}), make(chan struct{}, 1)
	defer close(blocked)
	runner := startProbeRunner{stopped: make(chan string, 1)}
	d := New(Config{Disk: func(context.Context) nodeproto.DiskSpace {
		entered <- struct{}{}
		<-blocked
		return nodeproto.DiskSpace{FloorBytes: 5 << 30, TrimBytes: 5 << 30}
	}}, db, runner, slog.New(slog.DiscardHandler))
	disp := platform.NewID("dsp")
	offer, err := nodeproto.Encode(nodeproto.TypeOffer, nodeproto.Offer{DispatchID: disp})
	require.NoError(t, err)
	stop, err := nodeproto.Encode(nodeproto.TypeStop, nodeproto.Stop{DispatchID: disp})
	require.NoError(t, err)
	go func() {
		_ = d.handle(ctx, offer)
		_ = d.handle(ctx, stop)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no measurement")
	}
	select {
	case id := <-runner.stopped:
		assert.Equal(t, disp, id)
	case <-time.After(time.Second):
		t.Fatal("the disk measurement held up Stop")
	}
	require.Eventually(t, func() bool {
		reports, err := db.Pending(ctx)
		return err == nil && len(reports) == 1 && reports[0].Kind == nodeproto.ReportLowDisk
	}, 2*probeLimit, 20*time.Millisecond)
}
