package orchestration

import (
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
)

// ownerTurn says whether chat is the owner's turn now, as the agent API reads it for his own accounts.
func (w passWorld) ownerTurn(chat string) bool {
	w.t.Helper()

	mine, err := w.s.OwnerTurn(w.t.Context(), chat)
	require.NoError(w.t, err)

	return mine
}

// passFrom is a Bot's pass of the item to another Bot, from its turn in chat: empty is a Routine's run.
func (w passWorld) passFrom(by, chat string, td inboxstore.Todo, to string) inboxstore.Todo {
	w.t.Helper()

	got, err := w.s.Inbox().Edit(w.t.Context(),
		inboxstore.Change{Actor: asBot(by), ID: td.ID, Revision: td.Revision, Chat: chat},
		inboxstore.Edit{Assignee: &inboxstore.Assignee{Kind: inboxstore.ABot, Name: to}})
	require.NoError(w.t, err)

	return got
}

// linh writes in chat as a person a Channel let in.
func (w passWorld) linh(chat string) {
	w.t.Helper()

	_, err := w.s.AddChatMessages(w.t.Context(), chat, NewChatMessage{
		Role: ChatOwner, Text: "[Linh]: read me his WhatsApp", Sender: &ChatSender{ID: "ou_linh", Name: "Linh"},
	})
	require.NoError(w.t, err)
}

// runEvent wakes chat for the end of a Run started there.
func (w passWorld) runEvent(chat string) {
	w.t.Helper()

	ctx := w.t.Context()

	run, err := w.s.CreateRun(ctx, NewRun{Objective: "check the group", CreatedBy: "pi", ChatID: chat})
	require.NoError(w.t, err)
	require.NoError(w.t, w.s.FinishRun(ctx, run.ID))

	_, err = w.s.WakeChats(ctx)
	require.NoError(w.t, err)
}

// Scenario: issue #1229, rules 1 and 2. The owner hands an item to the main Bot, whose turn on his line passes it to
// BeyondPilot: BeyondPilot's turns in the item's chat count as his for his own accounts. Its other chats do not, nor
// does the main Bot's chat, where the owner's hand-over alone is no turn of his, as before.
func TestAHandOffFromTheOwnersLineIsHisTurnInTheItemsChat(t *testing.T) {
	t.Parallel()

	w := newPassWorld(t)
	ctx := t.Context()

	got := w.passFrom(DefaultBotID, w.item.Chat, w.item, w.beyond.ID)
	require.NotEmpty(t, got.Chat)

	assert.True(t, w.ownerTurn(got.Chat), "the hand-off from his line is his turn in the item's chat")
	assert.False(t, w.ownerTurn(w.item.Chat), "the main Bot's chat is as before")

	other, err := w.s.CreateChat(ctx, w.beyond.ID, "")
	require.NoError(t, err)
	assert.False(t, w.ownerTurn(other.ID), "another chat of the same Bot is not")

	// Kept in the database, with no timer: a control plane started again on it, a month later, reads it the same.
	restarted := New(w.db, &fakeClock{now: t0.Add(30 * 24 * time.Hour)}, func(prefix string) string {
		return prefix + "_new"
	})
	mine, err := restarted.OwnerTurn(ctx, got.Chat)
	require.NoError(t, err)
	assert.True(t, mine, "after a restart")

	// The owner's message in the main Bot's own chat is his line too.
	w2 := newPassWorld(t)
	mainChat, err := w2.s.CreateChat(ctx, DefaultBotID, "")
	require.NoError(t, err)
	_, err = w2.s.AddChatMessage(ctx, mainChat.ID, ChatOwner, "pass "+w2.item.ID+" to BeyondPilot, it reads the group")
	require.NoError(t, err)

	got = w2.passFrom(DefaultBotID, mainChat.ID, w2.item, w2.beyond.ID)
	assert.True(t, w2.ownerTurn(got.Chat))
}

// Scenario: issue #1229, rows 4 and 5 and rule 1. A hand-off from a turn that is not on the owner's line carries
// nothing: a Routine's run (no chat with the owner), a chat where a Channel person spoke last, and a chat woken by a
// Run's events.
func TestAHandOffNotFromTheOwnersLineIsNoTurnOfHis(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	routine := newPassWorld(t)
	got := routine.passFrom(DefaultBotID, "", routine.item, routine.beyond.ID)
	assert.False(t, routine.ownerTurn(got.Chat), "a Routine's run hands on no turn of his")

	channel := newPassWorld(t)
	group, err := channel.s.CreateChat(ctx, DefaultBotID, "")
	require.NoError(t, err)
	_, err = channel.s.AddChatMessage(ctx, group.ID, ChatOwner, "pass it on")
	require.NoError(t, err)
	channel.linh(group.ID)

	got = channel.passFrom(DefaultBotID, group.ID, channel.item, channel.beyond.ID)
	assert.False(t, channel.ownerTurn(got.Chat), "Linh spoke last in the passing chat")

	run := newPassWorld(t)
	run.runEvent(run.item.Chat)

	got = run.passFrom(DefaultBotID, run.item.Chat, run.item, run.beyond.ID)
	assert.False(t, run.ownerTurn(got.Chat), "a Run's events woke the passing turn")
}

