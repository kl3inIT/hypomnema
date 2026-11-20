package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// offerOfTask is the offer a node is sent for the Task's queued Dispatch.
func (w *world) offerOfTask(taskID string) Offer {
	w.t.Helper()

	d := w.dispatch(taskID)

	pending, err := w.s.NodePending(w.t.Context(), w.node.ID)
	require.NoError(w.t, err)

	for _, o := range pending.Offers {
		if o.DispatchID == d.ID {
			return o
		}
	}

	w.t.Fatalf("no offer for dispatch %s", d.ID)

	return Offer{}
}

// Scenario (issue #690, case a): pull request #873's author Task was canceled in a Run that has since finished, so it
// takes no follow-up. A Task of a new Run of the same repository continues it: it starts from and pushes to that
// Task's branch, so the pull request keeps its number and reviews. While it is unfinished no other Task may push to
// that branch, nor one of another repository continue it; its push is recorded for its own Run.
func TestATaskContinuesTheBranchOfATaskOfAFinishedRun(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	old, err := w.runOn("kl3inIT/hypomnema", "")
	require.NoError(t, err)
	w.run = old
	earlier := w.tasks(NewTask{Ref: "impl", Spec: "implement #867"})[0]
	require.NoError(t, w.s.CancelTask(ctx, earlier.ID))
	require.NoError(t, w.s.FinishRun(ctx, old.ID))

	fresh, err := w.runOn("kl3inIT/hypomnema", "")
	require.NoError(t, err)
	w.run = fresh
	branch := "hyp/" + earlier.ID

	fix := w.tasks(NewTask{Ref: "fix", Spec: "fix the review's findings", Continues: earlier.ID})[0]
	assert.Equal(t, [4]string{earlier.ID, branch, branch, ""}, [4]string{fix.Continues, fix.PushBranch, fix.StartFrom,
		fix.PRBase}, "it starts from and pushes to the earlier Task's branch; a new pull request would target the base")

	_, err = w.s.CreateTasks(ctx, fresh.ID, []NewTask{{Ref: "again", Spec: "x", Continues: earlier.ID}})
	require.ErrorIs(t, err, ErrConflict, "two unfinished Tasks would push to one branch")
	require.ErrorContains(t, err, fix.ID)
	_, err = w.s.CreateTasks(ctx, fresh.ID, []NewTask{{Ref: "chain", Spec: "x", Continues: fix.ID}})
	require.ErrorIs(t, err, ErrConflict, "a Task that continues fix pushes to the same branch")

	other, err := w.runOn("kl3inIT/vadan", "")
	require.NoError(t, err)
	_, err = w.s.CreateTasks(ctx, other.ID, []NewTask{{Ref: "x", Spec: "x", Continues: earlier.ID}})
	require.ErrorIs(t, err, ErrConflict, "another repository's branch")
	_, err = w.s.CreateTasks(ctx, fresh.ID, []NewTask{{Ref: "x", Spec: "x", Continues: "tsk_none"}})
	require.ErrorIs(t, err, ErrNotFound)
	_, err = w.s.CreateTasks(ctx, fresh.ID, []NewTask{{Ref: "x", Spec: "x", Continues: earlier.ID, StartFrom: "main"}})
	require.ErrorIs(t, err, ErrInvalid, "a continued branch is where it starts")
	_, err = w.s.CreateTasks(ctx, w.runNoRepo().ID, []NewTask{{Ref: "x", Spec: "x", Continues: earlier.ID}})
	require.ErrorIs(t, err, ErrInvalid, "a Run without a repository has no branch")

	offer := w.offerOfTask(fix.ID)
	assert.Equal(t, [3]string{branch, branch, ""}, [3]string{offer.PushBranch, offer.StartFrom, offer.PRBase})
	assert.Equal(t, DefaultBase, offer.Base)

	call := w.accept(w.dispatchOf(offer.DispatchID))
	require.NoError(t, w.s.ReportStarted(ctx, w.again(call), "pid:1", "sess_1"))
	sha := "0123456789012345678901234567890123456789"
	require.ErrorIs(t, w.s.RecordPublishedHead(ctx, w.again(call), PublishedHead{Branch: "hyp/other", SHA: sha}),
		ErrConflict)
	require.NoError(t, w.s.RecordPublishedHead(ctx, w.again(call), PublishedHead{Branch: branch, SHA: sha}))
	heads, err := w.s.ListPublishedHeads(ctx, fresh.ID)
	require.NoError(t, err)
	assert.Equal(t, []PublishedHead{{Branch: branch, SHA: sha}}, heads,
		"the push is this Run's evidence for the pull request it updated")
	require.NoError(t, w.finish(call, success("pushed to #873")))

	next := w.tasks(NewTask{Ref: "next", Spec: "one more round", Continues: fix.ID})[0]
	assert.Equal(t, branch, next.PushBranch, "once fix is done, a Task that continues it pushes to the same branch")
}

