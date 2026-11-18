package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

const reviewPR = "https://github.com/owner/repo/pull/840"
const reviewSpec = "Review pull request: " + reviewPR + "\n\nReview the change."

func TestReviewWithNoWorkLeftWakesItsCoordinatorOnce(t *testing.T) {
	t.Parallel()

	for _, scenario := range []struct {
		verdict, mergeable, state, checks string
	}{
		{"incorrect", "MERGEABLE", "BLOCKED", "FAILING"},
		{"correct", "CONFLICTING", "DIRTY", "PASSING"},
	} {
		t.Run(scenario.verdict, func(t *testing.T) {
			t.Parallel()

			world := newWorld(t)
			ctx := t.Context()
			chat, err := world.s.CreateChat(ctx, "", "Review a PR")
			require.NoError(t, err)
			world.run, err = world.s.CreateRun(ctx, NewRun{Objective: "Review a PR", CreatedBy: "pi", ChatID: chat.ID})
			require.NoError(t, err)
			review := world.tasks(NewTask{Ref: "review", Profile: "reviewer", Spec: reviewPR + "\n\nReview the change."})[0]
			finishReview(world, review.ID, "**Verdict:** `"+scenario.verdict+"`\nMergeable: "+scenario.mergeable+
				"\nMerge state: "+scenario.state+"\nChecks: "+scenario.checks+"\nFindings: test regression", nil)

			run, err := world.s.GetRun(ctx, world.run.ID)
			require.NoError(t, err)
			require.NotNil(t, run.ReviewStall)
			assert.Equal(t, &ReviewStall{
				TaskID: review.ID, PullRequest: "https://github.com/owner/repo/pull/840", Verdict: scenario.verdict,
				Mergeable: scenario.mergeable, MergeState: scenario.state, Checks: scenario.checks,
			}, run.ReviewStall)

			added, err := world.s.WakeChats(ctx)
			require.NoError(t, err)
			assert.Equal(t, 1, added)
			added, err = world.s.WakeChats(ctx)
			require.NoError(t, err)
			assert.Zero(t, added)
			wakes, err := world.s.Undelivered(ctx, chat.ID)
			require.NoError(t, err)
			require.Len(t, wakes, 1)
			assert.Contains(t, wakes[0].Text, scenario.verdict+" on https://github.com/owner/repo/pull/840")
			assert.Contains(t, wakes[0].Text, "mergeable: "+scenario.mergeable+", merge state: "+scenario.state+
				", required checks: "+scenario.checks)

			fix := world.tasks(NewTask{Ref: "fix", Spec: "Fix the PR"})[0]
			run, err = world.s.GetRun(ctx, world.run.ID)
			require.NoError(t, err)
			assert.Nil(t, run.ReviewStall, "adding a Task clears the stall")
			world.succeed(fix.ID, "Updated the PR")
			run, err = world.s.GetRun(ctx, world.run.ID)
			require.NoError(t, err)
			assert.Nil(t, run.ReviewStall, "a historical stall does not reappear")
		})
	}
}

func TestReviewLimitLeavesRunWaitingForOwner(t *testing.T) {
	t.Parallel()

	world := newWorld(t)
	ctx := t.Context()
	legacy := "Earlier PR: https://github.com/owner/repo/pull/827\n\nReview this change:\n\n" + reviewPR
	for index, spec := range []string{legacy, reviewSpec, reviewSpec} {
		review := world.tasks(NewTask{Ref: "review", Profile: "reviewer", Spec: spec})[0]
		finishReview(world, review.ID, "Verdict: incorrect\nFindings: [P1] the change loses data",
			[]Evidence{{Kind: "pull_request", Ref: reviewPR}})

		run, err := world.s.GetRun(ctx, world.run.ID)
		require.NoError(t, err)
		if !assert.NotNil(t, run.ReviewStall, "round %d", index+1) {
			continue
		}
		assert.Equal(t, index == maxReviewRounds-1, run.ReviewStall.WaitingForOwner)
		assert.Equal(t, reviewPR, run.ReviewStall.PullRequest)
		assert.Equal(t, "UNKNOWN", run.ReviewStall.Checks)
		assert.Equal(t, RunOpen, run.Status)
	}

	require.NoError(t, world.s.FinishRun(ctx, world.run.ID))
	run, err := world.s.GetRun(ctx, world.run.ID)
	require.NoError(t, err)
	assert.Nil(t, run.ReviewStall)
}

