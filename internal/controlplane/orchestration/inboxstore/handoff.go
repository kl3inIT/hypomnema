package inboxstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// An item given to a Bot, and the item's thread (issue #1111): the owner hands an item to a Bot with a prompt, as
// GitHub's "Assign to Copilot" takes additional instructions and Linear assigns an issue to an agent; the Bot is
// woken in a chat of its own for the item, as Orca's bot item belongs to a chat (src/main/bots/bot-items-schema.ts,
// items.chat_id), and whatever gives an item to a Bot wakes it so, the mechanism project Bots reuse later. The
// item's thread is its events with comments among them, as Orca's BotItemView shows an item's comments with its
// activity; the owner's comment reaches the Bot in the item's chat, as Orca mails an item's chat what its owner
// wrote (src/main/bots/bot-items-mail-text.ts), and a Bot comments with inbox_comment, as Linear's agent reports in
// the issue's comments.

// EventCommented is a comment in an item's thread: after holds its text.
const EventCommented = "commented"

// MaxComment bounds a comment, and the prompt an item is handed over with, which is its first comment.
const MaxComment = 10_000

// The kinds of Wake.
const (
	// WakeGiven: the item was given to the Bot, with the owner's prompt when he handed it over.
	WakeGiven = "given"
	// WakeComment: the owner commented on an item whose chat the Bot has.
	WakeComment = "comment"
)

// Wake is a wake-up of the Bot an item is given to, in the item's own chat.
type Wake struct {
	Kind string
	// By is who gave the item or wrote the comment: the owner, or the Bot that passed it (issue #1118).
	By Actor
	// Todo is the item as the change left it.
	Todo Todo
	// Bot is the Bot woken.
	Bot string
	// Chat is the item's chat with that Bot when it has one; empty opens a new one.
	Chat string
	// Text is the owner's prompt (WakeGiven, optional) or his comment (WakeComment).
	Text string
}

// Waker opens the item's chat with a Bot when it has none and gives the Bot the wake-up there, in the transaction
// of the change that causes it, as the root package writes a chat's tables: the chat it was given in.
type Waker interface {
	WakeTx(ctx context.Context, q *store.Queries, now int64, w Wake) (chat string, err error)
}

// WithWaker lets the store wake a Bot an item is given to (issue #1111); without it, nothing is woken.
func (s *Store) WithWaker(w Waker) *Store {
	s.waker = w

	return s
}

// wakeTx wakes bot about the item id, in the item's chat with that Bot when it still has one, and keeps the chat it
// was woken in as the item's.
func (s *Store) wakeTx(ctx context.Context, q *store.Queries, now int64, by Actor, id, kind, bot, text string,
) error {
	if s.waker == nil {
		return nil
	}

	row, err := q.GetTodo(ctx, id)
	if err != nil {
		return storeutil.NotFound(err, "to-do", id)
	}

	chat, err := chatOf(ctx, q, row, bot)
	if err != nil {
		return err
	}

	t, err := fromRow(row, storeutil.Millis(now))
	if err != nil {
		return err
	}

	woke, err := s.waker.WakeTx(ctx, q, now, Wake{Kind: kind, By: by, Todo: t, Bot: bot, Chat: chat, Text: text})
	if err != nil {
		return err
	}

	if woke == chat {
		return nil
	}

	if err := q.SetTodoChat(ctx, store.SetTodoChatParams{ChatID: nullString(woke), ID: id}); err != nil {
		return fmt.Errorf("keeping the chat of to-do %s: %w", id, err)
	}

	return nil
}

// chatOf is the item's own chat when it is bot's and still there; empty when the item has none for that Bot.
func chatOf(ctx context.Context, q *store.Queries, row store.Todo, bot string) (string, error) {
	if !row.ChatID.Valid {
		return "", nil
	}

	of, err := q.GetTodoChatBot(ctx, row.ChatID.String)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("reading the chat of to-do %s: %w", row.ID, err)
	case of != bot:
		return "", nil
	}

	return row.ChatID.String, nil
}

// givenTx wakes the Bot an item was just given to: assigned to a Bot when it was not that Bot's before.
func (s *Store) givenTx(ctx context.Context, q *store.Queries, now int64, by Actor, id string, before *Assignee,
	after Assignee,
) error {
	if after.Kind != ABot || (before != nil && *before == after) {
		return nil
	}

	return s.wakeTx(ctx, q, now, by, id, WakeGiven, after.Name, "")
}

func checkComment(text string) error {
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > MaxComment {
		return fmt.Errorf("%w: a comment is 1 to %d characters", storeerr.ErrInvalid, MaxComment)
	}

	return nil
}

// commentTx adds a comment to the item's thread at the revision read, which it moves on as every change does.
func commentTx(ctx context.Context, q *store.Queries, now int64, actor Actor, id string, revision int64, text string,
) error {
	n, err := q.TouchTodo(ctx, store.TouchTodoParams{Now: now, ID: id, Revision: revision})
	if err != nil {
		return fmt.Errorf("commenting on to-do %s: %w", id, err)
	}

	if n != 1 {
		return fmt.Errorf("%w: to-do %s changed meanwhile", storeerr.ErrConflict, id)
	}

	return addEvent(ctx, q, id, now, revision+1, actor, EventCommented, map[string]any{}, map[string]any{"text": text})
}

