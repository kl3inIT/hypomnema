package inboxstore_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

var t0 = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

const zone = "Asia/Ho_Chi_Minh"

// world is the Inbox over a migrated database, with two Bots: kai, which the items are given to, and lan.
type world struct {
	db       *sql.DB
	clock    *fakeClock
	inbox    *inboxstore.Store
	kai, lan string
}

func newWorld(t *testing.T) *world {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	n := 0
	ids := func(prefix string) string {
		n++

		return fmt.Sprintf("%s_%04d", prefix, n)
	}

	clock := &fakeClock{now: t0}
	s := orchestration.New(db, clock, ids)

	kai, err := s.PutBot(t.Context(), orchestration.Bot{Name: "kai"})
	require.NoError(t, err)
	lan, err := s.PutBot(t.Context(), orchestration.Bot{Name: "lan"})
	require.NoError(t, err)

	return &world{db: db, clock: clock, inbox: s.Inbox(), kai: kai.ID, lan: lan.ID}
}

var (
	me     = inboxstore.Assignee{Kind: inboxstore.Me}
	owner  = inboxstore.Actor{Kind: inboxstore.Owner}
	system = inboxstore.Actor{Kind: inboxstore.System, ID: "approvals"}
)

func bot(id string) inboxstore.Actor { return inboxstore.Actor{Kind: inboxstore.Bot, ID: id} }

// by is a change of item t by actor at the revision read.
func by(actor inboxstore.Actor, t inboxstore.Todo) inboxstore.Change {
	return inboxstore.Change{Actor: actor, ID: t.ID, Revision: t.Revision}
}

// inAttempt is a Bot's change of an item in its attempt.
func inAttempt(actor inboxstore.Actor, t inboxstore.Todo) inboxstore.Change {
	c := by(actor, t)
	if t.Work != nil {
		c.WorkID = t.Work.ID
	}

	return c
}

// approval adds an Approval in a status.
func (w *world) approval(t *testing.T, id, status string) string {
	t.Helper()

	_, err := w.db.ExecContext(t.Context(), `INSERT INTO approvals (id, integration, label, action, action_name,
		asked_by, title, payload, match_hash, status, created_at) VALUES (?, 'google', 'work', 'gmail.send',
		'Send mail', 'pi', 'Send mail', '[]', 'h', 'pending', 1)`, id)
	require.NoError(t, err)
	w.decide(t, id, status)

	return id
}

func (w *world) decide(t *testing.T, id, status string) {
	t.Helper()

	_, err := w.db.ExecContext(t.Context(), `UPDATE approvals SET status = ? WHERE id = ?`, status, id)
	require.NoError(t, err)
}

// open is an item the owner wrote, given to kai.
func (w *world) open(t *testing.T) inboxstore.Todo {
	t.Helper()

	td, err := w.inbox.Create(t.Context(), owner, inboxstore.NewTodo{
		Title: "Send Kai the contract", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
		Assignee: inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.kai},
	})
	require.NoError(t, err)

	return td
}

// proposed is an item kai found in a Routine's run.
func (w *world) proposed(t *testing.T) inboxstore.Todo {
	t.Helper()

	td, err := w.inbox.Create(t.Context(), bot(w.kai), inboxstore.NewTodo{
		Title: "Answer Linh's mail", State: inboxstore.Proposed,
		Source:   inboxstore.Source{Kind: inboxstore.FromRoutine, ID: "rrn_1", Quote: "Can you answer by Friday?"},
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
	})
	require.NoError(t, err)

	return td
}

// doing is an open item kai started in its chat.
func (w *world) doing(t *testing.T) inboxstore.Todo {
	t.Helper()

	td, err := w.inbox.Start(t.Context(), by(bot(w.kai), w.open(t)), inboxstore.Work{Kind: inboxstore.WorkChat,
		ID: "cht_kai"})
	require.NoError(t, err)

	return td
}

// review is a doing item waiting for a pending Approval.
func (w *world) review(t *testing.T) inboxstore.Todo {
	t.Helper()

	d := w.doing(t)
	td, err := w.inbox.LinkApproval(t.Context(), by(system, d), w.approval(t, "apr_"+d.ID, "pending"))
	require.NoError(t, err)

	return td
}

