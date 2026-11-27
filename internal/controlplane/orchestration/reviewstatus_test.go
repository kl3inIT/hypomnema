package orchestration

import (
	"context"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// statusHost is a host the `review` status is read from and posted to: one pull request state for every read,
// unless byNumber names one for the request's number.
type statusHost struct {
	state    githost.RequestState
	byNumber map[int]githost.RequestState
	readErr  error
	reads    int
	posted   []ReviewStatus
}

func (h *statusHost) host() ReviewStatusHost {
	return ReviewStatusHost{
		Read: func(_ context.Context, _ githost.Kind, r githost.Request) (githost.RequestState, error) {
			h.reads++
			if st, ok := h.byNumber[r.Number]; ok {
				return st, h.readErr
			}

			return h.state, h.readErr
		},
		Post: func(_ context.Context, s ReviewStatus) error {
			h.posted = append(h.posted, s)

			return nil
		},
	}
}

var reviewRequest840 = githost.Request{Repo: githost.Repo{Host: "github.com", Path: "owner/repo"}, Number: 840}

const (
	statusHead    = "0c6ca51b4e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b"
	statusNewHead = "d45b955b0a1b2c3d4e5f60718293a4b5c6d7e8f9"
)

// Scenario (issue #1279): a reviewer's verdict correct for a head, named abbreviated as reviewers do, posts the
// commit status `review` = success on exactly that head, the full SHA the host reads, naming the reviewer Task,
// once. A push makes a new head, which has none until it is reviewed; its own correct verdict posts it there.
func TestACorrectVerdictPostsReviewSuccessOnExactlyItsHead(t *testing.T) {
	t.Parallel()

	world := newWorld(t)
	ctx := t.Context()
	review := world.tasks(NewTask{Ref: "review", Profile: "reviewer", Spec: reviewSpec + "\nHead: " + statusHead})[0]
	finishReview(world, review.ID, "Verdict: correct\nHead: 0c6ca51b\nEvidence: test-verified", nil)

	h := &statusHost{state: githost.RequestState{Status: githost.RequestOpen, Head: statusHead}}
	require.NoError(t, world.s.PostReviewStatuses(ctx, h.host()))
	require.Len(t, h.posted, 1)
	assert.Equal(t, ReviewStatus{
		PullRequest: reviewPR, Kind: githost.GitHub, Request: reviewRequest840, Head: statusHead,
		State: githost.StatusSuccess, Description: "correct: reviewed by Task " + review.ID, TaskID: review.ID,
	}, h.posted[0])

	require.NoError(t, world.s.PostReviewStatuses(ctx, h.host()))
	assert.Len(t, h.posted, 1, "a status is posted once")

	h.state.Head = statusNewHead
	require.NoError(t, world.s.PostReviewStatuses(ctx, h.host()))
	assert.Len(t, h.posted, 1, "a new head has none until it is reviewed")

	later := world.tasks(NewTask{Ref: "later", Profile: "reviewer", Spec: reviewSpec + "\nHead: " + statusNewHead})[0]
	finishReview(world, later.ID, "Verdict: correct\nHead: "+statusNewHead+"\nEvidence: test-verified", nil)
	require.NoError(t, world.s.PostReviewStatuses(ctx, h.host()))
	require.Len(t, h.posted, 2)
	assert.Equal(t, statusNewHead, h.posted[1].Head)
	assert.Equal(t, githost.StatusSuccess, h.posted[1].State)
	assert.Equal(t, later.ID, h.posted[1].TaskID)
}

// Scenario (issue #1279): the pull request moved past the reviewed head before the status was posted. Nothing is
// posted, on either head, and the verdict is not read again.
func TestAVerdictOnAHeadThePullRequestMovedPastPostsNothing(t *testing.T) {
	t.Parallel()

	world := newWorld(t)
	ctx := t.Context()
	review := world.tasks(NewTask{Ref: "review", Profile: "reviewer", Spec: reviewSpec + "\nHead: " + statusHead})[0]
	finishReview(world, review.ID, "Verdict: correct\nHead: "+statusHead, nil)

	h := &statusHost{state: githost.RequestState{Status: githost.RequestOpen, Head: statusNewHead}}
	require.NoError(t, world.s.PostReviewStatuses(ctx, h.host()))
	assert.Empty(t, h.posted)

	require.NoError(t, world.s.PostReviewStatuses(ctx, h.host()))
	assert.Equal(t, 1, h.reads, "a dropped status is not read again")

	h.state = githost.RequestState{Status: githost.RequestMerged, Head: statusHead}
	other := world.tasks(NewTask{Ref: "other", Profile: "reviewer",
		Spec: "Review pull request: https://github.com/owner/repo/pull/841\nHead: " + statusHead})[0]
	finishReview(world, other.ID, "Verdict: correct\nHead: "+statusHead, nil)
	require.NoError(t, world.s.PostReviewStatuses(ctx, h.host()))
	assert.Empty(t, h.posted, "a pull request no longer open gets none")
}

// Scenario (issue #1279): an incorrect or blocked verdict posts `review` = failure on its head, its description
// the verdict and the review's first finding.
func TestAnIncorrectOrBlockedVerdictPostsReviewFailureWithItsFirstFinding(t *testing.T) {
	t.Parallel()

	for _, scenario := range []struct{ name, summary, description string }{
		{"incorrect", "Verdict: incorrect\nHead: 0c6ca51b\nEvidence: test-verified\nMerge state: CLEAN\n\n" +
			"Hold the pull request.\n\n## Execution\n\n- `go test ./...` passed.\n\n## Findings\n\n" +
			"- **P1 — The change loses data.** A retry drops the row.\n- P2 — a typo.",
			"incorrect: P1 — The change loses data. A retry drops the row."},
		{"inline", "Verdict: incorrect\nHead: 0c6ca51b\nFindings: [P1] the change loses data",
			"incorrect: [P1] the change loses data"},
		{"blocked", "Verdict: blocked\nHead: 0c6ca51b\nEvidence: blocked\n\nThe test database would not start.",
			"blocked: The test database would not start."},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()

			world := newWorld(t)
			review := world.tasks(NewTask{Ref: "review", Profile: "reviewer",
				Spec: reviewSpec + "\nHead: " + statusHead})[0]
			finishReview(world, review.ID, scenario.summary, nil)

			h := &statusHost{state: githost.RequestState{Status: githost.RequestOpen, Head: statusHead}}
			require.NoError(t, world.s.PostReviewStatuses(t.Context(), h.host()))
			require.Len(t, h.posted, 1)
			assert.Equal(t, githost.StatusFailure, h.posted[0].State)
			assert.Equal(t, statusHead, h.posted[0].Head)
			assert.Equal(t, scenario.description, h.posted[0].Description)
		})
	}
}

