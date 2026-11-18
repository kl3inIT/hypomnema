package orchestration

import (
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

const testDiskFloor int64 = 5 << 30

func (w *world) diskSample(free int64) nodeproto.DiskSpace {
	return nodeproto.DiskSpace{
		FreeBytes: &free, FloorBytes: testDiskFloor, TrimBytes: 15 << 30, SampleAt: w.clock.now.UnixMilli(),
	}
}

func TestDiskAdmissionAndRecovery(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	task := w.tasks(NewTask{Ref: "disk", Spec: "place only with a fresh disk sample"})[0]
	hello := Hello{
		WorktreeRoot: "/srv/wt", DiskPolicy: true, Runtimes: []RuntimeInfo{{Runtime: "claude", Path: "/bin/claude"}},
	}
	require.NoError(t, w.s.NodeHello(ctx, w.node.ID, hello))

	for _, free := range []int64{-1, 0, testDiskFloor} {
		if free >= 0 {
			require.NoError(t, w.s.NodeDisk(ctx, w.node.ID, w.diskSample(free)))
		}
		if free == testDiskFloor {
			w.advance(NodeOfflineAfter)
			require.NoError(t, w.s.NodeHeartbeat(ctx, w.node.ID))
		}
		out, err := w.s.Schedule(ctx)
		require.NoError(t, err)
		assert.Empty(t, out.Created)
		assert.Equal(t, ReasonNodesLowDisk, w.task(task.ID).WaitingReason)
		_, err = w.create(task.ID, w.spec())
		require.ErrorContains(t, err, ReasonNodesLowDisk)
		machine, err := w.s.GetNode(ctx, w.node.ID)
		require.NoError(t, err)
		assert.True(t, machine.Connected)
		require.NotNil(t, machine.Disk)
		if free == 0 {
			require.NotNil(t, machine.Disk.FreeBytes)
			assert.Zero(t, *machine.Disk.FreeBytes)
		} else {
			assert.Nil(t, machine.Disk.FreeBytes)
		}
	}

	require.NoError(t, w.s.NodeDisk(ctx, w.node.ID, w.diskSample(testDiskFloor)))
	out, err := w.s.Schedule(ctx)
	require.NoError(t, err)
	require.Len(t, out.Created, 1, "exactly at the floor can take work")
	dispatch := w.dispatchOf(out.Created[0])
	decline := w.call(dispatch, 0)
	require.NoError(t, w.s.DeclineLowDisk(ctx, decline, w.diskSample(0)))
	require.NoError(t, w.s.DeclineLowDisk(ctx, decline, w.diskSample(0)))
	assert.Equal(t, TaskReady, w.task(task.ID).Status)
	assert.Zero(t, w.task(task.ID).FailureCount)
	assert.Equal(t, ReasonNodesLowDisk, w.task(task.ID).WaitingReason)
	assert.Equal(t, DispatchExpired, w.dispatchOf(dispatch.ID).Status)
	_, err = w.s.AcceptDispatch(ctx, w.call(dispatch, 0))
	require.ErrorIs(t, err, ErrConflict)

	require.NoError(t, w.s.NodeDisk(ctx, w.node.ID, w.diskSample(testDiskFloor)))
	retry, err := w.s.Schedule(ctx)
	require.NoError(t, err)
	require.Len(t, retry.Created, 1)
	assert.Empty(t, w.task(task.ID).WaitingReason)
}

func TestWorktreeCleanupRequiresClosedRunAndSettledDispatches(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.s.newID = platform.NewID
	w.run = newRun(t, w.s)
	ctx := t.Context()
	task := w.tasks(NewTask{Ref: "disk", Spec: "keep my worktree for follow-ups"})[0]
	tree := nodeproto.WorktreePath{RunID: w.run.ID, TaskID: task.ID, Path: path.Join("/srv/wt", w.run.ID, task.ID)}
	unknown := nodeproto.WorktreePath{RunID: w.run.ID, TaskID: platform.NewID("tsk")}
	unknown.Path = path.Join("/srv/wt", unknown.RunID, unknown.TaskID)
	check := func() []nodeproto.CleanupWorktree {
		t.Helper()
		approved, err := w.s.CheckWorktreeCleanup(ctx, w.node.ID, []nodeproto.WorktreePath{tree, unknown})
		require.NoError(t, err)
		return approved
	}

	first, err := w.s.Schedule(ctx)
	require.NoError(t, err)
	require.Len(t, first.Created, 1)
	dispatch := w.dispatchOf(first.Created[0])
	call := w.accept(dispatch)
	require.NoError(t, w.s.ReportStarted(ctx, w.again(call), "pid:123", "session"))
	require.NoError(t, w.finish(call, success("ready for review")))
	assert.Empty(t, check(), "a succeeded Task in an open Run can receive a follow-up")
	_, err = w.s.FollowUpTask(ctx, task.ID, AddressOwner, "add the tests")
	require.NoError(t, err)
	require.NoError(t, w.s.NodeDisk(ctx, w.node.ID, w.diskSample(0)))
	_, err = w.create(task.ID, DispatchSpec{Continue: true})
	require.ErrorContains(t, err, ReasonNodesLowDisk)
	waiting, err := w.s.Schedule(ctx)
	require.NoError(t, err)
	assert.Empty(t, waiting.Created)
	assert.Equal(t, ReasonNodesLowDisk, w.task(task.ID).WaitingReason)

	require.NoError(t, w.s.NodeDisk(ctx, w.node.ID, w.diskSample(testDiskFloor)))
	next, err := w.s.Schedule(ctx)
	require.NoError(t, err)
	require.Len(t, next.Created, 1)
	nextDispatch := w.dispatchOf(next.Created[0])
	assert.Equal(t, dispatch.ConversationID, nextDispatch.ConversationID)
	nextCall := w.accept(nextDispatch)
	require.NoError(t, w.s.CancelRun(ctx, w.run.ID))
	assert.Empty(t, check(), "canceling the Run has not yet stopped the Dispatch")
	require.NoError(t, w.s.ReportStopped(ctx, w.again(nextCall)))
	approved := check()
	require.Len(t, approved, 1, "the unknown path is never approved")
	assert.Equal(t, tree, approved[0].WorktreePath)
	assert.ElementsMatch(t, []string{dispatch.ID, nextDispatch.ID}, approved[0].Accepted)
}
