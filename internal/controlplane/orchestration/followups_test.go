package orchestration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: a reviewer found a gap in the pull request of a Task that
// succeeded, and the coordinator sends the fix to the worker that wrote it
// (issue #666). The Task is ready again with its failure count reset; the
// next Dispatch continues the same Conversation, in the same worktree and CLI
// session, and its worker is given the note. The Task succeeds again, and a
// second follow-up is kept after the first and reaches only the next
// Dispatch.
func TestFollowUpContinuesTheWorkersConversation(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "add the login button"})[0]
	w.failOnce(task.ID, FailureWorkerError)
	w.advance(BackoffMax * 2)

	d, c := w.start(task.ID)
	require.NoError(t, w.finish(c, success("PR #7 adds the button")))
	assert.Equal(t, 1, w.task(task.ID).FailureCount)

	first, err := w.s.FollowUpTask(t.Context(), task.ID, AddressCoordinator,
		"  The review found: no test for the button.\n")
	require.NoError(t, err)
	assert.Equal(t, "The review found: no test for the button.", first.Note)
	assert.Equal(t, AddressCoordinator, first.AskedBy)

	got := w.task(task.ID)
	assert.Equal(t, TaskReady, got.Status)
	assert.Zero(t, got.FailureCount, "the follow-up is new work with its own count")

	next, err := w.create(task.ID, DispatchSpec{Continue: true})
	require.NoError(t, err)
	assert.Equal(t, d.ConversationID, next.ConversationID)
	assert.Equal(t, first.ID, next.FollowUpID)
	assert.Equal(t, "/srv/wt/a", w.conversation(next.ConversationID).Worktree)

	acc, err := w.s.AcceptDispatch(t.Context(), w.call(next, 0))
	require.NoError(t, err)
	assert.Equal(t, first.Note, acc.FollowUp)
	assert.Equal(t, "sess_1", acc.CLISessionRef, "the worker's own session goes on")

	nc := w.call(next, acc.Epoch)
	require.NoError(t, w.s.ReportStarted(t.Context(), nc, "pid:43@2", "sess_1"))
	require.NoError(t, w.finish(nc, success("PR #7 now has the test")))
	assert.Equal(t, TaskSucceeded, w.task(task.ID).Status)

	second, err := w.s.FollowUpTask(t.Context(), task.ID, AddressOwner, "Rename the button to Sign in.")
	require.NoError(t, err)

	third, err := w.create(task.ID, DispatchSpec{Continue: true})
	require.NoError(t, err)
	assert.Equal(t, second.ID, third.FollowUpID, "a follow-up that was done is not given again")

	all, err := w.s.ListFollowUps(t.Context(), task.ID)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, []string{first.ID, second.ID}, []string{all[0].ID, all[1].ID})
	assert.Equal(t, AddressOwner, all[1].AskedBy)
	assert.Contains(t, w.events(0, EventFilter{TaskID: task.ID}), "task.status add the login button succeeded>ready")
}

// Scenario: the worker given a follow-up is lost, and the retry opens a new
// Conversation. Its worker is given the follow-up too, so the retry does not
// redo the first spec only and report it done.
func TestFollowUpReachesEveryDispatchUntilOneSucceeds(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0]
	w.succeed(task.ID, "done")

	f, err := w.s.FollowUpTask(t.Context(), task.ID, AddressCoordinator, "also update the README")
	require.NoError(t, err)

	lost, err := w.create(task.ID, DispatchSpec{Continue: true})
	require.NoError(t, err)
	c := w.accept(lost)
	require.NoError(t, w.s.ReportStarted(t.Context(), w.again(c), "pid:43@2", "sess_1"))
	w.loseLease()
	w.connect(nil)
	require.Equal(t, ConversationClosed, w.conversation(lost.ConversationID).Status)

	w.advance(BackoffMax * 2)
	retry := w.dispatch(task.ID)
	assert.NotEqual(t, lost.ConversationID, retry.ConversationID)
	assert.Equal(t, f.ID, retry.FollowUpID)
	assert.True(t, retry.Recovery)
}

