package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

func runCoordinator(ctx context.Context, q *store.Queries, name string) (Token, error) {
	token, err := coordinatorTokenNamed(ctx, q, name)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid) {
		return Token{}, fmt.Errorf("%w: configure a working coordinator token named %q before assigning Runs to chats",
			ErrConflict, name)
	}

	return token, err
}

// runChat is the chat a Run is assigned to: the chat named, else the main chat of bot, made when it has none.
func (s *Service) runChat(ctx context.Context, q *store.Queries, now int64, chatID, bot string) (store.Chat, error) {
	if chatID == "" {
		var err error
		chatID, _, err = s.createChatTx(ctx, q, now, "", bot, untitled, ChatModel{}, mainIfNone)
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

// RoutedBy is why a Run went to its chat (issue #1334), as OpenClaw's resolved route says what it matched by
// (src/routing/resolve-route.ts, matchedBy).
type RoutedBy string

// The reasons, in the order they are tried: an explicit target beats a binding, and no match goes to the main Bot, as
// OpenClaw resolves an agent (src/routing/resolve-route.ts, the bindings tried from the most specific, then the
// default agent; docs/concepts/multi-agent.md, "Explicit targets and routing bindings take precedence").
const (
	// RoutedChat: the chat its creator named (`runs create --chat`).
	RoutedChat RoutedBy = "chat"
	// RoutedOwnChat: the Bot chat that created it, whose Bot owns its repository or which no Bot owns.
	RoutedOwnChat RoutedBy = "own_chat"
	// RoutedRepoOwner: the main chat of the Bot that owns its repository (Bot.Repos).
	RoutedRepoOwner RoutedBy = "repo_owner"
	// RoutedDefault: the main Bot's main chat, for a Run made outside a Bot chat on a repository no Bot owns.
	RoutedDefault RoutedBy = "default"
)

// runRoute is where a new Run goes and why.
type runRoute struct {
	by RoutedBy
	// from is the Bot chat that created the Run when it went to the main chat of another Bot, the one that owns its
	// repository; fromBot is that chat's Bot. Both empty otherwise.
	from, fromBot string
}

// routeRun picks the chat of a new Run and sets nr's chat, Bot and coordinator token to it (issue #1334). The order
// is OpenClaw's (src/routing/resolve-route.ts:578-602): the chat named; else, from a Bot chat whose Bot owns the
// repository, or a repository no Bot owns, that chat; else the main chat of the Bot that owns the repository; else
// the main Bot's main chat. As Paperclip resolves an imported issue's assignee before the issue is made
// (docs/specs/external-task-protocol.md, section 8.1), the Run is made in the chat chosen; it is not moved after.
// With coordinator, the create is the owner's or a coordinator token's, outside a Bot chat (CreateChatRunOnce); a
// Bot's token gives nr.Bot and its own chat. A Run of a Routine's script, with neither chat nor coordinator, keeps
// no chat, as before.
func (s *Service) routeRun(ctx context.Context, q *store.Queries, now int64, nr *NewRun, repo githost.Repo,
	coordinator string,
) (runRoute, error) {
	owner, err := ownerOfRepo(ctx, q, repo)
	if err != nil {
		return runRoute{}, err
	}

	switch {
	case coordinator != "":
		parent, err := runCoordinator(ctx, q, coordinator)
		if err != nil {
			return runRoute{}, err
		}

		if nr.CoordinatorTokenID != "" && nr.CoordinatorTokenID != parent.ID {
			return runRoute{}, fmt.Errorf(
				"%w: this is not the configured chat coordinator; use an owner token to create a Run", ErrForbidden)
		}

		route, bot := runRoute{by: RoutedChat}, DefaultBotID

		switch {
		case nr.ChatID != "":
		case owner != "":
			route.by, bot = RoutedRepoOwner, owner
		default:
			route.by = RoutedDefault
		}

		chat, err := s.runChat(ctx, q, now, nr.ChatID, bot)
		if err != nil {
			return runRoute{}, err
		}

		nr.ChatID, nr.CoordinatorTokenID, nr.Bot = chat.ID, parent.ID, chat.BotID

		return route, nil
	case nr.ChatID == "":
		return runRoute{}, nil
	}

	if _, err := q.GetChat(ctx, nr.ChatID); err != nil {
		return runRoute{}, notFound(err, "chat", nr.ChatID)
	}

	switch {
	case nr.Bot == "" || nr.ChatNamed:
		return runRoute{by: RoutedChat}, nil
	case owner == "" || owner == nr.Bot:
		return runRoute{by: RoutedOwnChat}, nil
	}

	chat, err := s.runChat(ctx, q, now, "", owner)
	if err != nil {
		return runRoute{}, err
	}

	route := runRoute{by: RoutedRepoOwner, from: nr.ChatID, fromBot: nr.Bot}
	nr.ChatID, nr.Bot = chat.ID, chat.BotID

	return route, nil
}

// ownerOfRepo is the id of the Bot that owns a repository, or "" when none does or no repository is named.
func ownerOfRepo(ctx context.Context, q *store.Queries, repo githost.Repo) (string, error) {
	if repo.Path == "" {
		return "", nil
	}

	row, err := q.OwnerOfRepo(ctx, store.OwnerOfRepoParams{Host: repo.Host, Path: repo.Path})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("reading the Bot that owns %s: %w", repo, err)
	}

	return row.BotID, nil
}

// tellRouted writes, when a Bot chat's Run went to the Bot that owns its repository, a line in the creating chat
// naming the Run and the chat it went to (issue #1334, decision 4), as moveMainRunsTx tells the chat a Run left; and
// wakes the owning Bot's chat with the Run, as AdoptRun tells the chat a Run is assigned to: the creating Bot's token
// may not add the Run's Tasks (clientapi policy, "belongs to another chat"), so the owning Bot plans them.
func tellRouted(ctx context.Context, q *store.Queries, now int64, runID string, repo githost.Repo, route runRoute,
	to, toBot string,
) error {
	if route.from == "" {
		return nil
	}

	from, err := q.GetBot(ctx, route.fromBot)
	if err != nil {
		return notFound(err, "bot", route.fromBot)
	}

	owner, err := q.GetBot(ctx, toBot)
	if err != nil {
		return notFound(err, "bot", toBot)
	}

	text := fmt.Sprintf("%s created run %s on %s from its chat %s; it is yours, since this Bot owns %s. Read its "+
		"objective, plan its Tasks and coordinate it to its end: hypomnema runs show %s --json",
		from.Name, runID, repo, route.from, repo, runID)
	if err := q.InsertRunAdoptionNotice(ctx, store.InsertRunAdoptionNoticeParams{
		ChatID: to, Text: text, Now: now,
	}); err != nil {
		return err
	}

	if err := q.TouchChat(ctx, store.TouchChatParams{ID: to, Now: now}); err != nil {
		return err
	}

	_, err = q.InsertChatMessage(ctx, store.InsertChatMessageParams{
		ChatID: route.from, Role: string(ChatSystem), Kind: string(ChatWake), Now: now,
		Text: fmt.Sprintf("Run %s went to chat %s of %s, which owns %s; that chat plans and coordinates it, and its "+
			"events wake it.", runID, to, owner.Name, repo),
	})

	return err
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

		chat, err := s.runChat(ctx, q, now, chatID, DefaultBotID)
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
