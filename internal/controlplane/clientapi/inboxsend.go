package clientapi

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
)

// Handing an item to a Bot, its thread and the owner's Send (issue #1111). Handing over is the owner's, as GitHub's
// "Assign to Copilot" with its additional instructions is the person's; a comment is the owner's or a Bot's on its
// own items, as Linear's agent reports in an issue's comments; Send is the owner's alone, pressed by him on a draft
// as inbox-zero and Zero Mail send an AI draft only when the user presses Send. A Bot's token never sends as the
// owner (owner, 2026-11-17 and 2026-11-21; Send from the web is his decision of 2026-11-23).

// InboxSender sends a text as the owner to an Inbox item's own chat or thread, through his own account: a Zalo or
// WhatsApp chat of the Connection the item names, a Lark chat with his Lark sign-in, or a reply in the item's Gmail
// thread. It gives the message sent as the evidence that closes the item (inboxstore's *_sent links, a mail).
type InboxSender interface {
	SendAsOwner(ctx context.Context, t inboxstore.Todo, text string) (inboxstore.Link, error)
}

// WithInboxSender sets what sends the owner's replies from the Inbox.
func (s *Server) WithInboxSender(x InboxSender) *Server {
	s.inboxSender = x

	return s
}

// HandTodo gives a to-do to a Bot with the owner's prompt and wakes the Bot in the item's own chat.
func (s *Server) HandTodo(ctx context.Context, req openapi.HandTodoRequestObject,
) (openapi.HandTodoResponseObject, error) {
	b := req.Body

	bot := strings.TrimSpace(val(b.Bot))
	if bot == "" {
		bot = orchestration.DefaultBotID
	}

	t, err := s.svc.Inbox().HandWith(ctx, inboxstore.Change{
		Actor: inboxstore.Actor{Kind: inboxstore.Owner}, ID: req.TodoId, Revision: b.Revision,
	}, bot, inboxstore.HandInput{
		Prompt: val(b.Prompt), ChatID: val(b.ChatId), Attachments: fromAttachments(val(b.Attachments)),
	})
	if err != nil {
		return nil, err
	}

	return openapi.HandTodo200JSONResponse(todoOut(t, true)), nil
}

// CommentTodo adds a comment to a to-do's thread, as the token's caller.
func (s *Server) CommentTodo(ctx context.Context, req openapi.CommentTodoRequestObject,
) (openapi.CommentTodoResponseObject, error) {
	c, err := s.inboxCallerOf(ctx)
	if err != nil {
		return nil, err
	}

	t, err := s.svc.Inbox().CommentWith(ctx, c.actor, req.TodoId, req.Body.Text, inboxstore.Attached{
		Uploads: fromAttachments(val(req.Body.Attachments)),
	})
	if err != nil {
		return nil, err
	}

	return openapi.CommentTodo200JSONResponse(todoOut(t, true)), nil
}

// SendTodoDraft sends the owner's reply to the item's own chat or thread and closes the item done by him, with the
// message sent as its evidence. Only the item's source is sent to: the request names no chat.
func (s *Server) SendTodoDraft(ctx context.Context, req openapi.SendTodoDraftRequestObject,
) (openapi.SendTodoDraftResponseObject, error) {
	if principal(ctx).Kind != bearertoken.Owner {
		return nil, fmt.Errorf("%w: only the owner sends a reply from the Inbox", errForbidden)
	}

	if s.inboxSender == nil {
		return nil, fmt.Errorf("%w: this control plane sends no replies", orchestration.ErrUnavailable)
	}

	b := req.Body
	if strings.TrimSpace(b.Text) == "" {
		return nil, fmt.Errorf("%w: a reply has its text", orchestration.ErrInvalid)
	}

	// One Send at a time for each item, so that a second press while the first is sent finds the item closed (or
	// its revision moved) rather than sending twice.
	lock, _ := s.inboxSends.LoadOrStore(req.TodoId, &sync.Mutex{})
	mu, _ := lock.(*sync.Mutex)

	mu.Lock()
	defer mu.Unlock()

	inbox := s.svc.Inbox()

	t, err := inbox.Get(ctx, req.TodoId)

	switch {
	case err != nil:
		return nil, err
	case t.Revision != b.Revision:
		return nil, fmt.Errorf("%w: to-do %s is at revision %d, not %d; nothing was sent", orchestration.ErrConflict,
			t.ID, t.Revision, b.Revision)
	case inboxstore.Closed(t.State):
		return nil, fmt.Errorf("%w: to-do %s is %s; nothing was sent", orchestration.ErrConflict, t.ID, t.State)
	case t.Source.ConnectionIntegration() == "":
		return nil, fmt.Errorf("%w: to-do %s came from %s, which has no chat of the owner's to send to",
			orchestration.ErrInvalid, t.ID, t.Source.Kind)
	}

	if err := inbox.CheckSourceAccount(ctx, t.Source); err != nil {
		return nil, err
	}

	sent, err := s.inboxSender.SendAsOwner(ctx, t, b.Text)
	if err != nil {
		return nil, err
	}

	s.logger.InfoContext(ctx, "the owner sent a reply from the inbox", slog.String("event", "clientapi.inbox_send"),
		slog.String("todo", t.ID), slog.String("source", t.Source.Kind))

	// The message is sent: the item is closed with it as the evidence (issue #1004's), and a close that cannot be
	// made now (a proposal is accepted first; an item in review waits for its Approval) leaves the item as it is.
	closed, err := s.closeSent(ctx, t.ID, sent)
	if err != nil {
		s.logger.WarnContext(ctx, "a reply was sent but its item stays open", slog.String("todo", t.ID),
			slog.String("error", err.Error()))

		if closed, err = inbox.Get(ctx, t.ID); err != nil {
			return nil, err
		}
	}

	return openapi.SendTodoDraft200JSONResponse(openapi.TodoSent{Sent: linkOut(sent), Todo: todoOut(closed, true)}), nil
}

// closeSent closes an item done by the owner by hand, with the message he sent as the evidence; a proposal is
// accepted first.
func (s *Server) closeSent(ctx context.Context, id string, sent inboxstore.Link) (inboxstore.Todo, error) {
	inbox := s.svc.Inbox()
	owner := inboxstore.Actor{Kind: inboxstore.Owner}

	t, err := inbox.Get(ctx, id)
	if err != nil {
		return inboxstore.Todo{}, err
	}

	if t.State == inboxstore.Proposed {
		if t, err = inbox.Accept(ctx, inboxstore.Change{Actor: owner, ID: id, Revision: t.Revision}); err != nil {
			return inboxstore.Todo{}, err
		}
	}

	return inbox.Done(ctx, inboxstore.Change{Actor: owner, ID: id, Revision: t.Revision}, inboxstore.Closing{
		DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByHand, Reason: "Sent from the Inbox",
		Evidence: []inboxstore.Link{sent},
	})
}
