package orchestration

import (
	"context"
	"encoding/json"
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

func reviewRequest(text string, kindOf func(string) (githost.Kind, bool)) string {
	for _, ref := range reviewURL.FindAllString(text, -1) {
		if pr := canonicalReviewRequest(strings.TrimRight(ref, ".,;:!?"), kindOf); pr != "" {
			return pr
		}
	}

	return ""
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

func reviewReport(ctx context.Context, q *store.Queries, task store.Task, result string) (ReviewStall, error) {
	if task.Profile != "reviewer" && !strings.HasPrefix(task.Profile, "reviewer-") {
		return ReviewStall{}, nil
	}
	kindOf := func(host string) (githost.Kind, bool) {
		registered, err := gitHostOf(ctx, q, host)
		return registered.Kind, err == nil
	}
	pr := reviewRequest(reviewField(task.Spec, "Review pull request"), kindOf)
	if pr == "" {
		pr = reviewRequest(task.Spec, kindOf)
	}
	if pr == "" {
		pr = reviewRequest(result, kindOf)
	}
	if pr == "" {
		var err error
		pr, err = reviewEvidence(ctx, q, task.ID, kindOf)
		if err != nil || pr == "" {
			return ReviewStall{}, err
		}
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
	for _, evidence := range result.Evidence {
		if pr := canonicalReviewRequest(evidence.Ref, kindOf); pr != "" {
			return pr, nil
		}
	}

	return "", nil
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

func reviewAfterTask(ctx context.Context, q *store.Queries, task store.Task, result string) (ReviewStall, error) {
	review, err := reviewReport(ctx, q, task, result)
	if err != nil || review.PullRequest == "" {
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
