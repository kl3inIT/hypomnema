package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// request_connection (issue #471, part D, the grant card; design approved by the owner on 2026-11-21): a Bot's pi
// in its main chat asks the owner for a Connection outside its limited list, and the owner answers on a card. The
// tool is listed, with the names of the Connections it may ask for, only to a Bot's pi in its Bot's main chat, and
// only while there is one to ask for: as OpenMausBot's bot is told which apps were assigned to it and are not
// connected, and asks with COMPOSIO_MANAGE_CONNECTIONS (server/cloud-connections-prompt.ts). A worker, the
// coordinator token, a Routine's script and its run's chat, a Lark chat and every other chat of the Bot neither see
// it nor may call it (review P1-4). It returns at once with the request's state, as t3code's t3_environment_link
// returns pending ("Do not wait, poll, or call it again to check", apps/server/src/mcp/toolkits/orchestrator/
// tools.ts) and OpenMausBot tells its bot to end the turn (server/connector-proxy.ts); the answer comes as a line in
// the chat that wakes pi (orchestration.DecideConnectionRequest).

// asksConnections says whether the agent may be given request_connection, in a chat it is then checked to be its
// Bot's main one: a Bot's pi, not a worker, not a Routine's read run.
func asksConnections(a agent) bool {
	return a.dispatchID == "" && a.isBotPi() && !a.readRun
}

// grantableFor are the Connections the agent may ask for: none outside its Bot's main chat. A Bot listed for any
// chat (BotTools) is not checked for one.
func (s *Server) grantableFor(ctx context.Context, a agent) ([]orchestration.Connection, error) {
	if !asksConnections(a) || !a.botRow.Connections.Limited {
		return nil, nil
	}

	if a.chatID != anyChat {
		main, err := s.store.IsMainChat(ctx, a.bot, a.chatID)
		if err != nil || !main {
			return nil, err
		}
	}

	return s.store.GrantableConnections(ctx, a.botRow)
}

// requestConnectionTool is request_connection as the agent is offered it, with the names it may ask for; none when
// there is nothing to ask for.
func (s *Server) requestConnectionTool(ctx context.Context, a agent) (agenttool.BrowserTool, bool) {
	grantable, err := s.grantableFor(ctx, a)
	if err != nil {
		s.logger.WarnContext(ctx, "the connections a bot may ask for could not be read",
			slog.String("event", "agentapi.grantable_failed"), slog.String("agent", a.name),
			slog.String("error", err.Error()))

		return agenttool.BrowserTool{}, false
	}

	if len(grantable) == 0 {
		return agenttool.BrowserTool{}, false
	}

	names := make([]string, 0, len(grantable))
	for _, c := range grantable {
		names = append(names, orchestration.Use(c.Integration, c.Label))
	}

	raw, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"use": map[string]any{"type": "string", "enum": names,
				"description": "The Connection, as integration:label."},
			"reason": map[string]any{"type": "string", "maxLength": maxRequestReason,
				"description": "Why you need it, in one sentence the owner reads on the card."},
		},
		"required": []string{"use", "reason"},
	})
	if err != nil {
		return agenttool.BrowserTool{}, false
	}

	return agenttool.BrowserTool{
		Name: agenttool.ToolRequestConnection,
		Description: "Ask the owner for an account you were not given, of those switched on for chats: " +
			strings.Join(names, ", ") + ". Use it when a task needs one of them; the owner answers on a card with " +
			"Always for this Bot or Decline. It returns at once: end your turn then, and do not call it again or " +
			"poll; you are woken in this chat with his answer. When he declines, do not ask again; go on without " +
			"it, or tell him what it would take.",
		InputSchema: raw,
	}, true
}

// maxRequestReason bounds the reason a card shows.
const maxRequestReason = 500

// requestConnectionCall runs request_connection. A chat that is not its Bot's main one has no such tool.
func (s *Server) requestConnectionCall(w http.ResponseWriter, r *http.Request, a agent, call agenttool.BrowserCall) {
	main, err := s.store.IsMainChat(r.Context(), a.bot, a.chatID)
	if err != nil {
		s.fail(w, r, err)

		return
	}

	if !main {
		refuse(w, http.StatusNotFound, "this agent has no tool "+call.Tool)

		return
	}

	var in agenttool.RequestConnectionCall
	if json.Unmarshal(orEmpty(call.Arguments), &in) != nil || in.Use == "" || strings.TrimSpace(in.Reason) == "" {
		answerTool(w, "request_connection takes {use, reason}: the Connection as the tool lists it, and why", true)

		return
	}

	if len([]rune(in.Reason)) > maxRequestReason {
		answerTool(w, fmt.Sprintf("say why in at most %d characters", maxRequestReason), true)

		return
	}

	asked, err := s.store.RequestConnection(r.Context(), a.bot, a.chatID, in.Use, strings.TrimSpace(in.Reason))

	switch {
	case errors.Is(err, orchestration.ErrConflict):
		answerTool(w, fmt.Sprintf("You may use %s already: use it. Nothing was asked.", in.Use), false)

		return
	case errors.Is(err, orchestration.ErrNotFound), errors.Is(err, orchestration.ErrInvalid),
		errors.Is(err, orchestration.ErrForbidden):
		answerTool(w, fmt.Sprintf("%s is none of the accounts request_connection lists; ask only for one of those, "+
			"and do not try another way.", in.Use), true)

		return
	case err != nil:
		s.fail(w, r, err)

		return
	}

	// Which Bot asked for which account, and never why.
	s.logger.InfoContext(r.Context(), "a bot asked for a connection",
		slog.String("event", "agentapi.connection_request"), slog.String("agent", a.name),
		slog.String("connection", asked.Use()), slog.String("request", asked.ID),
		slog.String("state", string(asked.State)))

	answerTool(w, requestAnswer(asked), false)
}

// requestAnswer is what the pi is told of its request: its state, and what to do now.
func requestAnswer(r orchestration.ConnectionRequest) string {
	switch r.State {
	case orchestration.RequestGranted:
		return fmt.Sprintf("granted: the owner gave you %s. Use it.", r.Use())
	case orchestration.RequestDeclined:
		return fmt.Sprintf("declined: the owner did not give you %s. Do not ask for it again now; go on without it, "+
			"or tell him what it would take.", r.Use())
	case orchestration.RequestStale:
		return fmt.Sprintf("stale: %s could not be given, since the request no longer held. Do not ask for it again "+
			"now.", r.Use())
	default:
		return fmt.Sprintf("pending: a card was shown to the owner asking him to give you %s (request %s). End your "+
			"turn now; do not call request_connection again or poll: you are woken in this chat with his answer.",
			r.Use(), r.ID)
	}
}
