package orchestration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// Scenario (issues #1053, #1168): a worker turned auto-merge on and finished without waiting for CI, and without
// saying so in a set sentence: its summary is free text and its evidence names no pull request. The control
// plane finds the pull request by the Task's branch and reads from the host that auto-merge is armed. It goes red
// after the Task succeeded: the pass sends the author a follow-up naming the failed check, and wakes the
// coordinator once. While it is pending nothing happens, and the quick pass that looks up branches without a pull
// request does not read it again. The worker fixes it and finishes again; the pass follows that report too, and
// once the pull request merged the coordinator is woken to finish the Run. A read that fails leaves it for the
// next pass, and a settled report is not read again.
func TestAPullRequestFoundByItsBranchThatGoesRedSendsItsAuthorAFollowUp(t *testing.T) {
	t.Parallel()

	const pr = "https://github.com/kl3inIT/hypomnema/pull/1054"

	w := newWorld(t)
	ctx := t.Context()
	chat, err := w.s.CreateChat(ctx, "", "Ship the fix")
	require.NoError(t, err)
	w.run, err = w.s.CreateRun(ctx, NewRun{
		Objective: "Ship the fix", CreatedBy: "pi", ChatID: chat.ID, Repo: "kl3inIT/hypomnema",
	})
	require.NoError(t, err)

	task := w.tasks(NewTask{Ref: "fix", Profile: "implementer", Spec: "fix the bug"})[0]
	_, c := w.start(task.ID)
	done := success("Auto-merge đã bật cho PR ở commit b5abccde.")
	done.Result.Evidence = nil
	require.NoError(t, w.finish(c, done))
	_, err = w.s.WakeChats(ctx)
	require.NoError(t, err)
	require.NoError(t, w.s.MarkDelivered(ctx, chat.ID, 1<<40))

	var reads []string

	found := githost.Request{Repo: githost.Repo{Host: "github.com", Path: "kl3inIT/hypomnema"}, Number: 1054}
	state := githost.RequestState{Status: githost.RequestOpen, Head: "b5abccde5aee", AutoMerge: true}
	read := func(_ context.Context, f FollowedRequest) (githost.Request, githost.RequestState, error) {
		hint := "none"
		if f.Hint != nil {
			hint = fmt.Sprint(f.Hint.Number)
		}

		reads = append(reads, f.Branch+" hint "+hint)

		return found, state, nil
	}

	require.NoError(t, w.s.FollowPullRequests(ctx, read, true))
	assert.Equal(t, []string{"hyp/" + task.ID + " hint none"}, reads, "found by the Task's branch")
	assert.Equal(t, TaskSucceeded, w.task(task.ID).Status, "pending: nothing is asked")

	reads = nil
	require.NoError(t, w.s.FollowPullRequests(ctx, read, false))
	assert.Empty(t, reads, "a pull request found is read by the minute's pass only")

	failing := func(context.Context, FollowedRequest) (githost.Request, githost.RequestState, error) {
		return githost.Request{}, githost.RequestState{}, errors.New("GitHub answered HTTP 502")
	}
	require.ErrorContains(t, w.s.FollowPullRequests(ctx, failing, true), "HTTP 502")
	assert.Equal(t, TaskSucceeded, w.task(task.ID).Status)

	state.Failed = []string{"Required"}
	require.NoError(t, w.s.FollowPullRequests(ctx, read, true))
	assert.Equal(t, "hyp/"+task.ID+" hint 1054", reads[len(reads)-1], "the pull request found is kept")

	ups, err := w.s.ListFollowUps(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, ups, 1, "the author is sent a follow-up")
	assert.Equal(t, AddressCoordinator, ups[0].AskedBy)
	assert.Contains(t, ups[0].Note, "The required checks of your pull request "+pr+" failed at b5abccde5aee, "+
		"after you finished with auto-merge on: Required.")
	assert.Contains(t, ups[0].Note, "turn auto-merge on again at the new head")
	assert.Equal(t, TaskReady, w.task(task.ID).Status)

	follow, err := w.s.PullRequestsToFollow(ctx, true)
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
	require.NoError(t, w.finish(nc, withPR(success("Fixed the test."), pr)))

	reads = nil
	state = githost.RequestState{Status: githost.RequestMerged, Head: "0c0ffee1"}
	require.NoError(t, w.s.FollowPullRequests(ctx, read, true))
	assert.Equal(t, []string{"hyp/" + task.ID + " hint 1054"}, reads, "the new report is followed, its evidence a hint")

	ups, err = w.s.ListFollowUps(ctx, task.ID)
	require.NoError(t, err)
	assert.Len(t, ups, 1, "a merge asks nothing of the author")
	assert.Equal(t, TaskSucceeded, w.task(task.ID).Status)

	wakes = undeliveredWakes(t, w, chat.ID)
	require.NotEmpty(t, wakes)
	assert.Contains(t, wakes[len(wakes)-1], "Pull request "+pr+" of task "+task.ID+" (run "+w.run.ID+") merged. "+
		"Finish the Run if nothing else is left")

	reads = nil
	require.NoError(t, w.s.FollowPullRequests(ctx, read, true))
	assert.Empty(t, reads, "a settled report is not read again")
}

