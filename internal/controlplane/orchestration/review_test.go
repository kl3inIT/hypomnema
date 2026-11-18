package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const reviewSpec = "Review pull request: https://github.com/owner/repo/pull/840\n\nReview the change."

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
			review := world.tasks(NewTask{Ref: "review", Spec: reviewSpec})[0]
			world.succeed(review.ID, "**Verdict:** `"+scenario.verdict+"`\nMergeable: "+scenario.mergeable+
				"\nMerge state: "+scenario.state+"\nChecks: "+scenario.checks+"\nFindings: test regression")

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
	for round := 1; round <= maxReviewRounds; round++ {
		review := world.tasks(NewTask{Ref: "review", Spec: reviewSpec})[0]
		world.succeed(review.ID, "Verdict: incorrect\nFindings: [P1] the change loses data")

		run, err := world.s.GetRun(ctx, world.run.ID)
		require.NoError(t, err)
		require.NotNil(t, run.ReviewStall)
		assert.Equal(t, round == maxReviewRounds, run.ReviewStall.WaitingForOwner)
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
		NewTask{Ref: "review", Spec: reviewSpec},
		NewTask{Ref: "next", Spec: "Next step", DependsOn: []string{"review"}},
	)[0]
	world.succeed(review.ID, "Verdict: incorrect")
	run, err := world.s.GetRun(t.Context(), world.run.ID)
	require.NoError(t, err)
	assert.Nil(t, run.ReviewStall)
}
