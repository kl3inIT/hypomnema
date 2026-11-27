package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
)

// botAdds is an item a Bot adds from its turn in chat, given to the Bot to.
func (w passWorld) botAdds(by, chat, to string) inboxstore.Todo {
	w.t.Helper()

	td, err := w.s.Inbox().Create(w.t.Context(), asBot(by), inboxstore.NewTodo{
		Title: "Read the group for Kai", State: inboxstore.Open, Chat: chat,
		Source:   inboxstore.Source{Kind: inboxstore.FromChat, ID: chat},
		Assignee: inboxstore.Assignee{Kind: inboxstore.ABot, Name: to},
	})
	require.NoError(w.t, err)

	return td
}

// Scenario: review of PR #1237, issue #1229 rules 2 and 5. The mark is the item's: from its marked chat BeyondPilot
// adds another item for itself, or one for the main Bot, and neither new chat is the owner's turn; the main Bot passing
// that other item on carries nothing either. Passing the marked item itself back still carries the mark.
func TestReviewOwnerMarkStaysWithTheItemItWasGivenFor(t *testing.T) {
	t.Parallel()

	w := newPassWorld(t)

	got := w.passFrom(DefaultBotID, w.item.Chat, w.item, w.beyond.ID)
	require.True(t, w.ownerTurn(got.Chat))

	self := w.botAdds(w.beyond.ID, got.Chat, w.beyond.ID)
	require.NotEqual(t, got.Chat, self.Chat)
	assert.False(t, w.ownerTurn(self.Chat), "an item it adds for itself")

	other := w.botAdds(w.beyond.ID, got.Chat, DefaultBotID)
	assert.False(t, w.ownerTurn(other.Chat), "an item it adds for the main Bot")

	on := w.passFrom(DefaultBotID, other.Chat, other, w.beyond.ID)
	assert.False(t, w.ownerTurn(on.Chat), "that item passed on")

	cur, err := w.s.Inbox().Get(t.Context(), got.ID)
	require.NoError(t, err)

	back := w.passFrom(w.beyond.ID, got.Chat, cur, DefaultBotID)
	assert.True(t, w.ownerTurn(back.Chat), "the marked item itself, given back")
}

// Scenario: review of PR #1237, and decision 7 of issue #1229. The owner's words typed in the Inbox are his turn in the
// item's chat: his prompt when he hands it over, and his comment. An item he gives without a word (Hand to Hypo with an
// empty prompt, or giving it in an edit) is no turn of his, and the main Bot's pass from it carries no mark.
func TestReviewOwnerMarkTakesTheOwnersOwnWords(t *testing.T) {
	t.Parallel()

	w := newPassWorld(t)
	ctx := t.Context()

	assert.True(t, w.ownerTurn(w.item.Chat), "handed over with his prompt")

	silent, err := w.s.Inbox().Create(ctx, inboxOwner, inboxstore.NewTodo{
		Title: "Answer Kai", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
	})
	require.NoError(t, err)

	silent, err = w.s.Inbox().Hand(ctx, inboxstore.Change{Actor: inboxOwner, ID: silent.ID, Revision: silent.Revision},
		DefaultBotID, "")
	require.NoError(t, err)
	assert.False(t, w.ownerTurn(silent.Chat), "handed over without a word")

	passed := w.passFrom(DefaultBotID, silent.Chat, silent, w.beyond.ID)
	assert.False(t, w.ownerTurn(passed.Chat), "passed on from a hand-over without a word")

	given, err := w.s.Inbox().Create(ctx, inboxOwner, inboxstore.NewTodo{
		Title: "Read the group", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
		Assignee: inboxstore.Assignee{Kind: inboxstore.ABot, Name: DefaultBotID},
	})
	require.NoError(t, err)
	assert.False(t, w.ownerTurn(given.Chat), "given in the item he adds, without a word")

	_, err = w.s.Inbox().Comment(ctx, inboxOwner, given.ID, "đọc nhóm WhatsApp của Kai giúp anh")
	require.NoError(t, err)
	assert.True(t, w.ownerTurn(given.Chat), "his comment is his turn")

	w.runEvent(given.Chat)
	assert.False(t, w.ownerTurn(given.Chat), "a Run's events after it are not")
}

