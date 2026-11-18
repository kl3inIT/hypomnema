package orchestration

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// What a Task and a Run used (issue #856), as Omnigent shows a session's cost (cli.py:7363-7404), Orca attributes
// usage to a run and Companion keeps a run's costUsd: the model gateway's requests of each Dispatch, found through
// the Dispatch's keys, and for a Dispatch whose runtime did not go through the gateway the tokens it reported.

// WorkUsage is what a Task, a Run or a Dispatch used.
type WorkUsage struct {
	// Dispatches counts the Dispatches; Gateway those that sent the gateway requests.
	Dispatches, Gateway int
	// The gateway's requests, their tokens, their cost at the vendor's API price in micro-dollars, and how many
	// had tokens the price book has no price for.
	Requests, Prompt, Completion, Cached, Reasoning, CacheWrite, CostMicros, Unpriced int64
	// Reported is what the runtimes reported of the Dispatches that sent the gateway nothing; it has no cost.
	Reported Usage
}

func (u *WorkUsage) add(o WorkUsage) {
	u.Dispatches += o.Dispatches
	u.Gateway += o.Gateway
	u.Requests += o.Requests
	u.Prompt += o.Prompt
	u.Completion += o.Completion
	u.Cached += o.Cached
	u.Reasoning += o.Reasoning
	u.CacheWrite += o.CacheWrite
	u.CostMicros += o.CostMicros
	u.Unpriced += o.Unpriced
	u.Reported.Input += o.Reported.Input
	u.Reported.Output += o.Reported.Output
	u.Reported.CacheRead += o.Reported.CacheRead
	u.Reported.CacheWrite += o.Reported.CacheWrite
}

// dispatchUsage is one Dispatch's use. The gateway's request rows are read while they are kept, and the totals
// written when the Dispatch settled once they are purged (9router-go carries the spend of the rows it prunes,
// internal/db/usage_retention.go:15-29): whichever counts more requests, so a request recorded after the Dispatch
// settled still counts, and a purge does not lose one.
func dispatchUsage(r store.ListDispatchUsageRow) WorkUsage {
	u := WorkUsage{Dispatches: 1}

	switch {
	case r.LiveRequests > 0 && r.LiveRequests >= r.KeptRequests:
		u.Gateway, u.Requests, u.Prompt, u.Completion = 1, r.LiveRequests, r.LivePromptTokens, r.LiveCompletionTokens
		u.Cached, u.Reasoning, u.CacheWrite = r.LiveCachedTokens, r.LiveReasoningTokens, r.LiveCacheWriteTokens
		u.CostMicros, u.Unpriced = r.LiveCostMicros, r.LiveUnpriced
	case r.KeptRequests > 0:
		u.Gateway, u.Requests, u.Prompt, u.Completion = 1, r.KeptRequests, r.KeptPromptTokens, r.KeptCompletionTokens
		u.Cached, u.Reasoning, u.CacheWrite = r.KeptCachedTokens, r.KeptReasoningTokens, r.KeptCacheWriteTokens
		u.CostMicros, u.Unpriced = r.KeptCostMicros, r.KeptUnpriced
	default:
		u.Reported = Usage{
			Input: r.ReportedInput, Output: r.ReportedOutput, CacheRead: r.ReportedCacheRead,
			CacheWrite: r.ReportedCacheWrite,
		}
	}

	return u
}

// TaskUsage is what a Task's Dispatches used.
func (s *Service) TaskUsage(ctx context.Context, taskID string) (WorkUsage, error) {
	if _, err := s.GetTask(ctx, taskID); err != nil {
		return WorkUsage{}, err
	}

	rows, err := s.read().ListDispatchUsage(ctx, store.ListDispatchUsageParams{TaskID: taskID})
	if err != nil {
		return WorkUsage{}, fmt.Errorf("reading the usage of task %s: %w", taskID, err)
	}

	var out WorkUsage
	for _, r := range rows {
		out.add(dispatchUsage(r))
	}

	return out, nil
}

// RunUsage is what a Run's Tasks used, in total and by Task id.
func (s *Service) RunUsage(ctx context.Context, runID string) (WorkUsage, map[string]WorkUsage, error) {
	if _, err := s.GetRun(ctx, runID); err != nil {
		return WorkUsage{}, nil, err
	}

	rows, err := s.read().ListDispatchUsage(ctx, store.ListDispatchUsageParams{RunID: runID})
	if err != nil {
		return WorkUsage{}, nil, fmt.Errorf("reading the usage of run %s: %w", runID, err)
	}

	var total WorkUsage

	byTask := map[string]WorkUsage{}

	for _, r := range rows {
		u := dispatchUsage(r)
		total.add(u)

		t := byTask[r.TaskID]
		t.add(u)
		byTask[r.TaskID] = t
	}

	return total, byTask, nil
}

// recordDispatchUsage keeps a settled Dispatch's gateway totals, so its Task's cost outlives the purge of the
// request rows (issue #856).
func recordDispatchUsage(ctx context.Context, q *store.Queries, now int64, dispatchID string) error {
	if err := q.RecordDispatchUsage(ctx, store.RecordDispatchUsageParams{Now: now, DispatchID: dispatchID}); err != nil {
		return fmt.Errorf("keeping the usage of dispatch %s: %w", dispatchID, err)
	}

	return nil
}

// repeatedFailureLookback is how many of a Task's failed Dispatches are read to count a repeated failure.
const repeatedFailureLookback = 20

// raiseRepeatedFailure tells the coordinator and the owner when a Task's Dispatch failed for the same reason as
// its previous failed Dispatch (issue #856): the same failure class, and the same cause when it was unknown, as
// OpenClaw's Workboard flags repeated failures at two (store-card-helpers.ts) and Hermes sends a Task to triage
// at its second block for one reason (kanban_db.py:113, BLOCK_RECURRENCE_LIMIT = 2). It changes nothing about the
// Task: retries go on.
func raiseRepeatedFailure(ctx context.Context, q *store.Queries, now int64, d store.Dispatch) error {
	rows, err := q.ListFailedDispatchesOfTask(ctx, store.ListFailedDispatchesOfTaskParams{
		TaskID: d.TaskID, Lim: repeatedFailureLookback,
	})
	if err != nil {
		return fmt.Errorf("reading the failures of task %s: %w", d.TaskID, err)
	}

	if len(rows) < 2 || rows[0].ID != d.ID {
		return nil
	}

	same := []string{rows[0].ID}

	for _, r := range rows[1:] {
		if r.FailureClass != rows[0].FailureClass || r.UnknownCause != rows[0].UnknownCause {
			break
		}

		same = append(same, r.ID)
	}

	if len(same) < 2 {
		return nil
	}

	reason := rows[0].FailureClass
	if rows[0].UnknownCause != "" {
		reason += ":" + rows[0].UnknownCause
	}

	return emitEscalation(ctx, q, now, Escalation{
		Kind: EscalationRepeatedFailure, DispatchID: d.ID, Reason: reason, Count: len(same),
		Dispatches: same,
	})
}