// Scenario (issue #1279): a quick Task's pull request needs no worker review: once the control plane reads its
// head it posts `review` = success, saying why, on that head. A narrow Task's pull request in the same Run gets
// none until it is reviewed.
func TestAQuickTasksPullRequestGetsReviewSuccessWithItsReason(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	var err error

	w.run, err = w.s.CreateRun(ctx, NewRun{Objective: "Ship two changes", CreatedBy: "pi", Repo: "kl3inIT/hypomnema"})
	require.NoError(t, err)

	tasks := w.tasks(NewTask{Ref: "quick", Profile: "implementer", Kind: "quick", Spec: "rename the setting"},
		NewTask{Ref: "narrow", Profile: "implementer", Kind: "narrow", Spec: "fix the retry"})
	quick, narrow := tasks[0], tasks[1]

	for _, task := range tasks {
		_, c := w.start(task.ID)
		require.NoError(t, w.finish(c, success("Opened the pull request.")))
	}

	repo := githost.Repo{Host: "github.com", Path: "kl3inIT/hypomnema"}
	numbers := map[string]int{"hyp/" + quick.ID: 1, "hyp/" + narrow.ID: 2}
	read := func(_ context.Context, f FollowedRequest) (githost.Request, githost.RequestState, error) {
		return githost.Request{Repo: repo, Number: numbers[f.Branch]},
			githost.RequestState{Status: githost.RequestOpen, Head: statusHead, Branch: f.Branch}, nil
	}
	require.NoError(t, w.s.FollowPullRequests(ctx, read, true))

	h := &statusHost{state: githost.RequestState{Status: githost.RequestOpen, Head: statusHead}}
	require.NoError(t, w.s.PostReviewStatuses(ctx, h.host()))
	require.Len(t, h.posted, 1, "the narrow Task's pull request waits for its review")
	assert.Equal(t, ReviewStatus{
		PullRequest: "https://github.com/kl3inIT/hypomnema/pull/1", Kind: githost.GitHub,
		Request: githost.Request{Repo: repo, Number: 1}, Head: statusHead, State: githost.StatusSuccess,
		Description: "no review needed: quick Task " + quick.ID, TaskID: quick.ID,
	}, h.posted[0])

	require.NoError(t, w.s.FollowPullRequests(ctx, read, true))
	require.NoError(t, w.s.PostReviewStatuses(ctx, h.host()))
	assert.Len(t, h.posted, 1, "a head read again is not posted again")
}

