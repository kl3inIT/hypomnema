package inboxstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// move is one change of state.
type move struct{ from, to string }

// transitions lists every change of state and the kinds of Actor that may make it, in one place (issue #845,
// Transitions; docs/conventions.md, "Transitions are guarded writes"). Each operation below adds its own condition.
// A change missing here is refused: in particular review to done, dropped or duplicate, since the Approval must be
// rejected first.
var transitions = map[move][]string{
	// The owner accepts, drops or marks duplicate a proposal; nothing accepts one by itself (Plane, Omi).
	{Proposed, Open}:      {Owner},
	{Proposed, Dropped}:   {Owner, Bot},
	{Proposed, Duplicate}: {Owner},
	// The Bot assigned starts (Chatwoot's assignee_agent_bot_id, Hermes kanban's running).
	{Open, Doing}: {Bot},
	// A Bot's action asks the owner (Twenty's propose_tool_call); his answer moves the item back.
	{Doing, Review}: {System},
	{Review, Doing}: {System},
	{Review, Open}:  {System},
	// The Bot of the attempt completes (Hermes complete_task) or gives up (Chatwoot bot_handoff!); the system ends
	// an attempt that ended.
	{Doing, Done}: {Owner, Bot},
	{Doing, Open}: {Bot, System},
	// The owner closes, or a Bot records it for him (Hermes's ownership fence for a doing item).
	{Open, Done}:      {Owner, Bot},
	{Open, Dropped}:   {Owner, Bot},
	{Doing, Dropped}:  {Owner, Bot},
	{Open, Duplicate}: {Owner},
	{Done, Open}:      {Owner},
	{Dropped, Open}:   {Owner},
	{Duplicate, Open}: {Owner},
}

// allowed says whether actor may move a to-do from one state to another: a conflict when the move is not one of the
// transitions, forbidden when it is but not for this actor.
func allowed(from, to string, actor Actor) error {
	who, ok := transitions[move{from, to}]
	if !ok {
		return fmt.Errorf("%w: a %s to-do cannot become %s", storeerr.ErrConflict, from, to)
	}

	if !slices.Contains(who, actor.Kind) {
		return fmt.Errorf("%w: a %s cannot move a to-do from %s to %s", storeerr.ErrForbidden, actor.Kind, from, to)
	}

	return nil
}

// Change is what every mutation carries: who makes it, the to-do, the revision the caller read and, for a Bot on an
// item in doing or review, the id of its attempt. A stale revision or another attempt's id is a conflict and
// changes nothing.
type Change struct {
	Actor    Actor
	ID       string
	Revision int
	WorkID   string
}

