package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// The queue of a chat (issue #497, item 2 of #254): an owner message written while the chat's pi works waits off
// the transcript until the turn settles, as OpenMausBot keeps follow-ups (server/channel-queue.ts), so the reply
// that runs cannot seem to answer words it never saw. The owner may remove one or send it now, which steers the
// turn (Omnigent QueuedMessagesStrip.tsx, t3code QueuedRunsControl.tsx). The supervisor moves the rows into the
// chat when it gives them to pi.

// QueuedMessage is an owner message that waits for the chat's pi to finish its turn.
type QueuedMessage struct {
	ID          int64
	ChatID      string
	Text        string
	Attachments []ChatAttachment
	CreatedAt   time.Time
}

// SendOwnerMessage keeps an owner message: in the chat's queue while its pi works (busy) or while messages queued
// before it still wait, so it never goes ahead of them; in the chat otherwise. Exactly one of the two results is
// set.
func (s *Service) SendOwnerMessage(ctx context.Context, chatID string, m NewChatMessage, busy bool,
) (*ChatMessage, *QueuedMessage, error) {
	if !busy {
		waiting, err := s.read().CountChatQueue(ctx, chatID)
		if err != nil {
			return nil, nil, fmt.Errorf("reading the queue of chat %s: %w", chatID, err)
		}

		busy = waiting > 0
	}

	if busy {
		q, err := s.QueueChatMessage(ctx, chatID, m)
		if err != nil {
			return nil, nil, err
		}

		return nil, &q, nil
	}

	msgs, err := s.AddChatMessages(ctx, chatID, m)
	if err != nil {
		return nil, nil, err
	}

	return &msgs[0], nil, nil
}

// QueueChatMessage keeps an owner message in the chat's queue.
func (s *Service) QueueChatMessage(ctx context.Context, chatID string, m NewChatMessage) (QueuedMessage, error) {
	if m.Role != ChatOwner {
		return QueuedMessage{}, fmt.Errorf("%w: only an owner message is queued", ErrInvalid)
	}

	if err := m.check(chatID); err != nil {
		return QueuedMessage{}, err
	}

	var out QueuedMessage

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := q.GetChat(ctx, chatID); err != nil {
			return notFound(err, "chat", chatID)
		}

		files, err := keptAttachments(m.Attachments)
		if err != nil {
			return err
		}

		row, err := q.InsertChatQueued(ctx, store.InsertChatQueuedParams{
			ChatID: chatID, Text: m.Text, Attachments: files, Now: now,
		})
		if err != nil {
			return fmt.Errorf("queueing a message: %w", err)
		}

		out = queuedMessage(row)

		return nil
	})
	if err != nil {
		return QueuedMessage{}, fmt.Errorf("queueing a message in chat %s: %w", chatID, err)
	}

	return out, nil
}

// ChatQueue returns the chat's queued messages, oldest first.
func (s *Service) ChatQueue(ctx context.Context, chatID string) ([]QueuedMessage, error) {
	q := s.read()
	if _, err := q.GetChat(ctx, chatID); err != nil {
		return nil, notFound(err, "chat", chatID)
	}

	rows, err := q.ListChatQueue(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("listing the queue of chat %s: %w", chatID, err)
	}

	out := make([]QueuedMessage, 0, len(rows))
	for _, r := range rows {
		out = append(out, queuedMessage(r))
	}

	return out, nil
}

// ChatsWithQueue lists the chats that have queued messages.
func (s *Service) ChatsWithQueue(ctx context.Context) ([]string, error) {
	ids, err := s.read().ChatsWithQueue(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing chats with queued messages: %w", err)
	}

	return ids, nil
}

// RemoveQueued takes a message out of the chat's queue and returns it; ErrNotFound once it went to pi.
func (s *Service) RemoveQueued(ctx context.Context, chatID string, id int64) (QueuedMessage, error) {
	var out QueuedMessage

	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		row, err := q.GetChatQueued(ctx, store.GetChatQueuedParams{ChatID: chatID, ID: id})
		if err != nil {
			return notFound(err, "queued message", fmt.Sprint(id))
		}

		out = queuedMessage(row)

		_, err = q.DeleteChatQueued(ctx, store.DeleteChatQueuedParams{ChatID: chatID, ID: id})

		return err
	})
	if err != nil {
		return QueuedMessage{}, fmt.Errorf("removing a queued message of chat %s: %w", chatID, err)
	}

	return out, nil
}

