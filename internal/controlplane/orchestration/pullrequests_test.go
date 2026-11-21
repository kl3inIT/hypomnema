package orchestration

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// Scenario (issue #1053): a worker turned auto-merge on and finished, saying so, without waiting for CI. Its pull
// request goes red after the Task succeeded: the control plane's pass reads it, sends the author a follow-up
// naming the failed check, and wakes the coordinator once. While it is pending nothing happens. The worker
// fixes it and finishes again with auto-merge on at a new head; the pass follows that report too, and once the
// pull request merged the coordinator is woken to finish the Run. A read that fails leaves it for the next pass,
// and a settled report is not read again.
func TestAPullRequestThatGoesRedAfterItsTaskSucceededSendsItsAuthorAFollowUp(t *testing.T) {
	t.Parallel()

	const pr = "https://github.com/kl3inIT/hypomnema/pull/1054"

	w := newWorld(t)
	ctx := t.Context()
	chat, err := w.s.CreateChat(ctx, "", "Ship the fix")
	require.NoError(t, err)
	w.run, err = w.s.CreateRun(ctx, NewRun{Objective: "Ship the fix", CreatedBy: "pi", ChatID: chat.ID})
	require.NoError(t, err)

	task := w.tasks(NewTask{Ref: "fix", Profile: "implementer", Spec: "fix the bug"})[0]
	_, c := w.start(task.ID)
	require.NoError(t, w.finish(c, withPR(success("Done; auto-merge on at `b5abccde`."), pr)))
	_, err = w.s.WakeChats(ctx)
	require.NoError(t, err)
	require.NoError(t, w.s.MarkDelivered(ctx, chat.ID, 1<<40))

	var reads []string

	state := githost.RequestState{Status: githost.RequestOpen, Head: "b5abccde5aee"}
	read := func(_ context.Context, f FollowedRequest) (githost.RequestState, error) {
		reads = append(reads, f.URL+"@"+f.Head)

		return state, nil
	}

	require.NoError(t, w.s.FollowPullRequests(ctx, read))
	assert.Equal(t, []string{pr + "@b5abccde"}, reads)
	assert.Equal(t, TaskSucceeded, w.task(task.ID).Status, "pending: nothing is asked")

	failing := func(context.Context, FollowedRequest) (githost.RequestState, error) {
		return githost.RequestState{}, errors.New("GitHub answered HTTP 502")
	}
	require.ErrorContains(t, w.s.FollowPullRequests(ctx, failing), "HTTP 502")
	assert.Equal(t, TaskSucceeded, w.task(task.ID).Status)

	state.Failed = []string{"Required"}
	require.NoError(t, w.s.FollowPullRequests(ctx, read))

	ups, err := w.s.ListFollowUps(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, ups, 1, "the author is sent a follow-up")
	assert.Equal(t, AddressCoordinator, ups[0].AskedBy)
	assert.Contains(t, ups[0].Note, "The required checks of your pull request "+pr+" failed at b5abccde5aee, "+
		"after you finished with auto-merge on: Required.")
	assert.Contains(t, ups[0].Note, "turn auto-merge on again at the new head")
	assert.Equal(t, TaskReady, w.task(task.ID).Status)

	follow, err := w.s.PullRequestsToFollow(ctx)
	require.NoError(t, err)
	assert.Empty(t, follow, "a Task at work is not followed")

	wakes := undeliveredWakes(t, w, chat.ID)
	require.Len(t, wakes, 1)
	assert.Contains(t, wakes[0], "Pull request "+pr+" of task "+task.ID)
	assert.Contains(t, wakes[0], "required checks failed at b5abccde5aee: Required. The control plane sent its "+
		"worker follow-up "+ups[0].ID)
	assert.Contains(t, wakes[0], "Keep the Run open")

	next, err := w.create(task.ID, DispatchSpec{Continue: true})
	require.NoError(t, err)
	acc, err := w.s.AcceptDispatch(ctx, w.call(next, 0))
	require.NoError(t, err)
	nc := w.call(next, acc.Epoch)
	require.NoError(t, w.s.ReportStarted(ctx, nc, "pid:43@2", "sess_1"))
	require.NoError(t, w.finish(nc, withPR(success("Fixed the test. Auto-merge is on at 0c0ffee1."), pr)))

	reads = nil
	state = githost.RequestState{Status: githost.RequestMerged, Head: "0c0ffee1"}
	require.NoError(t, w.s.FollowPullRequests(ctx, read))
	assert.Equal(t, []string{pr + "@0c0ffee1"}, reads, "the new report is followed")

	ups, err = w.s.ListFollowUps(ctx, task.ID)
	require.NoError(t, err)
	assert.Len(t, ups, 1, "a merge asks nothing of the author")
	assert.Equal(t, TaskSucceeded, w.task(task.ID).Status)

	wakes = undeliveredWakes(t, w, chat.ID)
	require.NotEmpty(t, wakes)
	assert.Contains(t, wakes[len(wakes)-1], "Pull request "+pr+" of task "+task.ID+" (run "+w.run.ID+") merged. "+
		"Finish the Run if nothing else is left")

	reads = nil
	require.NoError(t, w.s.FollowPullRequests(ctx, read))
	assert.Empty(t, reads, "a settled report is not read again")
}

