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

func TestReviewOfAHeadWithAVerdictIsRefusedInAnyRun(t *testing.T) {
	t.Parallel()

	// PR #874 got correct and then incorrect on commit 0c6ca51b from two Runs (2026-11-19, issue #953).
	const head = "0c6ca51b4e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b"
	world := newWorld(t)
	ctx := t.Context()
	first := world.tasks(NewTask{Ref: "review", Profile: "reviewer", Spec: reviewSpec})[0]
	finishReview(world, first.ID, "Verdict: `correct`, merge it\nHead: `0c6ca51b`\nEvidence: test-verified", nil)

	other, err := world.s.CreateRun(ctx, NewRun{Objective: "Review it again", CreatedBy: "pi"})
	require.NoError(t, err)
	_, err = world.s.CreateTasks(ctx, other.ID, []NewTask{{Ref: "again", Profile: "reviewer",
		Spec: "Review pull request: " + reviewPR + "\nHead: " + head + "\n\nReview the change."}})
	require.ErrorIs(t, err, ErrConflict)
	assert.ErrorContains(t, err, "already has the verdict correct from Task "+first.ID)

	_, err = world.s.CreateTasks(ctx, other.ID, []NewTask{{Ref: "work", Profile: "implementer",
		Spec: reviewSpec + "\nHead: " + head}})
	assert.NoError(t, err, "only a review is refused")
}

func TestBlockedReviewOfAHeadMayRunAgain(t *testing.T) {
	t.Parallel()

	world := newWorld(t)
	blocked := world.tasks(NewTask{Ref: "review", Profile: "reviewer", Spec: reviewSpec + "\nHead: 0c6ca51b"})[0]
	finishReview(world, blocked.ID, "Verdict: blocked\nHead: 0c6ca51b\nThe test database would not start.", nil)
	unread := world.tasks(NewTask{Ref: "again", Profile: "reviewer", Spec: reviewSpec + "\nHead: 0c6ca51b"})[0]
	finishReview(world, unread.ID, "Verdict: incorrect\nThe change loses data.", nil)

	again := world.tasks(NewTask{Ref: "third", Profile: "reviewer", Spec: reviewSpec + "\nHead: 0c6ca51b"})[0]
	assert.Equal(t, reviewSpec+"\nHead: 0c6ca51b", again.Spec,
		"a blocked verdict, or one without its head, is not a verdict on that head")
}

func TestLaterReviewIsGivenTheReviewedHead(t *testing.T) {
	t.Parallel()

	world := newWorld(t)
	first := world.tasks(NewTask{Ref: "review", Profile: "reviewer", Spec: reviewSpec + "\nHead: 0c6ca51b"})[0]
	finishReview(world, first.ID, "Verdict: incorrect\nHead: 0c6ca51b\nFindings: [P1] the change loses data", nil)

	later := world.tasks(NewTask{Ref: "later", Profile: "reviewer", Spec: reviewSpec + "\nHead: d45b955b"})[0]
	assert.Contains(t, later.Spec, "Task "+first.ID+" gave the verdict incorrect at head 0c6ca51b.")
	assert.Contains(t, later.Spec, "`git diff 0c6ca51b..<head>`")
}
