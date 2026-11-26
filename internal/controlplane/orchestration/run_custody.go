package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

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
		chatID, _, err = s.createChatTx(ctx, q, now, "", DefaultBotID, untitled, ChatModel{}, mainIfNone)
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

// moveMainRunsTx moves the open Runs of a Bot's former main chat to its new main chat when the main chat changes
// (issue #1169): a Run made from the main chat belongs to the Bot's main conversation, not to one chat of it, as
// OpenClaw keys an agent's main session by a stable key that a reset gives a new session id
// (src/routing/session-key.ts buildAgentMainSessionKey) and hands a sub-agent's completion to the requester's
// current session, also after a reset (src/agents/subagents/announce/subagent-announce-direct-delivery.ts, the
// requester's entry read by its key at delivery; subagent-announce-delivery.test.ts "also after a reset"). Their
// later events wake the new chat, whose pi is told which Runs came and reads each before acting, as AdoptRun tells
// the chat a Run is assigned to; the wake-ups the former chat's pi was not given yet are not given to it, since its
// token may no longer change them (clientapi policy, "belongs to another chat"); the former chat gets a line saying
// where they went. A Run of any other chat stays where it is. Their coordinator token stays: it is the control
// plane's one coordinator token, not the chat's.
func moveMainRunsTx(ctx context.Context, q *store.Queries, now int64, from, to string) error {
	if from == "" || from == to {
		return nil
	}

	if err := q.VoidOpenRunWakes(ctx, store.VoidOpenRunWakesParams{
		ChatID: from, Now: sql.NullInt64{Int64: now, Valid: true},
	}); err != nil {
		return fmt.Errorf("letting go of the wake-ups of chat %s: %w", from, err)
	}

	moved, err := q.MoveOpenRuns(ctx, store.MoveOpenRunsParams{
		FromChat: nullString(from), ToChat: nullString(to), Now: now,
	})
	if err != nil {
		return fmt.Errorf("moving the open Runs of chat %s to chat %s: %w", from, to, err)
	}

	if len(moved) == 0 {
		return nil
	}

	slices.Sort(moved)

	runs := "Run " + moved[0]
	if len(moved) > 1 {
		runs = "Runs " + strings.Join(moved, ", ")
	}

	text := fmt.Sprintf("%s moved here from chat %s when this chat became the Bot's main chat; this chat coordinates "+
		"them now and their events wake it. Read each one's current Tasks, unanswered questions and review evidence "+
		"before acting: hypomnema runs show <run> --json", runs, from)
	if err := q.InsertRunAdoptionNotice(ctx, store.InsertRunAdoptionNoticeParams{
		ChatID: to, Text: text, Now: now,
	}); err != nil {
		return err
	}

	if _, err := q.InsertChatMessage(ctx, store.InsertChatMessageParams{
		ChatID: from, Role: string(ChatSystem), Kind: string(ChatWake), Now: now,
		Text: fmt.Sprintf("%s moved to chat %s, the Bot's main chat now; their events wake that chat.", runs, to),
	}); err != nil {
		return err
	}

	return q.TouchChat(ctx, store.TouchChatParams{ID: to, Now: now})
}
