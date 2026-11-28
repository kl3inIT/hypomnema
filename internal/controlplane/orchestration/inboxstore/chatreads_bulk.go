package inboxstore

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// ChatReadTarget identifies a chat and the value it takes in the absence of an owner choice.
type ChatReadTarget struct {
	Chat         string
	DefaultReads bool
}

// ChatReadChange records the committed choice and the override it replaced, if there was one.
type ChatReadChange struct {
	Current  ChatRead
	Previous *ChatRead
}

// SetChatReads changes a batch in one transaction and captures the previous owner choices inside that transaction.
func (s *Store) SetChatReads(ctx context.Context, integration, label string, targets []ChatReadTarget,
	reads bool,
) ([]ChatReadChange, error) {
	out := []ChatReadChange{}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		rows, err := q.ListInboxChatReads(ctx, store.ListInboxChatReadsParams{Integration: integration, Label: label})
		if err != nil {
			return err
		}

		previous := make(map[string]ChatRead, len(rows))
		for _, row := range rows {
			previous[row.ChatID] = chatReadOf(row)
		}

		for _, target := range targets {
			state := target.DefaultReads
			change := ChatReadChange{}

			if old, ok := previous[target.Chat]; ok {
				state, change.Previous = old.Reads, &old
			}

			if state == reads {
				continue
			}

			row, err := q.SetInboxChatRead(ctx, store.SetInboxChatReadParams{
				Integration: integration, Label: label, ChatID: target.Chat, Reads: boolInt(reads), Now: now,
			})
			if err != nil {
				return err
			}

			change.Current = chatReadOf(row)
			out = append(out, change)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("setting chat choices for %s:%s: %w", integration, label, err)
	}

	return out, nil
}

// UndoChatReads restores the old overrides in one transaction. A later choice on any chat cancels the entire undo.
func (s *Store) UndoChatReads(ctx context.Context, integration, label string, changes []ChatReadChange) error {
	return s.write(ctx, func(q *store.Queries, _ int64) error {
		for _, change := range changes {
			ch := change.Current
			n, err := q.DeleteInboxChatReadIfCurrent(ctx, store.DeleteInboxChatReadIfCurrentParams{
				Integration: integration, Label: label, ChatID: ch.Chat, Reads: boolInt(ch.Reads), SetAt: ch.At.UnixMilli(),
			})
			if err != nil {
				return err
			}

			if n == 0 {
				return fmt.Errorf("%w: chat %s changed since this bulk action", storeerr.ErrConflict, ch.Chat)
			}

			if prev := change.Previous; prev != nil {
				_, err := q.AddInboxChatRead(ctx, store.AddInboxChatReadParams{
					Integration: integration, Label: label, ChatID: ch.Chat, Reads: boolInt(prev.Reads), Now: prev.At.UnixMilli(),
				})
				if err != nil {
					return err
				}
			}
		}

		return nil
	})
}