// change is one guarded write of a to-do: it reads the row, checks the revision and the attempt, lets apply make
// the next row and refuse, checks the move of state, writes the row at the revision read and in the state read, and
// records the event in the same transaction. extra goes into the event's after, such as a reason.
func (s *Store) change(
	ctx context.Context, c Change, kind string,
	apply func(q *store.Queries, cur store.Todo, next *store.Todo, now int64) (extra map[string]any, err error),
) (Todo, error) {
	if err := checkActor(c.Actor); err != nil {
		return Todo{}, err
	}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		cur, err := q.GetTodo(ctx, c.ID)
		if err != nil {
			return storeutil.NotFound(err, "to-do", c.ID)
		}

		if int(cur.Revision) != c.Revision {
			return fmt.Errorf("%w: to-do %s is at revision %d, not %d", storeerr.ErrConflict, c.ID, cur.Revision,
				c.Revision)
		}

		// The fence of an attempt (Hermes's ownership fence): a Bot writes to an item in doing or review only with
		// the id of the attempt on it, so an attempt the owner closed or that ended writes nothing more; and only the
		// Bot that started the attempt does (Hermes's claimer check), not another Bot that knows its id.
		if c.Actor.Kind == Bot && (cur.State == Doing || cur.State == Review || c.WorkID != "") {
			if c.WorkID != cur.WorkID.String {
				return fmt.Errorf("%w: to-do %s is not in the attempt %q", storeerr.ErrConflict, c.ID, c.WorkID)
			}

			if c.Actor.ID != cur.WorkBot.String {
				return fmt.Errorf("%w: the attempt on to-do %s is another Bot's", storeerr.ErrForbidden, c.ID)
			}
		}

		next := cur

		extra, err := apply(q, cur, &next, now)
		if err != nil {
			return err
		}

		stateful := kind == EventState || kind == EventLinkedApproval
		if !stateful && next == cur {
			return nil // Nothing changes: no write and no event.
		}

		if stateful || next.State != cur.State {
			if err := allowed(cur.State, next.State, c.Actor); err != nil {
				return err
			}

			next.SnoozedUntil.Valid = false

			if next.State != Doing && next.State != Review {
				next.WorkKind.Valid, next.WorkID.Valid, next.WorkBot.Valid = false, false, false
			}

			if next.State != Review {
				next.ApprovalID.Valid = false
			}
		}

		n, err := q.UpdateTodo(ctx, store.UpdateTodoParams{
			Title: next.Title, Notes: next.Notes, DoneWhen: next.DoneWhen, DueKind: next.DueKind,
			DueDate: next.DueDate, DueZone: next.DueZone, DueAt: next.DueAt, OverdueAt: next.OverdueAt,
			AssigneeKind: next.AssigneeKind, Assignee: next.Assignee, State: next.State,
			SnoozedUntil: next.SnoozedUntil, WorkKind: next.WorkKind, WorkID: next.WorkID, WorkBot: next.WorkBot,
			ApprovalID: next.ApprovalID, DoneByKind: next.DoneByKind, DoneBy: next.DoneBy,
			RecordedByKind: next.RecordedByKind, RecordedBy: next.RecordedBy, ClosedHow: next.ClosedHow,
			CloseReason: next.CloseReason, Evidence: next.Evidence, ClosedAt: next.ClosedAt,
			DuplicateOf: next.DuplicateOf, Now: now, ID: cur.ID, Revision: cur.Revision, FromState: cur.State,
		})
		if err != nil {
			return fmt.Errorf("changing to-do %s: %w", c.ID, err)
		}

		if n != 1 {
			return fmt.Errorf("%w: to-do %s changed meanwhile", storeerr.ErrConflict, c.ID)
		}

		written, err := q.GetTodo(ctx, c.ID)
		if err != nil {
			return fmt.Errorf("reading to-do %s: %w", c.ID, err)
		}

		before, after := diff(fields(cur), fields(written))
		maps.Copy(after, extra)

		return addEvent(ctx, q, c.ID, now, written.Revision, c.Actor, kind, before, after)
	})
	if err != nil {
		return Todo{}, err
	}

	return s.get(ctx, s.read(), c.ID)
}

// Accept opens a proposed to-do; only the owner does.
func (s *Store) Accept(ctx context.Context, c Change) (Todo, error) {
	return s.change(ctx, c, EventState,
		func(_ *store.Queries, cur store.Todo, next *store.Todo, _ int64) (map[string]any, error) {
			if cur.State != Proposed {
				return nil, fmt.Errorf("%w: only a proposed to-do is accepted", storeerr.ErrConflict)
			}

			next.State = Open

			return nil, nil
		})
}

// Start puts the Bot assigned on an open to-do, in the attempt w (its chat or a Run). Only an open item starts: an
// item in review leaves it only by its Approval's answer (ApprovalApproved, ApprovalEnded).
func (s *Store) Start(ctx context.Context, c Change, w Work) (Todo, error) {
	if (w.Kind != WorkChat && w.Kind != WorkRun) || w.ID == "" || len(w.ID) > MaxID {
		return Todo{}, fmt.Errorf("%w: an attempt is a chat or a Run, by its id", storeerr.ErrInvalid)
	}

	if c.Actor.Kind != Bot {
		return Todo{}, fmt.Errorf("%w: only the Bot assigned starts a to-do", storeerr.ErrForbidden)
	}

	return s.change(ctx, c, EventState,
		func(_ *store.Queries, cur store.Todo, next *store.Todo, _ int64) (map[string]any, error) {
			if cur.State != Open {
				return nil, fmt.Errorf("%w: to-do %s is %s, not open", storeerr.ErrConflict, cur.ID, cur.State)
			}

			if cur.AssigneeKind != ABot || cur.Assignee != c.Actor.ID {
				return nil, fmt.Errorf("%w: only the Bot assigned starts to-do %s", storeerr.ErrForbidden, cur.ID)
			}

			next.State = Doing
			next.WorkKind, next.WorkID = nullString(w.Kind), nullString(w.ID)
			next.WorkBot = nullString(c.Actor.ID)

			return nil, nil
		})
}