func TestReviewDoesNotStallRunWithQueuedWork(t *testing.T) {
	t.Parallel()

	world := newWorld(t)
	review := world.tasks(
		NewTask{Ref: "review", Profile: "reviewer", Spec: reviewSpec},
		NewTask{Ref: "next", Spec: "Next step", DependsOn: []string{"review"}},
	)[0]
	finishReview(world, review.ID, "Verdict: incorrect", nil)
	run, err := world.s.GetRun(t.Context(), world.run.ID)
	require.NoError(t, err)
	assert.Nil(t, run.ReviewStall)
}

func TestReviewStallOnRegisteredGitLabHost(t *testing.T) {
	t.Parallel()

	const pr = "https://gitlab.company.example/team/app/-/merge_requests/25"
	world := newWorld(t)
	ctx := t.Context()
	_, err := world.s.AddGitHost(ctx, "gitlab.company.example", githost.GitLab)
	require.NoError(t, err)
	chat, err := world.s.CreateChat(ctx, "", "Review a GitLab MR")
	require.NoError(t, err)
	world.run, err = world.s.CreateRun(ctx, NewRun{Objective: "Review a GitLab MR", CreatedBy: "pi", ChatID: chat.ID,
		Repo: "https://gitlab.company.example/team/app"})
	require.NoError(t, err)
	review := world.tasks(NewTask{Ref: "review", Profile: "reviewer", Spec: "Review the merge request (" + pr + ")."})[0]
	finishReview(world, review.ID, "Verdict: correct\nMerge state: DIRTY\nMergeable: CONFLICTING\nChecks: PASSING", nil)

	run, err := world.s.GetRun(ctx, world.run.ID)
	require.NoError(t, err)
	require.NotNil(t, run.ReviewStall)
	assert.Equal(t, "CONFLICTING", run.ReviewStall.Mergeable)
	assert.Equal(t, pr, run.ReviewStall.PullRequest)
	_, err = world.s.WakeChats(ctx)
	require.NoError(t, err)
	wakes, err := world.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, wakes, 1)
	assert.Contains(t, wakes[0].Text, "correct on "+pr)
	assert.Contains(t, wakes[0].Text, "mergeable: CONFLICTING")
}

func TestReviewStallFindsRequestInResultOrEvidence(t *testing.T) {
	t.Parallel()

	const pr = "https://github.com/owner/repo/pull/840"
	for _, source := range []string{"summary", "evidence", "implementer"} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()

			world := newWorld(t)
			profile := "reviewer"
			if source == "implementer" {
				profile = "implementer"
			}
			review := world.tasks(NewTask{Ref: "review", Profile: profile,
				Spec: "Review pull request: https://github.com/owner/repo/pull/827"})[0]
			summary := "Verdict: incorrect"
			var evidence []Evidence
			if source == "summary" {
				summary += "\nReviewed [the PR](" + pr + ")."
			} else if source == "evidence" {
				evidence = []Evidence{{Kind: "pull_request", Ref: "owner/repo#840"}}
			} else {
				evidence = []Evidence{{Kind: "pull_request", Ref: pr}}
			}
			finishReview(world, review.ID, summary, evidence)
			run, err := world.s.GetRun(t.Context(), world.run.ID)
			require.NoError(t, err)
			if source == "implementer" {
				assert.Nil(t, run.ReviewStall)
				return
			}
			require.NotNil(t, run.ReviewStall)
			assert.Equal(t, pr, run.ReviewStall.PullRequest)
		})
	}
}

func TestReviewSpecSelectsStatedTargetInsteadOfFirstURL(t *testing.T) {
	t.Parallel()

	const background = "Background: https://github.com/owner/repo/pull/827\n"
	assert.Equal(t, reviewPR, specReviewTarget(background+"Review the PR "+reviewPR, githost.KnownKind))
	assert.Equal(t, reviewPR, specReviewTarget(reviewSpec+"\n"+background, githost.KnownKind))
	assert.Equal(t, reviewPR, specReviewTarget("Review PR "+reviewPR+"; not "+reviewPR+"1", githost.KnownKind))
	assert.Empty(t, specReviewTarget(background+"Other background: "+reviewPR, githost.KnownKind))
}

func finishReview(world *world, taskID, summary string, evidence []Evidence) {
	world.t.Helper()
	_, call := world.start(taskID)
	outcome := success(summary)
	outcome.Result.Evidence = evidence
	require.NoError(world.t, world.finish(call, outcome))
}
