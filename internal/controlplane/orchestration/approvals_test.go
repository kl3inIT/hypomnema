package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Approval scenarios, in the same style as questions_test.go.

func share(dispatchID, digest string) NewApproval {
	return NewApproval{
		Integration: "google", Label: "work", Action: "drive.share", ActionName: "Share a file",
		DispatchID: dispatchID, AskedBy: "worker", Title: "Share a file: Release notes", MatchHash: digest,
		Payload: []Field{{Label: "emailAddress", Value: "team@example.com"}, {Label: "role", Value: "commenter"}},
	}
}

// Scenario: a worker's request waits for the owner, who is away. The worker
// is told so and asks him, which puts its Task where he looks. Hours later he
// approves the request itself: the question the worker waits on is answered
// with what to do, the same request then passes once, and the next one asks
// again. The Approval names the worker's Task and Run.
func TestAnApprovalAnsweredLaterAnswersTheQuestionItsWorkerWaitsOn(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	task := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "hand the notes to the reviewers"})[0]
	d, c := w.start(task.ID)

	asked, granted, err := w.s.AskApproval(ctx, share(d.ID, "digest-1"))
	require.NoError(t, err)
	require.False(t, granted)
	assert.Equal(t, task.ID, asked.TaskID)
	assert.Equal(t, task.RunID, asked.RunID)
	assert.Equal(t, ApprovalPending, asked.Status)

	again, granted, err := w.s.AskApproval(ctx, share(d.ID, "digest-1"))
	require.NoError(t, err)
	require.False(t, granted)
	assert.Equal(t, asked.ID, again.ID, "the same request waits on the same Approval")

	question := w.ask(c, Ask{Text: "Please approve sharing the release notes with the team.", To: AddressOwner})

	decided, err := w.s.DecideApproval(ctx, asked.ID, true, ScopeOnce)
	require.NoError(t, err)

	told, err := w.s.TellAgentOfApproval(ctx, decided)
	require.NoError(t, err)
	require.True(t, told)

	answered := w.question(question)
	assert.Equal(t, QuestionAnswered, answered.Status)
	assert.Equal(t, AddressOwner, answered.AnsweredBy)
	require.NotNil(t, answered.Answer)
	assert.Contains(t, answered.Answer.Text, "approved")
	assert.Contains(t, answered.Answer.Text, asked.ID)
	assert.Contains(t, answered.Answer.Text, "Run the same command again")

	_, granted, err = w.s.AskApproval(ctx, share(d.ID, "digest-1"))
	require.NoError(t, err)
	assert.True(t, granted, "the request the owner approved passes")

	next, granted, err := w.s.AskApproval(ctx, share(d.ID, "digest-1"))
	require.NoError(t, err)
	assert.False(t, granted, "and passes once")
	assert.NotEqual(t, asked.ID, next.ID)

	// The owner rejects it; the worker waits on no question now and is told nothing here: its tool hears of it
	// when it asks again.
	rejected, err := w.s.DecideApproval(ctx, next.ID, false, "")
	require.NoError(t, err)

	told, err = w.s.TellAgentOfApproval(ctx, rejected)
	require.NoError(t, err)
	assert.False(t, told)

	_, err = w.s.DecideApproval(ctx, next.ID, true, ScopeOnce)
	require.ErrorIs(t, err, ErrConflict)
}

// Scenario: approved for the session, an action stops asking for the rest
// of its Run, for any request; another Run's worker still asks. An approval
// with no Run or chat behind it cannot be for a session.
func TestAnApprovalForTheSessionCoversItsRunOnly(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	task := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0]
	d, _ := w.start(task.ID)

	asked, _, err := w.s.AskApproval(ctx, share(d.ID, "digest-1"))
	require.NoError(t, err)

	_, err = w.s.DecideApproval(ctx, asked.ID, true, ScopeSession)
	require.NoError(t, err)

	_, granted, err := w.s.AskApproval(ctx, share(d.ID, "another-file"))
	require.NoError(t, err)
	assert.True(t, granted)

	other, granted, err := w.s.AskApproval(ctx, share("dsp_of_no_run", "digest-1"))
	require.NoError(t, err)
	assert.False(t, granted, "a request that is not of that Run asks")

	_, err = w.s.DecideApproval(ctx, other.ID, true, ScopeSession)
	require.ErrorIs(t, err, ErrInvalid)

	pending, err := w.s.ListApprovals(ctx, ApprovalFilter{Status: ApprovalPending})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, other.ID, pending[0].ID)

	ofTask, err := w.s.ListApprovals(ctx, ApprovalFilter{TaskID: task.ID})
	require.NoError(t, err)
	require.Len(t, ofTask, 1)
	assert.Equal(t, ScopeSession, ofTask[0].Scope)
}
