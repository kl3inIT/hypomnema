package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// Following a pull request after its Task (issue #1053). A worker that turned the host's auto-merge on, pinned to
// its head, finishes saying "auto-merge on at <sha>" without waiting for CI (node/runner's prompt). Auto-merge
// lands the head once it is ready; what can still go wrong after the Task succeeded is a conflict with the base
// or a required check that fails, and then nothing lands. So the control plane follows such a pull request, as
// pstack's watcher follows an armed pull request until it reads merged (skills/poteto-mode/playbooks/
// shipping.md:8-9; scripts/watch-pr/policy.ts:59-140 reads merged, closed, a conflict and failed checks, every
// 60 s by default, cli.ts:110). A conflict comes before CI, and a flake earns one fresh build that the author
// gives it, as pstack's babysit does (babysit.md:8); the author is sent a follow-up on its succeeded Task, the
// existing path for more work on it (followups.go). Merged or closed wakes the coordinator, which then finishes
// the Run or decides. Each Dispatch's report is settled once: a follow-up's new Dispatch reports again.

// autoMergeOn reads the head a worker's summary says auto-merge is on at.
var autoMergeOn = regexp.MustCompile("(?i)auto-merge (?:is )?on at [`\"'*]*([0-9a-f]{7,40})\\b")

// FollowedRequest is a pull request the control plane follows: the one a succeeded Task's last Dispatch
// reported with auto-merge on at Head.
type FollowedRequest struct {
	TaskID     string
	RunID      string
	DispatchID string
	URL        string
	Kind       githost.Kind
	Request    githost.Request
	Head       string
}

// PullRequestOutcome is what settling a followed pull request did.
type PullRequestOutcome string

// The outcomes of a followed pull request.
const (
	// PullRequestPending: still open and nothing wrong yet; it is read again later.
	PullRequestPending PullRequestOutcome = "pending"
	// PullRequestMerged and PullRequestClosed: it is no longer open; the coordinator is woken.
	PullRequestMerged PullRequestOutcome = "merged"
	PullRequestClosed PullRequestOutcome = "closed"
	// PullRequestConflict and PullRequestFailed: the author was sent a follow-up, or the coordinator is told
	// why none could be sent.
	PullRequestConflict PullRequestOutcome = "conflict"
	PullRequestFailed   PullRequestOutcome = "failed"
	// PullRequestUnfollowed: the control plane cannot read it (no token for its repository); its worker waited
	// for its checks itself, and the coordinator is told once (review round 2).
	PullRequestUnfollowed PullRequestOutcome = "unfollowed"
	// PullRequestGone: the Run, the Task or its report moved on while the host was read; nothing was written.
	PullRequestGone PullRequestOutcome = "gone"
)

// ErrCannotFollow says the control plane cannot read a followed pull request: it holds no token for its
// repository, whose worker used the node's own login.
var ErrCannotFollow = errors.New("the control plane holds no token for the repository")

// PullRequestsToFollow lists the pull requests to read: of each succeeded, non-review Task of an open Run whose
// last Dispatch's summary says auto-merge is on at a head and names one pull request, and that was not settled
// yet.
func (s *Service) PullRequestsToFollow(ctx context.Context) ([]FollowedRequest, error) {
	q := s.read()

	tasks, err := q.SucceededTasksOfOpenRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the succeeded tasks of open runs: %w", err)
	}

	var out []FollowedRequest

	for _, task := range tasks {
		f, ok, err := followedRequestOf(ctx, q, task)
		if err != nil {
			return nil, err
		}

		if ok {
			out = append(out, f)
		}
	}

	return out, nil
}