// LinkApproval puts a doing to-do in review while the pending Approval approvalID, asked by its attempt, waits for
// the owner; the system does it. An item has one pending Approval: a second is refused, and so is one asked in
// another chat or Run than the item's attempt.
func (s *Store) LinkApproval(ctx context.Context, c Change, approvalID string) (Todo, error) {
	return s.change(ctx, c, EventLinkedApproval,
		func(q *store.Queries, cur store.Todo, next *store.Todo, _ int64) (map[string]any, error) {
			if cur.State == Review {
				return nil, fmt.Errorf("%w: to-do %s already waits for Approval %s", storeerr.ErrConflict, cur.ID,
					cur.ApprovalID.String)
			}

			a, err := q.GetApproval(ctx, approvalID)
			if err != nil {
				return nil, storeutil.NotFound(err, "Approval", approvalID)
			}

			if a.Status != "pending" {
				return nil, fmt.Errorf("%w: Approval %s is %s, not pending", storeerr.ErrConflict, a.ID, a.Status)
			}

			if cur.State != Doing {
				return nil, fmt.Errorf("%w: to-do %s is %s, not doing", storeerr.ErrConflict, cur.ID, cur.State)
			}

			asked := a.ChatID
			if cur.WorkKind.String == WorkRun {
				asked = a.RunID
			}

			if !asked.Valid || asked.String != cur.WorkID.String {
				return nil, fmt.Errorf("%w: Approval %s was not asked in the attempt on to-do %s", storeerr.ErrConflict,
					a.ID, cur.ID)
			}

			next.State = Review
			next.ApprovalID = nullString(approvalID)

			return nil, nil
		})
}

// ApprovalApproved puts a to-do in review back to doing once the owner approved its Approval: the attempt goes on
// (the service proxy continues a call on approval).
func (s *Store) ApprovalApproved(ctx context.Context, c Change) (Todo, error) {
	return s.change(ctx, c, EventState,
		func(q *store.Queries, cur store.Todo, next *store.Todo, _ int64) (map[string]any, error) {
			if err := decided(ctx, q, cur, "approved"); err != nil {
				return nil, err
			}

			next.State = Doing

			return nil, nil
		})
}

// ApprovalEnded opens a to-do in review again once its Approval was rejected or expired; the attempt is cleared and
// the reason recorded.
func (s *Store) ApprovalEnded(ctx context.Context, c Change, reason string) (Todo, error) {
	if err := checkReason(reason, true); err != nil {
		return Todo{}, err
	}

	return s.change(ctx, c, EventState,
		func(q *store.Queries, cur store.Todo, next *store.Todo, _ int64) (map[string]any, error) {
			if err := decided(ctx, q, cur, "rejected"); err != nil {
				return nil, err
			}

			next.State = Open

			return reasonOf(reason), nil
		})
}

// decided checks that the item is in review and its Approval has the status the owner's answer gives it; an
// expired Approval is a rejected one.
func decided(ctx context.Context, q *store.Queries, cur store.Todo, status string) error {
	if cur.State != Review {
		return fmt.Errorf("%w: to-do %s waits for no Approval", storeerr.ErrConflict, cur.ID)
	}

	a, err := q.GetApproval(ctx, cur.ApprovalID.String)
	if err != nil {
		return storeutil.NotFound(err, "Approval", cur.ApprovalID.String)
	}

	if a.Status != status {
		return fmt.Errorf("%w: Approval %s is %s, not %s", storeerr.ErrConflict, a.ID, a.Status, status)
	}

	return nil
}

// Release opens a doing to-do again: its Bot gives up, or the system ends an attempt that ended; the reason is
// recorded.
func (s *Store) Release(ctx context.Context, c Change, reason string) (Todo, error) {
	if err := checkReason(reason, true); err != nil {
		return Todo{}, err
	}

	return s.change(ctx, c, EventState,
		func(_ *store.Queries, cur store.Todo, next *store.Todo, _ int64) (map[string]any, error) {
			if cur.State != Doing {
				return nil, fmt.Errorf("%w: to-do %s is %s, not doing", storeerr.ErrConflict, cur.ID, cur.State)
			}

			next.State = Open

			return reasonOf(reason), nil
		})
}