// Scenario: review of PR #1237, issue #1229 rule 3. The main Bot passes the item into the chat it is in, and that chat
// is the Bot's main chat, where it asks for a Connection: declined, the next hand-off into the chat is no new ask of the
// owner's, and the Bot is answered the declined request, not a new card. His own message there asks anew.
func TestReviewOwnerMarkMakesNoNewConnectionCardAfterADecline(t *testing.T) {
	t.Parallel()

	w := newRequestWorld(t)
	ctx := t.Context()

	mainChat, err := w.s.CreateChat(ctx, DefaultBotID, "")
	require.NoError(t, err)
	_, err = w.s.AddChatMessage(ctx, mainChat.ID, ChatOwner, "ask hr to check the team's calendar")
	require.NoError(t, err)

	td, err := w.s.Inbox().Create(ctx, asBot(DefaultBotID), inboxstore.NewTodo{
		Title: "Check the team's calendar", State: inboxstore.Open, Chat: mainChat.ID,
		Source:   inboxstore.Source{Kind: inboxstore.FromChat, ID: mainChat.ID},
		Assignee: inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.bot.ID},
	})
	require.NoError(t, err)

	_, err = w.s.SetMainChat(ctx, w.bot.ID, td.Chat)
	require.NoError(t, err)

	w.main.ID = td.Chat
	require.NoError(t, w.s.MarkChatTurn(ctx, td.Chat, "pi-1"))

	asked := w.request(t, "google:team")
	require.Equal(t, RequestPending, asked.State)

	_, err = w.s.DecideConnectionRequest(ctx, asked.ID, false)
	require.NoError(t, err)

	back, err := w.s.Inbox().Edit(ctx, inboxstore.Change{Actor: asBot(w.bot.ID), ID: td.ID, Revision: td.Revision,
		Chat: td.Chat}, inboxstore.Edit{Assignee: &inboxstore.Assignee{Kind: inboxstore.Me}})
	require.NoError(t, err)

	again, err := w.s.Inbox().Edit(ctx, inboxstore.Change{Actor: asBot(DefaultBotID), ID: back.ID,
		Revision: back.Revision, Chat: mainChat.ID},
		inboxstore.Edit{Assignee: &inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.bot.ID}})
	require.NoError(t, err)
	require.Equal(t, td.Chat, again.Chat)

	require.NoError(t, w.s.SettleChatTurn(ctx, td.Chat, "pi-1"))
	require.NoError(t, w.s.MarkChatTurn(ctx, td.Chat, "pi-2"))

	same := w.request(t, "google:team")
	assert.Equal(t, asked.ID, same.ID, "the hand-off is no new ask of his")
	assert.Equal(t, RequestDeclined, same.State)

	require.NoError(t, w.s.SettleChatTurn(ctx, td.Chat, "pi-2"))
	_, err = w.s.AddChatMessage(ctx, td.Chat, ChatOwner, "try again, I'll allow it")
	require.NoError(t, err)
	require.NoError(t, w.s.MarkChatTurn(ctx, td.Chat, "pi-3"))

	fresh := w.request(t, "google:team")
	assert.Equal(t, RequestPending, fresh.State, "his own message asks anew")
}

// Scenario: review of PR #1237, issue #1229 rule 3. An item's chat from before the migration, woken by a Bot that
// passed it with nothing to show it was on the owner's line, takes no mark when a pass on his line wakes it again: the
// migration records that old wake-up as a Bot's, and the owner's old prompt as his.
func TestReviewOwnerMarkTakesNoChatWokenWithoutProofBefore(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	before, own := wakeByVersions(t)
	db, p := databaseAt(t, before)

	for _, stmt := range []string{
		`INSERT INTO chats (id, title, bot_id, created_at, updated_at) VALUES ('cht_item', 'Inbox: Kai', '` +
			DefaultBotID + `', 1, 1)`,
		`INSERT INTO chats (id, title, bot_id, created_at, updated_at) VALUES ('cht_his', 'Inbox: Lan', '` +
			DefaultBotID + `', 1, 1)`,
		`INSERT INTO todos (id, title, state, source_kind, created_by_kind, chat_id, assignee_kind, assignee,
			created_at, updated_at)
		 VALUES ('tdo_a', 'Read the group', 'open', 'owner', 'owner', 'cht_item', 'bot', '` + DefaultBotID + `', 1, 1)`,
		`INSERT INTO todos (id, title, state, source_kind, created_by_kind, chat_id, assignee_kind, assignee,
			created_at, updated_at)
		 VALUES ('tdo_b', 'Answer Lan', 'open', 'owner', 'owner', 'cht_his', 'bot', '` + DefaultBotID + `', 1, 1)`,
		`INSERT INTO chat_messages (chat_id, role, kind, text, to_coordinator, created_at)
		 VALUES ('cht_item', 'system', 'wake', 'VMAP passed you Inbox item tdo_a', 1, 2)`,
		`INSERT INTO chat_messages (chat_id, role, kind, text, to_coordinator, created_at, recall)
		 VALUES ('cht_his', 'system', 'wake', 'The owner handed you Inbox item tdo_b', 1, 2, 'Answer Lan')`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	_, err := p.UpTo(ctx, own)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `INSERT INTO chat_messages (chat_id, role, kind, text, to_coordinator, created_at,
		wake_by, wake_todo) VALUES ('cht_item', 'system', 'wake', 'Hypo passed you Inbox item tdo_a', 1, 3,
		'bot_for_owner', 'tdo_a')`)
	require.NoError(t, err)

	s := New(db, &fakeClock{now: t0}, func(prefix string) string { return prefix + "_new" })

	mine, err := s.OwnerTurn(ctx, "cht_item")
	require.NoError(t, err)
	assert.False(t, mine, "the old wake-up of a Bot clears the mark")

	mine, err = s.OwnerTurn(ctx, "cht_his")
	require.NoError(t, err)
	assert.True(t, mine, "his old prompt is his turn")
}

