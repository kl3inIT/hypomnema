package inboxstore_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
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

// world is the Inbox over a migrated database, with two Bots: kai, which the items are given to, and lan; and
// kai's chat with the owner, where the owner said something (said is his message as evidence).
type world struct {
	db       *sql.DB
	clock    *fakeClock
	svc      *orchestration.Service
	inbox    *inboxstore.Store
	kai, lan string
	chat     string
	said     []inboxstore.Link
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

	chat, err := s.CreateChat(t.Context(), kai.ID, "Kai")
	require.NoError(t, err)

	w := &world{db: db, clock: clock, svc: s, inbox: s.Inbox(), kai: kai.ID, lan: lan.ID, chat: chat.ID}
	w.said = w.message(t, chat.ID, orchestration.NewChatMessage{Role: orchestration.ChatOwner, Text: "xong 1"})

	return w
}

// message adds a message to a chat and gives it as evidence.
func (w *world) message(t *testing.T, chatID string, m orchestration.NewChatMessage) []inboxstore.Link {
	t.Helper()

	added, err := w.svc.AddChatMessages(t.Context(), chatID, m)
	require.NoError(t, err)

	return []inboxstore.Link{{Kind: inboxstore.LinkChatMessage, Ref: strconv.FormatInt(added[0].Seq, 10),
		Label: m.Text}}
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
		c.Attempt = t.Work.Attempt
	}

	return c
}

// approval adds an Approval in a status, asked now in kai's chat by the attempt at item td.
func (w *world) approval(t *testing.T, id, status string, td inboxstore.Todo) string {
	t.Helper()

	attempt := ""
	if td.Work != nil {
		attempt = td.Work.Attempt
	}

	return w.approvalIn(t, id, status, attempt, w.chat, "")
}

// approvalIn adds an Approval in a status, asked now by an attempt (none when empty) in a chat or a Run.
func (w *world) approvalIn(t *testing.T, id, status, attempt, chatID, runID string) string {
	t.Helper()

	_, err := w.db.ExecContext(t.Context(), `INSERT INTO approvals (id, integration, label, action, action_name,
		chat_id, run_id, attempt_id, asked_by, title, payload, match_hash, status, created_at) VALUES (?, 'google',
		'work', 'gmail.send', 'Send mail', nullif(?, ''), nullif(?, ''), nullif(?, ''), 'pi', 'Send mail', '[]', 'h',
		'pending', ?)`,
		id, chatID, runID, attempt, w.clock.now.UnixMilli())
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

// fromChat is an item kai added in its chat with the owner, for him to do.
func (w *world) fromChat(t *testing.T) inboxstore.Todo {
	t.Helper()

	td, err := w.inbox.Create(t.Context(), bot(w.kai), inboxstore.NewTodo{
		Title: "Renew the passport", State: inboxstore.Open, Assignee: me,
		Source: inboxstore.Source{Kind: inboxstore.FromChat, ID: w.chat},
	})
	require.NoError(t, err)

	return td
}

// doing is an open item kai started in its chat.
func (w *world) doing(t *testing.T) inboxstore.Todo {
	t.Helper()

	td, err := w.inbox.Start(t.Context(), by(bot(w.kai), w.open(t)), inboxstore.Work{Kind: inboxstore.WorkChat,
		ID: w.chat})
	require.NoError(t, err)

	return td
}

// unchanged checks that a refused change wrote nothing: the item keeps its revision and its history.
func (w *world) unchanged(t *testing.T, before inboxstore.Todo) {
	t.Helper()

	again, err := w.inbox.Get(t.Context(), before.ID)
	require.NoError(t, err)
	assert.Equal(t, before.Revision, again.Revision, "no revision bump")
	assert.Len(t, again.Events, len(before.Events), "no event")
	assert.Equal(t, before.State, again.State)
}

// review is a doing item waiting for a pending Approval.
func (w *world) review(t *testing.T) inboxstore.Todo {
	t.Helper()

	d := w.doing(t)
	td, err := w.inbox.LinkApproval(t.Context(), by(system, d), w.approval(t, "apr_"+d.ID, "pending", d))
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

var prLink = []inboxstore.Link{{Kind: inboxstore.LinkPullRequest, Ref: "https://github.com/o/r/pull/1"}}

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
			r := w.review(t)

			return w.inbox.LinkApproval(t.Context(), by(system, r), w.approval(t, "apr_2", "pending", r))
		}, storeerr.ErrConflict, ""},
		{"system links an answered approval", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			d := w.doing(t)

			return w.inbox.LinkApproval(t.Context(), by(system, d), w.approval(t, "apr_2", "approved", d))
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
			return w.inbox.Done(t.Context(), by(bot(w.kai), w.fromChat(t)), inboxstore.Closing{
				DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByHand,
				Evidence: w.said,
			})
		}, nil, inboxstore.Done},
		{"bot records for the owner without his message", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Done(t.Context(), by(bot(w.kai), w.fromChat(t)), inboxstore.Closing{
				DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByHand, Evidence: prLink,
			})
		}, storeerr.ErrInvalid, ""},
		{"another bot records for the owner an item the first added", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Done(t.Context(), by(bot(w.lan), w.fromChat(t)), inboxstore.Closing{
				DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByHand, Evidence: w.said,
			})
		}, storeerr.ErrForbidden, ""},
		{"closed by AI is refused for now", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Done(t.Context(), by(owner, w.open(t)), inboxstore.Closing{
				DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByAI})
		}, storeerr.ErrInvalid, ""},
		// proposed/open/doing → dropped: the owner or a Bot for him; a reason.
		{"owner drops a doing item", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Drop(t.Context(), by(owner, w.doing(t)), "No longer needed.", nil)
		}, nil, inboxstore.Dropped},
		{"bot drops for the owner", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Drop(t.Context(), by(bot(w.kai), w.fromChat(t)), "He said drop it.", w.said)
		}, nil, inboxstore.Dropped},
		{"another bot drops an item the first added", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Drop(t.Context(), by(bot(w.lan), w.fromChat(t)), "He said drop it.", w.said)
		}, storeerr.ErrForbidden, ""},
		{"drop without a reason", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Drop(t.Context(), by(owner, w.open(t)), " ", nil)
		}, storeerr.ErrInvalid, ""},
		{"bot drops without the owner's message", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Drop(t.Context(), by(bot(w.kai), w.fromChat(t)), "Not needed.", nil)
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
				Assignee: &inboxstore.Assignee{Kind: inboxstore.Person, Name: "Lan"}})
		}, storeerr.ErrForbidden, ""},
		// A Bot hands its item back to the owner with the reply it drafted (issue #1004).
		{"bot hands its item back to the owner", func(t *testing.T, w *world) (inboxstore.Todo, error) {
			return w.inbox.Edit(t.Context(), by(bot(w.kai), w.open(t)), inboxstore.Edit{
				Assignee: &inboxstore.Assignee{Kind: inboxstore.Me}})
		}, nil, inboxstore.Open},
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