// Closing is what Done records.
type Closing struct {
	// DoneBy is who did it: me, a person or a Bot.
	DoneBy Assignee
	// How is ClosedByHand or ClosedByBot.
	How      string
	Reason   string
	Evidence []Link
}

// Done closes an open or doing to-do as done. The owner closes any; closing a doing item fences its attempt. A Bot
// closes the item of its own attempt, done by itself, with evidence; or records for the owner what he did, done by
// him, with his message as evidence.
func (s *Store) Done(ctx context.Context, c Change, cl Closing) (Todo, error) {
	cl.DoneBy.Name = strings.TrimSpace(cl.DoneBy.Name)

	switch {
	case cl.How == ClosedByAI || cl.How == ClosedByTime:
		return Todo{}, fmt.Errorf("%w: a to-do is closed by hand or by a Bot for now", storeerr.ErrInvalid)
	case cl.How != ClosedByHand && cl.How != ClosedByBot:
		return Todo{}, fmt.Errorf("%w: unknown way of closing %q", storeerr.ErrInvalid, cl.How)
	case cl.DoneBy.Kind != Me && cl.DoneBy.Kind != Person && cl.DoneBy.Kind != ABot:
		return Todo{}, fmt.Errorf("%w: a to-do is done by me, a person or a Bot", storeerr.ErrInvalid)
	}

	if err := checkReason(cl.Reason, false); err != nil {
		return Todo{}, err
	}

	if err := checkEvidence(cl.Evidence); err != nil {
		return Todo{}, err
	}

	if c.Actor.Kind == Bot {
		if err := botCloses(c.Actor, cl.DoneBy, cl.Evidence); err != nil {
			return Todo{}, err
		}
	}

	forOwner := c.Actor.Kind == Bot && cl.DoneBy.Kind == Me

	evidence, err := json.Marshal(nonNil(cl.Evidence))
	if err != nil {
		return Todo{}, fmt.Errorf("writing the evidence: %w", err)
	}

	return s.change(ctx, c, EventState,
		func(q *store.Queries, cur store.Todo, next *store.Todo, now int64) (map[string]any, error) {
			if err := checkAssignee(ctx, q, cl.DoneBy); err != nil {
				return nil, err
			}

			// The Bot of the attempt closes only its own item in doing (Hermes complete_task); the fence proved the
			// attempt.
			if c.Actor.Kind == Bot && cl.DoneBy.Kind == ABot && cur.State != Doing {
				return nil, fmt.Errorf("%w: a Bot closes as its own only the to-do it is doing", storeerr.ErrForbidden)
			}

			if forOwner {
				if err := ownersMessage(ctx, q, cur, cl.Evidence); err != nil {
					return nil, err
				}
			}

			next.State = Done
			next.DoneByKind, next.DoneBy = nullString(cl.DoneBy.Kind), nullString(cl.DoneBy.Name)
			closeFields(next, c.Actor, cl.How, cl.Reason, string(evidence), now)

			return nil, nil
		})
}

// botCloses is who a Bot may close a to-do as: itself, with evidence of its work, or the owner, with his message.
func botCloses(actor Actor, doneBy Assignee, evidence []Link) error {
	switch {
	case doneBy.Kind == ABot && doneBy.Name == actor.ID:
		if len(evidence) == 0 {
			return fmt.Errorf("%w: a Bot closes a to-do with evidence of what it did", storeerr.ErrInvalid)
		}

		return nil
	case doneBy.Kind == Me:
		return nil // ownersMessage checks his message against the item.
	}

	return fmt.Errorf("%w: a Bot records a to-do done by itself or by the owner", storeerr.ErrForbidden)
}