// Scenario (issue #1053): a conflict comes before a failed check, as pstack's babysit orders them; a follow-up the
// control plane cannot send (the worker's conversation is closed) is said to the coordinator instead, with how
// to continue. A review Task, and a Task whose summary does not say auto-merge is on, are not followed.
func TestAConflictedPullRequestWhoseAuthorIsGoneWakesTheCoordinator(t *testing.T) {
	t.Parallel()

	const pr = "https://github.com/owner/repo/pull/9"

	w := newWorld(t)
	ctx := t.Context()
	chat, err := w.s.CreateChat(ctx, "", "Ship")
	require.NoError(t, err)
	w.run, err = w.s.CreateRun(ctx, NewRun{Objective: "Ship", CreatedBy: "pi", ChatID: chat.ID})
	require.NoError(t, err)

	tasks := w.tasks(
		NewTask{Ref: "a", Profile: "implementer", Spec: "change a"},
		NewTask{Ref: "b", Profile: "implementer", Spec: "change b"},
	)
	_, c := w.start(tasks[1].ID)
	require.NoError(t, w.finish(c, withPR(success("PR open; a review comes first."), pr)))

	// The worker's report reached the control plane only after its lease ran out: its conversation is closed.
	d, _ := w.start(tasks[0].ID)
	w.loseLease()
	require.NoError(t, w.s.SettleUnknown(ctx, d.ID, Settlement{Succeeded: true, Result: Result{
		Summary: "auto-merge on at abcdef1", Evidence: []Evidence{{Kind: "pull_request", Ref: pr}},
	}}))

	follow, err := w.s.PullRequestsToFollow(ctx)
	require.NoError(t, err)
	require.Len(t, follow, 1)
	assert.Equal(t, tasks[0].ID, follow[0].TaskID)
	assert.Equal(t, githost.GitHub, follow[0].Kind)
	assert.Equal(t, githost.Request{Repo: githost.Repo{Host: "github.com", Path: "owner/repo"}, Number: 9},
		follow[0].Request)

	outcome, err := w.s.SettlePullRequest(ctx, follow[0], githost.RequestState{
		Status: githost.RequestOpen, Head: "abcdef1", Conflict: true, Failed: []string{"Required"},
	})
	require.NoError(t, err)
	assert.Equal(t, PullRequestConflict, outcome)

	wakes := undeliveredWakes(t, w, chat.ID)
	require.NotEmpty(t, wakes)
	last := wakes[len(wakes)-1]
	assert.Contains(t, last, "conflicts with its base at abcdef1. A follow-up to its worker was refused")
	assert.Contains(t, last, `create a Task with "continues": "`+tasks[0].ID+`"`)

	follow, err = w.s.PullRequestsToFollow(ctx)
	require.NoError(t, err)
	assert.Empty(t, follow)

	review := w.tasks(NewTask{Ref: "review", Profile: "reviewer", Spec: "Review pull request: " + pr})[0]
	_, c = w.start(review.ID)
	require.NoError(t, w.finish(c, withPR(success("Verdict: correct; auto-merge on at abcdef1"), pr)))

	follow, err = w.s.PullRequestsToFollow(ctx)
	require.NoError(t, err)
	assert.Empty(t, follow, "a review is not followed")
}

func withPR(o Outcome, pr string) Outcome {
	o.Result.Evidence = []Evidence{{Kind: "pull_request", Ref: pr}}

	return o
}

func undeliveredWakes(t *testing.T, w *world, chatID string) []string {
	t.Helper()

	_, err := w.s.WakeChats(t.Context())
	require.NoError(t, err)

	msgs, err := w.s.Undelivered(t.Context(), chatID)
	require.NoError(t, err)

	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Text)
	}

	return out
}