// Scenario: a refused change writes nothing: a stale revision, another attempt's id, and a Bot writing to its
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
	other.Attempt = "att_other"
	_, err = w.inbox.Release(ctx, other, "Not me.")
	require.ErrorIs(t, err, storeerr.ErrConflict, "another attempt's id")

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
	assert.ElementsMatch(t, []string{p.ID, overdue.ID, r.ID}, idsOf(page.Items))

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

// Scenario: a date whose midnight its zone skips (Codex review of #848, 2026-11-19): in America/Santiago the
// clocks go from 23:59:59 on 2026-09-05 to 01:00 on 2026-09-06. An item due on the 6th is not overdue nor listed as
// due on the evening of the 5th; it is due from 01:00 on the 6th and overdue once the 7th starts there.
func TestADateWhoseMidnightIsSkippedStartsWhenTheClocksChange(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	santiago, err := time.LoadLocation("America/Santiago")
	require.NoError(t, err)

	item, err := w.inbox.Create(ctx, owner, inboxstore.NewTodo{
		Title: "Pay the rent", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand}, Assignee: me,
		Due: inboxstore.Due{Kind: inboxstore.DueDate, Date: "2026-09-06", Zone: "America/Santiago"},
	})
	require.NoError(t, err)

	eveningBefore := time.Date(2026, 9, 5, 23, 30, 0, 0, santiago)
	clocksChange := time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC) // 01:00 -03 on the 6th
	nextDay := time.Date(2026, 9, 7, 0, 0, 0, 0, santiago)

	for _, c := range []struct {
		at   time.Time
		want int
	}{{eveningBefore, 0}, {nextDay.Add(-time.Millisecond), 0}, {nextDay, 1}} {
		n, err := w.inbox.NeedsYou(ctx, c.at)
		require.NoError(t, err)
		assert.Equal(t, c.want, n, "overdue at %s", c.at.In(santiago))
	}

	page, err := w.inbox.List(ctx, inboxstore.Filter{DueTo: clocksChange}, "", 0)
	require.NoError(t, err)
	assert.Empty(t, page.Items, "not due before the 6th starts")

	page, err = w.inbox.List(ctx, inboxstore.Filter{DueFrom: clocksChange, DueTo: clocksChange.Add(time.Millisecond)},
		"", 0)
	require.NoError(t, err)
	assert.Equal(t, []string{item.ID}, idsOf(page.Items), "due from the moment the clocks change")
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

	// The Unix epoch is a bound like any other: nothing is due before it, everything due is after it.
	page, err = w.inbox.List(ctx, inboxstore.Filter{DueTo: time.UnixMilli(0)}, "", 0)
	require.NoError(t, err)
	assert.Equal(t, 0, page.Total, "due before the epoch")
	assert.Empty(t, page.Items)

	page, err = w.inbox.List(ctx, inboxstore.Filter{DueFrom: time.UnixMilli(0)}, "", 0)
	require.NoError(t, err)
	assert.Equal(t, []string{soon.ID, later.ID}, idsOf(page.Items), "due from the epoch: only the items with a due")

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

	for name, c := range map[string]struct {
		actor inboxstore.Actor
		n     inboxstore.NewTodo
	}{
		"empty title":     {owner, inboxstore.NewTodo{Title: " ", State: inboxstore.Open, Source: hand}},
		"long title":      {owner, inboxstore.NewTodo{Title: string(make([]rune, 301)), State: inboxstore.Open, Source: hand}},
		"date no zone":    {owner, inboxstore.NewTodo{Title: "x", State: inboxstore.Open, Source: hand, Due: inboxstore.Due{Kind: inboxstore.DueDate, Date: "2026-11-20"}}},
		"bad date":        {owner, inboxstore.NewTodo{Title: "x", State: inboxstore.Open, Source: hand, Due: inboxstore.Due{Kind: inboxstore.DueDate, Date: "20/11", Zone: zone}}},
		"date with time":  {owner, inboxstore.NewTodo{Title: "x", State: inboxstore.Open, Source: hand, Due: inboxstore.Due{Kind: inboxstore.DueDate, Date: "2026-11-20", At: t0, Zone: zone}}},
		"instant no time": {owner, inboxstore.NewTodo{Title: "x", State: inboxstore.Open, Source: hand, Due: inboxstore.Due{Kind: inboxstore.DueInstant, Zone: zone}}},
		"nameless person": {owner, inboxstore.NewTodo{Title: "x", State: inboxstore.Open, Source: hand, Assignee: inboxstore.Assignee{Kind: inboxstore.Person}}},
		"chat without id": {bot(w.kai), inboxstore.NewTodo{Title: "x", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.FromChat}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := w.inbox.Create(t.Context(), c.actor, c.n)
			require.ErrorIs(t, err, storeerr.ErrInvalid)
		})
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
	assert.JSONEq(t, `{"state":"open","work_attempt":null,"work_kind":null,"work_id":null,"work_bot":null,`+
		`"work_started_at":null,"approval_id":null,"reason":"Expired."}`, string(o.Events[3].After))
}

