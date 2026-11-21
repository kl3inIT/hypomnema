package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// Connection requests (issue #471, part D, the grant card; design approved by the owner on 2026-11-21, #471 comment
// 7056846217). A Bot whose Connections list is limited may ask the owner, from its pi in its main chat, for a
// Connection outside its list: one that exists, is switched on for chats and is not a user Connection. The owner
// answers on a card with Always for this Bot, which appends that one use to the Bot's list, or Decline. As
// OpenMausBot's bot lists the apps assigned to it that are not connected and asks for one with a card
// (server/cloud-connections-prompt.ts, COMPOSIO_MANAGE_CONNECTIONS), and t3code's agent asks for a link in a card and
// is told the outcome as a message (apps/server/src/peer/PeerLinkRequests.ts). It is a record of its own, not an
// Approval (review P1-3). Revoking is the Bot's page's, as for any Connection of its list: MayUse is read at every
// use.

// ConnectionRequestState is where a Connection request stands.
type ConnectionRequestState string

// The states of a Connection request: stale is an Always whose checks failed, which granted nothing.
const (
	RequestPending  ConnectionRequestState = "pending"
	RequestGranted  ConnectionRequestState = "granted"
	RequestDeclined ConnectionRequestState = "declined"
	RequestStale    ConnectionRequestState = "stale"
)

// maxConnectionRequests bounds a list of them.
const maxConnectionRequests = 200

// ConnectionRequest is a Bot's request for one Connection it was not given.
type ConnectionRequest struct {
	ID          string
	Bot         string
	Chat        string
	Integration string
	Label       string
	Reason      string
	State       ConnectionRequestState
	CreatedAt   time.Time
	// DecidedAt is when the owner answered; zero while pending.
	DecidedAt time.Time
	// Why says, of a request that became stale on this answer, which check failed; empty otherwise and when read
	// back.
	Why string
}

// Use is the request's Connection as integration:label.
func (r ConnectionRequest) Use() string { return Use(r.Integration, r.Label) }

// ConnectionRequestFilter selects Connection requests; empty fields match every one.
type ConnectionRequestFilter struct {
	State ConnectionRequestState
	Chat  string
}

// GrantableConnections are the Connections a Bot may ask for (review P1-1, the owner's choice of 2026-11-21 that a
// Bot sees their names): those that exist, are switched on for chats and are not a user Connection
// (Connection.User), outside the Bot's limited list. A Bot whose list is not limited may use every one already and
// is offered none.
func (s *Service) GrantableConnections(ctx context.Context, bot Bot) ([]Connection, error) {
	if !bot.Connections.Limited {
		return nil, nil
	}

	all, err := s.ListConnections(ctx)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(all, func(c Connection) bool { return !grantable(bot, c) }), nil
}

func grantable(bot Bot, c Connection) bool {
	return bot.Connections.Limited && c.Chats && !c.User() && !bot.Connections.Allows(Use(c.Integration, c.Label))
}

// IsMainChat says whether a chat is its Bot's main chat (issue #471): the only chat whose pi may ask for a Connection
// (review P1-4). A Routine's chat and a Lark chat never are, as SetMainChat refuses them; they are read again here.
func (s *Service) IsMainChat(ctx context.Context, botID, chatID string) (bool, error) {
	return isMainChat(ctx, s.read(), botID, chatID)
}

func isMainChat(ctx context.Context, q *store.Queries, botID, chatID string) (bool, error) {
	row, err := q.GetBot(ctx, botID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("reading bot %s: %w", botID, err)
	}

	if chatID == "" || row.MainChat.String != chatID {
		return false, nil
	}

	marks, err := q.GetChatMarks(ctx, chatID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("reading the marks of chat %s: %w", chatID, err)
	}

	return marks.Routine == 0 && marks.Channel == 0, nil
}

