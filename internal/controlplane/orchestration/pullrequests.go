package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// Following a pull request after its Task (issue #1053). A worker that turned the host's auto-merge on, pinned to
// its head, finishes without waiting for CI (node/runner's prompt). Auto-merge lands the head once it is ready;
// what can still go wrong after the Task succeeded is a conflict with the base or a required check that fails,
// and then nothing lands. So the control plane follows such a pull request, as pstack's watcher follows an armed
// pull request until it reads merged (skills/poteto-mode/playbooks/shipping.md:8-9; scripts/watch-pr/
// policy.ts:59-140 reads merged, closed, a conflict and failed checks, every 60 s by default, cli.ts:110). A
// conflict comes before CI, and a flake earns one fresh build that the author gives it, as pstack's babysit does
// (babysit.md:8); the author is sent a follow-up on its succeeded Task, the existing path for more work on it
// (followups.go). Merged or closed wakes the coordinator, which then finishes the Run or decides. Each Dispatch's
// report is settled once: a follow-up's new Dispatch reports again.
//
// The pull request is found by the Task's branch at the host, not by a sentence in the worker's summary (issue
// #1168), as Coder's gitsync resolves a chat's pull request from its branch (coderd/x/gitsync/gitsync.go:282-343)
// and Orca, t3code and Hermes find one by its head branch. The worker's pull_request evidence is a hint, read
// first and dropped when its head branch is another. What was found is kept (pull_request_lookups), as Coder keeps
// the URL, so a pull request that merged, and is no longer open on its branch, is still read. Whether auto-merge is
// armed is read from the host too: a conflict or a failed check is sent to the author only then, as before only a
// pull request left with auto-merge on was followed; a merge or a close wakes the coordinator either way.

// Looking up a branch's pull request: a branch without one yet is looked up again every lookUpEvery for
// lookUpWindow from its first lookup, then dropped, as Coder's gitsync retries a branch without a pull request
// every 15 s for 2 minutes (NoPRBackoff, NoPRRetryWindow; coderd/x/gitsync/worker.go:38-53).
const (
	LookUpEvery  = 15 * time.Second
	lookUpWindow = 2 * time.Minute
)

// FollowedRequest is a pull request the control plane follows: the one on the branch of a succeeded Task, by its
// last Dispatch. Hint is the pull request read first: the one found before (Found), else the one the worker's
// evidence names; with none the branch is looked up. URL, Request and Head are what was read, once it was.
type FollowedRequest struct {
	TaskID     string
	RunID      string
	DispatchID string
	Kind       githost.Kind
	Repo       githost.Repo
	Branch     string
	Hint       *githost.Request
	Found      bool
	URL        string
	Request    githost.Request
	Head       string

	lookedUp bool
}

// PullRequestOutcome is what settling a followed pull request did.
type PullRequestOutcome string

// The outcomes of a followed pull request.
const (
	// PullRequestPending: still open and nothing wrong yet, or auto-merge is not armed; it is read again later.
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

// PullRequestsToFollow lists the pull requests to read: of each succeeded, non-review Task of an open Run with a
// repository, by its last Dispatch, not settled yet, whose pull request was found, or that is still within its
// lookup window. With found false, those whose pull request was found are left out: a pass every LookUpEvery
// looks up the branches without one, and the minute's pass reads them all.
func (s *Service) PullRequestsToFollow(ctx context.Context, found bool) ([]FollowedRequest, error) {
	q := s.read()
	now := s.clock.Now().UnixMilli()

	tasks, err := q.SucceededTasksOfOpenRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the succeeded tasks of open runs: %w", err)
	}

	runs := map[string]store.Run{}

	var out []FollowedRequest

	for _, task := range tasks {
		run, ok := runs[task.RunID]
		if !ok {
			if run, err = q.GetRun(ctx, task.RunID); err != nil {
				return nil, notFound(err, "run", task.RunID)
			}

			runs[task.RunID] = run
		}

		f, ok, err := followedRequestOf(ctx, q, run, task, now)
		if err != nil {
			return nil, err
		}

		if ok && (found || !f.Found) {
			out = append(out, f)
		}
	}

	return out, nil
}