// Scenario: only the Bot that started an attempt writes to it; another Bot that gives the attempt's id is
// refused, and nothing is written.
func TestOnlyTheBotOfTheAttemptWritesToIt(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	d := w.doing(t)
	assert.Equal(t, w.kai, d.Work.Bot)

	_, err := w.inbox.Release(ctx, inAttempt(bot(w.lan), d), "Not mine.")
	require.ErrorIs(t, err, storeerr.ErrForbidden, "another Bot releases")
	_, err = w.inbox.Done(ctx, inAttempt(bot(w.lan), d), inboxstore.Closing{
		DoneBy: inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.lan}, How: inboxstore.ClosedByBot, Evidence: prLink})
	require.ErrorIs(t, err, storeerr.ErrForbidden, "another Bot closes as its own")
	_, err = w.inbox.Done(ctx, inAttempt(bot(w.lan), d), inboxstore.Closing{
		DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByHand, Evidence: w.said})
	require.ErrorIs(t, err, storeerr.ErrForbidden, "another Bot records for the owner")
	w.unchanged(t, d)

	_, err = w.inbox.Release(ctx, inAttempt(bot(w.kai), d), "The site is down.")
	require.NoError(t, err, "the Bot of the attempt releases")
}

// Scenario: an item leaves review only by its Approval's answer: Start, by the system or by the Bot, does not move
// it to doing nor replace its attempt; and only a Bot starts an open item.
func TestStartDoesNotLeaveReview(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	r := w.review(t)

	_, err := w.inbox.Start(ctx, by(system, r), inboxstore.Work{Kind: inboxstore.WorkRun, ID: "run_other"})
	require.ErrorIs(t, err, storeerr.ErrForbidden, "the system starts a review item")
	_, err = w.inbox.Start(ctx, inAttempt(bot(w.kai), r), inboxstore.Work{Kind: inboxstore.WorkRun, ID: "run_other"})
	require.ErrorIs(t, err, storeerr.ErrConflict, "the Bot starts its review item again")
	w.unchanged(t, r)

	again, err := w.inbox.Get(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, r.ApprovalID, again.ApprovalID, "the Approval stays linked")
	assert.Equal(t, r.Work, again.Work, "the attempt stays")

	o := w.open(t)
	_, err = w.inbox.Start(ctx, by(system, o), inboxstore.Work{Kind: inboxstore.WorkRun, ID: "run_1"})
	require.ErrorIs(t, err, storeerr.ErrForbidden, "the system starts an open item")
	w.unchanged(t, o)
}

// Scenario: a Bot recording for the owner cites his own message in the item's chat: a message that does not exist,
// the coordinator's, a Channel person's, or the owner's in another chat is refused and writes nothing.
func TestABotRecordsForTheOwnerOnlyWithHisMessage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	item := w.fromChat(t)

	other, err := w.svc.CreateChat(ctx, w.lan, "Lan")
	require.NoError(t, err)

	cases := map[string][]inboxstore.Link{
		"a message that does not exist": {{Kind: inboxstore.LinkChatMessage, Ref: "999999"}},
		"a ref that is no message":      {{Kind: inboxstore.LinkChatMessage, Ref: "cht_kai/12"}},
		"the coordinator's message": w.message(t, w.chat, orchestration.NewChatMessage{
			Role: orchestration.ChatCoordinator, Text: "Done, you said so."}),
		"a Channel person's message": w.message(t, w.chat, orchestration.NewChatMessage{
			Role: orchestration.ChatOwner, Text: "xong rồi",
			Sender: &orchestration.ChatSender{ID: "ou_linh", Name: "Linh"}}),
		"the owner's message in another chat": w.message(t, other.ID, orchestration.NewChatMessage{
			Role: orchestration.ChatOwner, Text: "xong"}),
	}

	item, err = w.inbox.Get(ctx, item.ID)
	require.NoError(t, err)

	for name, evidence := range cases {
		_, err := w.inbox.Done(ctx, by(bot(w.kai), item), inboxstore.Closing{
			DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByHand, Evidence: evidence})
		require.ErrorIs(t, err, storeerr.ErrInvalid, "done with %s", name)

		_, err = w.inbox.Drop(ctx, by(bot(w.kai), item), "He said drop it.", evidence)
		require.ErrorIs(t, err, storeerr.ErrInvalid, "dropped with %s", name)
	}

	w.unchanged(t, item)

	d := w.doing(t)
	_, err = w.inbox.Done(ctx, inAttempt(bot(w.kai), d), inboxstore.Closing{
		DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByHand, Evidence: w.said})
	require.NoError(t, err, "his message in the chat of the attempt")
}

// Scenario: the Approval an item waits for is one its attempt asked, whatever chat or Run it was asked in: a pending
// Approval of the item's Run or chat asked by no attempt or by another, or of another chat, is refused and writes
// nothing; the one its attempt asked in its Run is linked.
func TestLinkApprovalTakesOnlyTheAttemptsApproval(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	run, err := w.inbox.Start(ctx, by(bot(w.kai), w.open(t)), inboxstore.Work{Kind: inboxstore.WorkRun, ID: "run_1"})
	require.NoError(t, err)

	for _, a := range []string{
		w.approvalIn(t, "apr_run_none", "pending", "", "", "run_1"),
		w.approvalIn(t, "apr_run_other", "pending", "att_other", "", "run_1"),
		w.approvalIn(t, "apr_chat", "pending", "", "cht_other", ""),
		w.approvalIn(t, "apr_none", "pending", "", "", ""),
	} {
		_, err := w.inbox.LinkApproval(ctx, by(system, run), a)
		require.ErrorIs(t, err, storeerr.ErrConflict, a)
	}

	w.unchanged(t, run)

	linked, err := w.inbox.LinkApproval(ctx, by(system, run),
		w.approvalIn(t, "apr_run", "pending", run.Work.Attempt, "", "run_1"))
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Review, linked.State)
}