// Comment adds a comment to an item's thread (issue #1111). The owner comments on any item, and his comment wakes
// the Bot in the item's own chat when it has one (Orca mails the item's chat what its owner wrote); a Bot comments
// on the items it added, is given or has the chat of, to say where its work is (Linear's agent activity). A comment
// changes no field and needs no revision: it is added to the item as it is.
func (s *Store) Comment(ctx context.Context, actor Actor, id, text string) (Todo, error) {
	if err := checkActor(actor); err != nil {
		return Todo{}, err
	}

	if actor.Kind == System {
		return Todo{}, fmt.Errorf("%w: the system does not comment", storeerr.ErrForbidden)
	}

	if err := checkComment(text); err != nil {
		return Todo{}, err
	}

	var got Todo

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		cur, err := q.GetTodo(ctx, id)
		if err != nil {
			return storeutil.NotFound(err, "to-do", id)
		}

		chatBot := ""
		if cur.ChatID.Valid {
			if chatBot, err = q.GetTodoChatBot(ctx, cur.ChatID.String); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("reading the chat of to-do %s: %w", id, err)
			}
		}

		if actor.Kind == Bot {
			mine := (cur.CreatedByKind == Bot && cur.CreatedBy == actor.ID) ||
				(cur.AssigneeKind == ABot && cur.Assignee == actor.ID) || chatBot == actor.ID
			if !mine {
				return fmt.Errorf("%w: a Bot comments only on the to-dos it added, is given or has the chat of",
					storeerr.ErrForbidden)
			}
		}

		if err := commentTx(ctx, q, now, actor, id, cur.Revision, text); err != nil {
			return err
		}

		if actor.Kind == Owner && chatBot != "" {
			if err := s.wakeTx(ctx, q, now, actor, id, WakeComment, chatBot, text); err != nil {
				return err
			}
		}

		got, err = s.get(ctx, q, id)

		return err
	})
	if err != nil {
		return Todo{}, err
	}

	return got, nil
}

// Hand gives an item to a Bot with the owner's prompt (issue #1111): the owner does, for an item not closed, nor
// another Bot's attempt's. A proposal handed over is accepted, as the owner chose it; a snoozed item is brought
// back. The prompt is the item's first comment, and the Bot is woken with the item and the prompt in the item's own
// chat, a new one when it has none with that Bot. Handing an item again to the Bot it is given to wakes it again
// with the new prompt. The Bot decides what follows: it does the item itself, starts a Run, or asks the owner.
func (s *Store) Hand(ctx context.Context, c Change, bot, prompt string) (Todo, error) {
	if c.Actor.Kind != Owner {
		return Todo{}, fmt.Errorf("%w: only the owner hands a to-do to a Bot", storeerr.ErrForbidden)
	}

	if strings.TrimSpace(prompt) != "" {
		if err := checkComment(prompt); err != nil {
			return Todo{}, err
		}
	}

	to := Assignee{Kind: ABot, Name: strings.TrimSpace(bot)}
	if to.Name == "" {
		return Todo{}, fmt.Errorf("%w: a to-do is handed to a Bot by its id", storeerr.ErrInvalid)
	}

	var got Todo

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		t, err := s.changeTx(ctx, q, now, c, EventEdited,
			func(q *store.Queries, cur store.Todo, next *store.Todo, _ int64) (map[string]any, error) {
				switch {
				case Closed(cur.State):
					return nil, fmt.Errorf("%w: a %s to-do is reopened before it is handed over", storeerr.ErrConflict,
						cur.State)
				case (cur.State == Doing || cur.State == Review) && (cur.AssigneeKind != ABot || cur.Assignee != to.Name):
					return nil, fmt.Errorf("%w: a Bot's attempt is on to-do %s; it ends before the item is given to "+
						"another", storeerr.ErrConflict, cur.ID)
				}

				if err := checkAssignee(ctx, q, to); err != nil {
					return nil, err
				}

				next.AssigneeKind, next.Assignee = to.Kind, to.Name
				next.SnoozedUntil.Valid = false

				if cur.State == Proposed {
					next.State = Open
				}

				return map[string]any{}, nil
			})
		if err != nil {
			return err
		}

		if strings.TrimSpace(prompt) != "" {
			if err := commentTx(ctx, q, now, c.Actor, t.ID, int64(t.Revision), prompt); err != nil {
				return err
			}
		}

		if err := s.wakeTx(ctx, q, now, c.Actor, t.ID, WakeGiven, to.Name, prompt); err != nil {
			return err
		}

		got, err = s.get(ctx, q, t.ID)

		return err
	})
	if err != nil {
		return Todo{}, err
	}

	return got, nil
}
