package clientapi

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
)

// The Inbox (issues #838, #966): the to-dos of the owner, the people he waits on and his Bots, over the store of
// part 1 (orchestration/inboxstore). Every token reads it. Who writes, and where an item comes from, is taken from
// the token, never from the body, as inboxstore's Change asks: the owner by hand, a Bot in its chat with the owner,
// a Bot in a Routine's run; a coordinator token only reads (policy.go, scopeActor).
// The transitions the system makes (LinkApproval, ApprovalApproved, ApprovalEnded) are not served: Approvals drive
// them (part 3).

// inboxCaller is who a request acts as in the Inbox, and where an item it adds comes from.
type inboxCaller struct {
	actor inboxstore.Actor
	// source is the kind of source the token adds from: by hand, a chat, or a Routine's run (which may name what of
	// the owner's it read instead).
	source string
	// chat is the chat with the owner a Bot acts in; run the Routine's run.
	chat, run string
}

// inboxCallerOf is the caller of the request: the owner; a Bot in a Routine's run (a script's token, or the pi of
// a prompt run's chat while the run is live); or a Bot in its chat with the owner. A coordinator token acts as no
// one.
func (s *Server) inboxCallerOf(ctx context.Context) (inboxCaller, error) {
	tok := principal(ctx)

	switch tok.Kind {
	case bearertoken.Owner:
		return inboxCaller{actor: inboxstore.Actor{Kind: inboxstore.Owner}, source: inboxstore.ByHand}, nil
	case bearertoken.Bot:
		bot := inboxstore.Actor{Kind: inboxstore.Bot, ID: tok.Bot}

		run, err := s.svc.RoutineRunOf(ctx, tok)
		if err != nil {
			return inboxCaller{}, err
		}

		if run != "" || tok.Routine != "" {
			return inboxCaller{actor: bot, source: inboxstore.FromRoutine, run: run}, nil
		}

		return inboxCaller{actor: bot, source: inboxstore.FromChat, chat: tok.Chat}, nil
	default:
		return inboxCaller{}, fmt.Errorf("%w: only the owner and a Bot write in the Inbox", errForbidden)
	}
}

// change is the store's Change of an item by the caller, at the revision the request read.
func (c inboxCaller) change(id string, revision int, attempt *string) inboxstore.Change {
	return inboxstore.Change{Actor: c.actor, ID: id, Revision: revision, Attempt: val(attempt), Chat: c.chat}
}

// ListTodos lists one page of the to-dos.
func (s *Server) ListTodos(ctx context.Context, req openapi.ListTodosRequestObject,
) (openapi.ListTodosResponseObject, error) {
	p := req.Params
	f := inboxstore.Filter{
		SourceKind: string(val(p.SourceKind)), NeedsYou: val(p.NeedsYou), WithSnoozed: val(p.IncludeSnoozed),
		WithClosed: val(p.IncludeClosed), WithStale: val(p.IncludeStale),
		DueFrom: val(p.DueFrom), DueTo: val(p.DueTo),
	}

	for _, st := range val(p.State) {
		f.States = append(f.States, string(st))
	}

	if p.AssigneeKind != nil {
		f.Assignee = &inboxstore.Assignee{Kind: string(*p.AssigneeKind), Name: val(p.Assignee)}
	} else if p.Assignee != nil {
		return nil, fmt.Errorf("%w: assignee names a person or a Bot with assignee_kind", orchestration.ErrInvalid)
	}

	page, err := s.svc.Inbox().List(ctx, f, val(p.Cursor), val(p.Limit))
	if err != nil {
		return nil, err
	}

	out := openapi.TodoList{Data: make([]openapi.Todo, 0, len(page.Items)), Total: page.Total, NextCursor: ptr(page.Next)}
	for _, t := range page.Items {
		out.Data = append(out.Data, todoOut(t, false))
	}

	return openapi.ListTodos200JSONResponse(out), nil
}

