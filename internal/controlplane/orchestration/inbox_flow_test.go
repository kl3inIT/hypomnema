package orchestration

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// inboxBot is the default Bot as the Inbox's actor.
var inboxBot = inboxstore.Actor{Kind: inboxstore.Bot, ID: DefaultBotID}

// doingIn is an item the owner gave the default Bot, which it started in the work given.
func (w *world) doingIn(work inboxstore.Work) inboxstore.Todo {
	w.t.Helper()

	ctx := w.t.Context()

	td, err := w.s.Inbox().Create(ctx, inboxstore.Actor{Kind: inboxstore.Owner}, inboxstore.NewTodo{
		Title: "Send Lan the contract", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
		Assignee: inboxstore.Assignee{Kind: inboxstore.ABot, Name: DefaultBotID},
	})
	require.NoError(w.t, err)

	td, err = w.s.Inbox().Start(ctx, inboxstore.Change{Actor: inboxBot, ID: td.ID, Revision: td.Revision}, work)
	require.NoError(w.t, err)

	return td
}

func (w *world) todo(id string) inboxstore.Todo {
	w.t.Helper()

	td, err := w.s.Inbox().Get(w.t.Context(), id)
	require.NoError(w.t, err)

	return td
}

// askMail is the mail a Bot sends in a chat, in the attempt given, as the service proxy asks it.
func (w *world) askMail(chatID, attempt, hash string) Approval {
	w.t.Helper()

	a, granted, err := w.s.AskApproval(w.t.Context(), NewApproval{
		Integration: "google", Label: "work", Action: "gmail.send", ActionName: "Send mail", ChatID: chatID,
		AttemptID: attempt, AskedBy: "pi", Title: "Send mail to Lan", MatchHash: hash,
	})
	require.NoError(w.t, err)
	require.False(w.t, granted)

	return a
}

// Scenario: issue #1004, section 5. A mail the Bot sends while doing an item waits for the owner's Approval, which
// records the item's attempt: the item goes to review in the same write. Approved, it is doing again in the same
// attempt; a second mail rejected gives it back open with the reason, its attempt cleared. A mail asked outside
// any attempt moves no item.
func TestAnApprovalMovesTheItemItsAttemptAsked(t *testing.T) {
	t.Parallel()

	w := newSeedWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, DefaultBotID, "")
	require.NoError(t, err)

	td := w.doingIn(inboxstore.Work{Kind: inboxstore.WorkChat, ID: chat.ID})

	w.askMail(chat.ID, "", "other")
	assert.Equal(t, inboxstore.Doing, w.todo(td.ID).State, "a mail of no attempt is no item's")

	first := w.askMail(chat.ID, td.Work.Attempt, "first")
	got := w.todo(td.ID)
	assert.Equal(t, inboxstore.Review, got.State)
	assert.Equal(t, first.ID, got.ApprovalID)

	again := w.askMail(chat.ID, td.Work.Attempt, "first")
	assert.Equal(t, first.ID, again.ID, "the same mail asked again waits for the same Approval")

	n, err := w.s.Inbox().NeedsYou(ctx, w.clock.now)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "an item in review needs the owner")

	_, err = w.s.DecideApproval(ctx, first.ID, true, ScopeOnce)
	require.NoError(t, err)

	got = w.todo(td.ID)
	assert.Equal(t, inboxstore.Doing, got.State, "approved, the attempt goes on")
	assert.Equal(t, td.Work.Attempt, got.Work.Attempt)

	second := w.askMail(chat.ID, td.Work.Attempt, "second")
	assert.Equal(t, inboxstore.Review, w.todo(td.ID).State)

	_, err = w.s.DecideApproval(ctx, second.ID, false, "")
	require.NoError(t, err)

	got = w.todo(td.ID)
	assert.Equal(t, inboxstore.Open, got.State, "rejected, the item is open again")
	assert.Nil(t, got.Work)
	assert.Empty(t, got.ApprovalID)

	last := got.Events[len(got.Events)-1]
	assert.Equal(t, inboxstore.Actor{Kind: inboxstore.System, ID: "approvals"}, last.Actor)
	assert.Contains(t, string(last.After), "the owner rejected")
}