// SendQueuedNow moves one queued message into the chat, where the supervisor gives it to pi at once, as a steer
// while pi works; ErrNotFound once it went to pi.
func (s *Service) SendQueuedNow(ctx context.Context, chatID string, id int64) (ChatMessage, error) {
	var seq int64

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetChatQueued(ctx, store.GetChatQueuedParams{ChatID: chatID, ID: id})
		if err != nil {
			return notFound(err, "queued message", fmt.Sprint(id))
		}

		seq, err = unqueueTx(ctx, q, row, now)

		return err
	})
	if err != nil {
		return ChatMessage{}, fmt.Errorf("sending a queued message of chat %s: %w", chatID, err)
	}

	out, err := s.ListChatMessages(ctx, chatID, seq-1, 1)
	if err != nil || len(out) != 1 {
		return ChatMessage{}, errors.Join(err, fmt.Errorf("reading message %d", seq))
	}

	return out[0], nil
}

// TakeChatQueue moves all of the chat's queued messages into the chat, in order, in one write, and says how many
// it moved: the supervisor does it when pi is idle and gives them to it.
func (s *Service) TakeChatQueue(ctx context.Context, chatID string) (int, error) {
	var n int

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		rows, err := q.ListChatQueue(ctx, chatID)
		if err != nil {
			return err
		}

		for _, r := range rows {
			if _, err := unqueueTx(ctx, q, r, now); err != nil {
				return err
			}
		}

		n = len(rows)

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("taking the queue of chat %s: %w", chatID, err)
	}

	return n, nil
}

// unqueueTx moves one queued row into the chat as an owner message pi has not had yet.
func unqueueTx(ctx context.Context, q *store.Queries, r store.ChatQueue, now int64) (int64, error) {
	seq, err := q.InsertChatMessage(ctx, store.InsertChatMessageParams{
		ChatID: r.ChatID, Role: string(ChatOwner), Text: r.Text, Attachments: r.Attachments, Now: now,
	})
	if err != nil {
		return 0, fmt.Errorf("inserting a queued message: %w", err)
	}

	if _, err := q.DeleteChatQueued(ctx, store.DeleteChatQueuedParams{ChatID: r.ChatID, ID: r.ID}); err != nil {
		return 0, fmt.Errorf("deleting a queued message: %w", err)
	}

	if err := q.TitleUntitledChat(ctx, store.TitleUntitledChatParams{
		ID: r.ChatID, Title: titleOf(r.Text, attachmentsOf(r.Attachments)),
	}); err != nil {
		return 0, fmt.Errorf("titling the chat: %w", err)
	}

	return seq, q.TouchChat(ctx, store.TouchChatParams{ID: r.ChatID, Now: now})
}

// keptAttachments is the JSON an attachments column keeps; null for none.
func keptAttachments(files []ChatAttachment) (sql.NullString, error) {
	if len(files) == 0 {
		return sql.NullString{}, nil
	}

	raw, err := json.Marshal(files)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("keeping the attachments: %w", err)
	}

	return sql.NullString{String: string(raw), Valid: true}, nil
}

func queuedMessage(r store.ChatQueue) QueuedMessage {
	return QueuedMessage{
		ID: r.ID, ChatID: r.ChatID, Text: r.Text, Attachments: attachmentsOf(r.Attachments),
		CreatedAt: millis(r.CreatedAt),
	}
}

// MarkDeliveredSeqs records that pi got these messages of the chat, and only these: an owner message sent now,
// while pi works, goes ahead of the wake-ups that wait for the turn to settle.
func (s *Service) MarkDeliveredSeqs(ctx context.Context, chatID string, seqs []int64) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		for _, seq := range seqs {
			if err := q.MarkChatMessageDelivered(ctx, store.MarkChatMessageDeliveredParams{
				ChatID: chatID, Seq: seq, Now: sql.NullInt64{Int64: now, Valid: true},
			}); err != nil {
				return err
			}
		}

		return nil
	}); err != nil {
		return fmt.Errorf("marking messages of chat %s delivered: %w", chatID, err)
	}

	return nil
}
