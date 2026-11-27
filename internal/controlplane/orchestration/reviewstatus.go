package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
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
//   - a quick Task's pull request: success, "no review needed", on each of its heads, as the coordinator's guide
//     says quick work needs green CI and no worker review. The Task is kept when it succeeds (keepNoReview; the
//     migration keeps those that succeeded before), whatever becomes of its Run, which the coordinator may finish
//     at once (review round 1); each pass finds its pull request by the Task's branch, as the follower does, and
//     queues a head only when a quick Task pushed it (quickHead): a narrow or broad Task that continues it, in any Run,
//     makes the heads it pushes need their review (review rounds 2 and 3).
//
// Each is queued in the transaction that learns it and posted by the control plane's follower with the token of
// the repository's Connection, the one its pull requests are read with. Before posting, the pull request is read:
// one no longer open, or whose head is not exactly the queued one, gets nothing; a repository without a token
// gets nothing (ErrCannotFollow). A head is a full SHA: a verdict naming an abbreviated head is not queued, as a
// prefix may name a newer commit too (review round 1). A post that fails is tried again by the next pass, for reviewStatusWindow.
// Pull requests no Task made (Dependabot's, the owner's own branches) are not followed and get nothing: the owner
// posts theirs by hand (docs/ci.md).

// ReviewStatusContext is the name of the commit status, the required check the owner adds.
const ReviewStatusContext = "review"

// reviewStatusWindow is how long a queued status is tried before it is dropped, as a branch without a pull
// request is dropped after its lookup window (lookUpWindow; Coder's NoPRRetryWindow).
const reviewStatusWindow = 24 * time.Hour

// A quick Task's branch without a pull request yet is looked up every pass for noRequestFastFor, then every
// noRequestSlowEvery, for reviewStatusWindow (review round 2). As the follower's lookup (Coder's NoPRBackoff then
// NoPRRetryWindow, coderd/x/gitsync/worker.go:38-53), but kept for the day a slow worker may take to open it; the
// slower phase, which bounds the host's requests for a branch that never gets one, is this change's choice.
const (
	noRequestFastFor   = 10 * time.Minute
	noRequestSlowEvery = 15 * time.Minute
)

// The outcomes of a queued status.
const (
	statusPosted     = "posted"
	statusClosed     = "closed"
	statusMoved      = "moved"
	statusUnfollowed = "unfollowed"
	statusExpired    = "expired"
	statusSuperseded = "superseded"
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

// ReviewStatusHost finds a branch's pull request, reads one and posts a status at its host; the control plane
// gives it the repository's token. Each says ErrCannotFollow when it holds none.
type ReviewStatusHost struct {
	Find ReadPullRequest
	Read func(ctx context.Context, kind githost.Kind, r githost.Request) (githost.RequestState, error)
	Post func(ctx context.Context, s ReviewStatus) error
}

// fullSHA is a whole commit SHA, lowercase hex, as a host names a head.
var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// queueVerdictStatus queues the status of a verdict on head, replacing what that head had; an abbreviated head is
// not queued.
func queueVerdictStatus(ctx context.Context, q *store.Queries, now int64, taskID, pr, head, verdict string) error {
	if !fullSHA.MatchString(head) {
		return nil
	}

	if err := q.QueueReviewVerdictStatus(ctx, store.QueueReviewVerdictStatusParams{
		PullRequest: pr, HeadSha: head, TaskID: taskID, Reason: verdict, Now: now,
	}); err != nil {
		return fmt.Errorf("queueing the review status of %s: %w", pr, err)
	}

	return nil
}

// keepNoReview keeps a quick Task that succeeded with a repository, so its pull request gets success "no review
// needed" (review round 1): with the pull request its report's evidence names, if any, as the follower's hint.
func keepNoReview(ctx context.Context, q *store.Queries, now int64, task store.Task) error {
	if task.Kind != "quick" || reviewerProfile(task.Profile) {
		return nil
	}

	run, err := q.GetRun(ctx, task.RunID)
	if err != nil {
		return notFound(err, "run", task.RunID)
	}

	if run.Repo.String == "" || run.RepoHost.String == "" {
		return nil
	}

	repo := githost.Repo{Host: run.RepoHost.String, Path: run.Repo.String}
	hint := ""

	if host, err := gitHostOf(ctx, q, repo.Host); err == nil {
		d, err := q.LastDispatchOfTask(ctx, task.ID)

		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return fmt.Errorf("reading the last dispatch of task %s: %w", task.ID, err)
		default:
			if result, ok := dispatchResult(d); ok {
				if r := evidenceHint(result, repo, registeredKind(ctx, q)); r != nil {
					hint = requestURL(host.Kind, *r)
				}
			}
		}
	}

	if err := q.KeepNoReviewRequest(ctx, store.KeepNoReviewRequestParams{
		TaskID: task.ID, RepoHost: repo.Host, Repo: repo.Path, Branch: pushBranchOf(task), PullRequest: hint,
		Now: now,
	}); err != nil {
		return fmt.Errorf("keeping quick task %s for its review status: %w", task.ID, err)
	}

	return nil
}