// ownersMessage is the evidence a Bot recording for the owner gives: a chat_message Link whose ref is the seq of a
// message that exists, was written by the owner (role owner and no sender: a Channel person's or the coordinator's
// message is not his, as OwnerTurn reads it) and is in the item's chat: the chat it came from or the chat of its
// attempt.
func ownersMessage(ctx context.Context, q *store.Queries, cur store.Todo, evidence []Link) error {
	var chats []string

	if cur.SourceKind == FromChat {
		chats = append(chats, cur.SourceID)
	}

	if cur.WorkKind.String == WorkChat {
		chats = append(chats, cur.WorkID.String)
	}

	for _, l := range evidence {
		if l.Kind != LinkChatMessage {
			continue
		}

		seq, err := strconv.ParseInt(l.Ref, 10, 64)
		if err != nil {
			continue
		}

		m, err := q.GetTodoChatMessage(ctx, seq)

		switch {
		case errors.Is(err, sql.ErrNoRows):
			continue
		case err != nil:
			return fmt.Errorf("reading chat message %d: %w", seq, err)
		case m.Role == "owner" && !m.SenderID.Valid && slices.Contains(chats, m.ChatID):
			return nil
		}
	}

	return fmt.Errorf("%w: a Bot recording for the owner gives as evidence his message in the to-do's chat",
		storeerr.ErrInvalid)
}

// Drop closes a proposed, open or doing to-do as dropped, with its reason; the owner does, or a Bot recording it
// for him with his message as evidence.
func (s *Store) Drop(ctx context.Context, c Change, reason string, evidence []Link) (Todo, error) {
	if err := checkReason(reason, true); err != nil {
		return Todo{}, err
	}

	if err := checkEvidence(evidence); err != nil {
		return Todo{}, err
	}

	b, err := json.Marshal(nonNil(evidence))
	if err != nil {
		return Todo{}, fmt.Errorf("writing the evidence: %w", err)
	}

	return s.change(ctx, c, EventState,
		func(q *store.Queries, cur store.Todo, next *store.Todo, now int64) (map[string]any, error) {
			if c.Actor.Kind == Bot {
				if err := ownersMessage(ctx, q, cur, evidence); err != nil {
					return nil, err
				}
			}

			next.State = Dropped
			closeFields(next, c.Actor, ClosedByHand, reason, string(b), now)

			return nil, nil
		})
}

// MarkDuplicate closes a proposed or open to-do as a duplicate of another, which exists, is not the item, is not a
// duplicate itself; and the item is the target of no other duplicate. The owner does.
func (s *Store) MarkDuplicate(ctx context.Context, c Change, of string) (Todo, error) {
	return s.change(ctx, c, EventState,
		func(q *store.Queries, cur store.Todo, next *store.Todo, now int64) (map[string]any, error) {
			if of == cur.ID {
				return nil, fmt.Errorf("%w: a to-do is not a duplicate of itself", storeerr.ErrInvalid)
			}

			target, err := q.GetTodo(ctx, of)
			if err != nil {
				return nil, storeutil.NotFound(err, "to-do", of)
			}

			if target.State == Duplicate {
				return nil, fmt.Errorf("%w: to-do %s is itself a duplicate of %s", storeerr.ErrConflict, of,
					target.DuplicateOf.String)
			}

			n, err := q.CountDuplicatesOf(ctx, nullString(cur.ID))
			if err != nil {
				return nil, fmt.Errorf("reading the duplicates of to-do %s: %w", cur.ID, err)
			}

			if n > 0 {
				return nil, fmt.Errorf("%w: to-do %s has duplicates of its own", storeerr.ErrConflict, cur.ID)
			}

			next.State = Duplicate
			next.DuplicateOf = nullString(of)
			closeFields(next, c.Actor, ClosedByHand, "", "[]", now)

			return nil, nil
		})
}

// Reopen opens a closed to-do again; the owner does. Its close, duplicate link, attempt and Approval are cleared;
// the history keeps them, and nothing is run again (Chatwoot's reopen).
func (s *Store) Reopen(ctx context.Context, c Change) (Todo, error) {
	return s.change(ctx, c, EventState,
		func(_ *store.Queries, cur store.Todo, next *store.Todo, _ int64) (map[string]any, error) {
			if !Closed(cur.State) {
				return nil, fmt.Errorf("%w: to-do %s is %s, not closed", storeerr.ErrConflict, cur.ID, cur.State)
			}

			next.State = Open
			next.DoneByKind.Valid, next.DoneBy.Valid = false, false
			next.RecordedByKind.Valid, next.RecordedBy.Valid = false, false
			next.ClosedHow.Valid, next.ClosedAt.Valid, next.DuplicateOf.Valid = false, false, false
			next.CloseReason, next.Evidence = "", "[]"

			return nil, nil
		})
}