// runNoRepo is a Run with no repository.
func (w *world) runNoRepo() Run {
	w.t.Helper()

	return newRun(w.t, w.s)
}

// Scenario (issue #690, case b): stacked work. A Task starts from its parent's branch and opens its pull request
// against it, or against the Run's base when the coordinator says so; a named branch works the same. A branch name
// git would refuse, or one read as an option, never reaches a node.
func TestAStackedTaskStartsFromItsParentsBranch(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	run, err := w.runOn("kl3inIT/hypomnema", "")
	require.NoError(t, err)
	w.run = run

	parent := w.tasks(NewTask{Ref: "api", Spec: "the endpoint"})[0]
	branch := "hyp/" + parent.ID

	created := w.tasks(
		NewTask{Ref: "stacked", Spec: "the page", StartFrom: branch, DependsOn: []string{parent.ID}},
		NewTask{Ref: "on-main", Spec: "the docs", StartFrom: branch, PRBase: PRBaseRunBase},
		NewTask{Ref: "release", Spec: "the backport", StartFrom: "release/2.0", PRBase: PRBaseStartFrom},
		NewTask{Ref: "base", Spec: "plain", StartFrom: DefaultBase},
	)
	stacked, onMain, release, plain := created[0], created[1], created[2], created[3]

	assert.Equal(t, [3]string{"", branch, branch}, [3]string{stacked.PushBranch, stacked.StartFrom, stacked.PRBase})
	assert.Equal(t, [2]string{branch, ""}, [2]string{onMain.StartFrom, onMain.PRBase})
	assert.Equal(t, [2]string{"release/2.0", "release/2.0"}, [2]string{release.StartFrom, release.PRBase})
	assert.Equal(t, [2]string{"", ""}, [2]string{plain.StartFrom, plain.PRBase}, "the Run's base is the default")

	for _, bad := range []NewTask{
		{StartFrom: "-c"}, {StartFrom: "a..b"}, {StartFrom: "a b"}, {StartFrom: "x.lock"}, {StartFrom: "a/"},
		{StartFrom: "a:b"}, {StartFrom: ".hidden"}, {StartFrom: "a@{1}"},
		{PRBase: PRBaseRunBase}, {StartFrom: "main2", PRBase: "elsewhere"},
	} {
		bad.Ref, bad.Spec = "bad", "x"
		_, err := w.s.CreateTasks(ctx, run.ID, []NewTask{bad})
		require.ErrorIs(t, err, ErrInvalid, "%+v", bad)
	}

	_, err = w.s.CreateTasks(ctx, run.ID, []NewTask{{Ref: "x", Spec: "x", StartFrom: "hyp/tsk_none"}})
	require.ErrorIs(t, err, ErrNotFound, "hyp/ names a Task's branch")

	// The parent is done; a fix Task continues its branch, and a Task stacked on the parent's branch then starts
	// from the branch the parent's pull request is on.
	w.succeed(parent.ID, "PR #1")
	fix := w.tasks(NewTask{Ref: "fix", Spec: "fix", Continues: parent.ID})[0]
	child := w.tasks(NewTask{Ref: "child", Spec: "more", StartFrom: "hyp/" + fix.ID})[0]
	assert.Equal(t, branch, child.StartFrom)

	offer := w.offerOfTask(onMain.ID)
	assert.Equal(t, [3]string{"", branch, ""}, [3]string{offer.PushBranch, offer.StartFrom, offer.PRBase})
}

// Scenario (issue #690): while a Task continues another's branch, the other's worker takes no follow-up and a failed
// one is not reopened: both would push to the same branch at once.
func TestTheTaskWhoseBranchIsContinuedWaits(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	run, err := w.runOn("kl3inIT/hypomnema", "")
	require.NoError(t, err)
	w.run = run

	tasks := w.tasks(NewTask{Ref: "done", Spec: "a"}, NewTask{Ref: "failed", Spec: "b"})
	done, failed := tasks[0].ID, tasks[1].ID
	w.succeed(done, "PR #1")
	w.failForGood(failed)

	w.tasks(NewTask{Ref: "fix-done", Spec: "x", Continues: done}, NewTask{Ref: "fix-failed", Spec: "y", Continues: failed})

	_, err = w.s.FollowUpTask(ctx, done, AddressCoordinator, "more")
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorContains(t, err, "hyp/"+done)
	require.ErrorIs(t, w.s.ReopenTask(ctx, failed), ErrConflict)
}
