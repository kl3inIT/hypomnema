package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

const maxReviewRounds = 3

type ReviewStall struct {
	TaskID          string `json:"task_id"`
	PullRequest     string `json:"pull_request"`
	Verdict         string `json:"verdict"`
	MergeState      string `json:"merge_state"`
	Mergeable       string `json:"mergeable"`
	Checks          string `json:"checks"`
	WaitingForOwner bool   `json:"waiting_for_owner"`
}

func reviewField(text, name string) string {
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-* "))
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.Trim(key, "* "), name) {
			return strings.Trim(value, "*` \t\r")
		}
	}

	return ""
}

var reviewURL = regexp.MustCompile(`https://[^\s<>\x60"'\[\]()*]+`)
var reviewTargetLine = regexp.MustCompile(`(?i)^(?:#{1,6}\s+)?(?:please\s+)?review(?:ed)?\s+` +
	`(?:(?:the|this)\s+)?(?:pull request|merge request|pr|mr)\b[ :\t]*`)

func reviewRequest(text string, kindOf func(string) (githost.Kind, bool)) string {
	target := ""
	for _, ref := range reviewURL.FindAllString(text, -1) {
		if pr := canonicalReviewRequest(strings.TrimRight(ref, ".,;:!?"), kindOf); pr != "" {
			if target != "" && !strings.EqualFold(target, pr) {
				return ""
			}
			target = pr
		}
	}

	return target
}

func statedReviewTarget(text string, kindOf func(string) (githost.Kind, bool)) string {
	if pr := reviewTargetValue(reviewField(text, "Review pull request"), kindOf); pr != "" {
		return pr
	}
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if match := reviewTargetLine.FindStringIndex(line); match != nil {
			if pr := reviewTargetValue(line[match[1]:], kindOf); pr != "" {
				return pr
			}
		}
	}
	return ""
}

func reviewTargetValue(text string, kindOf func(string) (githost.Kind, bool)) string {
	text = strings.TrimLeft(text, " \t<`*(")
	if match := reviewURL.FindStringIndex(text); match != nil && match[0] == 0 {
		return canonicalReviewRequest(strings.TrimRight(text[:match[1]], ".,;:!?"), kindOf)
	}
	return reviewRequest(text, kindOf)
}

func specReviewTarget(spec string, kindOf func(string) (githost.Kind, bool)) string {
	if pr := statedReviewTarget(spec, kindOf); pr != "" {
		return pr
	}
	heading, _, _ := strings.Cut(strings.TrimSpace(spec), "\n")
	if pr := canonicalReviewRequest(strings.Trim(heading, "<>` \r\t"), kindOf); pr != "" {
		return pr
	}
	return reviewRequest(spec, kindOf)
}

func canonicalReviewRequest(ref string, kindOf func(string) (githost.Kind, bool)) string {
	kind := githost.GitHub
	request, ok := githost.ParseRequest(ref, func(host string) (githost.Kind, bool) {
		resolved, known := kindOf(host)
		kind = resolved
		return resolved, known
	})
	if !ok {
		return ""
	}
	adapter, _ := githost.For(kind)
	return adapter.RequestURL(request.Repo, request.Number)
}

func reviewerProfile(profile string) bool {
	return profile == "reviewer" || strings.HasPrefix(profile, "reviewer-")
}

func registeredKind(ctx context.Context, q *store.Queries) func(string) (githost.Kind, bool) {
	return func(host string) (githost.Kind, bool) {
		registered, err := gitHostOf(ctx, q, host)
		return registered.Kind, err == nil
	}
}

func reviewReport(ctx context.Context, q *store.Queries, task store.Task, result string) (ReviewStall, error) {
	if !reviewerProfile(task.Profile) {
		return ReviewStall{}, nil
	}
	kindOf := registeredKind(ctx, q)
	pr := statedReviewTarget(result, kindOf)
	if pr == "" {
		var err error
		pr, err = reviewEvidence(ctx, q, task.ID, kindOf)
		if err != nil {
			return ReviewStall{}, err
		}
	}
	if pr == "" {
		pr = reviewRequest(result, kindOf)
	}
	if pr == "" {
		pr = specReviewTarget(task.Spec, kindOf)
	}
	if pr == "" {
		return ReviewStall{}, nil
	}

	verdict := strings.ToLower(reviewField(result, "Verdict"))
	if verdict != "correct" && verdict != "incorrect" {
		verdict = "unknown"
	}

	return ReviewStall{
		PullRequest: pr, Verdict: verdict,
		MergeState: reviewValue(result, "Merge state", "BEHIND", "BLOCKED", "CLEAN", "DIRTY", "DRAFT",
			"HAS_HOOKS", "UNKNOWN", "UNSTABLE"),
		Mergeable: reviewValue(result, "Mergeable", "MERGEABLE", "CONFLICTING", "UNKNOWN"),
		Checks:    reviewValue(result, "Checks", "PASSING", "FAILING", "PENDING", "UNKNOWN"),
	}, nil
}