// Scenario: the owner's Lark and Zalo chats are sources (owner, 2026-11-19): a Bot proposes an item from one with
// the chat's id and the message as a typed reference; without its message, or open, it is refused.
func TestABotProposesFromLarkAndZalo(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)

	for _, src := range []inboxstore.Source{
		{Kind: inboxstore.FromLark, ID: "oc_123", Quote: "Gửi anh hợp đồng nhé",
			Ref: &inboxstore.Link{Kind: inboxstore.LinkLarkMessage, Ref: "om_456"}},
		{Kind: inboxstore.FromZalo, ID: "g_789", Account: "84901234567", Quote: "Mai họp lúc 9h",
			Ref: &inboxstore.Link{Kind: inboxstore.LinkZaloMessage, Ref: "1700000000000"}},
	} {
		td, err := w.inbox.Create(ctx, bot(w.kai), inboxstore.NewTodo{Title: "Answer " + src.Kind,
			State: inboxstore.Proposed, Source: src, Assignee: me})
		require.NoError(t, err)
		assert.Equal(t, src, td.Source)

		_, err = w.inbox.Create(ctx, bot(w.kai), inboxstore.NewTodo{Title: "Answer " + src.Kind,
			State: inboxstore.Open, Source: src, Assignee: me})
		require.ErrorIs(t, err, storeerr.ErrForbidden, "open from %s", src.Kind)

		noRef := src
		noRef.Ref = nil
		_, err = w.inbox.Create(ctx, bot(w.kai), inboxstore.NewTodo{Title: "Answer " + src.Kind,
			State: inboxstore.Proposed, Source: noRef, Assignee: me})
		require.ErrorIs(t, err, storeerr.ErrInvalid, "from %s without its message", src.Kind)

		wrongRef := src
		wrongRef.Ref = &inboxstore.Link{Kind: inboxstore.LinkURL, Ref: "https://example.com"}
		_, err = w.inbox.Create(ctx, bot(w.kai), inboxstore.NewTodo{Title: "Answer " + src.Kind,
			State: inboxstore.Proposed, Source: wrongRef, Assignee: me})
		require.ErrorIs(t, err, storeerr.ErrInvalid, "from %s with another kind of reference", src.Kind)
	}

	page, err := w.inbox.List(ctx, inboxstore.Filter{SourceKind: inboxstore.FromZalo}, "", 0)
	require.NoError(t, err)
	assert.Equal(t, 1, page.Total)
}

func idsOf(items []inboxstore.Todo) []string {
	ids := make([]string, 0, len(items))
	for _, td := range items {
		ids = append(ids, td.ID)
	}

	return ids
}

// Scenario: each attempt has its own id and the Approval an item waits for is one its own attempt asked (Codex
// review of #848 at 2bf0ee1, 2026-11-19; Hermes binds a session to task, run and claim, gateway/session_kanban.py):
// Kai works on two items in one chat at once; an Approval the contract's attempt asked in the same millisecond, and
// one asked in the chat after both started by no attempt, are refused for the invoice and write nothing; each item
// links its own; and after the invoice is released and started again in the same chat, its earlier attempt's
// Approval is refused and the old attempt's id writes nothing.
func TestAnApprovalIsTheItemsOfItsAttempt(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	contract := w.doing(t)

	invoice, err := w.inbox.Start(ctx, by(bot(w.kai), w.open(t)),
		inboxstore.Work{Kind: inboxstore.WorkChat, ID: w.chat})
	require.NoError(t, err, "a second item in the same chat")
	assert.NotEqual(t, contract.Work.Attempt, invoice.Work.Attempt, "each attempt has its own id")
	assert.Equal(t, contract.Work.StartedAt, invoice.Work.StartedAt, "both in the same millisecond")

	forContract := w.approval(t, "apr_contract", "pending", contract)
	unrelated := w.approvalIn(t, "apr_unrelated", "pending", "", w.chat, "")

	for _, a := range []string{forContract, unrelated} {
		_, err = w.inbox.LinkApproval(ctx, by(system, invoice), a)
		require.ErrorIs(t, err, storeerr.ErrConflict, a)
	}

	w.unchanged(t, invoice)

	linked, err := w.inbox.LinkApproval(ctx, by(system, contract), forContract)
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Review, linked.State, "the contract waits for its own")

	earlier := w.approval(t, "apr_invoice_1", "pending", invoice)
	released, err := w.inbox.Release(ctx, inAttempt(bot(w.kai), invoice), "Later.")
	require.NoError(t, err)

	again, err := w.inbox.Start(ctx, by(bot(w.kai), released), inboxstore.Work{Kind: inboxstore.WorkChat, ID: w.chat})
	require.NoError(t, err)
	assert.NotEqual(t, invoice.Work.Attempt, again.Work.Attempt, "a new attempt, a new id")

	_, err = w.inbox.LinkApproval(ctx, by(system, again), earlier)
	require.ErrorIs(t, err, storeerr.ErrConflict, "the earlier attempt's Approval")

	stale := inAttempt(bot(w.kai), invoice)
	stale.Revision = again.Revision
	_, err = w.inbox.Release(ctx, stale, "From the old attempt.")
	require.ErrorIs(t, err, storeerr.ErrConflict, "the earlier attempt's id, in the same chat")
	w.unchanged(t, again)

	linked, err = w.inbox.LinkApproval(ctx, by(system, again), w.approval(t, "apr_invoice_2", "pending", again))
	require.NoError(t, err)
	assert.Equal(t, "apr_invoice_2", linked.ApprovalID)
}

// Scenario: the owner gives a doing item to another only once its attempt ended (Codex review of #848,
// 2026-11-19): while Kai is on it the change is refused and writes nothing; once Kai released it, it goes to Lan,
// who starts it, and Kai's old attempt writes nothing more.
func TestReassigningWaitsForTheAttempt(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	d := w.doing(t)
	toLan := inboxstore.Edit{Assignee: &inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.lan}}

	_, err := w.inbox.Edit(ctx, by(owner, d), toLan)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	w.unchanged(t, d)

	released, err := w.inbox.Release(ctx, inAttempt(bot(w.kai), d), "Lan takes it.")
	require.NoError(t, err)

	given, err := w.inbox.Edit(ctx, by(owner, released), toLan)
	require.NoError(t, err)

	started, err := w.inbox.Start(ctx, by(bot(w.lan), given), inboxstore.Work{Kind: inboxstore.WorkRun, ID: "run_lan"})
	require.NoError(t, err)

	old := inboxstore.Change{Actor: bot(w.kai), ID: d.ID, Revision: started.Revision, Attempt: d.Work.Attempt}
	_, err = w.inbox.Done(ctx, old, inboxstore.Closing{
		DoneBy: inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.kai}, How: inboxstore.ClosedByBot, Evidence: prLink})
	require.ErrorIs(t, err, storeerr.ErrConflict, "Kai's old attempt")
	w.unchanged(t, started)
}

