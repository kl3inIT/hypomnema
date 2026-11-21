package agentapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// The owner's "log in again" items (issue #1023, section 6; the owner's plan of 2026-11-21): a Bot that keeps his
// Inbox reads his accounts in a Routine's run that reads only, and each read says whether the account's sign-in
// still works. A read refused for it (HTTP 401 or 403 through account_read, a Zalo or WhatsApp account that must be
// linked again) adds or keeps the account's one open item; a read that works closes it with that read as evidence
// (inboxstore.SignInFailed, SignInWorks). It is done here, by the code, so that a broken account is never one item
// per failure, whatever the Bot's prompt says.

// deviceStates says whether a messaging account must be linked again: integration.Service's DeviceState.
type deviceStates interface {
	DeviceState(integration, label string) messaging.State
}

// keepsSignIns says whether an agent's reads keep the owner's sign-in items: a read run, its pi or its check
// script, of a Bot that keeps the Inbox (its switches give it the Inbox's tools).
func keepsSignIns(a agent) bool {
	return a.readRun && a.bot != "" && a.botSwitches.Allows(agenttool.ToolInboxAdd)
}

// signInRead records what a read of the owner's account (integration:label) said of its sign-in: works, or refused
// with why. Its own failure is logged: the read's answer stands.
func (s *Server) signInRead(ctx context.Context, a agent, account string, works bool, why string) {
	if !keepsSignIns(a) {
		return
	}

	runID, err := s.runOf(ctx, a)
	if err != nil || runID == "" {
		if err != nil {
			s.logger.WarnContext(ctx, "the run of a read could not be read", slog.String("event", "agentapi.failed"),
				slog.String("agent", a.name), slog.String("error", err.Error()))
		}

		return
	}

	bot := inboxstore.Actor{Kind: inboxstore.Bot, ID: a.bot}

	var changed bool

	if works {
		_, changed, err = s.store.Inbox().SignInWorks(ctx, bot, account, runID, account+" was read")
	} else {
		_, changed, err = s.store.Inbox().SignInFailed(ctx, bot, account, runID, why)
	}

	if err != nil {
		s.logger.WarnContext(ctx, "the sign-in item of an account could not be kept",
			slog.String("event", "agentapi.failed"), slog.String("agent", a.name), slog.String("account", account),
			slog.String("error", err.Error()))

		return
	}

	if changed {
		s.logger.InfoContext(ctx, "an account's sign-in item changed", slog.String("event", "agentapi.sign_in"),
			slog.String("agent", a.name), slog.String("account", account), slog.Bool("works", works))
	}
}

// runOf is the Routine's run a read-run agent reads in: its chat's, or its Routine's live run for its check.
func (s *Server) runOf(ctx context.Context, a agent) (string, error) {
	if a.routine != "" {
		return s.store.RoutineRunOf(ctx, orchestration.Token{Kind: bearertoken.Bot, Bot: a.bot, Routine: a.routine})
	}

	return s.store.RoutineRunOf(ctx, orchestration.Token{Kind: bearertoken.Bot, Bot: a.bot, Chat: a.chatID})
}

// accountReadSignIn reads an answer of account_read for the account's sign-in: 401 and 403 are refusals of it,
// as the service proxy answers 403 when the account's token cannot be had; another answer under 400 says it works.
func (s *Server) accountReadSignIn(ctx context.Context, a agent, account, host string, status int) {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		s.signInRead(ctx, a, account, false, fmt.Sprintf("HTTP %d from %s", status, host))
	case status < http.StatusBadRequest:
		s.signInRead(ctx, a, account, true, "")
	}
}

// messagingSignIn reads a read of a messaging account's chats or messages for its sign-in: an account the service
// logged out, or whose session is not open, must be linked again; one read whose session needs no link works.
func (s *Server) messagingSignIn(ctx context.Context, a agent, m messagingService, label string, readErr error) {
	if !keepsSignIns(a) {
		return
	}

	account := m.integration + ":" + label

	if states, ok := s.messaging.(deviceStates); ok {
		if st := states.DeviceState(m.integration, label); st.NeedsLink() {
			s.signInRead(ctx, a, account, false, account+" must be linked again: "+string(st.Status))

			return
		}
	}

	switch {
	case readErr == nil:
		s.signInRead(ctx, a, account, true, "")
	case errors.Is(readErr, orchestration.ErrUnavailable):
		s.signInRead(ctx, a, account, false, readErr.Error())
	}
}