// Scenario: issue #1004, section 7. A Routine's run ends: what its Bot was doing in it is open again, and so is the
// item whose Approval the end refused, with the reason. Deleting a chat gives back what its Bot was doing there.
func TestAnAttemptEndsWithItsRunOrItsChat(t *testing.T) {
	t.Parallel()

	w := newSeedWorld(t)
	ctx := t.Context()

	r := w.routine(daily("brief"))
	started, err := w.s.RunRoutineNow(ctx, r.ID)
	require.NoError(t, err)

	run := inboxstore.Work{Kind: inboxstore.WorkRun, ID: started.Run.ID}
	doing := w.doingIn(run)
	waiting := w.doingIn(run)
	w.askMail(started.Run.ChatID, waiting.Work.Attempt, "brief")
	require.Equal(t, inboxstore.Review, w.todo(waiting.ID).State)

	w.end(started.Run.ID, succeeded)

	for _, id := range []string{doing.ID, waiting.ID} {
		got := w.todo(id)
		assert.Equal(t, inboxstore.Open, got.State, id)
		assert.Nil(t, got.Work, id)
		assert.Contains(t, string(got.Events[len(got.Events)-1].After), "run ended", id)
	}

	chat, err := w.s.CreateChat(ctx, DefaultBotID, "")
	require.NoError(t, err)

	inChat := w.doingIn(inboxstore.Work{Kind: inboxstore.WorkChat, ID: chat.ID})

	_, err = w.s.DeleteChat(ctx, chat.ID)
	require.NoError(t, err)

	got := w.todo(inChat.ID)
	assert.Equal(t, inboxstore.Open, got.State)
	assert.Equal(t, inboxstore.Actor{Kind: inboxstore.System, ID: "chats"}, got.Events[len(got.Events)-1].Actor)
	assert.Contains(t, string(got.Events[len(got.Events)-1].After), "the chat was deleted")
}

