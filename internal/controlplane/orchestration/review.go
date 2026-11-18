package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

func reviewReport(spec, result string) *ReviewStall {
	heading, _, _ := strings.Cut(strings.TrimSpace(spec), "\n")
	pr := reviewField(heading, "Review pull request")
	request, ok := githost.ParseRequest(pr, githost.KnownKind)
	if !ok {
		return nil
	}
	kind, _ := githost.KnownKind(request.Repo.Host)
	adapter, _ := githost.For(kind)
	if !strings.EqualFold(pr, adapter.RequestURL(request.Repo, request.Number)) {
		return nil
	}

	verdict := strings.ToLower(reviewField(result, "Verdict"))
	if verdict != "correct" && verdict != "incorrect" {
		verdict = "unknown"
	}

	return &ReviewStall{
		PullRequest: pr, Verdict: verdict,
		MergeState: reviewValue(result, "Merge state", "BEHIND", "BLOCKED", "CLEAN", "DIRTY", "DRAFT", "HAS_HOOKS", "UNKNOWN", "UNSTABLE"),
		Mergeable:  reviewValue(result, "Mergeable", "MERGEABLE", "CONFLICTING", "UNKNOWN"),
		Checks:     reviewValue(result, "Checks", "PASSING", "FAILING", "PENDING", "UNKNOWN"),
	}
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

func reviewAfterTask(ctx context.Context, q *store.Queries, task store.Task, result string) (*ReviewStall, error) {
	review := reviewReport(task.Spec, result)
	if review == nil {
		return nil, nil
	}

	run, err := q.GetRun(ctx, task.RunID)
	if err != nil {
		return nil, fmt.Errorf("reading review run: %w", err)
	}
	if run.Status != string(RunOpen) {
		return nil, nil
	}

	tasks, err := q.ListTasksByRun(ctx, task.RunID)
	if err != nil {
		return nil, fmt.Errorf("reading review rounds: %w", err)
	}

	rounds := 0
	for _, candidate := range tasks {
		if !TaskStatus(candidate.Status).terminal() {
			return nil, nil
		}
		if candidate.Status != string(TaskSucceeded) {
			continue
		}
		if earlier := reviewReport(candidate.Spec, candidate.Result.String); earlier != nil && earlier.Verdict != "unknown" &&
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
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
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