func followedRequestOf(ctx context.Context, q *store.Queries, run store.Run, task store.Task, now int64,
) (FollowedRequest, bool, error) {
	if reviewerProfile(task.Profile) || run.Repo.String == "" || run.RepoHost.String == "" {
		return FollowedRequest{}, false, nil
	}

	d, err := q.LastDispatchOfTask(ctx, task.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return FollowedRequest{}, false, nil
	}

	if err != nil {
		return FollowedRequest{}, false, fmt.Errorf("reading the last dispatch of task %s: %w", task.ID, err)
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

	host, err := gitHostOf(ctx, q, run.RepoHost.String)
	if err != nil {
		return FollowedRequest{}, false, nil //nolint:nilerr // a host no longer registered is not followed
	}

	f := FollowedRequest{
		TaskID: task.ID, RunID: task.RunID, DispatchID: d.ID, Kind: host.Kind,
		Repo: githost.Repo{Host: run.RepoHost.String, Path: run.Repo.String}, Branch: pushBranchOf(task),
	}

	kindOf := registeredKind(ctx, q)

	lookup, err := q.PullRequestLookupOf(ctx, d.ID)

	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return FollowedRequest{}, false, fmt.Errorf("reading the pull request found for task %s: %w", task.ID, err)
	case lookup.Url != "":
		f.lookedUp, f.Found = true, true
		f.Hint = hintOf(lookup.Url, f.Repo, kindOf)

		return f, f.Hint != nil, nil
	case now-lookup.Since > lookUpWindow.Milliseconds():
		return FollowedRequest{}, false, nil
	default:
		f.lookedUp = true
	}

	if result, ok := dispatchResult(d); ok {
		f.Hint = evidenceHint(result, f.Repo, kindOf)
	}

	return f, true, nil
}

// evidenceHint is the one pull request of the Run's repository a report's evidence names, if it names one.
func evidenceHint(result Result, repo githost.Repo, kindOf func(string) (githost.Kind, bool)) *githost.Request {
	var hint *githost.Request

	for _, e := range result.Evidence {
		if e.Kind != "pull_request" && e.Kind != "merge_request" {
			continue
		}

		r := hintOf(e.Ref, repo, kindOf)
		if r == nil {
			continue
		}

		if hint != nil && *hint != *r {
			return nil
		}

		hint = r
	}

	return hint
}