// Scenario: Get reads an item and its history together (Codex review of #848, 2026-11-19): while the item changes,
// every read's last event is the item's revision, never a later one.
func TestGetReadsTheItemAndItsHistoryTogether(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	item := w.open(t)
	done := make(chan error, 1)

	go func() {
		cur := item

		for i := range 200 {
			next, err := w.inbox.Edit(ctx, by(owner, cur), inboxstore.Edit{Notes: new(strconv.Itoa(i))})
			if err != nil {
				done <- err

				return
			}

			cur = next
		}

		done <- nil
	}()

	for {
		select {
		case err := <-done:
			require.NoError(t, err)

			return
		default:
		}

		got, err := w.inbox.Get(ctx, item.ID)
		require.NoError(t, err)
		require.Equal(t, got.Revision, got.Events[len(got.Events)-1].Revision, "the history ends at the item's revision")
	}
}

// Scenario: a Bot records for the owner what he told it in the chat it acts in (Codex review of #848, 2026-11-19):
// a proposal from a Routine has no chat of its own, so his message in the Bot's chat drops it; without that chat,
// or with a chat where he did not say it, the drop is refused and writes nothing.
func TestABotRecordsWhatTheOwnerToldItInItsChat(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	p := w.proposed(t)

	other, err := w.svc.CreateChat(ctx, w.lan, "Lan")
	require.NoError(t, err)

	for name, chat := range map[string]string{"no chat": "", "a chat where he did not say it": other.ID} {
		c := by(bot(w.kai), p)
		c.Chat = chat
		_, err := w.inbox.Drop(ctx, c, "He said drop it.", w.said)
		require.ErrorIs(t, err, storeerr.ErrInvalid, name)
	}

	w.unchanged(t, p)

	c := by(bot(w.kai), p)
	c.Chat = w.chat
	dropped, err := w.inbox.Drop(ctx, c, "He said drop it.", w.said)
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Dropped, dropped.State)
	assert.Equal(t, &inboxstore.Actor{Kind: inboxstore.Bot, ID: w.kai}, dropped.RecordedBy)

	o := w.open(t)
	c = by(bot(w.kai), o)
	c.Chat = w.chat
	closed, err := w.inbox.Done(ctx, c, inboxstore.Closing{DoneBy: me, How: inboxstore.ClosedByHand, Evidence: w.said})
	require.NoError(t, err, "an item written by hand, done as he said in the Bot's chat")
	assert.Equal(t, &me, closed.DoneBy)
}

// Scenario: a Zalo source names the owner's account, as the Zalo store keys a message by account, chat and id
// (Codex review of #848, 2026-11-19): the same chat and message id in two accounts are two sources; a Zalo source
// without its account, or another source with one, is refused.
func TestAZaloSourceNamesItsAccount(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	ref := &inboxstore.Link{Kind: inboxstore.LinkZaloMessage, Ref: "1700000000000"}

	for _, account := range []string{"84901234567", "84907654321"} {
		td, err := w.inbox.Create(ctx, bot(w.kai), inboxstore.NewTodo{Title: "Answer Linh", State: inboxstore.Proposed,
			Source:   inboxstore.Source{Kind: inboxstore.FromZalo, ID: "g_789", Account: account, Ref: ref},
			Assignee: me})
		require.NoError(t, err)
		assert.Equal(t, account, td.Source.Account)
	}

	for name, src := range map[string]inboxstore.Source{
		"zalo without its account": {Kind: inboxstore.FromZalo, ID: "g_789", Ref: ref},
		"whatsapp without its account": {Kind: inboxstore.FromWhatsApp, ID: "84901@s.whatsapp.net",
			Ref: &inboxstore.Link{Kind: inboxstore.LinkWhatsAppMessage, Ref: "3EB0"}},
		"a routine with an account": {Kind: inboxstore.FromRoutine, ID: "rrn_1", Account: "84901234567"},
	} {
		_, err := w.inbox.Create(ctx, bot(w.kai), inboxstore.NewTodo{Title: "Answer Linh", State: inboxstore.Proposed,
			Source: src, Assignee: me})
		require.ErrorIs(t, err, storeerr.ErrInvalid, name)
	}
}