// Scenario (issue #1279): a repository the control plane holds no token for is not followed: nothing is posted,
// and the status is not tried again.
func TestAnUnfollowedRepositoryGetsNoReviewStatus(t *testing.T) {
	t.Parallel()

	world := newWorld(t)
	ctx := t.Context()
	review := world.tasks(NewTask{Ref: "review", Profile: "reviewer", Spec: reviewSpec + "\nHead: " + statusHead})[0]
	finishReview(world, review.ID, "Verdict: correct\nHead: "+statusHead, nil)

	h := &statusHost{readErr: ErrCannotFollow}
	require.NoError(t, world.s.PostReviewStatuses(ctx, h.host()))
	assert.Empty(t, h.posted)

	require.NoError(t, world.s.PostReviewStatuses(ctx, h.host()))
	assert.Equal(t, 1, h.reads, "an unfollowed repository is not asked again")
}

// reviewStatusVersions are the versions of the migration before issue #1279's and of its own, found by its name: it
// is named by its time until it is numbered right before its merge.
func reviewStatusVersions(t *testing.T) (before, own int64) {
	t.Helper()

	entries, err := fs.ReadDir(migrations, "migrations")
	require.NoError(t, err)

	for _, e := range entries {
		version, err := strconv.ParseInt(strings.SplitN(e.Name(), "_", 2)[0], 10, 64)
		require.NoError(t, err)

		if strings.HasSuffix(e.Name(), "_review_statuses.sql") {
			return before, version
		}

		before = version
	}

	require.FailNow(t, "no review_statuses migration")

	return 0, 0
}

// Scenario (issue #1279): the pull requests open when the control plane takes this change get the status of their
// latest verdict: the migration queues it, and the first pass posts it on a pull request still open at that head.
// An earlier verdict on another head of the same pull request is not posted.
func TestTheMigrationQueuesEachPullRequestsLatestVerdict(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	world := newWorld(t)

	before, own := reviewStatusVersions(t)
	p, err := provider(world.db)
	require.NoError(t, err)

	_, err = p.DownTo(ctx, before)
	require.NoError(t, err)

	first := world.tasks(NewTask{Ref: "first", Profile: "reviewer", Spec: reviewSpec})[0]
	later := world.tasks(NewTask{Ref: "later", Profile: "reviewer", Spec: reviewSpec})[0]

	for _, v := range []struct{ task, head, verdict string }{
		{first.ID, statusHead, "incorrect"}, {later.ID, statusNewHead, "correct"},
	} {
		world.advance(time.Minute)
		_, err = world.db.ExecContext(ctx, `INSERT INTO review_verdicts (task_id, pull_request, head_sha, verdict,
			created_at) VALUES (?, ?, ?, ?, ?)`, v.task, reviewPR, v.head, v.verdict, world.clock.now.UnixMilli())
		require.NoError(t, err)
	}

	_, err = p.UpTo(ctx, own)
	require.NoError(t, err)
	_, err = p.Up(ctx)
	require.NoError(t, err)

	h := &statusHost{state: githost.RequestState{Status: githost.RequestOpen, Head: statusNewHead}}
	require.NoError(t, world.s.PostReviewStatuses(ctx, h.host()))
	require.Len(t, h.posted, 1)
	assert.Equal(t, githost.StatusSuccess, h.posted[0].State)
	assert.Equal(t, statusNewHead, h.posted[0].Head)
	assert.Equal(t, later.ID, h.posted[0].TaskID)
	assert.Equal(t, 1, h.reads, "only the latest verdict is queued")
}
