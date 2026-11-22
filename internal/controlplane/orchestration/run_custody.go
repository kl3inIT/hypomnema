package orchestration

import (
	"context"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

func runCoordinator(ctx context.Context, q *store.Queries, name string) (Token, error) {
	token, err := coordinatorTokenNamed(ctx, q, name)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid) {
		return Token{}, fmt.Errorf("%w: configure a working coordinator token named %q before assigning Runs to chats",
			ErrConflict, name)
	}

	return token, err
}

func (s *Service) runChat(ctx context.Context, q *store.Queries, now int64, chatID string) (store.Chat, error) {
	if chatID == "" {
		var err error
		chatID, _, err = s.createChatTx(ctx, q, now, "", DefaultBotID, untitled, ChatModel{}, mainIfNone, 0)
		if err != nil {
			return store.Chat{}, err
		}
	}

	chat, err := q.GetChat(ctx, chatID)
	if err != nil {
		return store.Chat{}, notFound(err, "chat", chatID)
	}

	read, err := q.IsReadRun(ctx, chatID)
	if err != nil {
		return store.Chat{}, fmt.Errorf("reading routine access of chat %s: %w", chatID, err)
	}
	if read {
		return store.Chat{}, fmt.Errorf("%w: chat %s is read-only and cannot coordinate a Run", ErrConflict, chatID)
	}

	return chat, nil
}

func (s *Service) bindNewRun(ctx context.Context, q *store.Queries, now int64, nr NewRun, coordinator string,
) (store.Chat, string, error) {
	parent, err := runCoordinator(ctx, q, coordinator)
	if err != nil {
		return store.Chat{}, "", err
	}

	if nr.CoordinatorTokenID != "" && nr.CoordinatorTokenID != parent.ID {
		return store.Chat{}, "", fmt.Errorf(
			"%w: this is not the configured chat coordinator; use an owner token to create a Run", ErrForbidden)
	}

	chat, err := s.runChat(ctx, q, now, nr.ChatID)

	return chat, parent.ID, err
}

// AdoptRun assigns an open, unbound Run to a supervised chat at the owner's request.
func (s *Service) AdoptRun(ctx context.Context, runID, chatID, coordinator string) (Run, error) {
	err := s.write(ctx, func(q *store.Queries, now int64) error {
		run, err := q.GetRun(ctx, runID)
		if err != nil {
			return notFound(err, "run", runID)
		}

		if RunStatus(run.Status) != RunOpen {
			return fmt.Errorf("%w: run %s is not open", ErrConflict, runID)
		}

		parent, err := runCoordinator(ctx, q, coordinator)
		if err != nil {
			return err
		}

		if run.ChatID.Valid {
			if run.CoordinatorTokenID.String == parent.ID && (chatID == "" || run.ChatID.String == chatID) {
				return nil
			}

			return fmt.Errorf("%w: run %s already belongs to chat %s", ErrConflict, runID, run.ChatID.String)
		}

		if err := mayAdoptRun(ctx, q, run, parent.ID); err != nil {
			return err
		}

		chat, err := s.runChat(ctx, q, now, chatID)
		if err != nil {
			return err
		}

		bot, err := q.GetBot(ctx, chat.BotID)
		if err != nil {
			return notFound(err, "bot", chat.BotID)
		}

		destination := botFromRow(bot)
		if _, err := runUses(ctx, q, splitUses(run.Uses), true, &destination); err != nil {
			return err
		}

		if err := q.BindRunToChat(ctx, store.BindRunToChatParams{
			ID: runID, ChatID: nullString(chat.ID), CoordinatorTokenID: nullString(parent.ID),
			Bot: nullString(chat.BotID), Now: now,
		}); err != nil {
			return err
		}

		text := fmt.Sprintf("The owner assigned run %s to this chat. Read its current Tasks, unanswered questions and "+
			"review evidence before acting: hypomnema runs show %s --json", runID, runID)
		if err := q.InsertRunAdoptionNotice(ctx, store.InsertRunAdoptionNoticeParams{
			ChatID: chat.ID, Text: text, Now: now,
		}); err != nil {
			return err
		}

		return q.TouchChat(ctx, store.TouchChatParams{ID: chat.ID, Now: now})
	})
	if err != nil {
		return Run{}, fmt.Errorf("adopting run %s: %w", runID, err)
	}

	return s.GetRun(ctx, runID)
}

func mayAdoptRun(ctx context.Context, q *store.Queries, run store.Run, parent string) error {
	if run.BotID.Valid {
		return fmt.Errorf("%w: unbound run %s was created by a Bot and may belong to a script Routine", ErrConflict, run.ID)
	}

	if !run.CoordinatorTokenID.Valid || run.CoordinatorTokenID.String == parent {
		return nil
	}

	incumbent, err := q.GetClientToken(ctx, run.CoordinatorTokenID.String)
	if err != nil {
		return notFound(err, "token", run.CoordinatorTokenID.String)
	}

	if !incumbent.RevokedAt.Valid {
		return fmt.Errorf("%w: run %s still belongs to another working coordinator token; left unchanged",
			ErrConflict, run.ID)
	}

	return nil
}