// Scenario: the owner's triage of 2026-11-20 (issue #966): a Bot in a Routine's run, or reading his Lark or Zalo
// chats, opens an item only when it was asked directly or promised, with the quote and the reference to the
// message; without one of them it may only propose. why_open is kept in the item and its created event, and is
// refused where it means nothing.
func TestARoutineOpensOnlyWhatIsClear(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	ref := &inboxstore.Link{Kind: inboxstore.LinkLarkMessage, Ref: "om_456"}
	lark := inboxstore.Source{Kind: inboxstore.FromLark, ID: "oc_123", Quote: "Anh check giúp em nhé", Ref: ref}
	routine := inboxstore.Source{Kind: inboxstore.FromRoutine, ID: "rrn_1", Quote: "để em check",
		Ref: &inboxstore.Link{Kind: inboxstore.LinkURL, Ref: "https://example.com/m/1"}}

	create := func(actor inboxstore.Actor, state, why string, src inboxstore.Source) (inboxstore.Todo, error) {
		return w.inbox.Create(ctx, actor, inboxstore.NewTodo{Title: "Check the invoice", State: state, WhyOpen: why,
			Source: src, Assignee: me})
	}

	noQuote := routine
	noQuote.Quote = "  "
	noRef := routine
	noRef.Ref = nil

	for _, c := range []struct {
		name  string
		why   string
		src   inboxstore.Source
		state string
		want  error
	}{
		{"open without why_open", "", routine, inboxstore.Open, storeerr.ErrForbidden},
		{"open without a quote", inboxstore.AskedDirectly, noQuote, inboxstore.Open, storeerr.ErrForbidden},
		{"open without a reference", inboxstore.Promised, noRef, inboxstore.Open, storeerr.ErrForbidden},
		{"an unknown reason", "urgent", routine, inboxstore.Open, storeerr.ErrInvalid},
		{"why_open on a proposal", inboxstore.Promised, routine, inboxstore.Proposed, storeerr.ErrInvalid},
		{"why_open from a chat", inboxstore.Promised, inboxstore.Source{Kind: inboxstore.FromChat, ID: w.chat},
			inboxstore.Open, storeerr.ErrInvalid},
		{"proposed without why_open", "", noRef, inboxstore.Proposed, nil},
		{"asked directly at Lark", inboxstore.AskedDirectly, lark, inboxstore.Open, nil},
		{"promised in a run", inboxstore.Promised, routine, inboxstore.Open, nil},
	} {
		td, err := create(bot(w.kai), c.state, c.why, c.src)
		if c.want != nil {
			require.ErrorIs(t, err, c.want, c.name)

			continue
		}

		require.NoError(t, err, c.name)
		assert.Equal(t, c.state, td.State, c.name)
		assert.Equal(t, c.why, td.WhyOpen, c.name)

		var after map[string]any
		require.NoError(t, json.Unmarshal(td.Events[0].After, &after))

		if c.why == "" {
			assert.Nil(t, after["why_open"], c.name)
		} else {
			assert.Equal(t, c.why, after["why_open"], "%s: the created event records why", c.name)
		}
	}

	_, err := w.inbox.Create(ctx, owner, inboxstore.NewTodo{Title: "Pay", State: inboxstore.Open,
		WhyOpen: inboxstore.Promised, Source: inboxstore.Source{Kind: inboxstore.ByHand}, Assignee: me})
	require.ErrorIs(t, err, storeerr.ErrInvalid, "the owner's own item says no why")
}

// Scenario: a proposal nobody touched for StaleAfter is stale (issue #966): it leaves List unless asked for and is
// not counted by NeedsYou, nor listed by its filter; it stays proposed with its history. One touched since it was
// made, one younger than StaleAfter, and an open item as old are not stale.
func TestAnUntouchedProposalGoesStale(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)

	untouched := w.proposed(t)
	touched := w.proposed(t)
	open := w.fromChat(t)

	w.clock.now = t0.Add(time.Hour)
	note := "Linh asked again."
	touched, err := w.inbox.Edit(ctx, by(owner, touched), inboxstore.Edit{Notes: &note})
	require.NoError(t, err)

	listed := func(f inboxstore.Filter) []string {
		t.Helper()

		page, err := w.inbox.List(ctx, f, "", 0)
		require.NoError(t, err)
		assert.Equal(t, len(page.Items), page.Total, "the total counts what the filter lists")

		return idsOf(page.Items)
	}

	w.clock.now = t0.Add(inboxstore.StaleAfter - time.Millisecond)
	assert.ElementsMatch(t, []string{untouched.ID, touched.ID, open.ID}, listed(inboxstore.Filter{}),
		"just younger than StaleAfter")
	n, err := w.inbox.NeedsYou(ctx, w.clock.now)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	w.clock.now = t0.Add(inboxstore.StaleAfter)
	assert.ElementsMatch(t, []string{touched.ID, open.ID}, listed(inboxstore.Filter{}))
	assert.ElementsMatch(t, []string{untouched.ID, touched.ID, open.ID}, listed(inboxstore.Filter{WithStale: true}))
	assert.Equal(t, []string{touched.ID}, listed(inboxstore.Filter{NeedsYou: true}))
	assert.Equal(t, []string{touched.ID}, listed(inboxstore.Filter{NeedsYou: true, WithStale: true}),
		"a stale proposal never needs the owner")

	n, err = w.inbox.NeedsYou(ctx, w.clock.now)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	got, err := w.inbox.Get(ctx, untouched.ID)
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Proposed, got.State, "neither accepted nor dropped")
	assert.Len(t, got.Events, 1)
	assert.True(t, got.Stale)

	page, err := w.inbox.List(ctx, inboxstore.Filter{WithStale: true}, "", 0)
	require.NoError(t, err)

	for _, td := range page.Items {
		assert.Equal(t, td.ID == untouched.ID, td.Stale, "only the untouched proposal is stale")
	}

	// Accepted, it is open and in sight again.
	_, err = w.inbox.Accept(ctx, by(owner, got))
	require.NoError(t, err)
	assert.Contains(t, listed(inboxstore.Filter{}), untouched.ID)
}

// sentMessages is what the server keeps of the owner's own accounts: messages by service, account, chat and id.
type sentMessages map[string]inboxstore.SentMessage

func (m sentMessages) SentMessage(_ context.Context, service, account, chat, id string,
) (inboxstore.SentMessage, error) {
	got, ok := m[service+"/"+account+"/"+chat+"/"+id]
	if !ok {
		return inboxstore.SentMessage{}, storeerr.ErrNotFound
	}

	return got, nil
}

// replyTo is "Reply to Lan", a reply owed in Lan's chat at a messaging service, found by kai in a Routine's run.
func (w *world) replyTo(t *testing.T, service string) inboxstore.Todo {
	t.Helper()

	kind := map[string]string{inboxstore.FromZalo: inboxstore.LinkZaloMessage,
		inboxstore.FromWhatsApp: inboxstore.LinkWhatsAppMessage}[service]

	td, err := w.inbox.Create(t.Context(), bot(w.kai), inboxstore.NewTodo{
		Title: "Reply to Lan", State: inboxstore.Open, WhyOpen: inboxstore.AskedDirectly, Assignee: me,
		Source: inboxstore.Source{Kind: service, ID: "lan", Account: "main", Quote: "Anh xem giúp em?",
			Ref: &inboxstore.Link{Kind: kind, Ref: "asked"}},
	})
	require.NoError(t, err)

	return td
}