// markedByMain is an item the main Bot adds for BeyondPilot on the owner's message in its chat, which marks the item's
// chat with BeyondPilot, and the main Bot's chat.
func (w passWorld) markedByMain() (string, inboxstore.Todo) {
	w.t.Helper()

	ctx := w.t.Context()

	mainChat, err := w.s.CreateChat(ctx, DefaultBotID, "")
	require.NoError(w.t, err)
	_, err = w.s.AddChatMessage(ctx, mainChat.ID, ChatOwner, "ask BeyondPilot to read the group")
	require.NoError(w.t, err)

	td := w.botAdds(DefaultBotID, mainChat.ID, w.beyond.ID)
	require.True(w.t, w.ownerTurn(td.Chat))

	return mainChat.ID, td
}

// passedAgain is BeyondPilot giving the item back to the owner from its chat, and the main Bot passing it to
// BeyondPilot again from its chat with the owner, which wakes BeyondPilot in the same chat.
func (w passWorld) passedAgain(mainChat, id string) inboxstore.Todo {
	w.t.Helper()

	ctx := w.t.Context()

	cur, err := w.s.Inbox().Get(ctx, id)
	require.NoError(w.t, err)

	back, err := w.s.Inbox().Edit(ctx, inboxstore.Change{Actor: asBot(w.beyond.ID), ID: cur.ID, Revision: cur.Revision,
		Chat: cur.Chat}, inboxstore.Edit{Assignee: &inboxstore.Assignee{Kind: inboxstore.Me}})
	require.NoError(w.t, err)

	again := w.passFrom(DefaultBotID, mainChat, back, w.beyond.ID)
	require.Equal(w.t, cur.Chat, again.Chat, "the item's chat with BeyondPilot is the same")

	return again
}

// Scenario: review round 2 of PR #1237, issue #1229 rule 3. The owner takes the marked item back and gives it to the
// same Bot without a word, which wakes it in the same chat: no turn of his, and it clears the mark for good, so the
// main Bot passing it there again from his message brings it back no more.
func TestReviewEmptyHandoffClearsTheMark(t *testing.T) {
	t.Parallel()

	w := newPassWorld(t)
	ctx := t.Context()

	mainChat, td := w.markedByMain()

	taken, err := w.s.Inbox().Edit(ctx, inboxstore.Change{Actor: inboxOwner, ID: td.ID, Revision: td.Revision},
		inboxstore.Edit{Assignee: &inboxstore.Assignee{Kind: inboxstore.Me}})
	require.NoError(t, err)

	given, err := w.s.Inbox().Hand(ctx, inboxstore.Change{Actor: inboxOwner, ID: taken.ID, Revision: taken.Revision},
		w.beyond.ID, "")
	require.NoError(t, err)
	require.Equal(t, td.Chat, given.Chat)
	assert.False(t, w.ownerTurn(given.Chat), "given without a word")

	again := w.passedAgain(mainChat, td.ID)
	assert.False(t, w.ownerTurn(again.Chat), "cleared for good")
}

// Scenario: review round 2 of PR #1237, issue #1229 rule 3. A background command ends in the marked chat and its line
// wakes the Bot: no turn of his, and it clears the mark for good, as any input but his own words, the hand-off's and a
// Run's events does.
func TestReviewBackgroundWakeClearsTheMark(t *testing.T) {
	t.Parallel()

	w := newPassWorld(t)
	ctx := t.Context()

	mainChat, td := w.markedByMain()

	require.NoError(t, w.s.StartBackground(ctx, td.Chat, "pi-1", "bg_0123456789ab"))

	code := 0
	woke, err := w.s.EndBackground(ctx, td.Chat, BackgroundEnd{ID: "bg_0123456789ab", ExitCode: &code})
	require.NoError(t, err)
	require.True(t, woke)
	assert.False(t, w.ownerTurn(td.Chat), "the command's end is no turn of his")

	again := w.passedAgain(mainChat, td.ID)
	assert.False(t, w.ownerTurn(again.Chat), "cleared for good")
}
