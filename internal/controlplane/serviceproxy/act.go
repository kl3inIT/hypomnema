package serviceproxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// An act (issue #645): something the control plane does itself for a Connection, not an HTTP request to the
// service, such as sending a WhatsApp message through the session it holds. It is answered by the same decision
// as a request (decide): the action's answer for the Connection, the owner's Approval when it asks, and the log
// line. The caller does it only when Judge lets it.

// Act is one act an agent asks for.
type Act struct {
	// Caller is who asks; the API that takes the act has checked that it may use the Connection.
	Caller      Caller
	Integration string
	Label       string
	// ChatID is the chat a coordinator works in, so that the Approval it waits for is answered there.
	ChatID string
	// Action is the id of the action, one of the Integration's catalog.
	Action string
	// Subject says which one it is, after the action's name in the Approval's title.
	Subject string
	// Fields are what the owner is shown.
	Fields []orchestration.Field
	// Hash is the digest of everything the act does, so that the same act asked again is the same Approval.
	Hash string
	// AttemptID is the attempt at an Inbox item the act is made in (issue #1004), which its Approval records.
	AttemptID string
	// Opened is the agent's request the act is made in, whose credential is read again when an Approval the act
	// waited for is answered (issue #1071); nil keeps Caller as it is.
	Opened *http.Request
}

// Judge answers an act: nil when it may be done now, a *RefusedError with what the caller is told otherwise.
func (p *Proxy) Judge(ctx context.Context, a Act) error {
	catalog, ok := CatalogOf(a.Integration)
	if !ok {
		return fmt.Errorf("%w: %s has no actions", orchestration.ErrInvalid, a.Integration)
	}

	action, ok := catalog.Action(a.Action)
	if !ok {
		return fmt.Errorf("%w: %s has no action %s", orchestration.ErrInvalid, a.Integration, a.Action)
	}

	s := session{
		caller: a.Caller, integration: a.Integration, label: a.Label, chatID: a.ChatID, catalog: catalog,
		attempt: a.AttemptID, opened: a.Opened,
	}

	if err := p.readsOnly(ctx, &s); err != nil {
		return err
	}

	title := action.Name
	if a.Subject != "" {
		title += ": " + clipTitle(a.Subject)
	}

	_, verdict, message, err := p.decide(ctx, action, s, false, func(Action) asking {
		return asking{title: title, fields: a.Fields, hash: a.Hash}
	})
	if err != nil {
		p.cfg.Logger.ErrorContext(ctx, "the proxy could not decide an act", slog.String("event", "serviceproxy.failed"),
			slog.String("agent", s.caller.Name), slog.String("action", action.ID), slog.String("error", err.Error()))

		verdict, message = "error", "the control plane could not decide this; it was not done. Try again"
	}

	p.cfg.Logger.InfoContext(ctx, "an act through the service proxy",
		slog.String("event", "serviceproxy.request"), slog.String("agent", s.caller.Name),
		slog.String("connection", s.connection()), slog.String("action", action.ID), slog.String("verdict", verdict))

	if message != "" {
		return &RefusedError{Message: message}
	}

	return nil
}
