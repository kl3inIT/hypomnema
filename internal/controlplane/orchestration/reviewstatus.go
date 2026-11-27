package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// The `review` commit status (issue #1279). A narrow or broad Task's pull request merges only after a reviewer
// judged its head correct; the control plane says so on the head itself, as a commit status the owner makes a
// required check, as claude-code-action's agent-approval-check posts one on the head it judged
// (agent-approval-check/README.md, "Mark agent-approval-check as a required status check"). A status is of one
// commit: a push makes a new head without one, which needs its own review (OpenClaw scripts/pr stamps a review to
// the exact head), kept cheap by reviewedHead's `git diff <reviewed_sha>..<head>`.
//
// What is posted, and when:
//   - a verdict recorded with its head (recordReviewVerdict): correct is success, incorrect or blocked failure
//     with the review's first finding;
//   - a quick Task's pull request, when the follower reads its head (FollowPullRequests): success, "no review
//     needed", as the coordinator's guide says quick work needs green CI and no worker review.
//
// Each is queued in the transaction that learns it and posted by the control plane's follower with the token of
// the repository's Connection, the one its pull requests are read with. Before posting, the pull request is read:
// one no longer open, or whose head is another than the queued one, gets nothing; a repository without a token
// gets nothing (ErrCannotFollow). A post that fails is tried again by the next pass, for reviewStatusWindow.
// Pull requests no Task made (Dependabot's, the owner's own branches) are not followed and get nothing: the owner
// posts theirs by hand (docs/ci.md).

// ReviewStatusContext is the name of the commit status, the required check the owner adds.
const ReviewStatusContext = "review"

// reviewStatusWindow is how long a queued status is tried before it is dropped, as a branch without a pull
// request is dropped after its lookup window (lookUpWindow; Coder's NoPRRetryWindow).
const reviewStatusWindow = 24 * time.Hour

// The outcomes of a queued status.
const (
	statusPosted     = "posted"
	statusClosed     = "closed"
	statusMoved      = "moved"
	statusUnfollowed = "unfollowed"
	statusExpired    = "expired"
)

// ReviewStatus is a `review` status to post: State on Head, the full SHA read from the host, of the pull request,
// decided by Task TaskID.
type ReviewStatus struct {
	PullRequest string
	Kind        githost.Kind
	Request     githost.Request
	Head        string
	State       githost.StatusState
	Description string
	TaskID      string
}

// ReviewStatusHost reads a pull request and posts a status at its host; the control plane gives it the
// repository's token. Either says ErrCannotFollow when it holds none.
type ReviewStatusHost struct {
	Read func(ctx context.Context, kind githost.Kind, r githost.Request) (githost.RequestState, error)
	Post func(ctx context.Context, s ReviewStatus) error
}

// queueVerdictStatus queues the status of a verdict on head, replacing what that head had.
func queueVerdictStatus(ctx context.Context, q *store.Queries, now int64, taskID, pr, head, verdict string) error {
	if err := q.QueueReviewVerdictStatus(ctx, store.QueueReviewVerdictStatusParams{
		PullRequest: pr, HeadSha: head, TaskID: taskID, Reason: verdict, Now: now,
	}); err != nil {
		return fmt.Errorf("queueing the review status of %s: %w", pr, err)
	}

	return nil
}

// queueNoReview queues success on the head of a followed pull request whose Task needs no review.
func (s *Service) queueNoReview(ctx context.Context, f FollowedRequest) error {
	err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.QueueNoReviewStatus(ctx, store.QueueNoReviewStatusParams{
			PullRequest: f.URL, HeadSha: f.Head, TaskID: f.TaskID, Now: now,
		})
	})
	if err != nil {
		return fmt.Errorf("queueing the review status of %s: %w", f.URL, err)
	}

	return nil
}

// PostReviewStatuses posts each queued status whose pull request is still open at its head, and drops the
// others. A read or post that fails leaves its status for the next pass; the errors are returned joined, after
// every one was tried.
func (s *Service) PostReviewStatuses(ctx context.Context, host ReviewStatusHost) error {
	q := s.read()

	waiting, err := q.WaitingReviewStatuses(ctx)
	if err != nil {
		return fmt.Errorf("listing the review statuses to post: %w", err)
	}

	kindOf := registeredKind(ctx, q)

	var errs []error

	for _, row := range waiting {
		outcome, err := s.postReviewStatus(ctx, q, host, kindOf, row)
		if err != nil {
			errs = append(errs, fmt.Errorf("posting the review status of %s: %w", row.PullRequest, err))

			continue
		}

		err = s.write(ctx, func(q *store.Queries, now int64) error {
			return q.FinishReviewStatus(ctx, store.FinishReviewStatusParams{
				Now: sql.NullInt64{Int64: now, Valid: true}, Outcome: nullString(outcome), PullRequest: row.PullRequest,
				HeadSha: row.HeadSha, TaskID: row.TaskID, QueuedAt: row.QueuedAt,
			})
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("keeping the review status of %s: %w", row.PullRequest, err))
		}
	}

	return errors.Join(errs...)
}