// closed is an item the owner closed as done.
func (w *world) done(t *testing.T) inboxstore.Todo {
	t.Helper()

	td, err := w.inbox.Done(t.Context(), by(owner, w.open(t)), inboxstore.Closing{
		DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByHand, Reason: "Sent it myself.",
		Evidence: []inboxstore.Link{{Kind: inboxstore.LinkMail, Ref: "msg-1", Label: "The mail"}},
	})
	require.NoError(t, err)

	return td
}

var (
	ownerMessage = []inboxstore.Link{{Kind: inboxstore.LinkChatMessage, Ref: "cht_kai/12", Label: "xong 1"}}
	prLink       = []inboxstore.Link{{Kind: inboxstore.LinkPullRequest, Ref: "https://github.com/o/r/pull/1"}}
)

// Scenario: every row of the transitions of issue #845, once allowed and once refused. A refused change leaves the
// item as it was.
func TestTransitions(t *testing.T) {
	t.Parallel()

	type step func(t *testing.T, w *world) (inboxstore.Todo, error)

	cases := []struct {
		name  string
		do    step
		want  error
		state string
	}{
		// (new) → open: the owner by hand; a Bot in a chat with the owner, never in a Routine's run.
		{"owner adds open", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Create(t.Context(), owner, inboxstore.NewTodo{Title: "Call the bank",
				State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand}, Assignee: me})
		}, nil, inboxstore.Open},
		{"bot adds open in a chat", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Create(t.Context(), bot(w.kai), inboxstore.NewTodo{Title: "Book the flight",
				State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.FromChat, ID: "cht_kai"}, Assignee: me})
		}, nil, inboxstore.Open},
		{"bot adds open outside a chat", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Create(t.Context(), bot(w.kai), inboxstore.NewTodo{Title: "Book the flight",
				State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.FromRoutine, ID: "rrn_1"}, Assignee: me})
		}, storeerr.ErrForbidden, ""},
		{"bot adds open as by hand", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Create(t.Context(), bot(w.kai), inboxstore.NewTodo{Title: "Book the flight",
				State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand}, Assignee: me})
		}, storeerr.ErrForbidden, ""},
		// (new) → proposed: a Bot in a Routine's run.
		{"bot proposes in a run", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.proposed(t), nil
		}, nil, inboxstore.Proposed},
		{"owner proposes", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Create(t.Context(), owner, inboxstore.NewTodo{Title: "Call the bank",
				State: inboxstore.Proposed, Source: inboxstore.Source{Kind: inboxstore.ByHand}, Assignee: me})
		}, storeerr.ErrForbidden, ""},
		// proposed → open, dropped, duplicate: the owner; nothing accepts by itself.
		{"owner accepts", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Accept(t.Context(), by(owner, w.proposed(t)))
		}, nil, inboxstore.Open},
		{"bot accepts", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Accept(t.Context(), by(bot(w.kai), w.proposed(t)))
		}, storeerr.ErrForbidden, ""},
		{"owner drops a proposal", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Drop(t.Context(), by(owner, w.proposed(t)), "Not mine.", nil)
		}, nil, inboxstore.Dropped},
		{"owner marks a proposal duplicate", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			first := w.open(t)
			return w.inbox.MarkDuplicate(t.Context(), by(owner, w.proposed(t)), first.ID)
		}, nil, inboxstore.Duplicate},
		{"bot marks a proposal duplicate", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			first := w.open(t)
			return w.inbox.MarkDuplicate(t.Context(), by(bot(w.kai), w.proposed(t)), first.ID)
		}, storeerr.ErrForbidden, ""},
		// open → doing: the Bot assigned.
		{"assigned bot starts", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.doing(t), nil
		}, nil, inboxstore.Doing},
		{"another bot starts", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Start(t.Context(), by(bot(w.lan), w.open(t)),
				inboxstore.Work{Kind: inboxstore.WorkRun, ID: "run_1"})
		}, storeerr.ErrForbidden, ""},
		{"owner starts", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Start(t.Context(), by(owner, w.open(t)),
				inboxstore.Work{Kind: inboxstore.WorkRun, ID: "run_1"})
		}, storeerr.ErrForbidden, ""},
		// doing → review: the system, one pending Approval per item.
		{"system links an approval", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.review(t), nil
		}, nil, inboxstore.Review},
		{"system links a second approval", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.LinkApproval(t.Context(), by(system, w.review(t)), w.approval(t, "apr_2", "pending"))
		}, storeerr.ErrConflict, ""},
		{"system links an answered approval", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.LinkApproval(t.Context(), by(system, w.doing(t)), w.approval(t, "apr_2", "approved"))
		}, storeerr.ErrConflict, ""},
		// review → doing: the system once the Approval is approved.
		{"approved goes on", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			r := w.review(t)
			w.decide(t, r.ApprovalID, "approved")

			return w.inbox.ApprovalApproved(t.Context(), by(system, r))
		}, nil, inboxstore.Doing},
		{"still pending does not go on", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.ApprovalApproved(t.Context(), by(system, w.review(t)))
		}, storeerr.ErrConflict, ""},
		// review → open: the system once the Approval is rejected or expired.
		{"rejected opens again", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			r := w.review(t)
			w.decide(t, r.ApprovalID, "rejected")

			return w.inbox.ApprovalEnded(t.Context(), by(system, r), "The owner said no.")
		}, nil, inboxstore.Open},
		{"bot ends the approval", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			r := w.review(t)
			w.decide(t, r.ApprovalID, "rejected")

			return w.inbox.ApprovalEnded(t.Context(), inAttempt(bot(w.kai), r), "No.")
		}, storeerr.ErrForbidden, ""},
		// doing → done: the Bot of the attempt, with evidence.
		{"bot completes with evidence", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Done(t.Context(), inAttempt(bot(w.kai), w.doing(t)), inboxstore.Closing{
				DoneBy: inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.kai}, How: inboxstore.ClosedByBot,
				Evidence: prLink,
			})
		}, nil, inboxstore.Done},
		{"bot completes without evidence", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Done(t.Context(), inAttempt(bot(w.kai), w.doing(t)), inboxstore.Closing{
				DoneBy: inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.kai}, How: inboxstore.ClosedByBot,
			})
		}, storeerr.ErrInvalid, ""},
		{"bot completes an open item as its own", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Done(t.Context(), by(bot(w.kai), w.open(t)), inboxstore.Closing{
				DoneBy: inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.kai}, How: inboxstore.ClosedByBot,
				Evidence: prLink,
			})
		}, storeerr.ErrForbidden, ""},
		// doing → open: the Bot gives up, or the system ends the attempt.
		{"bot gives up", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Release(t.Context(), inAttempt(bot(w.kai), w.doing(t)), "The site is down.")
		}, nil, inboxstore.Open},
		{"system ends the attempt", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Release(t.Context(), by(system, w.doing(t)), "The chat ended.")
		}, nil, inboxstore.Open},
		{"owner releases", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Release(t.Context(), by(owner, w.doing(t)), "Mine now.")
		}, storeerr.ErrForbidden, ""},
		// open/doing → done: the owner; a Bot recording for him with his message.
		{"owner closes a doing item", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Done(t.Context(), by(owner, w.doing(t)), inboxstore.Closing{
				DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByHand})
		}, nil, inboxstore.Done},
		{"bot records for the owner", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Done(t.Context(), by(bot(w.lan), w.open(t)), inboxstore.Closing{
				DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByHand,
				Evidence: ownerMessage,
			})
		}, nil, inboxstore.Done},
		{"bot records for the owner without his message", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Done(t.Context(), by(bot(w.lan), w.open(t)), inboxstore.Closing{
				DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByHand, Evidence: prLink,
			})
		}, storeerr.ErrInvalid, ""},
		{"closed by AI is refused for now", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Done(t.Context(), by(owner, w.open(t)), inboxstore.Closing{
				DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByAI})
		}, storeerr.ErrInvalid, ""},
		// proposed/open/doing → dropped: the owner or a Bot for him; a reason.
		{"owner drops a doing item", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Drop(t.Context(), by(owner, w.doing(t)), "No longer needed.", nil)
		}, nil, inboxstore.Dropped},
		{"bot drops for the owner", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Drop(t.Context(), by(bot(w.lan), w.open(t)), "He said drop it.", ownerMessage)
		}, nil, inboxstore.Dropped},
		{"drop without a reason", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Drop(t.Context(), by(owner, w.open(t)), " ", nil)
		}, storeerr.ErrInvalid, ""},
		{"bot drops without the owner's message", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Drop(t.Context(), by(bot(w.lan), w.open(t)), "Not needed.", nil)
		}, storeerr.ErrInvalid, ""},
		// proposed/open → duplicate: the owner.
		{"owner marks open duplicate", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			first := w.open(t)
			return w.inbox.MarkDuplicate(t.Context(), by(owner, w.open(t)), first.ID)
		}, nil, inboxstore.Duplicate},
		{"owner marks doing duplicate", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			first := w.open(t)
			return w.inbox.MarkDuplicate(t.Context(), by(owner, w.doing(t)), first.ID)
		}, storeerr.ErrConflict, ""},
		// done/dropped/duplicate → open: the owner reopens.
		{"owner reopens", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Reopen(t.Context(), by(owner, w.done(t)))
		}, nil, inboxstore.Open},
		{"bot reopens", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Reopen(t.Context(), by(bot(w.kai), w.done(t)))
		}, storeerr.ErrForbidden, ""},
		{"reopen an open item", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Reopen(t.Context(), by(owner, w.open(t)))
		}, storeerr.ErrConflict, ""},
		// review → done/dropped/duplicate, or reassigning it: refused until the Approval is rejected. The owner
		// edits the rest of it.
		{"owner closes a review item", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Done(t.Context(), by(owner, w.review(t)), inboxstore.Closing{
				DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByHand})
		}, storeerr.ErrConflict, ""},
		{"owner drops a review item", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Drop(t.Context(), by(owner, w.review(t)), "No.", nil)
		}, storeerr.ErrConflict, ""},
		{"owner marks a review item duplicate", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			first := w.open(t)
			return w.inbox.MarkDuplicate(t.Context(), by(owner, w.review(t)), first.ID)
		}, storeerr.ErrConflict, ""},
		{"owner reassigns a review item", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Edit(t.Context(), by(owner, w.review(t)), inboxstore.Edit{
				Assignee: &inboxstore.Assignee{Kind: inboxstore.Me}})
		}, storeerr.ErrConflict, ""},
		{"owner retitles a review item", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Edit(t.Context(), by(owner, w.review(t)), inboxstore.Edit{Title: new("Send the contract")})
		}, nil, inboxstore.Review},
		// snooze / unsnooze: the owner, proposed or open, a future time.
		{"owner snoozes", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Snooze(t.Context(), by(owner, w.proposed(t)), new(t0.Add(time.Hour)))
		}, nil, inboxstore.Proposed},
		{"owner unsnoozes", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			s, err := w.inbox.Snooze(t.Context(), by(owner, w.open(t)), new(t0.Add(time.Hour)))
			require.NoError(t, err)

			return w.inbox.Snooze(t.Context(), by(owner, s), nil)
		}, nil, inboxstore.Open},
		{"owner snoozes a doing item", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Snooze(t.Context(), by(owner, w.doing(t)), new(t0.Add(time.Hour)))
		}, storeerr.ErrConflict, ""},
		{"bot snoozes", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Snooze(t.Context(), by(bot(w.kai), w.open(t)), new(t0.Add(time.Hour)))
		}, storeerr.ErrForbidden, ""},
		// edit: the owner any item not closed; a Bot only its own or given items, never their assignee.
		{"owner edits", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Edit(t.Context(), by(owner, w.open(t)), inboxstore.Edit{
				Due: &inboxstore.Due{Kind: inboxstore.DueDate, Date: "2026-11-20", Zone: zone}})
		}, nil, inboxstore.Open},
		{"owner edits a closed item", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Edit(t.Context(), by(owner, w.done(t)), inboxstore.Edit{Notes: new("more")})
		}, storeerr.ErrConflict, ""},
		{"bot edits an item given to it", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Edit(t.Context(), by(bot(w.kai), w.open(t)), inboxstore.Edit{DoneWhen: new("Kai signed")})
		}, nil, inboxstore.Open},
		{"bot edits another's item", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Edit(t.Context(), by(bot(w.lan), w.open(t)), inboxstore.Edit{Notes: new("mine")})
		}, storeerr.ErrForbidden, ""},
		{"bot reassigns its item", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Edit(t.Context(), by(bot(w.kai), w.open(t)), inboxstore.Edit{
				Assignee: &inboxstore.Assignee{Kind: inboxstore.Me}})
		}, storeerr.ErrForbidden, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			w := newWorld(t)
			got, err := tc.do(t, w)

			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.state, got.State)
		})
	}
}