// findNoReviewPullRequests finds the pull request of each kept quick Task and queues success on its head; one no
// longer open, of a repository without a token, of a host no longer registered, without a pull request within
// the lookup window, or kept longer than reviewStatusWindow is let go.
func (s *Service) findNoReviewPullRequests(ctx context.Context, host ReviewStatusHost) error {
	q := s.read()

	kept, err := q.OpenNoReviewRequests(ctx)
	if err != nil {
		return fmt.Errorf("listing the quick tasks to find pull requests of: %w", err)
	}

	kindOf := registeredKind(ctx, q)

	var errs []error

	for _, row := range kept {
		done, err := s.findNoReview(ctx, q, host, kindOf, row)
		if err != nil {
			errs = append(errs, fmt.Errorf("finding the pull request of quick task %s: %w", row.TaskID, err))

			continue
		}

		if !done {
			continue
		}

		err = s.write(ctx, func(q *store.Queries, now int64) error {
			return q.EndNoReviewRequest(ctx, store.EndNoReviewRequestParams{
				Now: sql.NullInt64{Int64: now, Valid: true}, TaskID: row.TaskID, Since: row.Since,
			})
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("letting go of quick task %s: %w", row.TaskID, err))
		}
	}

	return errors.Join(errs...)
}

// quickHead reports whether head of branch was pushed by a quick Task: a Task that continues a quick one takes
// its branch and pull request, and the heads it pushes need what its own kind needs (review rounds 2 and 3). The
// Task that decides is the one whose Dispatch reported publishing that head; for a head no Dispatch reported (a
// push without publish_branch, or a node too old to report it), the Task pushing to branch whose Dispatch started
// last, so a Task created later but never run, such as one canceled before it ran, decides nothing.
func quickHead(ctx context.Context, q *store.Queries, branch, head string) (bool, error) {
	pusher, err := q.TaskThatPushed(ctx, store.TaskThatPushedParams{Branch: branch, Sha: head})
	if errors.Is(err, sql.ErrNoRows) {
		pusher, err = q.TaskLastStartedOn(ctx, branch)
	}

	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("reading the task that pushed %s to %s: %w", head, branch, err)
	}

	return pusher.Kind == "quick", nil
}

// findNoReview finds row's pull request, queues success on its open head, and says whether row is done.
func (s *Service) findNoReview(ctx context.Context, q *store.Queries, host ReviewStatusHost,
	kindOf func(string) (githost.Kind, bool), row store.NoReviewRequest,
) (bool, error) {
	now := s.clock.Now().UnixMilli()
	if now-row.Since > reviewStatusWindow.Milliseconds() {
		return true, nil
	}

	if row.PullRequest == "" && now-row.Since >= noRequestFastFor.Milliseconds() &&
		now-row.LookedAt < noRequestSlowEvery.Milliseconds() {
		return false, nil
	}

	registered, err := gitHostOf(ctx, q, row.RepoHost)
	if err != nil {
		return true, nil //nolint:nilerr // a host no longer registered is not followed
	}

	repo := githost.Repo{Host: row.RepoHost, Path: row.Repo}
	f := FollowedRequest{TaskID: row.TaskID, Kind: registered.Kind, Repo: repo, Branch: row.Branch}

	if row.PullRequest != "" {
		f.Hint = hintOf(row.PullRequest, repo, kindOf)
	}

	r, st, err := host.Find(ctx, f)

	switch {
	case errors.Is(err, ErrCannotFollow):
		return true, nil
	case errors.Is(err, githost.ErrNoRequest):
		err := s.write(ctx, func(q *store.Queries, now int64) error {
			return q.LookedUpNoReviewRequest(ctx, store.LookedUpNoReviewRequestParams{Now: now, TaskID: row.TaskID})
		})

		return false, err
	case err != nil:
		return false, err
	}

	url := requestURL(registered.Kind, r)
	head := strings.ToLower(st.Head)

	quick := false
	if st.Status == githost.RequestOpen && fullSHA.MatchString(head) {
		if quick, err = quickHead(ctx, q, row.Branch, head); err != nil {
			return false, err
		}
	}

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		if url != row.PullRequest {
			if err := q.NoteNoReviewRequest(ctx, store.NoteNoReviewRequestParams{
				PullRequest: url, TaskID: row.TaskID,
			}); err != nil {
				return err
			}
		}

		if !quick {
			return nil
		}

		return q.QueueNoReviewStatus(ctx, store.QueueNoReviewStatusParams{
			PullRequest: url, HeadSha: head, TaskID: row.TaskID, Now: now,
		})
	})
	if err != nil {
		return false, fmt.Errorf("queueing the review status of %s: %w", url, err)
	}

	// A head another kind of Task pushed hands the pull request to that Task: its heads need what it needs.
	return st.Status != githost.RequestOpen || (fullSHA.MatchString(head) && !quick), nil
}

// PostReviewStatuses posts each queued status whose pull request is still open at its head, and drops the
// others. A read or post that fails leaves its status for the next pass; the errors are returned joined, after
// every one was tried.
func (s *Service) PostReviewStatuses(ctx context.Context, host ReviewStatusHost) error {
	var errs []error
	if err := s.findNoReviewPullRequests(ctx, host); err != nil {
		errs = append(errs, err)
	}

	q := s.read()

	waiting, err := q.WaitingReviewStatuses(ctx)
	if err != nil {
		return fmt.Errorf("listing the review statuses to post: %w", err)
	}

	kindOf := registeredKind(ctx, q)

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
	if head != row.HeadSha {
		return statusMoved, nil
	}

	task, err := q.GetTask(ctx, row.TaskID)
	if err != nil {
		return "", notFound(err, "task", row.TaskID)
	}

	if row.Reason == "quick" {
		quick, err := quickHead(ctx, q, pushBranchOf(task), head)
		if err != nil {
			return "", err
		}

		if !quick {
			return statusSuperseded, nil
		}
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