// Scenario (issue #1168): a Task's branch has no pull request when its Task succeeds. It is looked up again within
// the window, every quick pass, and dropped after it. Another Task's evidence names a pull request of another
// branch: what the host finds on the branch is kept in its place, and is still read after the window, until it
// merged.
func TestABranchWithoutAPullRequestIsLookedUpWithinTheWindowThenDropped(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	chat, err := w.s.CreateChat(ctx, "", "Ship")
	require.NoError(t, err)
	w.run, err = w.s.CreateRun(ctx, NewRun{Objective: "Ship", CreatedBy: "pi", ChatID: chat.ID, Repo: "owner/repo"})
	require.NoError(t, err)

	tasks := w.tasks(
		NewTask{Ref: "docs", Profile: "implementer", Spec: "change the docs"},
		NewTask{Ref: "code", Profile: "implementer", Spec: "change the code"},
	)
	_, c := w.start(tasks[0].ID)
	require.NoError(t, w.finish(c, success("Pushed the docs to main.")))
	_, c = w.start(tasks[1].ID)
	require.NoError(t, w.finish(c, withPR(success("Opened it."), "https://github.com/owner/repo/pull/3")))

	repo := githost.Repo{Host: "github.com", Path: "owner/repo"}
	opened := false
	reads := map[string]int{}
	hints := map[string][]int{}
	read := func(_ context.Context, f FollowedRequest) (githost.Request, githost.RequestState, error) {
		reads[f.TaskID]++

		if f.Hint != nil {
			hints[f.TaskID] = append(hints[f.TaskID], f.Hint.Number)
		}

		if f.TaskID == tasks[0].ID || !opened {
			return githost.Request{}, githost.RequestState{}, githost.ErrNoRequest
		}

		return githost.Request{Repo: repo, Number: 4}, githost.RequestState{Status: githost.RequestOpen}, nil
	}

	require.NoError(t, w.s.FollowPullRequests(ctx, read, false))
	w.clock.now = w.clock.now.Add(LookUpEvery)
	opened = true
	require.NoError(t, w.s.FollowPullRequests(ctx, read, false))
	assert.Equal(t, map[string]int{tasks[0].ID: 2, tasks[1].ID: 2}, reads, "looked up again within the window")
	assert.Equal(t, []int{3, 3}, hints[tasks[1].ID], "the evidence is the hint")

	w.clock.now = w.clock.now.Add(2 * time.Minute)
	require.NoError(t, w.s.FollowPullRequests(ctx, read, true))
	assert.Equal(t, 2, reads[tasks[0].ID], "dropped after the window")
	assert.Equal(t, 3, reads[tasks[1].ID], "a pull request found is still read")
	assert.Equal(t, []int{3, 3, 4}, hints[tasks[1].ID], "the branch's own pull request replaced the evidence's")

	follow, err := w.s.PullRequestsToFollow(ctx, true)
	require.NoError(t, err)
	require.Len(t, follow, 1)
	assert.Equal(t, tasks[1].ID, follow[0].TaskID)
	assert.Equal(t, "hyp/"+tasks[1].ID, follow[0].Branch)
	assert.Equal(t, repo, follow[0].Repo)
	assert.Equal(t, githost.GitHub, follow[0].Kind)
}