// Scenario: a refused change writes nothing: a stale revision, another attempt's work id, and a Bot writing to its
// attempt after the owner closed the item (the fence) all conflict, and the item keeps its revision and history.
func TestStaleWritesConflictAndWriteNoEvent(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	d := w.doing(t)

	_, err := w.inbox.Edit(ctx, inboxstore.Change{Actor: owner, ID: d.ID, Revision: d.Revision - 1},
		inboxstore.Edit{Notes: new("late")})
	require.ErrorIs(t, err, storeerr.ErrConflict, "a stale revision")

	other := inAttempt(bot(w.kai), d)
	other.WorkID = "cht_other"
	_, err = w.inbox.Release(ctx, other, "Not me.")
	require.ErrorIs(t, err, storeerr.ErrConflict, "another attempt's work id")

	_, err = w.inbox.Release(ctx, by(bot(w.kai), d), "No id.")
	require.ErrorIs(t, err, storeerr.ErrConflict, "a Bot on a doing item names its attempt")

	again, err := w.inbox.Get(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, d.Revision, again.Revision)
	assert.Len(t, again.Events, len(d.Events), "no event for a refused change")

	closed, err := w.inbox.Done(ctx, by(owner, d), inboxstore.Closing{
		DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByHand})
	require.NoError(t, err)
	assert.Nil(t, closed.Work, "the attempt is cleared")

	late := inAttempt(bot(w.kai), d)
	late.Revision = closed.Revision
	_, err = w.inbox.Done(ctx, late, inboxstore.Closing{
		DoneBy: inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.kai}, How: inboxstore.ClosedByBot, Evidence: prLink})
	require.ErrorIs(t, err, storeerr.ErrConflict, "the owner's close fenced the attempt")

	final, err := w.inbox.Get(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, closed.Revision, final.Revision)
	assert.Len(t, final.Events, len(closed.Events))
}