// RequestConnection is a Bot's pi's ask, from its main chat, for a Connection outside its list, with the reason it
// gives. It returns at once with the request to tell the pi of (review P1-2):
//
//   - a pending request for the same Bot, chat and Connection, when there is one: a repeated or concurrent call makes
//     no second card, as OpenMausBot finds the card a request already made (server/host/connector-cards.ts, the
//     message of the same resumeKey and slug);
//   - the latest request's state, whatever it is, when it was made in this same turn of the chat or this turn is not
//     one the owner started (the turn the owner's answer wakes pi in, a Run's wake): only a later turn the owner
//     started asks again (coordinator, 2026-11-21), as OpenMausBot keys its cards by the turn's resumeKey and t3code
//     answers a retry of the same clientRequestId with that card's outcome (PeerLinkRequests.ts, resultOf);
//   - else a new pending request, the card the owner answers.
//
// A Bot that may use the Connection already is answered with its latest request granted, or with ErrConflict when it
// has none. A Connection that is not grantable is refused with the answer one that does not exist gets: the Bot
// learns no more than the names it is shown.
func (s *Service) RequestConnection(ctx context.Context, botID, chatID, use, reason string) (ConnectionRequest, error) {
	integration, label, ok := SplitUse(use)
	if !ok {
		return ConnectionRequest{}, fmt.Errorf("%w: %q: a connection is named integration:label, such as google:work",
			ErrInvalid, use)
	}

	var out ConnectionRequest

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		main, err := isMainChat(ctx, q, botID, chatID)
		if err != nil {
			return err
		}

		if !main {
			return fmt.Errorf("%w: a connection is asked for only from the bot's main chat", ErrForbidden)
		}

		row, err := q.GetBot(ctx, botID)
		if err != nil {
			return notFound(err, "bot", botID)
		}

		bot := botFromRow(row)
		if !bot.Connections.Limited {
			return fmt.Errorf("%w: bot %s may use every connection switched on for chats", ErrConflict, bot.Name)
		}

		key := store.LatestConnectionRequestParams{BotID: botID, ChatID: chatID, Integration: integration, Label: label}

		latest, err := q.LatestConnectionRequest(ctx, key)
		found := err == nil

		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("reading the requests for %s: %w", use, err)
		}

		if bot.Connections.Allows(use) {
			if found && latest.State == string(RequestGranted) {
				out = connectionRequestFromRow(latest)

				return nil
			}

			return fmt.Errorf("%w: bot %s was given %s already", ErrConflict, bot.Name, use)
		}

		conn, err := q.GetConnection(ctx, store.GetConnectionParams{Integration: integration, Label: label})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("reading connection %s: %w", use, err)
		}

		if err != nil || !grantable(bot, connectionFromRow(conn)) {
			return fmt.Errorf("%w: there is no connection %s to ask for", ErrNotFound, use)
		}

		turn, ownerTurn, err := chatTurnOf(ctx, q, chatID)
		if err != nil {
			return err
		}

		if found && (latest.State == string(RequestPending) || latest.Turn == turn || !ownerTurn) {
			out = connectionRequestFromRow(latest)

			return nil
		}

		id := s.newID("cnr")
		if err := q.InsertConnectionRequest(ctx, store.InsertConnectionRequestParams{
			ID: id, BotID: botID, ChatID: chatID, Integration: integration, Label: label, Reason: reason, Turn: turn,
			Now: now,
		}); err != nil {
			return fmt.Errorf("recording the request for %s: %w", use, err)
		}

		// The owner's browsers are told once it is committed (issue #1047).
		if err := queuePush(ctx, q, now, PushConnectionRequest, id); err != nil {
			return err
		}

		made, err := q.GetConnectionRequest(ctx, id)
		if err != nil {
			return fmt.Errorf("reading request %s: %w", id, err)
		}

		out = connectionRequestFromRow(made)

		return q.TouchChat(ctx, store.TouchChatParams{ID: chatID, Now: now})
	})
	if err != nil {
		return ConnectionRequest{}, fmt.Errorf("asking for connection %s: %w", use, err)
	}

	return out, nil
}