// GetInboxNeedsYou counts what in the Inbox needs the owner now: the to-dos, the pending Approvals and the open
// questions to him.
func (s *Server) GetInboxNeedsYou(ctx context.Context, _ openapi.GetInboxNeedsYouRequestObject,
) (openapi.GetInboxNeedsYouResponseObject, error) {
	n, err := s.svc.InboxNeedsYou(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.GetInboxNeedsYou200JSONResponse(openapi.InboxNeedsYou{Count: n}), nil
}

// CreateTodo adds a to-do, as the token's caller and from the token's source.
func (s *Server) CreateTodo(ctx context.Context, req openapi.CreateTodoRequestObject,
) (openapi.CreateTodoResponseObject, error) {
	c, err := s.inboxCallerOf(ctx)
	if err != nil {
		return nil, err
	}

	b := req.Body

	src, err := c.newSource(val(b.Source))
	if err != nil {
		return nil, err
	}

	n := inboxstore.NewTodo{
		Title: b.Title, Notes: val(b.Notes), DoneWhen: val(b.DoneWhen), Draft: val(b.Draft), Due: dueIn(b.Due),
		Assignee: assigneeIn(b.Assignee), Source: src, WhyOpen: string(val(b.WhyOpen)),
		IdempotencyKey: val(req.Params.IdempotencyKey),
	}

	// The state the token adds without saying more, or open when the Bot says why.
	switch {
	case b.State != nil:
		n.State = string(*b.State)
	case n.WhyOpen != "" || c.source != inboxstore.FromRoutine:
		n.State = inboxstore.Open
	default:
		n.State = inboxstore.Proposed
	}

	t, err := s.svc.Inbox().Create(ctx, c.actor, n)
	if err != nil {
		return nil, err
	}

	return openapi.CreateTodo201JSONResponse(todoOut(t, false)), nil
}

// newSource is where an item the caller adds comes from: the token says the kind and its chat or run; a Bot in a
// Routine's run may name what of the owner's it read instead (inboxstore.FoundKinds: a chat at Lark, Zalo or
// WhatsApp, a Gmail thread, a meeting). A kind the token does not add from is refused, and so is
// an id the token gives itself.
func (c inboxCaller) newSource(b openapi.NewTodoSource) (inboxstore.Source, error) {
	src := inboxstore.Source{Kind: c.source, Quote: val(b.Quote), Account: val(b.Account), Ref: linkIn(b.Ref)}

	if b.Kind != nil {
		src.Kind = string(*b.Kind)
	}

	switch {
	case src.Kind == c.source:
	case c.source == inboxstore.FromRoutine && inboxstore.Found(src.Kind):
		src.ID = val(b.Id)

		return src, nil
	default:
		return inboxstore.Source{}, fmt.Errorf("%w: this token adds to-dos from %s, not from %s", errForbidden,
			c.source, src.Kind)
	}

	if b.Id != nil {
		return inboxstore.Source{}, fmt.Errorf("%w: the chat or run a to-do comes from is the token's",
			orchestration.ErrInvalid)
	}

	switch c.source {
	case inboxstore.FromChat:
		src.ID = c.chat
	case inboxstore.FromRoutine:
		if c.run == "" {
			return inboxstore.Source{}, fmt.Errorf("%w: no run of this token's routine is live", orchestration.ErrConflict)
		}

		src.ID = c.run
	}

	return src, nil
}

// GetTodo returns a to-do with its history.
func (s *Server) GetTodo(ctx context.Context, req openapi.GetTodoRequestObject,
) (openapi.GetTodoResponseObject, error) {
	t, err := s.svc.Inbox().Get(ctx, req.TodoId)
	if err != nil {
		return nil, err
	}

	return openapi.GetTodo200JSONResponse(todoOut(t, true)), nil
}

// EditTodo changes a to-do's fields.
func (s *Server) EditTodo(ctx context.Context, req openapi.EditTodoRequestObject,
) (openapi.EditTodoResponseObject, error) {
	c, err := s.inboxCallerOf(ctx)
	if err != nil {
		return nil, err
	}

	b := req.Body
	e := inboxstore.Edit{Title: b.Title, Notes: b.Notes, DoneWhen: b.DoneWhen, Draft: b.Draft}

	if b.Due != nil {
		due := dueIn(b.Due)
		e.Due = &due
	}

	if b.Assignee != nil {
		a := assigneeIn(b.Assignee)
		e.Assignee = &a
	}

	t, err := s.svc.Inbox().Edit(ctx, c.change(req.TodoId, b.Revision, b.Attempt), e)
	if err != nil {
		return nil, err
	}

	return openapi.EditTodo200JSONResponse(todoOut(t, false)), nil
}

// AcceptTodo opens a proposed to-do.
func (s *Server) AcceptTodo(ctx context.Context, req openapi.AcceptTodoRequestObject,
) (openapi.AcceptTodoResponseObject, error) {
	t, err := s.transition(ctx, func(c inboxCaller) (inboxstore.Todo, error) {
		return s.svc.Inbox().Accept(ctx, c.change(req.TodoId, req.Body.Revision, req.Body.Attempt))
	})
	if err != nil {
		return nil, err
	}

	return openapi.AcceptTodo200JSONResponse(t), nil
}

// DoneTodo closes a to-do as done. The owner closes by hand, done by himself unless the body names who; a Bot
// closes as a Bot, done by itself unless it records for the owner.
func (s *Server) DoneTodo(ctx context.Context, req openapi.DoneTodoRequestObject,
) (openapi.DoneTodoResponseObject, error) {
	b := req.Body

	t, err := s.transition(ctx, func(c inboxCaller) (inboxstore.Todo, error) {
		cl := inboxstore.Closing{
			DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByHand, Reason: val(b.Reason),
			Evidence: linksIn(b.Evidence),
		}

		if c.actor.Kind == inboxstore.Bot {
			cl.DoneBy, cl.How = inboxstore.Assignee{Kind: inboxstore.ABot, Name: c.actor.ID}, inboxstore.ClosedByBot
		}

		if b.DoneBy != nil {
			cl.DoneBy = assigneeIn(b.DoneBy)
		}

		return s.svc.Inbox().Done(ctx, c.change(req.TodoId, b.Revision, b.Attempt), cl)
	})
	if err != nil {
		return nil, err
	}

	return openapi.DoneTodo200JSONResponse(t), nil
}

// DropTodo closes a to-do as dropped.
func (s *Server) DropTodo(ctx context.Context, req openapi.DropTodoRequestObject,
) (openapi.DropTodoResponseObject, error) {
	b := req.Body

	t, err := s.transition(ctx, func(c inboxCaller) (inboxstore.Todo, error) {
		return s.svc.Inbox().Drop(ctx, c.change(req.TodoId, b.Revision, b.Attempt), b.Reason, linksIn(b.Evidence))
	})
	if err != nil {
		return nil, err
	}

	return openapi.DropTodo200JSONResponse(t), nil
}

// DuplicateTodo closes a to-do as a duplicate of another.
func (s *Server) DuplicateTodo(ctx context.Context, req openapi.DuplicateTodoRequestObject,
) (openapi.DuplicateTodoResponseObject, error) {
	t, err := s.transition(ctx, func(c inboxCaller) (inboxstore.Todo, error) {
		return s.svc.Inbox().MarkDuplicate(ctx, c.change(req.TodoId, req.Body.Revision, nil), req.Body.Of)
	})
	if err != nil {
		return nil, err
	}

	return openapi.DuplicateTodo200JSONResponse(t), nil
}

// ReopenTodo opens a closed to-do again.
func (s *Server) ReopenTodo(ctx context.Context, req openapi.ReopenTodoRequestObject,
) (openapi.ReopenTodoResponseObject, error) {
	t, err := s.transition(ctx, func(c inboxCaller) (inboxstore.Todo, error) {
		return s.svc.Inbox().Reopen(ctx, c.change(req.TodoId, req.Body.Revision, req.Body.Attempt))
	})
	if err != nil {
		return nil, err
	}

	return openapi.ReopenTodo200JSONResponse(t), nil
}

// SnoozeTodo hides a to-do until a time, or shows it again.
func (s *Server) SnoozeTodo(ctx context.Context, req openapi.SnoozeTodoRequestObject,
) (openapi.SnoozeTodoResponseObject, error) {
	t, err := s.transition(ctx, func(c inboxCaller) (inboxstore.Todo, error) {
		return s.svc.Inbox().Snooze(ctx, c.change(req.TodoId, req.Body.Revision, nil), req.Body.Until)
	})
	if err != nil {
		return nil, err
	}

	return openapi.SnoozeTodo200JSONResponse(t), nil
}

// StartTodo puts the Bot on an open to-do given to it, in its chat or in the Routine's run it is in.
func (s *Server) StartTodo(ctx context.Context, req openapi.StartTodoRequestObject,
) (openapi.StartTodoResponseObject, error) {
	t, err := s.transition(ctx, func(c inboxCaller) (inboxstore.Todo, error) {
		w := inboxstore.Work{Kind: inboxstore.WorkChat, ID: c.chat}

		switch {
		case c.actor.Kind != inboxstore.Bot:
			return inboxstore.Todo{}, fmt.Errorf("%w: only the Bot assigned starts a to-do", errForbidden)
		case c.source == inboxstore.FromRoutine && c.run == "":
			return inboxstore.Todo{}, fmt.Errorf("%w: no run of this token's routine is live",
				orchestration.ErrConflict)
		case c.source == inboxstore.FromRoutine:
			w = inboxstore.Work{Kind: inboxstore.WorkRun, ID: c.run}
		}

		return s.svc.Inbox().Start(ctx, c.change(req.TodoId, req.Body.Revision, req.Body.Attempt), w)
	})
	if err != nil {
		return nil, err
	}

	return openapi.StartTodo200JSONResponse(t), nil
}

// ReleaseTodo gives up a doing to-do.
func (s *Server) ReleaseTodo(ctx context.Context, req openapi.ReleaseTodoRequestObject,
) (openapi.ReleaseTodoResponseObject, error) {
	b := req.Body

	t, err := s.transition(ctx, func(c inboxCaller) (inboxstore.Todo, error) {
		if c.actor.Kind != inboxstore.Bot {
			return inboxstore.Todo{}, fmt.Errorf("%w: only the Bot of the attempt gives a to-do up", errForbidden)
		}

		return s.svc.Inbox().Release(ctx, c.change(req.TodoId, b.Revision, b.Attempt), b.Reason)
	})
	if err != nil {
		return nil, err
	}

	return openapi.ReleaseTodo200JSONResponse(t), nil
}

// transition runs one change of a to-do as the request's caller and gives the item as it left it.
func (s *Server) transition(ctx context.Context, do func(c inboxCaller) (inboxstore.Todo, error),
) (openapi.Todo, error) {
	c, err := s.inboxCallerOf(ctx)
	if err != nil {
		return openapi.Todo{}, err
	}

	t, err := do(c)
	if err != nil {
		return openapi.Todo{}, err
	}

	return todoOut(t, false), nil
}

func dueIn(d *openapi.TodoDue) inboxstore.Due {
	if d == nil {
		return inboxstore.Due{Kind: inboxstore.DueNone}
	}

	due := inboxstore.Due{Kind: string(d.Kind), At: val(d.At), Zone: val(d.Zone)}
	if d.Date != nil {
		due.Date = d.Date.Format(time.DateOnly)
	}

	return due
}

func assigneeIn(a *openapi.TodoAssignee) inboxstore.Assignee {
	if a == nil {
		return inboxstore.Assignee{Kind: inboxstore.Me}
	}

	return inboxstore.Assignee{Kind: string(a.Kind), Name: val(a.Name)}
}

func linkIn(l *openapi.TodoLink) *inboxstore.Link {
	if l == nil {
		return nil
	}

	return &inboxstore.Link{
		Kind: string(l.Kind), Ref: l.Ref, Label: val(l.Label), Account: val(l.Account), Chat: val(l.Chat),
	}
}

func linksIn(ls *[]openapi.TodoLink) []inboxstore.Link {
	out := make([]inboxstore.Link, 0, len(val(ls)))
	for _, l := range val(ls) {
		out = append(out, *linkIn(&l))
	}

	return out
}

func linkOut(l inboxstore.Link) openapi.TodoLink {
	return openapi.TodoLink{
		Kind: openapi.TodoLinkKind(l.Kind), Ref: l.Ref, Label: ptr(l.Label), Account: ptr(l.Account), Chat: ptr(l.Chat),
	}
}

func actorOut(a inboxstore.Actor) openapi.TodoActor {
	return openapi.TodoActor{Kind: openapi.TodoActorKind(a.Kind), Id: ptr(a.ID)}
}

func assigneeOut(a inboxstore.Assignee) openapi.TodoAssignee {
	return openapi.TodoAssignee{Kind: openapi.TodoAssigneeKind(a.Kind), Name: ptr(a.Name)}
}

// todoOut is a to-do as the API shows it; with its history when events.
func todoOut(t inboxstore.Todo, events bool) openapi.Todo {
	out := openapi.Todo{
		Id: t.ID, Revision: t.Revision, Title: t.Title, Notes: t.Notes, DoneWhen: t.DoneWhen, Draft: t.Draft,
		Due: openapi.TodoDue{Kind: openapi.TodoDueKind(t.Due.Kind), Zone: ptr(t.Due.Zone)}, Assignee: assigneeOut(t.Assignee),
		State: openapi.TodoState(t.State), SnoozedUntil: t.SnoozedUntil,
		Source: openapi.TodoSource{
			Kind: openapi.TodoSourceKind(t.Source.Kind), Id: ptr(t.Source.ID), Account: ptr(t.Source.Account),
			Quote: ptr(t.Source.Quote),
		},
		CreatedBy: actorOut(t.CreatedBy), ApprovalId: ptr(t.ApprovalID), CloseReason: t.CloseReason,
		Evidence: make([]openapi.TodoLink, 0, len(t.Evidence)), ClosedAt: t.ClosedAt, DuplicateOf: ptr(t.DuplicateOf),
		Stale: t.Stale, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}

	if t.Due.Date != "" {
		if d, err := time.Parse(time.DateOnly, t.Due.Date); err == nil {
			out.Due.Date = &openapi_types.Date{Time: d}
		}
	}

	if !t.Due.At.IsZero() {
		out.Due.At = &t.Due.At
	}

	if t.Source.Ref != nil {
		ref := linkOut(*t.Source.Ref)
		out.Source.Ref = &ref
	}

	if t.WhyOpen != "" {
		why := openapi.TodoWhyOpen(t.WhyOpen)
		out.WhyOpen = &why
	}

	if t.Work != nil {
		out.Work = &openapi.TodoWork{
			Attempt: t.Work.Attempt, Kind: openapi.TodoWorkKind(t.Work.Kind), Id: t.Work.ID, Bot: t.Work.Bot,
			StartedAt: t.Work.StartedAt,
		}
	}

	if t.DoneBy != nil {
		by := assigneeOut(*t.DoneBy)
		out.DoneBy = &by
	}

	if t.RecordedBy != nil {
		by := actorOut(*t.RecordedBy)
		out.RecordedBy = &by
	}

	if t.ClosedHow != "" {
		how := openapi.TodoClosedHow(t.ClosedHow)
		out.ClosedHow = &how
	}

	for _, l := range t.Evidence {
		out.Evidence = append(out.Evidence, linkOut(l))
	}

	if events {
		list := make([]openapi.TodoEvent, 0, len(t.Events))

		for _, e := range t.Events {
			var before, after map[string]any

			_ = json.Unmarshal(e.Before, &before)
			_ = json.Unmarshal(e.After, &after)

			list = append(list, openapi.TodoEvent{
				Id: e.ID, At: e.At, Revision: e.Revision, Actor: actorOut(e.Actor), Kind: e.Kind,
				Before: nonNilMap(before), After: nonNilMap(after),
			})
		}

		out.Events = &list
	}

	return out
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}

	return m
}