// Scenario: the duplicate invariants: not of itself, not of a duplicate, and not an item that has duplicates.
func TestDuplicateRules(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	target, a, b := w.open(t), w.open(t), w.open(t)

	_, err := w.inbox.MarkDuplicate(ctx, by(owner, a), a.ID)
	require.ErrorIs(t, err, storeerr.ErrInvalid, "of itself")

	_, err = w.inbox.MarkDuplicate(ctx, by(owner, a), "tdo_missing")
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	a, err = w.inbox.MarkDuplicate(ctx, by(owner, a), target.ID)
	require.NoError(t, err)
	assert.Equal(t, target.ID, a.DuplicateOf)
	assert.NotNil(t, a.ClosedAt)

	_, err = w.inbox.MarkDuplicate(ctx, by(owner, b), a.ID)
	require.ErrorIs(t, err, storeerr.ErrConflict, "of a duplicate")

	target, err = w.inbox.Get(ctx, target.ID)
	require.NoError(t, err)
	_, err = w.inbox.MarkDuplicate(ctx, by(owner, target), b.ID)
	require.ErrorIs(t, err, storeerr.ErrConflict, "an item others are duplicates of")
}

// Scenario: a create retried with its idempotency key returns the first item and writes nothing; the same key with
// another request is a conflict.
func TestRetriedCreateReturnsTheFirstItem(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	n := inboxstore.NewTodo{Title: "Pay the rent", State: inboxstore.Open,
		Source: inboxstore.Source{Kind: inboxstore.FromChat, ID: "cht_kai"}, Assignee: me, IdempotencyKey: "req-1"}

	first, err := w.inbox.Create(ctx, bot(w.kai), n)
	require.NoError(t, err)

	again, err := w.inbox.Create(ctx, bot(w.kai), n)
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID)
	assert.Len(t, again.Events, 1, "one created event")

	n.Title = "Pay the bills"
	_, err = w.inbox.Create(ctx, bot(w.kai), n)
	require.ErrorIs(t, err, storeerr.ErrConflict)

	page, err := w.inbox.List(ctx, inboxstore.Filter{}, "", 0)
	require.NoError(t, err)
	assert.Equal(t, 1, page.Total)
}