// Scenario (issue #1053): a conflict comes before a failed check, as pstack's babysit orders them; a follow-up the
// control plane cannot send (the worker's conversation is closed) is said to the coordinator instead, with how
// to continue. A pull request without auto-merge armed (a review comes first) is left to its review: a conflict
// sends nothing. A review Task is not followed.
func TestAConflictedPullRequestWhoseAuthorIsGoneWakesTheCoordinator(t *testing.T) {
	t.Parallel()

	const pr = "https://github.com/owner/repo/pull/9"

	w := newWorld(t)
	ctx := t.Context()
	chat, err := w.s.CreateChat(ctx, "", "Ship")
	require.NoError(t, err)
	w.run, err = w.s.CreateRun(ctx, NewRun{Objective: "Ship", CreatedBy: "pi", ChatID: chat.ID, Repo: "owner/repo"})
	require.NoError(t, err)

	tasks := w.tasks(
		NewTask{Ref: "a", Profile: "implementer", Spec: "change a"},
		NewTask{Ref: "b", Profile: "implementer", Spec: "change b"},
	)
	_, c := w.start(tasks[1].ID)
	require.NoError(t, w.finish(c, withPR(success("PR open; a review comes first."), pr+"0")))

	// The author's conversation is closed since: its worker is gone.
	d, c := w.start(tasks[0].ID)
	require.NoError(t, w.finish(c, withPR(success("done"), pr)))
	w.closeConversation(d.ConversationID)

	review := w.tasks(NewTask{Ref: "review", Profile: "reviewer", Spec: "Review pull request: " + pr})[0]
	_, c = w.start(review.ID)
	require.NoError(t, w.finish(c, withPR(success("Verdict: correct"), pr)))

	read := func(_ context.Context, f FollowedRequest) (githost.Request, githost.RequestState, error) {
		require.NotNil(t, f.Hint)

		return *f.Hint, githost.RequestState{
			Status: githost.RequestOpen, Head: "abcdef1", Conflict: true, Failed: []string{"Required"},
			AutoMerge: f.TaskID == tasks[0].ID,
		}, nil
	}
	require.NoError(t, w.s.FollowPullRequests(ctx, read, true))

	wakes := undeliveredWakes(t, w, chat.ID)
	require.NotEmpty(t, wakes)
	last := wakes[len(wakes)-1]
	assert.Contains(t, last, "Pull request "+pr+" of task "+tasks[0].ID)
	assert.Contains(t, last, "conflicts with its base at abcdef1. A follow-up to its worker was refused")
	assert.Contains(t, last, `create a Task with "continues": "`+tasks[0].ID+`"`)

	for _, wake := range wakes {
		assert.NotContains(t, wake, pr+"0", "without auto-merge, a conflict is left to the review")
	}

	ups, err := w.s.ListFollowUps(ctx, tasks[1].ID)
	require.NoError(t, err)
	assert.Empty(t, ups)

	follow, err := w.s.PullRequestsToFollow(ctx, true)
	require.NoError(t, err)
	require.Len(t, follow, 1, "the settled one and the review are not followed")
	assert.Equal(t, tasks[1].ID, follow[0].TaskID)
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

// Scenario (issue #1053, review round 2): the control plane holds no token for the repository, so its worker used
// the node's login and waited for its checks itself. The pass cannot read the pull request its evidence names: it
// tells the coordinator once that it does not follow it, sends no follow-up, and does not list it again, so no Run
// waits for a wake that would never come. A Task whose evidence names none is not said at all (issue #1168).
func TestAPullRequestTheControlPlaneCannotReadIsSaidOnceAndLeft(t *testing.T) {
	t.Parallel()

	const pr = "https://github.com/owner/repo/pull/12"

	w := newWorld(t)
	ctx := t.Context()
	chat, err := w.s.CreateChat(ctx, "", "Ship")
	require.NoError(t, err)
	w.run, err = w.s.CreateRun(ctx, NewRun{Objective: "Ship", CreatedBy: "pi", ChatID: chat.ID, Repo: "owner/repo"})
	require.NoError(t, err)

	tasks := w.tasks(
		NewTask{Ref: "a", Profile: "implementer", Spec: "change a"},
		NewTask{Ref: "b", Profile: "implementer", Spec: "change b"},
	)
	_, c := w.start(tasks[0].ID)
	require.NoError(t, w.finish(c, withPR(success("Checks green; auto-merge on at abcdef1."), pr)))
	_, c = w.start(tasks[1].ID)
	require.NoError(t, w.finish(c, success("Pushed.")))
	_, err = w.s.WakeChats(ctx)
	require.NoError(t, err)
	require.NoError(t, w.s.MarkDelivered(ctx, chat.ID, 1<<40))

	reads := 0
	cannot := func(context.Context, FollowedRequest) (githost.Request, githost.RequestState, error) {
		reads++

		return githost.Request{}, githost.RequestState{}, fmt.Errorf("github.com/owner/repo: %w", ErrCannotFollow)
	}

	require.NoError(t, w.s.FollowPullRequests(ctx, cannot, true))
	w.clock.now = w.clock.now.Add(3 * time.Minute)
	require.NoError(t, w.s.FollowPullRequests(ctx, cannot, true))
	assert.Equal(t, 2, reads, "each said or dropped once, then left")

	ups, err := w.s.ListFollowUps(ctx, tasks[0].ID)
	require.NoError(t, err)
	assert.Empty(t, ups)
	assert.Equal(t, TaskSucceeded, w.task(tasks[0].ID).Status)

	wakes := undeliveredWakes(t, w, chat.ID)
	require.Len(t, wakes, 1)
	assert.Contains(t, wakes[0], "Pull request "+pr+" of task "+tasks[0].ID+" (run "+w.run.ID+") is not followed: "+
		"the control plane holds no token for its repository")
}

// Scenario (issue #1053, review round 2): the Run is canceled while its pull request is read at the host. What
// the host answers is not acted on: no follow-up, and no wake after the cancel's own.
func TestAPullRequestReadWhileItsRunIsCanceledSettlesNothing(t *testing.T) {
	t.Parallel()

	const pr = "https://github.com/owner/repo/pull/13"

	w := newWorld(t)
	ctx := t.Context()
	chat, err := w.s.CreateChat(ctx, "", "Ship")
	require.NoError(t, err)
	w.run, err = w.s.CreateRun(ctx, NewRun{Objective: "Ship", CreatedBy: "pi", ChatID: chat.ID, Repo: "owner/repo"})
	require.NoError(t, err)

	task := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "change a"})[0]
	_, c := w.start(task.ID)
	require.NoError(t, w.finish(c, withPR(success("done"), pr)))

	canceling := func(ctx context.Context, f FollowedRequest) (githost.Request, githost.RequestState, error) {
		require.NoError(t, w.s.CancelRun(ctx, w.run.ID))

		return *f.Hint, githost.RequestState{
			Status: githost.RequestOpen, Head: "abcdef1", Conflict: true, AutoMerge: true,
		}, nil
	}

	require.NoError(t, w.s.FollowPullRequests(ctx, canceling, true))

	ups, err := w.s.ListFollowUps(ctx, task.ID)
	require.NoError(t, err)
	assert.Empty(t, ups)

	for _, wake := range undeliveredWakes(t, w, chat.ID) {
		assert.NotContains(t, wake, "Pull request "+pr)
	}
}