func followedRequestOf(ctx context.Context, q *store.Queries, task store.Task) (FollowedRequest, bool, error) {
	if reviewerProfile(task.Profile) {
		return FollowedRequest{}, false, nil
	}

	d, err := q.LastDispatchOfTask(ctx, task.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return FollowedRequest{}, false, nil
	}

	if err != nil {
		return FollowedRequest{}, false, fmt.Errorf("reading the last dispatch of task %s: %w", task.ID, err)
	}

	result, ok := dispatchResult(d)
	if !ok {
		return FollowedRequest{}, false, nil
	}

	m := autoMergeOn.FindStringSubmatch(result.Summary)
	if m == nil {
		return FollowedRequest{}, false, nil
	}

	settled, err := q.PullRequestEscalationOf(ctx, store.PullRequestEscalationOfParams{
		TaskID: nullString(task.ID), DispatchID: nullString(d.ID),
	})
	if err != nil {
		return FollowedRequest{}, false, fmt.Errorf("reading what task %s's pull request did: %w", task.ID, err)
	}

	if settled > 0 {
		return FollowedRequest{}, false, nil
	}

	kindOf := registeredKind(ctx, q)

	url := ""

	for _, e := range result.Evidence {
		if e.Kind != "pull_request" && e.Kind != "merge_request" {
			continue
		}

		pr := canonicalReviewRequest(e.Ref, kindOf)
		if pr == "" {
			continue
		}

		if url != "" && !strings.EqualFold(url, pr) {
			return FollowedRequest{}, false, nil
		}

		url = pr
	}

	if url == "" {
		url = reviewRequest(result.Summary, kindOf)
	}

	kind := githost.GitHub

	request, parsed := githost.ParseRequest(url, func(host string) (githost.Kind, bool) {
		k, known := kindOf(host)
		kind = k

		return k, known
	})
	if !parsed {
		return FollowedRequest{}, false, nil
	}

	return FollowedRequest{
		TaskID: task.ID, RunID: task.RunID, DispatchID: d.ID, URL: url, Kind: kind, Request: request,
		Head: strings.ToLower(m[1]),
	}, true, nil
}

// dispatchResult is the report a Dispatch ended with; false when it has none that can be read.
func dispatchResult(d store.Dispatch) (Result, bool) {
	var result Result

	return result, d.Result.Valid && json.Unmarshal([]byte(d.Result.String), &result) == nil
}

// SettlePullRequest acts on what was read of a followed pull request: nothing while it is open and fine; a
// follow-up to its author on a conflict, else on a failed required check; and, for every outcome but pending, one
// pull_request escalation on its Dispatch, which wakes the coordinator and marks it settled.
func (s *Service) SettlePullRequest(ctx context.Context, f FollowedRequest, st githost.RequestState,
) (PullRequestOutcome, error) {
	outcome, what, note := settling(f, st)
	if outcome == PullRequestPending {
		return outcome, nil
	}

	return s.settlePullRequest(ctx, f, outcome, what, note)
}

// settlePullRequest writes, in one transaction, the follow-up a note asks for and the escalation that says what happened, so
// a stop between them cannot keep one without the other (review round 2). It first reads again what the pass
// read before the host answered: a Run that is no longer open, a Task that is no longer succeeded, a newer
// Dispatch, or a report already settled settles nothing (PullRequestGone).
func (s *Service) settlePullRequest(ctx context.Context, f FollowedRequest, outcome PullRequestOutcome,
	what, note string,
) (PullRequestOutcome, error) {
	gone := false

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		current, err := stillFollowed(ctx, q, f)
		if err != nil || !current {
			gone = err == nil

			return err
		}

		payload := map[string]any{
			"kind": string(EscalationPullRequest), "state": string(outcome), "pull_request": f.URL, "head": f.Head,
			"reason": what,
		}

		if note != "" {
			fid := s.newID("fup")

			switch err := followUpIn(ctx, q, now, f.TaskID, AddressCoordinator, fid, note); {
			case err == nil:
				payload["follow_up"] = fid
			case errors.Is(err, ErrConflict):
				// Refused (the worker's conversation closed): the coordinator decides. A refusal writes nothing.
				payload["follow_up_refused"] = err.Error()
			default:
				return err
			}
		}

		return emit(ctx, q, now, event{
			kind: EventEscalation, run: f.RunID, task: f.TaskID, dispatch: f.DispatchID, payload: payload,
		})
	})
	if err != nil {
		return outcome, fmt.Errorf("recording what the pull request of task %s did: %w", f.TaskID, err)
	}

	if gone {
		return PullRequestGone, nil
	}

	return outcome, nil
}

// stillFollowed reports whether f is still to be settled, inside the transaction that settles it.
func stillFollowed(ctx context.Context, q *store.Queries, f FollowedRequest) (bool, error) {
	task, err := q.GetTask(ctx, f.TaskID)
	if err != nil {
		return false, notFound(err, "task", f.TaskID)
	}

	run, err := q.GetRun(ctx, task.RunID)
	if err != nil {
		return false, notFound(err, "run", task.RunID)
	}

	if RunStatus(run.Status) != RunOpen || TaskStatus(task.Status) != TaskSucceeded {
		return false, nil
	}

	d, err := q.LastDispatchOfTask(ctx, f.TaskID)
	if err != nil {
		return false, fmt.Errorf("reading the last dispatch of task %s: %w", f.TaskID, err)
	}

	if d.ID != f.DispatchID {
		return false, nil
	}

	settled, err := q.PullRequestEscalationOf(ctx, store.PullRequestEscalationOfParams{
		TaskID: nullString(f.TaskID), DispatchID: nullString(f.DispatchID),
	})
	if err != nil {
		return false, fmt.Errorf("reading what task %s's pull request did: %w", f.TaskID, err)
	}

	return settled == 0, nil
}