func reviewEvidence(ctx context.Context, q *store.Queries, taskID string,
	kindOf func(string) (githost.Kind, bool),
) (string, error) {
	dispatch, err := q.LastDispatchOfTask(ctx, taskID)
	if err != nil {
		return "", fmt.Errorf("reading review dispatch: %w", err)
	}
	var result Result
	if !dispatch.Result.Valid {
		return "", nil
	}
	if err := json.Unmarshal([]byte(dispatch.Result.String), &result); err != nil {
		return "", fmt.Errorf("reading review evidence: %w", err)
	}
	target := ""
	for _, evidence := range result.Evidence {
		if evidence.Kind == "pull_request" || evidence.Kind == "merge_request" {
			if pr := canonicalReviewRequest(evidence.Ref, kindOf); pr != "" {
				if target != "" && !strings.EqualFold(target, pr) {
					return "", nil
				}
				target = pr
			}
		}
	}

	return target, nil
}

func reviewValue(result, field string, allowed ...string) string {
	value := strings.ToUpper(reviewField(result, field))
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}

	return "UNKNOWN"
}

func reviewAfterTask(ctx context.Context, q *store.Queries, now int64, task store.Task, result string,
) (ReviewStall, error) {
	review, err := reviewReport(ctx, q, task, result)
	if err != nil || review.PullRequest == "" {
		return ReviewStall{}, err
	}
	if err := recordReviewVerdict(ctx, q, now, task.ID, review.PullRequest, result); err != nil {
		return ReviewStall{}, err
	}

	run, err := q.GetRun(ctx, task.RunID)
	if err != nil {
		return ReviewStall{}, fmt.Errorf("reading review run: %w", err)
	}
	if run.Status != string(RunOpen) {
		return ReviewStall{}, nil
	}

	tasks, err := q.ListTasksByRun(ctx, task.RunID)
	if err != nil {
		return ReviewStall{}, fmt.Errorf("reading review rounds: %w", err)
	}

	rounds := 0
	for _, candidate := range tasks {
		if !TaskStatus(candidate.Status).terminal() {
			return ReviewStall{}, nil
		}
		if candidate.Status != string(TaskSucceeded) {
			continue
		}
		earlier := review
		if candidate.ID != task.ID {
			earlier, err = reviewReport(ctx, q, candidate, candidate.Result.String)
			if err != nil {
				return ReviewStall{}, err
			}
		}
		if earlier.Verdict != "unknown" &&
			strings.EqualFold(earlier.PullRequest, review.PullRequest) {
			rounds++
		}
	}

	review.TaskID = task.ID
	review.WaitingForOwner = review.Verdict == "incorrect" && rounds >= maxReviewRounds

	return review, nil
}

// The coordinator's review reference uses this same recommendation and reply path (issue #1331).
func (s *Service) reportReviewStall(ctx context.Context, q *store.Queries, now int64, task store.Task,
	review ReviewStall, result string,
) (string, error) {
	run, err := q.GetRun(ctx, task.RunID)
	if err != nil {
		return "", fmt.Errorf("reading the review stall's Run: %w", err)
	}

	head, specHead := reviewHead(result), reviewHead(task.Spec)
	if head == "" || (len(specHead) > len(head) && sameCommit(head, specHead)) {
		head = specHead
	}

	notes := fmt.Sprintf("Pull request: <%s>\n\nHead: %s\n\nVerdict: %s\n\n",
		review.PullRequest, or(head, "not reported"), review.Verdict)
	replyTo := "the coordinator"
	if run.ChatID.Valid {
		// InboxMarkdown links chat IDs to the existing chat.
		replyTo += " in " + run.ChatID.String
	}
	notes += "The coordinator’s recommendation follows in the comments.\n\n" +
		"Reply here or to " + replyTo + " with “one more round”, “merge as is and open an issue for the rest”, or “stop”."

	return s.inbox.AddReviewStallTx(ctx, q, now, run.ID, run.ChatID.String, head, review.PullRequest, notes, result)
}

// A Run can review a pull request without having an author Task in the Run. Follow the reported URL in that
// case, using the same host reader and cadence as the author path.
func (s *Service) followReviewStallRequests(ctx context.Context, read ReadPullRequest, seen map[string]bool) error {
	prs, err := s.read().OpenReviewStallRequests(ctx)
	if err != nil {
		return fmt.Errorf("listing pull requests with open review stall to-dos: %w", err)
	}

	kindOf := registeredKind(ctx, s.read())
	var errs []error
	for _, pr := range prs {
		if seen[strings.ToLower(pr)] {
			continue
		}
		var kind githost.Kind
		r, ok := githost.ParseRequest(pr, func(host string) (githost.Kind, bool) {
			var known bool
			kind, known = kindOf(host)
			return kind, known
		})
		if !ok {
			continue
		}

		_, state, err := read(ctx, FollowedRequest{Kind: kind, Repo: r.Repo, Hint: &r})
		switch {
		case errors.Is(err, ErrCannotFollow), errors.Is(err, githost.ErrNoRequest):
		case err != nil:
			errs = append(errs, fmt.Errorf("reading pull request %s for its review stall to-do: %w", pr, err))
		case state.Status == githost.RequestMerged:
			errs = append(errs, s.closeMergedReviewStalls(ctx, pr))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) closeMergedReviewStalls(ctx context.Context, pr string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		return s.inbox.CloseMergedReviewStallsTx(ctx, q, now, pr)
	})
}