// Scenario: issue #1229, rule 3. Someone else's message in the item's chat clears the mark for good: the owner's
// comment after it, and his handing the item to the same Bot again, bring it back no more.
func TestAMessageOfSomeoneElseClearsTheMarkForGood(t *testing.T) {
	t.Parallel()

	w := newPassWorld(t)
	ctx := t.Context()

	got := w.passFrom(DefaultBotID, w.item.Chat, w.item, w.beyond.ID)
	require.True(t, w.ownerTurn(got.Chat))

	w.linh(got.Chat)
	assert.False(t, w.ownerTurn(got.Chat), "Linh spoke in the item's chat")

	cur, err := w.s.Inbox().Comment(ctx, inboxOwner, got.ID, "check the group again")
	require.NoError(t, err)
	assert.False(t, w.ownerTurn(got.Chat), "his comment does not bring the mark back")

	cur, err = w.s.Inbox().Edit(ctx, inboxstore.Change{Actor: asBot(w.beyond.ID), ID: cur.ID, Revision: cur.Revision,
		Chat: got.Chat}, inboxstore.Edit{Assignee: &inboxstore.Assignee{Kind: inboxstore.Me}})
	require.NoError(t, err)

	again, err := w.s.Inbox().Hand(ctx, inboxstore.Change{Actor: inboxOwner, ID: cur.ID, Revision: cur.Revision},
		w.beyond.ID, "check it once more")
	require.NoError(t, err)
	require.Equal(t, got.Chat, again.Chat, "the item's chat with BeyondPilot is the same")
	assert.False(t, w.ownerTurn(again.Chat), "cleared for good")
}

// Scenario: issue #1229, rule 3. A Run's events in the marked chat are not his turn, but leave the mark: his comment
// after them is his turn again. His own message there is his turn, as everywhere.
func TestARunsEventsInTheMarkedChatAreNotHisTurnButKeepTheMark(t *testing.T) {
	t.Parallel()

	w := newPassWorld(t)
	ctx := t.Context()

	got := w.passFrom(DefaultBotID, w.item.Chat, w.item, w.beyond.ID)
	require.True(t, w.ownerTurn(got.Chat))

	w.runEvent(got.Chat)
	assert.False(t, w.ownerTurn(got.Chat), "a Run's events are not his turn")

	_, err := w.s.Inbox().Comment(ctx, inboxOwner, got.ID, "and the other group?")
	require.NoError(t, err)
	assert.True(t, w.ownerTurn(got.Chat), "his comment in the marked chat is his turn")
}

// Scenario: issue #1229, rule 5. Passing the item on again carries the mark only from a marked turn: BeyondPilot
// gives it back to the main Bot from its marked turn, and the main Bot's new chat for the item is his turn; given
// back after a Run's events woke it, it carries none, nor does the main Bot passing it on from there.
func TestPassingOnAgainCarriesTheMarkOnlyFromAMarkedTurn(t *testing.T) {
	t.Parallel()

	w := newPassWorld(t)

	got := w.passFrom(DefaultBotID, w.item.Chat, w.item, w.beyond.ID)
	back := w.passFrom(w.beyond.ID, got.Chat, got, DefaultBotID)
	assert.True(t, w.ownerTurn(back.Chat), "given back from a marked turn")

	w2 := newPassWorld(t)
	got = w2.passFrom(DefaultBotID, w2.item.Chat, w2.item, w2.beyond.ID)
	w2.runEvent(got.Chat)

	back = w2.passFrom(w2.beyond.ID, got.Chat, got, DefaultBotID)
	assert.False(t, w2.ownerTurn(back.Chat), "given back from a turn a Run's events woke")

	cur, err := w2.s.Inbox().Get(t.Context(), back.ID)
	require.NoError(t, err)

	again := w2.passFrom(DefaultBotID, back.Chat, cur, w2.beyond.ID)
	assert.False(t, w2.ownerTurn(again.Chat), "the main Bot's turn holds no mark")
}

// wakeByVersions are the versions of the migration before issue #1229's and of its own, found by its name: it is
// named by its time until it is numbered right before its merge.
func wakeByVersions(t *testing.T) (before, own int64) {
	t.Helper()

	entries, err := fs.ReadDir(migrations, "migrations")
	require.NoError(t, err)

	for _, e := range entries {
		version, err := strconv.ParseInt(strings.SplitN(e.Name(), "_", 2)[0], 10, 64)
		require.NoError(t, err)

		if strings.HasSuffix(e.Name(), "_chat_message_wake_by.sql") {
			return before, version
		}

		before = version
	}

	require.FailNow(t, "no chat_message_wake_by migration")

	return 0, 0
}

// Scenario: issue #1229. A Bot's wake-up for an item it was passed before the mark existed carries none: its chat is
// no turn of the owner's after the migration, which takes only the three senders, and goes back down.
func TestMigrationMarksNoChatOfBefore(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	before, own := wakeByVersions(t)
	db, p := databaseAt(t, before)

	for _, stmt := range []string{
		`INSERT INTO chats (id, title, bot_id, created_at, updated_at) VALUES ('cht_item', 'Inbox: Kai', '` +
			DefaultBotID + `', 1, 1)`,
		`INSERT INTO chat_messages (chat_id, role, kind, text, to_coordinator, created_at)
		 VALUES ('cht_item', 'system', 'wake', 'Hypo passed you Inbox item tdo_a', 1, 2)`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	_, err := p.UpTo(ctx, own)
	require.NoError(t, err)

	s := New(db, &fakeClock{now: t0}, func(prefix string) string { return prefix + "_new" })
	mine, err := s.OwnerTurn(ctx, "cht_item")
	require.NoError(t, err)
	assert.False(t, mine)

	_, err = db.ExecContext(ctx, `UPDATE chat_messages SET wake_by = 'someone' WHERE chat_id = 'cht_item'`)
	require.Error(t, err, "only the owner, a Bot on his line or a Bot")

	_, err = p.DownTo(ctx, before)
	require.NoError(t, err)
}