// Scenario: a snooze in the past is refused; a snoozed item is hidden until its time and shown after, and leaves the
// count of what needs the owner meanwhile.
func TestSnoozeHidesUntilItsTime(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	p := w.proposed(t)

	_, err := w.inbox.Snooze(ctx, by(owner, p), new(t0.Add(-time.Minute)))
	require.ErrorIs(t, err, storeerr.ErrInvalid)

	_, err = w.inbox.Snooze(ctx, by(owner, p), new(t0))
	require.ErrorIs(t, err, storeerr.ErrInvalid, "now is not to come")

	p, err = w.inbox.Snooze(ctx, by(owner, p), new(t0.Add(24*time.Hour)))
	require.NoError(t, err)
	require.NotNil(t, p.SnoozedUntil)

	page, err := w.inbox.List(ctx, inboxstore.Filter{}, "", 0)
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	assert.Equal(t, 0, page.Total)

	page, err = w.inbox.List(ctx, inboxstore.Filter{WithSnoozed: true}, "", 0)
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)

	n, err := w.inbox.NeedsYou(ctx, t0)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	w.clock.now = t0.Add(24 * time.Hour)

	page, err = w.inbox.List(ctx, inboxstore.Filter{}, "", 0)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, p.ID, page.Items[0].ID)

	n, err = w.inbox.NeedsYou(ctx, w.clock.now)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	accepted, err := w.inbox.Accept(ctx, by(owner, p))
	require.NoError(t, err)
	assert.Nil(t, accepted.SnoozedUntil, "a change of state clears the snooze")
}

