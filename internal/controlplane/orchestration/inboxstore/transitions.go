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
// item in doing or review, the id of its attempt (Work.Attempt, which Start gave). A stale revision or another
// attempt's id is a conflict and changes nothing.
type Change struct {
	Actor    Actor
	ID       string
	Revision int
	Attempt  string
	// Chat is the chat with the owner a Bot acts in, taken by the caller from the Bot's authenticated session as the
	// Actor is, never from a field the Bot sends; empty when it acts elsewhere. The owner's message in it is evidence
	// when the Bot records for him.
	Chat string
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

	var got Todo

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		var err error
		got, err = s.changeTx(ctx, q, now, c, kind, apply)

		return err
	})
	if err != nil {
		return Todo{}, err
	}

	// The item as this change left it, or as it was when nothing changed: read in the change's transaction.
	return got, nil
}

// changeTx is change with the caller's transaction: the system's transitions are made in the transaction of what
// causes them (an Approval asked or answered, a chat deleted, a Routine's run ended), so neither outlives the other.
func (s *Store) changeTx(
	ctx context.Context, q *store.Queries, now int64, c Change, kind string,
	apply func(q *store.Queries, cur store.Todo, next *store.Todo, now int64) (extra map[string]any, err error),
) (Todo, error) {
	var got Todo

	err := func() error {
		cur, err := q.GetTodo(ctx, c.ID)
		if err != nil {
			return storeutil.NotFound(err, "to-do", c.ID)
		}

		if int(cur.Revision) != c.Revision {
			return fmt.Errorf("%w: to-do %s is at revision %d, not %d", storeerr.ErrConflict, c.ID, cur.Revision,
				c.Revision)
		}

		// The fence of an attempt (Hermes's ownership fence, its claim_lock): a Bot writes to an item in doing or
		// review only with the id of the attempt on it, so an attempt the owner closed, that ended, or that a later
		// one replaced writes nothing more, even from the same chat or Run; and only the Bot that started the attempt
		// does (Hermes's claimer check), not another Bot that knows its id.
		if c.Actor.Kind == Bot && (cur.State == Doing || cur.State == Review || c.Attempt != "") {
			if c.Attempt != cur.WorkAttempt.String {
				return fmt.Errorf("%w: to-do %s is not in the attempt %q", storeerr.ErrConflict, c.ID, c.Attempt)
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
			// Nothing changes: no write and no event.
			got, err = s.get(ctx, q, c.ID)

			return err
		}

		if stateful || next.State != cur.State {
			if err := allowed(cur.State, next.State, c.Actor); err != nil {
				return err
			}

			next.SnoozedUntil.Valid = false

			if next.State != Doing && next.State != Review {
				next.WorkAttempt.Valid, next.WorkKind.Valid, next.WorkID.Valid = false, false, false
				next.WorkBot.Valid, next.WorkStartedAt.Valid = false, false
			}

			if next.State != Review {
				next.ApprovalID.Valid = false
			}
		}

		n, err := q.UpdateTodo(ctx, store.UpdateTodoParams{
			Title: next.Title, Notes: next.Notes, DoneWhen: next.DoneWhen, Draft: next.Draft, DueKind: next.DueKind,
			DueDate: next.DueDate, DueZone: next.DueZone, DueAt: next.DueAt, OverdueAt: next.OverdueAt,
			AssigneeKind: next.AssigneeKind, Assignee: next.Assignee, State: next.State,
			SourceKind: next.SourceKind, SourceID: next.SourceID, SourceAccount: next.SourceAccount,
			SourceQuote: next.SourceQuote, SourceRef: next.SourceRef,
			SnoozedUntil: next.SnoozedUntil, WorkAttempt: next.WorkAttempt, WorkKind: next.WorkKind, WorkID: next.WorkID,
			WorkBot: next.WorkBot, WorkStartedAt: next.WorkStartedAt, ApprovalID: next.ApprovalID,
			DoneByKind: next.DoneByKind, DoneBy: next.DoneBy,
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

		if err := addEvent(ctx, q, c.ID, now, written.Revision, c.Actor, kind, before, after); err != nil {
			return err
		}

		got, err = s.get(ctx, q, c.ID)

		return err
	}()

	return got, err
}

// applyFunc makes the next row of a change from the row read, or refuses it; extra goes into the event's after.
type applyFunc = func(q *store.Queries, cur store.Todo, next *store.Todo, now int64) (extra map[string]any, err error)

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

// Start puts the Bot assigned on an open to-do, in a new attempt in w (its chat or a Run). Only an open item starts:
// an item in review leaves it only by its Approval's answer (ApprovalApproved, ApprovalEnded). The attempt gets its
// own id (Work.Attempt), as Hermes binds a worker's session to its task, run and claim, not to the chat
// (gateway/session_kanban.py): the Bot's later writes carry it and so does an Approval it asks, so several items
// may be worked on in one chat or Run, each waiting only for its own Approval.
func (s *Store) Start(ctx context.Context, c Change, w Work) (Todo, error) {
	if (w.Kind != WorkChat && w.Kind != WorkRun) || w.ID == "" || len(w.ID) > MaxID {
		return Todo{}, fmt.Errorf("%w: an attempt is a chat or a Run, by its id", storeerr.ErrInvalid)
	}

	if c.Actor.Kind != Bot {
		return Todo{}, fmt.Errorf("%w: only the Bot assigned starts a to-do", storeerr.ErrForbidden)
	}

	return s.change(ctx, c, EventState,
		func(_ *store.Queries, cur store.Todo, next *store.Todo, now int64) (map[string]any, error) {
			if cur.State != Open {
				return nil, fmt.Errorf("%w: to-do %s is %s, not open", storeerr.ErrConflict, cur.ID, cur.State)
			}

			if cur.AssigneeKind != ABot || cur.Assignee != c.Actor.ID {
				return nil, fmt.Errorf("%w: only the Bot assigned starts to-do %s", storeerr.ErrForbidden, cur.ID)
			}

			next.State = Doing
			next.WorkAttempt = nullString(s.newID("att"))
			next.WorkKind, next.WorkID = nullString(w.Kind), nullString(w.ID)
			next.WorkBot = nullString(c.Actor.ID)
			next.WorkStartedAt = sql.NullInt64{Int64: now, Valid: true}

			return nil, nil
		})
}

// LinkApproval puts a doing to-do in review while the pending Approval approvalID, asked by its attempt, waits for
// the owner; the system does it. An item has one pending Approval: a second is refused, and so is one its current
// attempt did not ask (the Approval's AttemptID is another or none): another item's in the same chat or Run, an
// earlier attempt's at this item, or one asked outside any attempt. The chat or Run is only where the attempt works.
func (s *Store) LinkApproval(ctx context.Context, c Change, approvalID string) (Todo, error) {
	return s.change(ctx, c, EventLinkedApproval, linkApproval(ctx, approvalID))
}

func linkApproval(ctx context.Context, approvalID string) applyFunc {
	return func(q *store.Queries, cur store.Todo, next *store.Todo, _ int64) (map[string]any, error) {
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

		if !a.AttemptID.Valid || a.AttemptID.String != cur.WorkAttempt.String {
			return nil, fmt.Errorf("%w: Approval %s was not asked in the attempt on to-do %s", storeerr.ErrConflict,
				a.ID, cur.ID)
		}

		next.State = Review
		next.ApprovalID = nullString(approvalID)

		return nil, nil
	}
}

// ApprovalApproved puts a to-do in review back to doing once the owner approved its Approval: the attempt goes on
// (the service proxy continues a call on approval).
func (s *Store) ApprovalApproved(ctx context.Context, c Change) (Todo, error) {
	return s.change(ctx, c, EventState, approved(ctx))
}

func approved(ctx context.Context) applyFunc {
	return func(q *store.Queries, cur store.Todo, next *store.Todo, _ int64) (map[string]any, error) {
		if err := decided(ctx, q, cur, "approved"); err != nil {
			return nil, err
		}

		next.State = Doing

		return nil, nil
	}
}

// ApprovalEnded opens a to-do in review again once its Approval was rejected or expired; the attempt is cleared and
// the reason recorded.
func (s *Store) ApprovalEnded(ctx context.Context, c Change, reason string) (Todo, error) {
	if err := checkReason(reason, true); err != nil {
		return Todo{}, err
	}

	return s.change(ctx, c, EventState, ended(ctx, reason))
}

func ended(ctx context.Context, reason string) applyFunc {
	return func(q *store.Queries, cur store.Todo, next *store.Todo, _ int64) (map[string]any, error) {
		if err := decided(ctx, q, cur, "rejected"); err != nil {
			return nil, err
		}

		next.State = Open

		return reasonOf(reason), nil
	}
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

	return s.change(ctx, c, EventState, released(reason))
}

func released(reason string) applyFunc {
	return func(_ *store.Queries, cur store.Todo, next *store.Todo, _ int64) (map[string]any, error) {
		if cur.State != Doing {
			return nil, fmt.Errorf("%w: to-do %s is %s, not doing", storeerr.ErrConflict, cur.ID, cur.State)
		}

		next.State = Open

		return reasonOf(reason), nil
	}
}

// The system's transitions (issue #1004), each made in the transaction of what causes it, as the system Actor
// named after it.
var (
	byApprovals = Actor{Kind: System, ID: "approvals"}
	byChats     = Actor{Kind: System, ID: "chats"}
	byRoutines  = Actor{Kind: System, ID: "routines"}
)

// LinkApprovalTx puts the item the attempt is on in review while the pending Approval approvalID, which the attempt
// asked, waits for the owner: the control plane recorded the attempt on the Approval from the item the Bot named
// (orchestration's NewApproval.AttemptID), so the Bot never handles attempt ids. Nothing changes when no item is
// doing in that attempt any more (released or closed). When the item already waits for another Approval of the
// attempt (two requests that read it doing before the first put it in review), it is a conflict, which undoes the
// Approval's write: an item waits for one Approval, and none is left pending that no item waits for. The request
// is asked again once the owner answered the first.
func (s *Store) LinkApprovalTx(ctx context.Context, q *store.Queries, now int64, approvalID, attempt string) error {
	cur, err := q.GetTodoOfAttempt(ctx, nullString(attempt))

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("reading the to-do of attempt %s: %w", attempt, err)
	case cur.State == Review && cur.ApprovalID.String != approvalID:
		return fmt.Errorf("%w: to-do %s already waits for the owner's answer to another request; ask again once he "+
			"has answered it", storeerr.ErrConflict, cur.ID)
	case cur.State != Doing:
		return nil
	}

	_, err = s.changeTx(ctx, q, now, Change{Actor: byApprovals, ID: cur.ID, Revision: int(cur.Revision)},
		EventLinkedApproval, linkApproval(ctx, approvalID))

	return err
}

// ApprovalAnsweredTx moves the item that waits for an Approval the owner answered: back to doing when he approved
// it, open with the reason when he rejected it or nobody answered in time. Nothing changes when no item waits for
// it.
func (s *Store) ApprovalAnsweredTx(ctx context.Context, q *store.Queries, now int64, approvalID string, ok bool,
	reason string,
) error {
	cur, err := q.GetTodoOfApproval(ctx, nullString(approvalID))

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("reading the to-do of Approval %s: %w", approvalID, err)
	}

	apply := ended(ctx, reason)
	if ok {
		apply = approved(ctx)
	}

	_, err = s.changeTx(ctx, q, now, Change{Actor: byApprovals, ID: cur.ID, Revision: int(cur.Revision)}, EventState,
		apply)

	return err
}

// EndAttemptsTx gives back to open, with the reason, every item a Bot is doing in a chat that is deleted or a
// Routine's run that ended (Chatwoot's bot_handoff!; Coder ends an agent's token with its build), and every item there
// in review: its Approval, refused when the run ended or still pending, ends here as one nobody answered (a run's
// open Approvals are refused so when it ends), so no answer can later put the item back to doing in an attempt
// whose chat or run is gone. All in the transaction of the deletion or the end.
func (s *Store) EndAttemptsTx(ctx context.Context, q *store.Queries, now int64, kind, id, reason string) error {
	rows, err := q.ListTodosOfWork(ctx, store.ListTodosOfWorkParams{
		WorkKind: nullString(kind), WorkID: nullString(id),
	})
	if err != nil {
		return fmt.Errorf("reading the to-dos worked on in %s %s: %w", kind, id, err)
	}

	actor := byChats
	if kind == WorkRun {
		actor = byRoutines
	}

	for _, cur := range rows {
		apply := released(reason)

		if cur.State == Review {
			if _, err := q.ExpireApproval(ctx, store.ExpireApprovalParams{
				ID: cur.ApprovalID.String, Now: sql.NullInt64{Int64: now, Valid: true},
			}); err != nil {
				return fmt.Errorf("ending Approval %s: %w", cur.ApprovalID.String, err)
			}

			apply = ended(ctx, reason)
		}

		if _, err := s.changeTx(ctx, q, now, Change{Actor: actor, ID: cur.ID, Revision: int(cur.Revision)}, EventState,
			apply); err != nil {
			return err
		}
	}

	return nil
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

	// A message the owner sent is read before the write, from a store or an API outside this database; what it is
	// checked against (the item's source and when it was made) never changes after the item is made.
	sent := false

	if forOwner && slices.ContainsFunc(cl.Evidence, func(l Link) bool { return sentService[l.Kind] != "" }) {
		t, err := s.Get(ctx, c.ID)
		if err != nil {
			return Todo{}, err
		}

		if sent, err = s.ownerSent(ctx, t, cl.Evidence); err != nil {
			return Todo{}, err
		}
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

			if c.Actor.Kind == Bot {
				if err := notAnotherBots(cur, c.Actor); err != nil {
					return nil, err
				}
			}

			if forOwner && !sent {
				if err := ownersMessage(ctx, q, c, cur, cl.Evidence); err != nil {
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
// message is not his, as OwnerTurn reads it) and is in the item's chat (the chat it came from or the chat of its
// attempt) or in the chat the Bot acts in, where the owner told it (Change.Chat, from its session): a proposal from
// a Routine, Lark or Zalo has no chat of its own.
func ownersMessage(ctx context.Context, q *store.Queries, c Change, cur store.Todo, evidence []Link) error {
	var chats []string

	if c.Chat != "" {
		chats = append(chats, c.Chat)
	}

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

	return fmt.Errorf("%w: a Bot recording for the owner gives as evidence his message in the to-do's chat, or the "+
		"message he sent himself in the to-do's Zalo, WhatsApp or Lark chat after it was made", storeerr.ErrInvalid)
}

// ownerSent says whether a message the owner sent is evidence that he answered the item (issue #1004; owner,
// 2026-11-21: done when he sends): a LinkZaloSent, LinkWhatsAppSent or LinkLarkSent in the item's own chat at that
// service (its source, account and chat; an item without its account has none), that the server keeps of that account or the service's API returns, that
// he sent himself and that is later than the item. claude-chief-of-staff reads the Sent mail to skip a thread
// already answered (commands/triage.md:65); matching on the owner's own outgoing message is inference from the
// business analysis.
func (s *Store) ownerSent(ctx context.Context, t Todo, evidence []Link) (bool, error) {
	if s.sent == nil {
		return false, fmt.Errorf("%w: the messages the owner sent cannot be read on this control plane",
			storeerr.ErrUnavailable)
	}

	for _, l := range evidence {
		// Bound to the item's own account: an item without one (a Lark source names it only optionally) is never
		// closed so, since another of the owner's Connections in the same chat could show the message.
		service := sentService[l.Kind]
		if service == "" || t.Source.Kind != service || l.Chat != t.Source.ID || t.Source.Account == "" ||
			l.Account != t.Source.Account {
			continue
		}

		m, err := s.sent.SentMessage(ctx, service, l.Account, l.Chat, l.Ref)

		switch {
		case errors.Is(err, storeerr.ErrNotFound):
			continue
		case err != nil:
			return false, err
		case m.FromMe && m.Chat == l.Chat && m.At.After(t.CreatedAt):
			return true, nil
		}
	}

	return false, nil
}

// notAnotherBots refuses a Bot closing or dropping an item that is another Bot's: given to another, or added by
// another and not given to this one. An item the owner added stays his to record, with his message.
func notAnotherBots(cur store.Todo, bot Actor) error {
	other := cur.AssigneeKind == ABot && cur.Assignee != bot.ID
	if cur.AssigneeKind != ABot {
		other = cur.CreatedByKind == Bot && cur.CreatedBy != bot.ID
	}

	if other {
		return fmt.Errorf("%w: to-do %s is another Bot's", storeerr.ErrForbidden, cur.ID)
	}

	return nil
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
				if err := notAnotherBots(cur, c.Actor); err != nil {
					return nil, err
				}

				if err := ownersMessage(ctx, q, c, cur, evidence); err != nil {
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
		func(q *store.Queries, cur store.Todo, next *store.Todo, _ int64) (map[string]any, error) {
			if !Closed(cur.State) {
				return nil, fmt.Errorf("%w: to-do %s is %s, not closed", storeerr.ErrConflict, cur.ID, cur.State)
			}

			// An account's "log in again" item takes its account's key again (signin.go).
			if err := retake(ctx, q, cur); err != nil {
				return nil, err
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
	Draft    *string
	Due      *Due
	Assignee *Assignee
	// Source points the item at what it was read in (issue #1105): one of FoundKinds, with its quote and message.
	// A source the caller's token gives (the owner's hand, a chat, a Routine's run) is never set by an edit.
	Source *Source
}

// Edit changes the fields of a to-do that is not closed. The owner edits any; a Bot only the items it created or
// that are assigned to it, and not their assignee, except to hand the item back to the owner (Me) with the reply it
// drafted (issue #1004). An item in doing or review keeps its assignee while the Bot's
// attempt is on it: the attempt is released, closed or its Approval answered first, so the old Bot writes nothing
// more and the new one can start.
func (s *Store) Edit(ctx context.Context, c Change, e Edit) (Todo, error) {
	if e == (Edit{}) {
		return Todo{}, fmt.Errorf("%w: nothing to change", storeerr.ErrInvalid)
	}

	if c.Actor.Kind == System {
		return Todo{}, fmt.Errorf("%w: the system does not edit a to-do", storeerr.ErrForbidden)
	}

	var ref sql.NullString

	if e.Source != nil {
		if !Found(e.Source.Kind) {
			return Todo{}, fmt.Errorf("%w: an item is pointed only at what was read: %s, not %q", storeerr.ErrInvalid,
				strings.Join(FoundKinds, ", "), e.Source.Kind)
		}

		if err := checkSource(*e.Source); err != nil {
			return Todo{}, err
		}

		var err error
		if ref, err = optionalJSON(e.Source.Ref); err != nil {
			return Todo{}, err
		}
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
				if !mine || (e.Assignee != nil && e.Assignee.Kind != Me) {
					return nil, fmt.Errorf("%w: a Bot edits only the to-dos it added or is given, and gives them only "+
						"back to the owner", storeerr.ErrForbidden)
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

			if e.Draft != nil {
				if err := checkDraft(*e.Draft); err != nil {
					return nil, err
				}

				next.Draft = *e.Draft
			}

			if err := checkText(next.Title, next.Notes, next.DoneWhen); err != nil {
				return nil, err
			}

			if due != nil {
				next.DueKind, next.DueDate, next.DueZone = due.kind, due.date, due.zone
				next.DueAt, next.OverdueAt = due.at, due.overdueAt
			}

			if e.Source != nil {
				next.SourceKind, next.SourceID, next.SourceAccount = e.Source.Kind, e.Source.ID, e.Source.Account
				next.SourceQuote, next.SourceRef = e.Source.Quote, ref
			}

			if e.Assignee != nil {
				a := Assignee{Kind: e.Assignee.Kind, Name: strings.TrimSpace(e.Assignee.Name)}
				if (cur.State == Doing || cur.State == Review) && (a.Kind != cur.AssigneeKind || a.Name != cur.Assignee) {
					return nil, fmt.Errorf("%w: a Bot's attempt is on to-do %s; it ends before the item is given to "+
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
		"title": r.Title, "notes": r.Notes, "done_when": r.DoneWhen, "draft": r.Draft, "due_kind": r.DueKind,
		"assignee_kind": r.AssigneeKind, "assignee": r.Assignee, "state": r.State, "source_kind": r.SourceKind,
		"source_quote": r.SourceQuote,
		"close_reason": r.CloseReason, "evidence": json.RawMessage(r.Evidence),
	}

	for k, v := range map[string]any{
		"due_date": r.DueDate, "due_zone": r.DueZone, "due_at": r.DueAt, "snoozed_until": r.SnoozedUntil,
		"source_id": r.SourceID, "source_account": r.SourceAccount, "work_attempt": r.WorkAttempt, "work_kind": r.WorkKind, "work_id": r.WorkID,
		"work_bot": r.WorkBot, "work_started_at": r.WorkStartedAt, "approval_id": r.ApprovalID,
		"done_by_kind": r.DoneByKind, "done_by": r.DoneBy, "recorded_by_kind": r.RecordedByKind,
		"recorded_by": r.RecordedBy, "closed_how": r.ClosedHow, "closed_at": r.ClosedAt,
		"duplicate_of": r.DuplicateOf, "why_open": r.WhyOpen,
	} {
		m[k] = v
	}

	m["source_ref"] = nil
	if r.SourceRef.Valid {
		m["source_ref"] = json.RawMessage(r.SourceRef.String)
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