// chatTurnOf is the chat's turn now (ChatTurn) and whether the owner started it (OwnerTurn), read in the caller's
// transaction.
func chatTurnOf(ctx context.Context, q *store.Queries, chatID string) (turn string, ownerTurn bool, err error) {
	chat, err := q.GetChat(ctx, chatID)
	if err != nil {
		return "", false, notFound(err, "chat", chatID)
	}

	if chat.TurnToken.Valid && chat.TurnStartedAt.Valid {
		turn = fmt.Sprintf("%s@%d", chat.TurnToken.String, chat.TurnStartedAt.Int64)
	}

	last, err := q.LastTurnInput(ctx, chatID)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return turn, false, nil
	case err != nil:
		return "", false, fmt.Errorf("reading whose turn chat %s is: %w", chatID, err)
	}

	return turn, ChatRole(last.Role) == ChatOwner && !last.SenderID.Valid, nil
}

// GetConnectionRequest returns one Connection request.
func (s *Service) GetConnectionRequest(ctx context.Context, id string) (ConnectionRequest, error) {
	row, err := s.read().GetConnectionRequest(ctx, id)
	if err != nil {
		return ConnectionRequest{}, notFound(err, "connection request", id)
	}

	return connectionRequestFromRow(row), nil
}

// ListConnectionRequests lists Connection requests, the newest first.
func (s *Service) ListConnectionRequests(ctx context.Context, f ConnectionRequestFilter) ([]ConnectionRequest, error) {
	rows, err := s.read().ListConnectionRequests(ctx, store.ListConnectionRequestsParams{
		State: string(f.State), ChatID: f.Chat, MaxRows: maxConnectionRequests,
	})
	if err != nil {
		return nil, fmt.Errorf("listing connection requests: %w", err)
	}

	out := make([]ConnectionRequest, 0, len(rows))
	for _, r := range rows {
		out = append(out, connectionRequestFromRow(r))
	}

	return out, nil
}

// DecideConnectionRequest records the owner's answer (review P1-3, P1-5, P1-7). The first answer wins: the same
// answer again returns the request, a contrary one is a conflict, as DecideApproval answers. A request that became
// stale is returned as it is to either answer: it was cleared and granted nothing.
//
// Always re-reads, in the one transaction that grants, the Connection (it exists, is on for chats, is not a user
// Connection), the Bot (it exists, its list is limited) and that the chat is still its main chat; then appends that
// one use to the Bot's list, once, and changes no other setting (SetBotConnections, not UpdateBot). When a check fails
// the request becomes stale and grants nothing; Why says which. Decline needs no check.
//
// Either answer leaves a system line in the chat, marked for the coordinator, which pi is given once through the
// chat's undelivered messages and is woken by (UndeliveredChatMessages, Subscribe), as an Approval answered after
// its request ended is (TellChatOfApproval). WakeChats, which reads Runs' events, is not involved.
func (s *Service) DecideConnectionRequest(ctx context.Context, id string, grant bool) (ConnectionRequest, error) {
	want := RequestDeclined
	if grant {
		want = RequestGranted
	}

	var out ConnectionRequest

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetConnectionRequest(ctx, id)
		if err != nil {
			return notFound(err, "connection request", id)
		}

		if row.State != string(RequestPending) {
			if row.State == string(want) || row.State == string(RequestStale) {
				out = connectionRequestFromRow(row)

				return nil
			}

			return fmt.Errorf("%w: connection request %s was already answered", ErrConflict, id)
		}

		state, why := want, ""

		if grant {
			why, err = grantConnection(ctx, q, row, now)
			if err != nil {
				return err
			}

			if why != "" {
				state = RequestStale
			}
		}

		if _, err := q.DecideConnectionRequest(ctx, store.DecideConnectionRequestParams{
			ID: id, State: string(state), Now: sql.NullInt64{Int64: now, Valid: true},
		}); err != nil {
			return fmt.Errorf("answering connection request %s: %w", id, err)
		}

		if row, err = q.GetConnectionRequest(ctx, id); err != nil {
			return fmt.Errorf("reading connection request %s: %w", id, err)
		}

		out = connectionRequestFromRow(row)
		out.Why = why

		if err := q.InsertRunAdoptionNotice(ctx, store.InsertRunAdoptionNoticeParams{
			ChatID: out.Chat, Text: connectionRequestNotice(out), Now: now,
		}); err != nil {
			return fmt.Errorf("telling chat %s of connection request %s: %w", out.Chat, id, err)
		}

		return q.TouchChat(ctx, store.TouchChatParams{ID: out.Chat, Now: now})
	})
	if err != nil {
		return ConnectionRequest{}, err
	}

	return out, nil
}

