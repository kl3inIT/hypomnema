package inboxstore

import (
	"context"
	"database/sql"
	"fmt"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

var reviewStallActor = Actor{Kind: System, ID: "review-stall"}

// AddReviewStallTx opens one owner item per Run and head, in the transaction that records the stall.
// A closed report keeps its identity, so another review of the same head cannot open a second one.
func (s *Store) AddReviewStallTx(ctx context.Context, q *store.Queries, now int64, runID, chatID, head, pr, notes,
	report string,
) error {
	n, err := q.CountReviewStallTodosOfHead(ctx, store.CountReviewStallTodosOfHeadParams{RunID: runID, HeadSha: head})
	if err != nil {
		return fmt.Errorf("checking for an existing review stall to-do: %w", err)
	}
	if n > 0 {
		return nil
	}

	source, sourceID := FromChat, chatID
	if sourceID == "" {
		source, sourceID = FromRoutine, runID
	}
	ref, err := optionalJSON(&Link{Kind: LinkURL, Ref: pr, Label: "Pull request"})
	if err != nil {
		return err
	}

	// Keep the review verbatim. Longer reports go in the item's history, which the Inbox renders below the notes.
	longReport := utf8.RuneCountInString(notes)+2+utf8.RuneCountInString(report) > MaxNotes
	if longReport {
		notes += "\n\nThe full review is in the item thread below."
	} else {
		notes += "\n\n" + report
	}

	id := s.newID("tdo")
	if err := q.InsertTodo(ctx, store.InsertTodoParams{
		ID: id, Title: "Review limit reached", Notes: notes, DueKind: DueNone, AssigneeKind: Me, State: Open,
		SourceKind: source, SourceID: sourceID, SourceRef: ref,
		CreatedByKind: reviewStallActor.Kind, CreatedBy: reviewStallActor.ID, Now: now,
	}); err != nil {
		return fmt.Errorf("adding the review stall to-do: %w", err)
	}
	if err := q.InsertReviewStallTodo(ctx, store.InsertReviewStallTodoParams{
		RunID: runID, HeadSha: head, PullRequest: pr, TodoID: id,
	}); err != nil {
		return fmt.Errorf("linking the review stall to-do: %w", err)
	}
	row, err := q.GetTodo(ctx, id)
	if err != nil {
		return fmt.Errorf("reading the review stall to-do: %w", err)
	}
	if err := addEvent(ctx, q, id, now, 1, reviewStallActor, EventCreated, map[string]any{}, fields(row)); err != nil {
		return err
	}
	if longReport {
		if err := commentTx(ctx, q, now, reviewStallActor, id, 1, map[string]any{"text": report}); err != nil {
			return err
		}
	}
	return queuePush(ctx, q, now, reviewStallActor, id)
}

// CloseRunReviewStallsTx retires the Run's reports when its work or status changes.
func (s *Store) CloseRunReviewStallsTx(ctx context.Context, q *store.Queries, now int64, runID, reason string) error {
	items, err := q.OpenReviewStallTodosOfRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("reading the Run's review stall to-dos: %w", err)
	}
	return s.closeReviewStalls(ctx, q, now, items, reason)
}

// CloseMergedReviewStallsTx retires every report for the merged pull request.
func (s *Store) CloseMergedReviewStallsTx(ctx context.Context, q *store.Queries, now int64, pr string) error {
	items, err := q.OpenReviewStallTodosOfRequest(ctx, pr)
	if err != nil {
		return fmt.Errorf("reading the merged pull request's review stall to-dos: %w", err)
	}
	return s.closeReviewStalls(ctx, q, now, items, "The pull request merged.")
}

func (s *Store) closeReviewStalls(ctx context.Context, q *store.Queries, now int64, items []store.Todo, reason string,
) error {
	for _, item := range items {
		change := Change{Actor: reviewStallActor, ID: item.ID, Revision: int(item.Revision)}
		if _, err := s.changeTx(ctx, q, now, change, EventState,
			func(q *store.Queries, cur store.Todo, next *store.Todo, now int64) (map[string]any, error) {
				if cur.State == Review {
					if _, err := q.ExpireApproval(ctx, store.ExpireApprovalParams{
						ID: cur.ApprovalID.String, Now: sql.NullInt64{Int64: now, Valid: true},
					}); err != nil {
						return nil, fmt.Errorf("ending Approval %s: %w", cur.ApprovalID.String, err)
					}
				}
				next.State = Dropped
				closeFields(next, reviewStallActor, "", reason, "[]", now)
				return reasonOf(reason), nil
			}); err != nil {
			return err
		}
	}
	return nil
}