// Scenario: the Inbox's part 3 migration (issue #1004) on a database with to-dos: an item and its history stay
// whole, with no draft; an item from WhatsApp, which needs its account, can be added after it.
func TestInboxBotToolsMigrationKeepsTheItems(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, migrationVersion(t, p, "_inbox_bot_tools.sql")-1)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `INSERT INTO todos (id, title, state, source_kind, source_id, source_account,
		source_ref, created_by_kind, created_at, updated_at) VALUES ('tdo_1', 'Answer Linh', 'proposed', 'zalo',
		'g_1', 'main', '{"kind":"zalo_message","ref":"1"}', 'bot', 1, 1)`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO todo_events (todo_id, at, revision, actor_kind, kind)
		VALUES ('tdo_1', 1, 1, 'bot', 'created')`)
	require.NoError(t, err)

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	s := New(db, &fakeClock{now: t0}, func(prefix string) string { return prefix + "_new" })

	kept, err := s.Inbox().Get(ctx, "tdo_1")
	require.NoError(t, err)
	assert.Equal(t, "Answer Linh", kept.Title)
	assert.Equal(t, "main", kept.Source.Account)
	assert.Empty(t, kept.Draft)
	require.Len(t, kept.Events, 1, "the history is kept")

	made, err := s.Inbox().Create(ctx, inboxBot, inboxstore.NewTodo{
		Title: "Reply to Lan", State: inboxstore.Proposed, Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
		Source: inboxstore.Source{Kind: inboxstore.FromWhatsApp, ID: "8490@s.whatsapp.net", Account: "main",
			Ref: &inboxstore.Link{Kind: inboxstore.LinkWhatsAppMessage, Ref: "3EB0"}},
	})
	require.NoError(t, err)
	assert.Equal(t, inboxstore.FromWhatsApp, made.Source.Kind)
}

// Scenario: issue #1004, section 7, while the item waits for the owner. A chat is deleted while the item its Bot
// was doing there is in review: its pending Approval ends, refused as nobody answered, and the item is open again
// with the reason, in the deletion's transaction; the owner's late answer moves nothing, and the item can be
// started again from another chat.
func TestDeletingAChatEndsTheApprovalItsItemWaitsFor(t *testing.T) {
	t.Parallel()

	w := newSeedWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, DefaultBotID, "")
	require.NoError(t, err)

	td := w.doingIn(inboxstore.Work{Kind: inboxstore.WorkChat, ID: chat.ID})
	asked := w.askMail(chat.ID, td.Work.Attempt, "mail")
	require.Equal(t, inboxstore.Review, w.todo(td.ID).State)

	_, err = w.s.DeleteChat(ctx, chat.ID)
	require.NoError(t, err)

	got := w.todo(td.ID)
	assert.Equal(t, inboxstore.Open, got.State)
	assert.Nil(t, got.Work)
	assert.Empty(t, got.ApprovalID)
	assert.Contains(t, string(got.Events[len(got.Events)-1].After), "the chat was deleted")

	ended, err := w.s.GetApproval(ctx, asked.ID)
	require.NoError(t, err)
	assert.Equal(t, ApprovalRejected, ended.Status)
	assert.True(t, ended.Expired)

	_, err = w.s.DecideApproval(ctx, asked.ID, true, ScopeOnce)
	require.Error(t, err, "the Approval was answered by the deletion")
	assert.Equal(t, inboxstore.Open, w.todo(td.ID).State)

	other, err := w.s.CreateChat(ctx, DefaultBotID, "")
	require.NoError(t, err)

	cur := w.todo(td.ID)
	again, err := w.s.Inbox().Start(ctx, inboxstore.Change{Actor: inboxBot, ID: cur.ID, Revision: cur.Revision},
		inboxstore.Work{Kind: inboxstore.WorkChat, ID: other.ID})
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Doing, again.State)
}

// Scenario: issue #1004, section 5. Two sends of one attempt read the item doing before the first put it in review:
// the second is refused in its own write, recording no Approval that no item waits for, so the owner's answers
// always move the item. Once the first is answered, the same second request is asked again and waits as the item's.
func TestASecondApprovalOfAnAttemptWaitsForTheFirst(t *testing.T) {
	t.Parallel()

	w := newSeedWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, DefaultBotID, "")
	require.NoError(t, err)

	td := w.doingIn(inboxstore.Work{Kind: inboxstore.WorkChat, ID: chat.ID})
	first := w.askMail(chat.ID, td.Work.Attempt, "first")

	second := NewApproval{
		Integration: "google", Label: "work", Action: "gmail.send", ActionName: "Send mail", ChatID: chat.ID,
		AttemptID: td.Work.Attempt, AskedBy: "pi", Title: "Send mail to Minh", MatchHash: "second",
	}
	_, _, err = w.s.AskApproval(ctx, second)
	require.ErrorIs(t, err, ErrConflict)

	pending, err := w.s.ListApprovals(ctx, ApprovalFilter{Status: ApprovalPending})
	require.NoError(t, err)
	require.Len(t, pending, 1, "no Approval waits outside the item")
	assert.Equal(t, first.ID, pending[0].ID)

	_, err = w.s.DecideApproval(ctx, first.ID, true, ScopeOnce)
	require.NoError(t, err)
	require.Equal(t, inboxstore.Doing, w.todo(td.ID).State)

	retried, granted, err := w.s.AskApproval(ctx, second)
	require.NoError(t, err)
	require.False(t, granted)

	got := w.todo(td.ID)
	assert.Equal(t, inboxstore.Review, got.State)
	assert.Equal(t, retried.ID, got.ApprovalID)

	_, err = w.s.DecideApproval(ctx, retried.ID, false, "")
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Open, w.todo(td.ID).State, "the owner's no moves the item")
}