// Scenario: reopening a done item clears its close and keeps the history of it.
func TestReopenClearsTheCloseAndKeepsTheHistory(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	d := w.done(t)
	require.NotNil(t, d.DoneBy)
	assert.Equal(t, inboxstore.Actor{Kind: inboxstore.Owner}, *d.RecordedBy)

	o, err := w.inbox.Reopen(ctx, by(owner, d))
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Open, o.State)
	assert.Nil(t, o.DoneBy)
	assert.Nil(t, o.RecordedBy)
	assert.Nil(t, o.ClosedAt)
	assert.Empty(t, o.ClosedHow)
	assert.Empty(t, o.CloseReason)
	assert.Empty(t, o.Evidence)
	assert.Equal(t, d.Revision+1, o.Revision)

	require.Len(t, o.Events, 3)
	assert.Equal(t, []string{inboxstore.EventCreated, inboxstore.EventState, inboxstore.EventState},
		[]string{o.Events[0].Kind, o.Events[1].Kind, o.Events[2].Kind})

	var before map[string]any
	require.NoError(t, json.Unmarshal(o.Events[2].Before, &before))
	assert.Equal(t, "done", before["state"])
	assert.Equal(t, "Sent it myself.", before["close_reason"])
	assert.Equal(t, "me", before["done_by_kind"])
	assert.Equal(t, []any{map[string]any{"kind": "mail", "ref": "msg-1", "label": "The mail"}}, before["evidence"])
}

