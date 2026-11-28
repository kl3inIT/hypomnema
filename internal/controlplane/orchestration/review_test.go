package orchestration

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
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

func TestReviewLimitReportsToTheOwnerInbox(t *testing.T) {
	t.Parallel()

	for _, resolution := range []string{"new work", "merge webhook", "merge poll", "finished"} {
		t.Run(resolution, func(t *testing.T) {
			w := newWorld(t)
			ctx := t.Context()
			chat, err := w.s.CreateChat(ctx, "", "Review a PR")
			require.NoError(t, err)
			w.run, err = w.s.CreateRun(ctx, NewRun{Objective: "Review a PR", CreatedBy: "pi", ChatID: chat.ID})
			require.NoError(t, err)

			const blockers = "- [P1] Retry drops the original row.\n- [P0] Cancel clears **all** pending work."
			headOf := func(round int) string { return fmt.Sprintf("%08x%032x", round, round) }
			review := func(t *testing.T, round int, spec, head string) {
				t.Helper()
				task := w.tasks(NewTask{Ref: "review", Profile: "reviewer", Spec: spec})[0]
				finishReview(w, task.ID, "Verdict: incorrect\nHead: "+head+"\n\nFindings:\n"+blockers,
					[]Evidence{{Kind: "pull_request", Ref: reviewPR}})
				run, err := w.s.GetRun(t.Context(), w.run.ID)
				require.NoError(t, err)
				require.NotNil(t, run.ReviewStall)
				assert.Equal(t, round >= 3, run.ReviewStall.WaitingForOwner)
				assert.Equal(t, reviewPR, run.ReviewStall.PullRequest)
				assert.Equal(t, "UNKNOWN", run.ReviewStall.Checks)
				assert.Equal(t, RunOpen, run.Status)
			}

			for index, spec := range []string{
				"Earlier PR: https://github.com/owner/repo/pull/827\n\nReview this change:\n\n" + reviewPR,
				reviewSpec, reviewSpec,
			} {
				review(t, index+1, spec, headOf(index+1))
				if index < 2 {
					page, err := w.s.Inbox().List(ctx, inboxstore.Filter{WithClosed: true}, "", 10)
					require.NoError(t, err)
					assert.Empty(t, page.Items)
				}
			}

			report := func(round int) inboxstore.Todo {
				page, err := w.s.Inbox().List(ctx, inboxstore.Filter{NeedsYou: true}, "", 10)
				require.NoError(t, err)
				require.Len(t, page.Items, 1)
				item := page.Items[0]
				assert.Equal(t, inboxstore.Open, item.State)
				assert.Equal(t, inboxstore.Me, item.Assignee.Kind)
				assert.Equal(t, inboxstore.FromChat, item.Source.Kind)
				assert.Equal(t, chat.ID, item.Source.ID)
				for _, text := range []string{reviewPR, headOf(round), "Verdict: incorrect", blockers, chat.ID,
					"Recommendation", "one more round", "merge as is and open an issue for the rest", "stop"} {
					assert.Contains(t, item.Notes, text)
				}

				return item
			}

			item := report(3)
			_, err = w.s.WakeChats(ctx)
			require.NoError(t, err)
			_, err = w.s.WakeChats(ctx)
			require.NoError(t, err)
			assert.Equal(t, item.ID, report(3).ID)
			if resolution == "new work" {
				assert.NotContains(t, item.Notes, "Fix the remaining blockers in one more round before merging.")
				wakes, err := w.s.Undelivered(ctx, chat.ID)
				require.NoError(t, err)
				require.NotEmpty(t, wakes)
				assert.Contains(t, wakes[len(wakes)-1].Text, item.ID)
				assert.Contains(t, wakes[len(wakes)-1].Text, "inbox_comment")

				coordinator := inboxstore.Actor{Kind: inboxstore.Bot, ID: chat.Bot}
				const recommendation = "Recommendation: Stop. The retry change is superseded."
				_, err = w.s.Inbox().CommentWith(ctx, coordinator, item.ID, recommendation,
					inboxstore.Attached{From: chat.ID})
				assert.NoError(t, err)

				const reply = "one more round, focused on retry"
				commented, err := w.s.Inbox().Comment(ctx, inboxstore.Actor{Kind: inboxstore.Owner}, item.ID, reply)
				require.NoError(t, err)
				assert.Contains(t, fmt.Sprint(commented.Events), recommendation)
				wakes, err = w.s.Undelivered(ctx, chat.ID)
				require.NoError(t, err)
				assert.Len(t, wakes, 4, "only the owner's reply adds a wake")
				assert.Contains(t, wakes[len(wakes)-1].Text, reply)
			}
			all, err := w.s.Inbox().List(ctx, inboxstore.Filter{WithClosed: true}, "", 10)
			require.NoError(t, err)
			require.Len(t, all.Items, 1)

			questions, err := w.s.ListQuestions(ctx, QuestionFilter{})
			require.NoError(t, err)
			assert.Empty(t, questions)

			request := githost.Request{Repo: githost.Repo{Host: "github.com", Path: "owner/repo"}, Number: 840}
			switch resolution {
			case "new work":
				fix := w.tasks(NewTask{Ref: "fix", Spec: "Fix the blockers"})[0]
				w.succeed(fix.ID, "Updated the PR")
			case "merge webhook":
				_, err = w.s.SettleClosedPullRequest(ctx, request, githost.RequestState{Status: githost.RequestMerged})
				require.NoError(t, err)
			case "merge poll":
				require.NoError(t, w.s.FollowPullRequests(ctx, func(_ context.Context, f FollowedRequest) (
					githost.Request, githost.RequestState, error,
				) {
					require.NotNil(t, f.Hint)
					assert.Equal(t, request, *f.Hint)
					return request, githost.RequestState{Status: githost.RequestMerged}, nil
				}, true))
			case "finished":
				require.NoError(t, w.s.FinishRun(ctx, w.run.ID))
			}

			closed, err := w.s.Inbox().Get(ctx, item.ID)
			require.NoError(t, err)
			assert.True(t, inboxstore.Closed(closed.State))

			if resolution == "new work" {
				review(t, 3, reviewSpec, headOf(3)[:8])
				all, err := w.s.Inbox().List(ctx, inboxstore.Filter{WithClosed: true}, "", 10)
				require.NoError(t, err)
				require.Len(t, all.Items, 1, "another report of the same commit does not create a second item")
				assert.True(t, inboxstore.Closed(all.Items[0].State))

				review(t, 4, reviewSpec, headOf(4))
				assert.NotEqual(t, item.ID, report(4).ID)
				require.NoError(t, w.s.FinishRun(ctx, w.run.ID))
			}
			if resolution == "new work" || resolution == "finished" {
				run, err := w.s.GetRun(ctx, w.run.ID)
				require.NoError(t, err)
				assert.Nil(t, run.ReviewStall)
			}
		})
	}
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
	require.ErrorContains(t, err, "already has the verdict correct from Task "+first.ID)

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