// settling decides, from what was read: the outcome, a short reason, and the follow-up's note when the author
// has work to do.
func settling(f FollowedRequest, st githost.RequestState) (PullRequestOutcome, string, string) {
	head := st.Head
	if head == "" {
		head = f.Head
	}

	again := " Push the fix, turn auto-merge on again at the new head as your merge steps say, and call `finish` " +
		"again, saying \"auto-merge on at <sha>\"."

	switch {
	case st.Status == githost.RequestMerged:
		return PullRequestMerged, "merged", ""
	case st.Status == githost.RequestClosed:
		return PullRequestClosed, "closed without a merge", ""
	case st.Conflict:
		return PullRequestConflict, "conflicts with its base at " + short(head),
			fmt.Sprintf("Your pull request %s conflicts with its base at %s, after you finished with auto-merge on, "+
				"so it cannot merge. Merge the base into your branch, resolve the conflict keeping the intent of "+
				"both sides, and rerun the checks the merge affects.%s", f.URL, short(head), again)
	case len(st.Failed) > 0:
		checks := strings.Join(st.Failed, ", ")

		return PullRequestFailed, "required checks failed at " + short(head) + ": " + checks,
			fmt.Sprintf("The required checks of your pull request %s failed at %s, after you finished with "+
				"auto-merge on: %s. Fix a failure your diff causes; a failure outside your diff gets one rerun "+
				"and an issue, as your rules say for a flake.%s If the rerun is green and nothing else changed, "+
				"auto-merge is still on: call `finish` again, saying \"auto-merge on at %s\".",
				f.URL, short(head), checks, again, short(head))
	default:
		return PullRequestPending, "", ""
	}
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}

	return sha
}

// pullRequestWakeText is what a pull_request escalation asks of the coordinator.
func pullRequestWakeText(e store.Event, p map[string]any) string {
	str := func(k string) string { v, _ := p[k].(string); return v }

	at := fmt.Sprintf("Pull request %s of task %s (run %s)", str("pull_request"), e.TaskID.String, e.RunID.String)

	switch PullRequestOutcome(str("state")) {
	case PullRequestMerged:
		return fmt.Sprintf("%s merged. Finish the Run if nothing else is left: hypomnema runs show %s --json",
			at, e.RunID.String)
	case PullRequestClosed:
		return fmt.Sprintf("%s was closed without a merge: hypomnema tasks show %s --json", at, e.TaskID.String)
	case PullRequestUnfollowed:
		return fmt.Sprintf("%s has auto-merge on, but the control plane holds no token for its repository and "+
			"does not follow it; its worker waited for its checks. Read it with the host's CLI, then finish the Run "+
			"if nothing else is left: hypomnema tasks show %s --json", at, e.TaskID.String)
	case PullRequestPending, PullRequestConflict, PullRequestFailed, PullRequestGone:
	}

	if refused := str("follow_up_refused"); refused != "" {
		return fmt.Sprintf("%s %s. A follow-up to its worker was refused (%s): create a Task with "+
			"\"continues\": %q for it: hypomnema tasks show %s --json", at, str("reason"), refused, e.TaskID.String,
			e.TaskID.String)
	}

	return fmt.Sprintf("%s %s. The control plane sent its worker follow-up %s; its end wakes you. Keep the Run "+
		"open: hypomnema tasks show %s --json", at, str("reason"), str("follow_up"), e.TaskID.String)
}

// ReadPullRequest reads a followed pull request at its host; the control plane gives it the Connection's token.
type ReadPullRequest func(ctx context.Context, f FollowedRequest) (githost.RequestState, error)

// FollowPullRequests reads each pull request to follow once and settles it. A read that fails leaves that pull
// request for the next pass; its error is returned with the others, joined, after every one was tried.
func (s *Service) FollowPullRequests(ctx context.Context, read ReadPullRequest) error {
	follow, err := s.PullRequestsToFollow(ctx)
	if err != nil {
		return err
	}

	var errs []error

	for _, f := range follow {
		st, err := read(ctx, f)
		if errors.Is(err, ErrCannotFollow) {
			if _, err := s.settlePullRequest(ctx, f, PullRequestUnfollowed, "not followed: no token for its repository",
				""); err != nil {
				errs = append(errs, err)
			}

			continue
		}

		if err != nil {
			errs = append(errs, fmt.Errorf("reading %s: %w", f.URL, err))

			continue
		}

		if _, err := s.SettlePullRequest(ctx, f, st); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