// grantConnection appends a request's Connection to its Bot's list, after the checks of Always; why is the check
// that failed, when one did, and nothing was granted.
func grantConnection(ctx context.Context, q *store.Queries, r store.ConnectionRequest, now int64) (why string,
	err error,
) {
	use := Use(r.Integration, r.Label)

	bot, found, err := botIfAny(ctx, q, r.BotID)
	if err != nil {
		return "", err
	}

	main := false
	if found {
		if main, err = isMainChat(ctx, q, r.BotID, r.ChatID); err != nil {
			return "", err
		}
	}

	conn, err := q.GetConnection(ctx, store.GetConnectionParams{Integration: r.Integration, Label: r.Label})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("reading connection %s: %w", use, err)
	}

	c := connectionFromRow(conn)

	switch {
	case !found:
		return "the bot was removed", nil
	case !main:
		return "this chat is no longer the bot's main chat", nil
	case !bot.Connections.Limited:
		return "the bot may use every connection already", nil
	case errors.Is(err, sql.ErrNoRows):
		return "the connection was removed", nil
	case !c.Chats:
		return "the connection is switched off for chats", nil
	case c.User():
		return "the connection is the owner's own account", nil
	case bot.Connections.Allows(use):
		// Given by hand meanwhile: the list already has it, once.
		return "", nil
	}

	names := append(slices.Clone(bot.Connections.Names), use)

	if err := q.SetBotConnections(ctx, store.SetBotConnectionsParams{
		Connections: AllowList{Limited: true, Names: names}.stored(), Now: now, ID: r.BotID,
	}); err != nil {
		return "", fmt.Errorf("giving bot %s connection %s: %w", r.BotID, use, err)
	}

	return "", nil
}

// connectionRequestNotice is the chat's line of the owner's answer, which pi is given: what it may do now.
func connectionRequestNotice(r ConnectionRequest) string {
	switch r.State {
	case RequestGranted:
		return fmt.Sprintf("Connection granted: the owner gave you %s, always for this Bot. Go on with what you "+
			"asked it for.", r.Use())
	case RequestStale:
		return fmt.Sprintf("Connection not granted: %s was not given, since %s. Do not ask for it again now; go on "+
			"without it, or tell the owner what it would take.", r.Use(), r.Why)
	default:
		return fmt.Sprintf("Connection declined: the owner did not give you %s. Do not ask for it again now; go on "+
			"without it, or tell the owner what it would take.", r.Use())
	}
}

func connectionRequestFromRow(r store.ConnectionRequest) ConnectionRequest {
	out := ConnectionRequest{
		ID: r.ID, Bot: r.BotID, Chat: r.ChatID, Integration: r.Integration, Label: r.Label, Reason: r.Reason,
		State: ConnectionRequestState(r.State), CreatedAt: millis(r.CreatedAt),
	}

	if r.DecidedAt.Valid {
		out.DecidedAt = millis(r.DecidedAt.Int64)
	}

	return out
}