func activeReviewStall(ctx context.Context, q *store.Queries, runID string) (*ReviewStall, error) {
	payload, err := q.LatestRunTaskEvent(ctx, nullString(runID))
	if err != nil {
		return nil, fmt.Errorf("reading run review stall: %w", err)
	}

	var event struct {
		Review *ReviewStall `json:"review"`
	}
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return nil, fmt.Errorf("reading review event: %w", err)
	}

	return event.Review, nil
}

// A full or abbreviated commit SHA, as git accepts one (git rev-parse resolves at least 4 hex digits; 7 is its
// default abbreviation, core.abbrev).
var reviewSHA = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// reviewWord is the first word of a summary's field, lowercase: "Verdict: `incorrect` — do not merge" is incorrect.
func reviewWord(text, name string) string {
	fields := strings.Fields(reviewField(text, name))
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(strings.Trim(fields[0], "`*.,;"))
}

// reviewHead is the commit a review summary or spec names on its "Head:" line, or "" (issue #953).
func reviewHead(text string) string {
	head := reviewWord(text, "Head")
	if !reviewSHA.MatchString(head) {
		return ""
	}
	return head
}

// sameCommit reports whether two SHAs, either of them abbreviated, name one commit, as git resolves a prefix.
func sameCommit(a, b string) bool {
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// recordReviewVerdict keeps the verdict of a succeeded review Task with the head it names. pstack keeps a verdict
// per patch (skills/poteto-mode/playbooks/shipping.md:7) and drops a result that does not record its SHAs
// (skills/swarm/SKILL.md:37), so a summary without a readable verdict or "Head:" records nothing (issue #953).
func recordReviewVerdict(ctx context.Context, q *store.Queries, now int64, taskID, pr, result string) error {
	verdict := reviewWord(result, "Verdict")
	head := reviewHead(result)
	if head == "" || (verdict != "correct" && verdict != "incorrect" && verdict != "blocked") {
		return nil
	}
	if err := q.RecordReviewVerdict(ctx, store.RecordReviewVerdictParams{
		TaskID: taskID, PullRequest: pr, HeadSha: head, Verdict: verdict, Now: now,
	}); err != nil {
		return fmt.Errorf("recording the review verdict: %w", err)
	}
	return queueVerdictStatus(ctx, q, now, taskID, pr, head, verdict)
}

// reviewedHead checks a new review Task against the verdicts its pull request already has, in any Run. A head gets
// one verdict: a review of a head with a correct or incorrect verdict is refused, naming that Task and verdict; a
// blocked review may be run again. A later review is given the head the last verdict covers, so it reviews
// git diff <reviewed_sha>..<head> (pstack skills/poteto-mode/playbooks/shipping.md:7; issue #953). It returns the
// spec to store.
func reviewedHead(ctx context.Context, q *store.Queries, nt NewTask) (string, error) {
	if !reviewerProfile(nt.Profile) {
		return nt.Spec, nil
	}
	pr := specReviewTarget(nt.Spec, registeredKind(ctx, q))
	if pr == "" {
		return nt.Spec, nil
	}
	verdicts, err := q.ListReviewVerdicts(ctx, pr)
	if err != nil {
		return "", fmt.Errorf("reading the review verdicts of %s: %w", pr, err)
	}

	head := reviewHead(nt.Spec)
	var last *store.ReviewVerdict
	for i, verdict := range verdicts {
		if verdict.Verdict == "blocked" {
			continue
		}
		if head != "" && sameCommit(verdict.HeadSha, head) {
			return "", fmt.Errorf("%w: %s at head %s already has the verdict %s from Task %s; a head gets one "+
				"verdict: review a newer head, covering git diff %s..<head>", ErrConflict, pr, head,
				verdict.Verdict, verdict.TaskID, verdict.HeadSha)
		}
		if last == nil {
			last = &verdicts[i]
		}
	}
	if last == nil {
		return nt.Spec, nil
	}

	return strings.TrimRight(nt.Spec, "\n") + fmt.Sprintf("\n\n## Reviewed head\nTask %s gave the verdict %s "+
		"at head %s. Check its findings and review `git diff %s..<head>`, where <head> is the pull request's head "+
		"now.", last.TaskID, last.Verdict, last.HeadSha, last.HeadSha), nil
}