// Scenario: issue #1004, section 4. A reply owed in a Zalo or WhatsApp chat closes when kai records for the owner
// the message he sent in that chat after the item was made, with that message as its evidence; the message of
// someone else, one in another chat or of another account, one older than the item, or one the server does not
// keep is refused and writes nothing. Without a way to read what he sent, nothing is closed so.
func TestTheOwnersSentMessageClosesTheReply(t *testing.T) {
	t.Parallel()

	for _, service := range []string{inboxstore.FromZalo, inboxstore.FromWhatsApp} {
		t.Run(service, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			w := newWorld(t)
			sentKind := map[string]string{inboxstore.FromZalo: inboxstore.LinkZaloSent,
				inboxstore.FromWhatsApp: inboxstore.LinkWhatsAppSent}[service]
			sent := func(account, chat, id string) []inboxstore.Link {
				return []inboxstore.Link{{Kind: sentKind, Ref: id, Account: account, Chat: chat}}
			}
			closing := func(evidence []inboxstore.Link) inboxstore.Closing {
				return inboxstore.Closing{DoneBy: me, How: inboxstore.ClosedByBot, Evidence: evidence}
			}

			item := w.replyTo(t, service)

			_, err := w.inbox.Done(ctx, by(bot(w.kai), item), closing(sent("main", "lan", "mine")))
			require.ErrorIs(t, err, storeerr.ErrUnavailable, "no store of what he sent")

			later, earlier := t0.Add(time.Minute), t0.Add(-time.Minute)
			w.inbox.WithSent(sentMessages{
				service + "/main/lan/mine":   {Chat: "lan", At: later, FromMe: true},
				service + "/main/lan/hers":   {Chat: "lan", At: later},
				service + "/main/lan/before": {Chat: "lan", At: earlier, FromMe: true},
				service + "/main/hoa/mine":   {Chat: "hoa", At: later, FromMe: true},
				service + "/work/lan/mine":   {Chat: "lan", At: later, FromMe: true},
			})

			for name, evidence := range map[string][]inboxstore.Link{
				"Lan's own message":              sent("main", "lan", "hers"),
				"his message from before":        sent("main", "lan", "before"),
				"his message in another chat":    sent("main", "hoa", "mine"),
				"his message of another account": sent("work", "lan", "mine"),
				"a message the server lacks":     sent("main", "lan", "gone"),
			} {
				_, err := w.inbox.Done(ctx, by(bot(w.kai), item), closing(evidence))
				require.ErrorIs(t, err, storeerr.ErrInvalid, name)
			}

			w.unchanged(t, item)

			done, err := w.inbox.Done(ctx, by(bot(w.kai), item), closing(sent("main", "lan", "mine")))
			require.NoError(t, err)
			assert.Equal(t, inboxstore.Done, done.State)
			assert.Equal(t, &me, done.DoneBy, "the owner answered; kai recorded it")
			assert.Equal(t, sent("main", "lan", "mine"), done.Evidence)

			other := w.replyTo(t, service)
			_, err = w.inbox.Done(ctx, by(bot(w.lan), other), closing(sent("main", "lan", "mine")))
			require.ErrorIs(t, err, storeerr.ErrForbidden, "another Bot does not close kai's item")
		})
	}
}

// Scenario: issue #1004, section 3. kai drafts the reply on its item and hands it back to the owner: the item, his
// own, needs him with its draft even before it is due; a Bot gives an item to no one else.
func TestADraftHandedBackNeedsTheOwner(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	item := w.open(t)

	n, err := w.inbox.NeedsYou(ctx, t0)
	require.NoError(t, err)
	require.Zero(t, n)

	draft := "Dạ em gửi anh hợp đồng ạ."
	got, err := w.inbox.Edit(ctx, by(bot(w.kai), item), inboxstore.Edit{Draft: &draft, Assignee: &me})
	require.NoError(t, err)
	assert.Equal(t, draft, got.Draft)
	assert.Equal(t, me, got.Assignee)

	n, err = w.inbox.NeedsYou(ctx, t0)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	page, err := w.inbox.List(ctx, inboxstore.Filter{NeedsYou: true}, "", 0)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, draft, page.Items[0].Draft)

	_, err = w.inbox.Edit(ctx, by(bot(w.kai), got), inboxstore.Edit{Draft: &draft,
		Assignee: &inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.lan}})
	require.ErrorIs(t, err, storeerr.ErrForbidden)
}

// Scenario: issue #1004, section 4, for Lark, whose source names its account only optionally. The owner's sent
// message is evidence only in the item's own account: a Lark item without one is never closed by a lark_sent,
// whatever account it names, since another Connection in the same group could show it; an item with its account
// closes only with that account's message.
func TestALarkReplyClosesOnlyWithItsOwnAccountsMessage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	later := t0.Add(time.Minute)
	w.inbox.WithSent(sentMessages{
		inboxstore.FromLark + "/me/oc_pmo/mine":   {Chat: "oc_pmo", At: later, FromMe: true},
		inboxstore.FromLark + "/work/oc_pmo/mine": {Chat: "oc_pmo", At: later, FromMe: true},
	})

	reply := func(account string) inboxstore.Todo {
		td, err := w.inbox.Create(ctx, bot(w.kai), inboxstore.NewTodo{
			Title: "Reply to the PMO group", State: inboxstore.Open, WhyOpen: inboxstore.AskedDirectly, Assignee: me,
			Source: inboxstore.Source{Kind: inboxstore.FromLark, ID: "oc_pmo", Account: account,
				Quote: "@Đạt check giúp?", Ref: &inboxstore.Link{Kind: inboxstore.LinkLarkMessage, Ref: "om_asked"}},
		})
		require.NoError(t, err)

		return td
	}
	closing := func(account string) inboxstore.Closing {
		return inboxstore.Closing{DoneBy: me, How: inboxstore.ClosedByBot, Evidence: []inboxstore.Link{
			{Kind: inboxstore.LinkLarkSent, Ref: "mine", Account: account, Chat: "oc_pmo"},
		}}
	}

	unbound := reply("")
	for _, account := range []string{"me", "work"} {
		_, err := w.inbox.Done(ctx, by(bot(w.kai), unbound), closing(account))
		require.ErrorIs(t, err, storeerr.ErrInvalid, "an item without its account is not closed by %s's message",
			account)
	}

	w.unchanged(t, unbound)

	bound := reply("me")
	_, err := w.inbox.Done(ctx, by(bot(w.kai), bound), closing("work"))
	require.ErrorIs(t, err, storeerr.ErrInvalid, "another Connection's message")

	done, err := w.inbox.Done(ctx, by(bot(w.kai), bound), closing("me"))
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Done, done.State)
}