// postReviewStatus reads row's pull request and posts its status, and says what became of it.
func (s *Service) postReviewStatus(ctx context.Context, q *store.Queries, host ReviewStatusHost,
	kindOf func(string) (githost.Kind, bool), row store.ReviewStatus,
) (string, error) {
	if s.clock.Now().UnixMilli()-row.QueuedAt > reviewStatusWindow.Milliseconds() {
		return statusExpired, nil
	}

	kind := githost.GitHub

	request, ok := githost.ParseRequest(row.PullRequest, func(h string) (githost.Kind, bool) {
		resolved, known := kindOf(h)
		kind = resolved

		return resolved, known
	})
	if !ok {
		return statusUnfollowed, nil
	}

	st, err := host.Read(ctx, kind, request)

	switch {
	case errors.Is(err, ErrCannotFollow):
		return statusUnfollowed, nil
	case err != nil:
		return "", err
	case st.Status != githost.RequestOpen:
		return statusClosed, nil
	}

	head := strings.ToLower(st.Head)
	if head == "" || !sameCommit(head, row.HeadSha) {
		return statusMoved, nil
	}

	task, err := q.GetTask(ctx, row.TaskID)
	if err != nil {
		return "", notFound(err, "task", row.TaskID)
	}

	state, description := reviewStatusOf(row.Reason, task)

	err = host.Post(ctx, ReviewStatus{
		PullRequest: row.PullRequest, Kind: kind, Request: request, Head: head, State: state,
		Description: description, TaskID: row.TaskID,
	})

	switch {
	case errors.Is(err, ErrCannotFollow):
		return statusUnfollowed, nil
	case err != nil:
		return "", err
	}

	return statusPosted, nil
}

// reviewStatusOf is the state and description a reason gives: the reviewer Task's verdict, or why none is needed.
func reviewStatusOf(reason string, task store.Task) (githost.StatusState, string) {
	switch reason {
	case "correct":
		return githost.StatusSuccess, "correct: reviewed by Task " + task.ID
	case "quick":
		return githost.StatusSuccess, "no review needed: quick Task " + task.ID
	}

	finding := firstFinding(task.Result.String)
	if finding == "" {
		finding = "see review Task " + task.ID
	}

	return githost.StatusFailure, reason + ": " + finding
}

// The fields a reviewer's summary starts with (deploy/profiles/reviewer.yaml), which are not findings.
var reviewSummaryFields = []string{"verdict", "head", "evidence", "merge state", "mergeable", "checks"}

// firstFinding is a review summary's first finding: its "Findings:" field, else the first line under a
// "Findings" heading, else the first line of prose before any heading (a blocked review says there what
// stopped it). Markdown emphasis and list marks are dropped.
func firstFinding(text string) string {
	if v := reviewField(text, "Findings"); v != "" {
		return plainFinding(v)
	}

	lines := strings.Split(text, "\n")

	for i, line := range lines {
		if !findingsHeading(line) {
			continue
		}

		for _, next := range lines[i+1:] {
			if strings.TrimSpace(next) != "" {
				return plainFinding(next)
			}
		}
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)

		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#"):
			return ""
		case summaryField(line):
			continue
		default:
			return plainFinding(line)
		}
	}

	return ""
}

func findingsHeading(line string) bool {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "**") {
		return false
	}

	return strings.EqualFold(strings.Trim(line, "#*: \t"), "findings")
}

func summaryField(line string) bool {
	key, _, ok := strings.Cut(strings.TrimLeft(line, "-* "), ":")
	if !ok {
		return false
	}

	key = strings.ToLower(strings.Trim(key, "* "))
	for _, field := range reviewSummaryFields {
		if key == field {
			return true
		}
	}

	return false
}

func plainFinding(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimLeft(line, "-*+ ")
	line = strings.ReplaceAll(line, "**", "")

	return strings.Join(strings.Fields(line), " ")
}