// Scenario: NeedsYou counts each item once, though one is both proposed and his own and overdue; a review item
// counts while its Approval is pending; closed and snoozed items do not count. The list's filter is the same.
func TestNeedsYouCountsEachItemOnce(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)

	p, err := w.inbox.Create(ctx, bot(w.kai), inboxstore.NewTodo{
		Title: "Answer Linh", State: inboxstore.Proposed,
		Source:   inboxstore.Source{Kind: inboxstore.FromRoutine, ID: "rrn_1"},
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
		Due:      inboxstore.Due{Kind: inboxstore.DueDate, Date: "2026-11-17", Zone: zone},
	})
	require.NoError(t, err)

	overdue, err := w.inbox.Create(ctx, owner, inboxstore.NewTodo{
		Title: "Renew the domain", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand}, Assignee: me,
		Due: inboxstore.Due{Kind: inboxstore.DueInstant, At: t0.Add(-time.Hour), Zone: zone},
	})
	require.NoError(t, err)

	// Due today in Hồ Chí Minh City: not overdue before the day ends there.
	_, err = w.inbox.Create(ctx, owner, inboxstore.NewTodo{
		Title: "Call mum", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand}, Assignee: me,
		Due: inboxstore.Due{Kind: inboxstore.DueDate, Date: "2026-11-18", Zone: zone},
	})
	require.NoError(t, err)

	r := w.review(t)
	w.open(t) // given to kai: not the owner's

	n, err := w.inbox.NeedsYou(ctx, t0)
	require.NoError(t, err)
	assert.Equal(t, 3, n, "the proposal, the overdue item and the review item, once each")

	page, err := w.inbox.List(ctx, inboxstore.Filter{NeedsYou: true}, "", 0)
	require.NoError(t, err)
	assert.Equal(t, 3, page.Total)
	assert.ElementsMatch(t, []string{p.ID, overdue.ID, r.ID},
		[]string{page.Items[0].ID, page.Items[1].ID, page.Items[2].ID})

	w.decide(t, r.ApprovalID, "rejected")

	_, err = w.inbox.Drop(ctx, by(owner, overdue), "Let it lapse.", nil)
	require.NoError(t, err)

	n, err = w.inbox.NeedsYou(ctx, t0)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "a decided Approval and a closed item no longer count")

	// The day ends in Hồ Chí Minh City at 17:00 UTC.
	n, err = w.inbox.NeedsYou(ctx, time.Date(2026, 11, 18, 17, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, 2, n, "a date is overdue once its day has ended in its zone")
}

// Scenario: List pages by due, those without one last, then by created; each page has the total of the filter, and
// the filters by state, assignee, source and due window hold.
func TestListPagesByDue(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)

	add := func(title string, due inboxstore.Due) inboxstore.Todo {
		td, err := w.inbox.Create(ctx, owner, inboxstore.NewTodo{Title: title, State: inboxstore.Open,
			Source: inboxstore.Source{Kind: inboxstore.ByHand}, Due: due, Assignee: me})
		require.NoError(t, err)
		w.clock.now = w.clock.now.Add(time.Second)

		return td
	}

	none := add("No due", inboxstore.Due{})
	later := add("Later", inboxstore.Due{Kind: inboxstore.DueDate, Date: "2026-12-01", Zone: zone})
	soon := add("Soon", inboxstore.Due{Kind: inboxstore.DueInstant, At: t0.Add(2 * time.Hour), Zone: zone})
	unknown := add("Some day", inboxstore.Due{Kind: inboxstore.DueUnknown})
	proposal := w.proposed(t)

	var ids []string

	cursor := ""

	for {
		page, err := w.inbox.List(ctx, inboxstore.Filter{}, cursor, 2)
		require.NoError(t, err)
		assert.Equal(t, 5, page.Total)

		for _, td := range page.Items {
			ids = append(ids, td.ID)
		}

		if page.Next == "" {
			break
		}

		cursor = page.Next
	}

	assert.Equal(t, []string{soon.ID, later.ID, none.ID, unknown.ID, proposal.ID}, ids)

	page, err := w.inbox.List(ctx, inboxstore.Filter{States: []string{inboxstore.Proposed}}, "", 0)
	require.NoError(t, err)
	assert.Equal(t, 1, page.Total)

	page, err = w.inbox.List(ctx, inboxstore.Filter{SourceKind: inboxstore.FromRoutine}, "", 0)
	require.NoError(t, err)
	assert.Equal(t, []string{proposal.ID}, []string{page.Items[0].ID})

	page, err = w.inbox.List(ctx, inboxstore.Filter{Assignee: &inboxstore.Assignee{Kind: inboxstore.Me}}, "", 0)
	require.NoError(t, err)
	assert.Equal(t, 5, page.Total)

	page, err = w.inbox.List(ctx, inboxstore.Filter{DueFrom: t0, DueTo: t0.Add(24 * time.Hour)}, "", 0)
	require.NoError(t, err)
	assert.Equal(t, 1, page.Total)
	assert.Equal(t, soon.ID, page.Items[0].ID)

	_, err = w.inbox.Drop(ctx, by(owner, none), "Gone.", nil)
	require.NoError(t, err)

	page, err = w.inbox.List(ctx, inboxstore.Filter{}, "", 0)
	require.NoError(t, err)
	assert.Equal(t, 4, page.Total, "closed items are hidden")

	page, err = w.inbox.List(ctx, inboxstore.Filter{WithClosed: true}, "", 0)
	require.NoError(t, err)
	assert.Equal(t, 5, page.Total)

	_, err = w.inbox.List(ctx, inboxstore.Filter{}, "", inboxstore.MaxPage+1)
	require.ErrorIs(t, err, storeerr.ErrInvalid)
}