// Snooze hides a proposed or open to-do until a time in the future (Plane's snoozed_till, Chatwoot's
// snoozed_until); nil shows it again. The owner does; any change of state clears it.
func (s *Store) Snooze(ctx context.Context, c Change, until *time.Time) (Todo, error) {
	return s.change(ctx, c, EventSnoozed,
		func(_ *store.Queries, cur store.Todo, next *store.Todo, now int64) (map[string]any, error) {
			switch {
			case c.Actor.Kind != Owner:
				return nil, fmt.Errorf("%w: only the owner snoozes a to-do", storeerr.ErrForbidden)
			case cur.State != Proposed && cur.State != Open:
				return nil, fmt.Errorf("%w: a %s to-do is not snoozed", storeerr.ErrConflict, cur.State)
			case until == nil:
				next.SnoozedUntil.Valid = false
			case until.UnixMilli() <= now:
				return nil, fmt.Errorf("%w: a to-do is snoozed until a time to come", storeerr.ErrInvalid)
			default:
				next.SnoozedUntil.Int64, next.SnoozedUntil.Valid = until.UnixMilli(), true
			}

			return nil, nil
		})
}

// Edit is a change of a to-do's fields; nil leaves a field as it is.
type Edit struct {
	Title    *string
	Notes    *string
	DoneWhen *string
	Due      *Due
	Assignee *Assignee
}

// Edit changes the fields of a to-do that is not closed. The owner edits any; a Bot only the items it created or
// that are assigned to it, and not their assignee. An item in review keeps its assignee until its Approval is
// answered.
func (s *Store) Edit(ctx context.Context, c Change, e Edit) (Todo, error) {
	if e == (Edit{}) {
		return Todo{}, fmt.Errorf("%w: nothing to change", storeerr.ErrInvalid)
	}

	if c.Actor.Kind == System {
		return Todo{}, fmt.Errorf("%w: the system does not edit a to-do", storeerr.ErrForbidden)
	}

	var due *dueColumns

	if e.Due != nil {
		d, err := dueRow(*e.Due)
		if err != nil {
			return Todo{}, err
		}

		due = &d
	}

	return s.change(ctx, c, EventEdited,
		func(q *store.Queries, cur store.Todo, next *store.Todo, _ int64) (map[string]any, error) {
			if Closed(cur.State) {
				return nil, fmt.Errorf("%w: a %s to-do is reopened before it is edited", storeerr.ErrConflict, cur.State)
			}

			if c.Actor.Kind == Bot {
				mine := (cur.CreatedByKind == Bot && cur.CreatedBy == c.Actor.ID) ||
					(cur.AssigneeKind == ABot && cur.Assignee == c.Actor.ID)
				if !mine || e.Assignee != nil {
					return nil, fmt.Errorf("%w: a Bot edits only the to-dos it added or is given, and not who does them",
						storeerr.ErrForbidden)
				}
			}

			if e.Title != nil {
				next.Title = strings.TrimSpace(*e.Title)
			}

			if e.Notes != nil {
				next.Notes = *e.Notes
			}

			if e.DoneWhen != nil {
				next.DoneWhen = *e.DoneWhen
			}

			if err := checkText(next.Title, next.Notes, next.DoneWhen); err != nil {
				return nil, err
			}

			if due != nil {
				next.DueKind, next.DueDate, next.DueZone = due.kind, due.date, due.zone
				next.DueAt, next.OverdueAt = due.at, due.overdueAt
			}

			if e.Assignee != nil {
				a := Assignee{Kind: e.Assignee.Kind, Name: strings.TrimSpace(e.Assignee.Name)}
				if cur.State == Review && (a.Kind != cur.AssigneeKind || a.Name != cur.Assignee) {
					return nil, fmt.Errorf("%w: to-do %s waits for an Approval; reject it before giving the item to "+
						"another", storeerr.ErrConflict, cur.ID)
				}

				if err := checkAssignee(ctx, q, a); err != nil {
					return nil, err
				}

				next.AssigneeKind, next.Assignee = a.Kind, a.Name
			}

			return nil, nil
		})
}

