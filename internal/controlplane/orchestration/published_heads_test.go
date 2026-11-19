package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishedHeadsRequireTheDispatchNodeAndEpoch(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	run, err := w.s.CreateRun(ctx, NewRun{Objective: "implementation", Repo: "owner/repo", CreatedBy: "owner"})
	require.NoError(t, err)
	otherRun := w.run
	w.run = run
	task := w.tasks(NewTask{Ref: "impl", Profile: "implementer", Spec: "implement"})[0]
	head := PublishedHead{Branch: "hyp/" + task.ID, SHA: "0123456789012345678901234567890123456789"}
	d := w.dispatch(task.ID)
	call := w.accept(d)
	require.ErrorIs(t, w.s.RecordPublishedHead(ctx, w.again(call), head), ErrConflict)
	require.NoError(t, w.s.ReportStarted(ctx, w.again(call), "pid:1", "sess_1"))
	wrongNode := w.again(call)
	wrongNode.NodeID = "another_node"
	require.ErrorIs(t, w.s.RecordPublishedHead(ctx, wrongNode, head), ErrConflict)
	stale := w.again(call)
	stale.Epoch--
	require.ErrorIs(t, w.s.RecordPublishedHead(ctx, stale, head), ErrConflict)
	wrongBranch := PublishedHead{Branch: "hyp/other", SHA: head.SHA}
	require.ErrorIs(t, w.s.RecordPublishedHead(ctx, w.again(call), wrongBranch), ErrConflict)
	require.ErrorIs(t, w.s.RecordEffect(ctx, w.again(call), Effect{
		Seq: 1, Kind: "git_push", Phase: EffectDone, Ref: head.Branch, Payload: head.SHA,
	}), ErrInvalid)
	before, err := w.s.ListPublishedHeads(ctx, run.ID)
	require.NoError(t, err)
	assert.Empty(t, before)

	require.NoError(t, w.s.RecordPublishedHead(ctx, w.again(call), head))
	require.NoError(t, w.s.RecordPublishedHead(ctx, w.again(call), head))
	saved, err := w.s.ListPublishedHeads(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, []PublishedHead{head}, saved)
	effects, err := w.s.ListEffects(ctx, d.ID)
	require.NoError(t, err)
	assert.Len(t, effects, 1)
	other, err := w.s.ListPublishedHeads(ctx, otherRun.ID)
	require.NoError(t, err)
	assert.Empty(t, other)
}