// Scenario: what a to-do holds is checked: a title's length, a due without its zone, a date with a time, a person
// without a name, an unknown Bot, and more than 20 pieces of evidence.
func TestWhatATodoHoldsIsChecked(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	hand := inboxstore.Source{Kind: inboxstore.ByHand}

	for name, n := range map[string]inboxstore.NewTodo{
		"empty title":     {Title: " ", State: inboxstore.Open, Source: hand},
		"long title":      {Title: string(make([]rune, 301)), State: inboxstore.Open, Source: hand},
		"date no zone":    {Title: "x", State: inboxstore.Open, Source: hand, Due: inboxstore.Due{Kind: inboxstore.DueDate, Date: "2026-11-20"}},
		"bad date":        {Title: "x", State: inboxstore.Open, Source: hand, Due: inboxstore.Due{Kind: inboxstore.DueDate, Date: "20/11", Zone: zone}},
		"date with time":  {Title: "x", State: inboxstore.Open, Source: hand, Due: inboxstore.Due{Kind: inboxstore.DueDate, Date: "2026-11-20", At: t0, Zone: zone}},
		"instant no time": {Title: "x", State: inboxstore.Open, Source: hand, Due: inboxstore.Due{Kind: inboxstore.DueInstant, Zone: zone}},
		"nameless person": {Title: "x", State: inboxstore.Open, Source: hand, Assignee: inboxstore.Assignee{Kind: inboxstore.Person}},
		"chat without id": {Title: "x", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.FromChat}},
	} {
		_, err := w.inbox.Create(ctx, owner, n)
		if name == "chat without id" {
			require.ErrorIs(t, err, storeerr.ErrForbidden, name)

			continue
		}

		require.ErrorIs(t, err, storeerr.ErrInvalid, name)
	}

	_, err := w.inbox.Create(ctx, owner, inboxstore.NewTodo{Title: "x", State: inboxstore.Open, Source: hand,
		Assignee: inboxstore.Assignee{Kind: inboxstore.ABot, Name: "bot_nobody"}})
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	_, err = w.inbox.Done(ctx, by(owner, w.open(t)), inboxstore.Closing{
		DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByHand,
		Evidence: make([]inboxstore.Link, inboxstore.MaxEvidence+1)})
	require.ErrorIs(t, err, storeerr.ErrInvalid)

	page, err := w.inbox.List(ctx, inboxstore.Filter{}, "", 0)
	require.NoError(t, err)
	assert.Equal(t, 1, page.Total, "only the item made to close")
}

// Every change of an item is one event, in its transaction, with the actor and the fields that changed.
func TestEveryChangeIsAnEvent(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	r := w.review(t)

	require.Len(t, r.Events, 3)
	assert.Equal(t, inboxstore.EventCreated, r.Events[0].Kind)
	assert.Equal(t, bot(w.kai), r.Events[1].Actor)
	assert.Equal(t, inboxstore.EventLinkedApproval, r.Events[2].Kind)
	assert.Equal(t, system, r.Events[2].Actor)
	assert.JSONEq(t, `{"state":"doing","approval_id":null}`, string(r.Events[2].Before))
	assert.JSONEq(t, fmt.Sprintf(`{"state":"review","approval_id":%q}`, r.ApprovalID), string(r.Events[2].After))

	same, err := w.inbox.Edit(ctx, by(owner, r), inboxstore.Edit{Title: new(r.Title)})
	require.NoError(t, err)
	assert.Equal(t, r.Revision, same.Revision, "an edit that changes nothing writes nothing")
	assert.Len(t, same.Events, 3)

	w.decide(t, r.ApprovalID, "rejected")
	o, err := w.inbox.ApprovalEnded(ctx, by(system, r), "Expired.")
	require.NoError(t, err)
	assert.Nil(t, o.Work)
	assert.Empty(t, o.ApprovalID)
	assert.JSONEq(t, `{"state":"open","work_kind":null,"work_id":null,"approval_id":null,"reason":"Expired."}`,
		string(o.Events[3].After))
}