// closeFields records who closed a to-do, how and why.
func closeFields(next *store.Todo, actor Actor, how, reason, evidence string, now int64) {
	next.RecordedByKind, next.RecordedBy = nullString(actor.Kind), nullString(actor.ID)
	next.ClosedHow = nullString(how)
	next.CloseReason, next.Evidence = reason, evidence
	next.ClosedAt.Int64, next.ClosedAt.Valid = now, true
}

func checkReason(reason string, required bool) error {
	switch {
	case required && strings.TrimSpace(reason) == "":
		return fmt.Errorf("%w: a reason is required", storeerr.ErrInvalid)
	case utf8.RuneCountInString(reason) > MaxReason:
		return fmt.Errorf("%w: a reason is at most %d characters", storeerr.ErrInvalid, MaxReason)
	}

	return nil
}

func reasonOf(reason string) map[string]any {
	if reason == "" {
		return nil
	}

	return map[string]any{"reason": reason}
}

func nonNil(l []Link) []Link {
	if l == nil {
		return []Link{}
	}

	return l
}

// fields are the fields of a to-do an event shows, by their names in the table.
func fields(r store.Todo) map[string]any {
	m := map[string]any{
		"title": r.Title, "notes": r.Notes, "done_when": r.DoneWhen, "due_kind": r.DueKind,
		"assignee_kind": r.AssigneeKind, "assignee": r.Assignee, "state": r.State, "source_kind": r.SourceKind,
		"close_reason": r.CloseReason, "evidence": json.RawMessage(r.Evidence),
	}

	for k, v := range map[string]any{
		"due_date": r.DueDate, "due_zone": r.DueZone, "due_at": r.DueAt, "snoozed_until": r.SnoozedUntil,
		"source_id": r.SourceID, "work_kind": r.WorkKind, "work_id": r.WorkID, "work_bot": r.WorkBot, "approval_id": r.ApprovalID,
		"done_by_kind": r.DoneByKind, "done_by": r.DoneBy, "recorded_by_kind": r.RecordedByKind,
		"recorded_by": r.RecordedBy, "closed_how": r.ClosedHow, "closed_at": r.ClosedAt,
		"duplicate_of": r.DuplicateOf,
	} {
		m[k] = v
	}

	return nullJSON(m)
}

// diff gives the fields that differ, before and after; the values are JSON, so a NULL is null.
func diff(before, after map[string]any) (map[string]any, map[string]any) {
	b, a := map[string]any{}, map[string]any{}

	for k, v := range after {
		if !equalJSON(before[k], v) {
			b[k], a[k] = before[k], v
		}
	}

	return b, a
}

func equalJSON(x, y any) bool {
	bx, errx := json.Marshal(x)
	by, erry := json.Marshal(y)

	return errx == nil && erry == nil && string(bx) == string(by)
}

// addEvent records a change of a to-do in its history, in the transaction of the change.
func addEvent(
	ctx context.Context, q *store.Queries, id string, now, revision int64, actor Actor, kind string,
	before, after map[string]any,
) error {
	b, err := json.Marshal(before)
	if err != nil {
		return fmt.Errorf("writing the event: %w", err)
	}

	a, err := json.Marshal(after)
	if err != nil {
		return fmt.Errorf("writing the event: %w", err)
	}

	if err := q.InsertTodoEvent(ctx, store.InsertTodoEventParams{
		TodoID: id, At: now, Revision: revision, ActorKind: actor.Kind, Actor: actor.ID, Kind: kind,
		Before: string(b), After: string(a),
	}); err != nil {
		return fmt.Errorf("recording the change of to-do %s: %w", id, err)
	}

	return nil
}

// nullJSON writes the sql.Null values of a map as their value or null.
func nullJSON(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))

	for k, v := range m {
		switch n := v.(type) {
		case driver.Valuer:
			val, err := n.Value()
			if err != nil {
				val = nil
			}

			out[k] = val
		default:
			out[k] = v
		}
	}

	return out
}