// Scenario: a follow-up is refused where there is no finished worker to
// continue: a Task still running, one that failed (reopen is for that), one
// whose worker's Conversation was closed (its context is gone: the error
// names the branch a new Task starts from), and a Task of a finished Run. A
// note that is empty or too long is refused before anything is read.
func TestFollowUpIsRefusedWithoutAFinishedWorker(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(
		NewTask{Ref: "running", Profile: "implementer", Spec: "a"},
		NewTask{Ref: "failed", Profile: "implementer", Spec: "b"},
		NewTask{Ref: "closed", Profile: "implementer", Spec: "c"},
	)
	running, failed, closed := tasks[0].ID, tasks[1].ID, tasks[2].ID

	w.failForGood(failed)

	d, c := w.start(closed)
	require.NoError(t, w.finish(c, success("PR #9")))
	w.closeConversation(d.ConversationID)
	require.Equal(t, TaskSucceeded, w.task(closed).Status)

	w.start(running)

	for _, id := range []string{running, failed} {
		_, err := w.s.FollowUpTask(t.Context(), id, AddressCoordinator, "more")
		require.ErrorIs(t, err, ErrConflict, id)
	}

	_, err := w.s.FollowUpTask(t.Context(), closed, AddressCoordinator, "more")
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorContains(t, err, `"continues": "`+closed)

	_, err = w.s.FollowUpTask(t.Context(), closed, AddressCoordinator, " \n")
	require.ErrorIs(t, err, ErrInvalid)
	_, err = w.s.FollowUpTask(t.Context(), closed, AddressCoordinator, strings.Repeat("x", MaxFollowUpText+1))
	require.ErrorIs(t, err, ErrInvalid)

	done := newWorld(t)
	task := done.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0]
	done.succeed(task.ID, "done")
	require.NoError(t, done.s.FinishRun(t.Context(), done.run.ID))
	_, err = done.s.FollowUpTask(t.Context(), task.ID, AddressCoordinator, "more")
	require.ErrorIs(t, err, ErrConflict, "a finished Run takes no more work")

	all, err := w.s.ListFollowUps(t.Context(), closed)
	require.NoError(t, err)
	assert.Empty(t, all, "a refused follow-up keeps nothing")
}

// Scenario: the worker ended at a Gate ("Merge it?"), and the review's
// findings arrive before the owner answers (issue #714, decision 9). The
// follow-up replaces the Gate: the Gate is closed as superseded, the Task is
// ready, and the next Dispatch continues the same Conversation with the note
// and without the Gate. A Task blocked for another reason is still refused.
func TestFollowUpReplacesAnOpenGate(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(
		NewTask{Ref: "a", Profile: "implementer", Spec: "a"},
		NewTask{Ref: "b", Profile: "implementer", Spec: "b"},
		NewTask{Ref: "c", Profile: "implementer", Spec: "c", DependsOn: []string{"b"}},
	)
	task := tasks[0]

	d, c := w.start(task.ID)
	gate := mergeGate()
	require.NoError(t, w.finish(c, Outcome{Gate: &gate, Result: Result{Summary: "PR #7 is ready"}}))
	require.Equal(t, "blocked:gate", w.statuses()["a"])

	f, err := w.s.FollowUpTask(t.Context(), task.ID, AddressCoordinator, "The review found: no test for the button.")
	require.NoError(t, err)

	got := w.task(task.ID)
	assert.Equal(t, TaskReady, got.Status)
	assert.Equal(t, BlockedNone, got.BlockedReason)

	qs, err := w.s.ListQuestions(t.Context(), QuestionFilter{TaskID: task.ID})
	require.NoError(t, err)
	require.Len(t, qs, 1)
	assert.Equal(t, QuestionCanceled, qs[0].Status)
	assert.Equal(t, "superseded", qs[0].CancelReason)

	_, err = w.s.AnswerQuestion(t.Context(), qs[0].ID, AddressOwner, Answer{Option: choice(0)})
	require.ErrorIs(t, err, ErrConflict, "a replaced gate takes no answer")

	next, err := w.create(task.ID, DispatchSpec{Continue: true})
	require.NoError(t, err)
	assert.Equal(t, d.ConversationID, next.ConversationID)
	assert.Equal(t, f.ID, next.FollowUpID)
	assert.Empty(t, next.GateQuestionID, "the replaced gate is not resumed from")

	acc, err := w.s.AcceptDispatch(t.Context(), w.call(next, 0))
	require.NoError(t, err)
	assert.Equal(t, f.Note, acc.FollowUp)
	assert.Nil(t, acc.Gate)

	evs, err := w.s.ListEvents(t.Context(), 0, EventFilter{TaskID: task.ID}, EventPageMax)
	require.NoError(t, err)

	var canceled []string

	for _, e := range evs.Events {
		if e.Kind == EventQuestionCanceled {
			canceled = append(canceled, string(e.Payload))
		}
	}

	require.Len(t, canceled, 1)
	assert.Contains(t, canceled[0], `"reason":"superseded"`)
	assert.Contains(t, canceled[0], `"follow_up":"`+f.ID+`"`)

	w.failForGood(tasks[1].ID)
	require.Equal(t, "blocked:dependency_failed", w.statuses()["c"])

	_, err = w.s.FollowUpTask(t.Context(), tasks[2].ID, AddressCoordinator, "more")
	require.ErrorIs(t, err, ErrConflict, "only a gate is replaced")
}

// closeConversation closes a Conversation as an interrupted Dispatch leaves it, for a Task whose worker is gone.
func (w *world) closeConversation(id string) {
	w.t.Helper()

	_, err := w.db.ExecContext(w.t.Context(),
		`UPDATE conversations SET status = 'closed', close_reason = 'interrupted' WHERE id = ?`, id)
	require.NoError(w.t, err)
}