// hintOf reads a pull request's URL, kept only when it is of repo.
func hintOf(ref string, repo githost.Repo, kindOf func(string) (githost.Kind, bool)) *githost.Request {
	r, ok := githost.ParseRequest(ref, kindOf)
	if !ok || !strings.EqualFold(r.Repo.Host, repo.Host) || !strings.EqualFold(r.Repo.Path, repo.Path) {
		return nil
	}

	r.Repo = repo

	return &r
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

			switch err := s.followUpIn(ctx, q, now, f.TaskID, AddressCoordinator, fid, note); {
			case err == nil:
				payload["follow_up"] = fid
			case errors.Is(err, ErrConflict):
				// Refused (the worker's conversation closed): the coordinator decides. A refusal writes nothing.
				payload["follow_up_refused"] = err.Error()
			default:
				return err
			}
		}

		if outcome == PullRequestMerged {
			if err := s.inbox.CloseMergedReviewStallsTx(ctx, q, now, f.URL); err != nil {
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
	case !st.AutoMerge:
		// Open without auto-merge: a review or the owner merges it, and its merge or close wakes the coordinator.
		return PullRequestPending, "", ""
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
		return fmt.Sprintf("%s is not followed: the control plane holds no token for its repository; its worker "+
			"waited for its checks. Read it with the host's CLI, then finish the Run "+
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

// ReadPullRequest finds and reads a followed pull request at its host (githost.FindRequest); the control plane
// gives it the Connection's token. githost.ErrNoRequest says the branch has none open.
type ReadPullRequest func(ctx context.Context, f FollowedRequest) (githost.Request, githost.RequestState, error)

// FollowPullRequests reads each pull request to follow once and settles it (with found false, only the branches
// whose pull request was not found yet). A branch without one is noted, so its lookup window runs from its first
// lookup; a pull request found is kept. A read that fails leaves that pull request for the next pass; its error is
// returned with the others, joined, after every one was tried.
func (s *Service) FollowPullRequests(ctx context.Context, read ReadPullRequest, found bool) error {
	follow, err := s.PullRequestsToFollow(ctx, found)
	if err != nil {
		return err
	}

	var errs []error
	seen := map[string]bool{}

	for _, f := range follow {
		r, st, err := read(ctx, f)

		switch {
		case errors.Is(err, ErrCannotFollow) && f.Hint != nil:
			f.URL = requestURL(f.Kind, *f.Hint)
			if _, err := s.settlePullRequest(ctx, f, PullRequestUnfollowed, "not followed: no token for its repository",
				""); err != nil {
				errs = append(errs, err)
			}
		case errors.Is(err, ErrCannotFollow), errors.Is(err, githost.ErrNoRequest):
			// Nothing to read: the lookup window runs from the first time, and the branch is dropped after it.
			if !f.lookedUp {
				errs = append(errs, s.notePullRequest(ctx, f, ""))
			}
		case err != nil:
			errs = append(errs, fmt.Errorf("reading the pull request of %s: %w", f.Branch, err))
		default:
			f.Request, f.URL = r, requestURL(f.Kind, r)
			if st.Head != "" {
				f.Head = strings.ToLower(st.Head)
			}

			if !f.Found || f.Hint == nil || *f.Hint != r {
				if err := s.notePullRequest(ctx, f, f.URL); err != nil {
					errs = append(errs, err)

					continue
				}
			}

			outcome, err := s.SettlePullRequest(ctx, f, st)
			if err != nil {
				errs = append(errs, err)
			} else if found && outcome != PullRequestGone {
				seen[strings.ToLower(f.URL)] = true
			}
		}
	}

	if found {
		errs = append(errs, s.followReviewStallRequests(ctx, read, seen))
	}

	return errors.Join(errs...)
}

// notePullRequest keeps what a lookup of f's branch found: url, or an empty one for none.
func (s *Service) notePullRequest(ctx context.Context, f FollowedRequest, url string) error {
	err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.NotePullRequestLookup(ctx, store.NotePullRequestLookupParams{
			DispatchID: f.DispatchID, Url: url, Now: now,
		})
	})
	if err != nil {
		return fmt.Errorf("keeping the pull request of task %s: %w", f.TaskID, err)
	}

	return nil
}

func requestURL(kind githost.Kind, r githost.Request) string {
	adapter, ok := githost.For(kind)
	if !ok {
		return ""
	}

	return adapter.RequestURL(r.Repo, r.Number)
}

// SettleClosedPullRequest settles, without reading the host, the followed pull request a pull_request event of the
// GitHub App said merged or closed (issue #1262): the one found on a succeeded Task's branch, as the minute's pass
// would settle it on its next read. It says how many it settled; one not found yet is left to that pass.
func (s *Service) SettleClosedPullRequest(ctx context.Context, r githost.Request, st githost.RequestState,
) (int, error) {
	if st.Status != githost.RequestMerged && st.Status != githost.RequestClosed {
		return 0, nil
	}

	if st.Status == githost.RequestMerged {
		if kind, known := registeredKind(ctx, s.read())(r.Repo.Host); known {
			if err := s.closeMergedReviewStalls(ctx, requestURL(kind, r)); err != nil {
				return 0, err
			}
		}
	}

	follow, err := s.PullRequestsToFollow(ctx, true)
	if err != nil {
		return 0, err
	}

	settled := 0

	for _, f := range follow {
		if !f.Found || f.Hint == nil || f.Hint.Number != r.Number || !strings.EqualFold(f.Repo.Host, r.Repo.Host) ||
			!strings.EqualFold(f.Repo.Path, r.Repo.Path) {
			continue
		}

		f.Request, f.URL = *f.Hint, requestURL(f.Kind, *f.Hint)
		if st.Head != "" {
			f.Head = strings.ToLower(st.Head)
		}

		outcome, err := s.SettlePullRequest(ctx, f, st)
		if err != nil {
			return settled, err
		}

		if outcome != PullRequestGone {
			settled++
		}
	}

	return settled, nil
}