// Scenario: issue #1105, section 2. A Bot that read the owner's Gmail adds an item that names his Google account,
// the thread and the mail it came from; one that read a meeting names the meeting and, when the action has one, its
// line. Both are kept as given and listed by their source, and open only when the Bot says why, as every source it
// reads. A Gmail item without its account or its mail, or a meeting item with another kind of reference or an
// account, is refused.
func TestAMailAndAMeetingAreSources(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)

	mail := inboxstore.Source{Kind: inboxstore.FromGmail, ID: "18c2f0a9b1d2e3f4", Account: "work",
		Quote: "Could you confirm the PoC scope by Friday?", Ref: &inboxstore.Link{Kind: inboxstore.LinkMail,
			Ref: "18c2f0a9b1d2e3f5", Label: "Alibaba Cloud"}}
	meeting := inboxstore.Source{Kind: inboxstore.FromMeeting, ID: "mtg_1", Quote: "Để em check tiles.",
		Ref: &inboxstore.Link{Kind: inboxstore.LinkMeetingLine, Ref: "mtg_1/12"}}
	noLine := inboxstore.Source{Kind: inboxstore.FromMeeting, ID: "mtg_1", Quote: "Lan sends the deck."}

	for _, src := range []inboxstore.Source{mail, meeting, noLine} {
		td, err := w.inbox.Create(ctx, bot(w.kai), inboxstore.NewTodo{Title: "From " + src.Kind,
			State: inboxstore.Proposed, Source: src, Assignee: me})
		require.NoError(t, err, src.Kind)
		assert.Equal(t, src, td.Source)
	}

	for _, src := range []inboxstore.Source{mail, meeting} {
		_, err := w.inbox.Create(ctx, bot(w.kai), inboxstore.NewTodo{Title: "Open from " + src.Kind,
			State: inboxstore.Open, Source: src, Assignee: me})
		require.ErrorIs(t, err, storeerr.ErrForbidden, "open from %s without why", src.Kind)

		td, err := w.inbox.Create(ctx, bot(w.kai), inboxstore.NewTodo{Title: "Open from " + src.Kind,
			State: inboxstore.Open, Source: src, Assignee: me, WhyOpen: inboxstore.Promised})
		require.NoError(t, err, src.Kind)
		assert.Equal(t, inboxstore.Promised, td.WhyOpen)
	}

	for kind, want := range map[string]int{inboxstore.FromGmail: 2, inboxstore.FromMeeting: 3} {
		page, err := w.inbox.List(ctx, inboxstore.Filter{SourceKind: kind}, "", 0)
		require.NoError(t, err)
		assert.Equal(t, want, page.Total, kind)
	}

	noAccount := mail
	noAccount.Account = ""
	noMail := mail
	noMail.Ref = nil
	lineAsURL := meeting
	lineAsURL.Ref = &inboxstore.Link{Kind: inboxstore.LinkURL, Ref: "https://example.com"}
	meetingAccount := meeting
	meetingAccount.Account = "work"

	for name, src := range map[string]inboxstore.Source{
		"gmail without its account": noAccount, "gmail without its mail": noMail,
		"a meeting with another reference": lineAsURL, "a meeting with an account": meetingAccount,
	} {
		_, err := w.inbox.Create(ctx, bot(w.kai), inboxstore.NewTodo{Title: name, State: inboxstore.Proposed,
			Source: src, Assignee: me})
		require.ErrorIs(t, err, storeerr.ErrInvalid, name)
	}
}

// Scenario: issue #1105, section 1. A Bot points an item it added in its chat at the Zalo message it came from: the
// item keeps its state, and the change is an event with the source before and after. A source only the token gives
// (a chat, a Routine's run, the owner's hand) is never set by an edit, and a Bot does not repoint another Bot's item.
func TestABotPointsItsItemAtWhereItCameFrom(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	td := w.fromChat(t)

	zalo := inboxstore.Source{Kind: inboxstore.FromZalo, ID: "g_laura", Account: "main",
		Quote: "tầm chiều tối em gửi cô cái báo cáo", Ref: &inboxstore.Link{Kind: inboxstore.LinkZaloMessage, Ref: "z_20"}}

	moved, err := w.inbox.Edit(ctx, by(bot(w.kai), td), inboxstore.Edit{Source: &zalo})
	require.NoError(t, err)
	assert.Equal(t, zalo, moved.Source)
	assert.Equal(t, inboxstore.Open, moved.State)

	last := moved.Events[len(moved.Events)-1]
	assert.Equal(t, inboxstore.EventEdited, last.Kind)
	assert.JSONEq(t, `{"source_kind":"zalo","source_id":"g_laura","source_account":"main",`+
		`"source_quote":"tầm chiều tối em gửi cô cái báo cáo","source_ref":{"kind":"zalo_message","ref":"z_20"}}`,
		string(last.After))

	for _, kind := range []string{inboxstore.FromChat, inboxstore.FromRoutine, inboxstore.ByHand} {
		_, err := w.inbox.Edit(ctx, by(bot(w.kai), moved), inboxstore.Edit{Source: &inboxstore.Source{Kind: kind,
			ID: "chat_x"}})
		require.ErrorIs(t, err, storeerr.ErrInvalid, kind)
	}

	_, err = w.inbox.Edit(ctx, by(bot(w.lan), moved), inboxstore.Edit{Source: &zalo})
	require.ErrorIs(t, err, storeerr.ErrForbidden, "another Bot's item")
}
