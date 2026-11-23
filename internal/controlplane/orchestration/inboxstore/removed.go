package inboxstore

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// byBots is who passes a removed Bot's items on.
var byBots = Actor{Kind: System, ID: "bots"}

// PassTx passes the items not closed that the Bot from added or was given to the Bot to, in the transaction of
// from's removal (issue #1124): a Bot edits only what it added or was given (edit, notAnotherBots), so a removed
// Bot's items would otherwise stay in the Inbox with no Bot to keep them. Each records an edited event by the
// system with the reason; its created event still says who added it. Who does it is unchanged unless it is from,
// and a closed item stays as it was. Nothing is woken: the Bot finds them in the Inbox.
func (s *Store) PassTx(ctx context.Context, q *store.Queries, now int64, from, fromName, to string) error {
	rows, err := q.ListOpenTodosOfBot(ctx, from)
	if err != nil {
		return fmt.Errorf("reading the to-dos of bot %s: %w", from, err)
	}

	reason := fmt.Sprintf("Bot %s was removed; its items pass to the main Bot", fromName)

	for _, cur := range rows {
		createdBy, assignee := cur.CreatedBy, cur.Assignee
		before, after := map[string]any{}, map[string]any{"reason": reason}

		if cur.CreatedByKind == Bot && cur.CreatedBy == from {
			createdBy = to
			before["created_by"], after["created_by"] = from, to
		}

		if cur.AssigneeKind == ABot && cur.Assignee == from {
			assignee = to
			before["assignee"], after["assignee"] = from, to
		}

		n, err := q.PassTodo(ctx, store.PassTodoParams{
			CreatedBy: createdBy, Assignee: assignee, Now: now, ID: cur.ID, Revision: cur.Revision,
		})
		if err != nil {
			return fmt.Errorf("passing to-do %s to bot %s: %w", cur.ID, to, err)
		}

		if n != 1 {
			return fmt.Errorf("%w: to-do %s changed meanwhile", storeerr.ErrConflict, cur.ID)
		}

		if err := addEvent(ctx, q, cur.ID, now, cur.Revision+1, byBots, EventEdited, before, after); err != nil {
			return err
		}
	}

	return nil
}
