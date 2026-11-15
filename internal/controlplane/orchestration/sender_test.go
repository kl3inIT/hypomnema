package orchestration

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Scenario (issue #468, rule 7): a chat where the owner writes, then someone
// a Channel let in, then the owner again, then a Run's event wakes pi. It is
// the owner's turn only while the last message pi is given is his: another
// person's message ends it, his own begins it again, and a wake-up ends it
// whoever wrote last. A chat with nothing said is no turn of his. Each
// message keeps who wrote it, and a message that waited in the queue keeps
// its writer when it goes into the chat.
func TestItIsTheOwnersTurnOnlyWhileHisMessageIsTheLastOneGivenToPi(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	turn := func() bool {
		t.Helper()

		mine, err := w.s.OwnerTurn(ctx, chat.ID)
		require.NoError(t, err)

		return mine
	}

	assert.False(t, turn(), "nothing was asked yet")

	_, err = w.s.AddChatMessages(ctx, chat.ID, NewChatMessage{Role: ChatOwner, Text: "read my unread mail"})
	require.NoError(t, err)
	assert.True(t, turn())

	_, err = w.s.AddChatMessage(ctx, chat.ID, ChatCoordinator, "Reading it.")
	require.NoError(t, err)
	assert.True(t, turn(), "the Bot's own reply does not end the owner's turn")

	linh := &ChatSender{ID: "ou_linh", Name: "Linh"}

	_, err = w.s.AddChatMessages(ctx, chat.ID, NewChatMessage{Role: ChatOwner, Text: "and mine?", Sender: linh})
	require.NoError(t, err)
	assert.False(t, turn(), "someone else spoke last")

	_, err = w.s.AddChatMessages(ctx, chat.ID, NewChatMessage{Role: ChatSystem, Text: "x", Sender: linh})
	require.ErrorIs(t, err, ErrInvalid, "only a person's message has a sender")

	// Linh's next message waits while pi works; it keeps her name when it goes in.
	_, queued, err := w.s.SendOwnerMessage(ctx, chat.ID,
		NewChatMessage{Role: ChatOwner, Text: "please", Sender: linh}, true)
	require.NoError(t, err)
	require.NotNil(t, queued)
	assert.Equal(t, linh, queued.Sender)

	_, err = w.s.AddChatMessages(ctx, chat.ID, NewChatMessage{Role: ChatOwner, Text: "no, only mine"})
	require.NoError(t, err)
	assert.True(t, turn())

	moved, err := w.s.TakeChatQueue(ctx, chat.ID)
	require.NoError(t, err)
	require.Equal(t, 1, moved)
	assert.False(t, turn(), "her queued message, now in the chat, is the last")

	all, err := w.s.ListChatMessages(ctx, chat.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, all, 5)
	assert.Nil(t, all[0].Sender, "the owner's message has no sender")
	assert.Equal(t, linh, all[2].Sender)
	assert.Equal(t, linh, all[4].Sender)

	_, err = w.s.AddChatMessages(ctx, chat.ID, NewChatMessage{Role: ChatOwner, Text: "go on"})
	require.NoError(t, err)
	assert.True(t, turn())

	// A Run of the chat ends: the wake-up that tells pi is never the owner's turn.
	run, err := w.s.CreateRun(ctx, NewRun{Objective: "x", CreatedBy: "pi", ChatID: chat.ID})
	require.NoError(t, err)
	require.NoError(t, w.s.FinishRun(ctx, run.ID))

	n, err := w.s.WakeChats(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	assert.False(t, turn(), "a wake-up ends the owner's turn")
}

// Scenario (issue #468): a database from before senders were kept, with a
// Channel whose owner and Linh each have a direct chat with its Bot, and a
// group's chat. After the migration Linh's messages are hers, with her name;
// the owner's direct chat is still his; the group's messages are of someone
// not known, so the group is no owner's turn until he writes there again.
func TestMigrationRecordsWhoWroteTheLarkMessagesOfBefore(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 86)
	require.NoError(t, err)

	for _, stmt := range []string{
		`INSERT INTO channels (name, app_id, secret, bot_id, mode, owner_id, enabled, created_at, updated_at)
			VALUES ('main', 'cli_a1b2', 'lark-channel-main', '` + DefaultBotID + `', 'allowlist', 'ou_owner', 1, 1, 1)`,
		`INSERT INTO channel_people (channel, person_id, email, name, created_at)
			VALUES ('main', 'ou_linh', 'linh@example.com', 'Linh', 1)`,
		`INSERT INTO chats (id, title, bot_id, created_at, updated_at) VALUES ('cht_owner', 'Mine', '` +
			DefaultBotID + `', 1, 1)`,
		`INSERT INTO chats (id, title, bot_id, created_at, updated_at) VALUES ('cht_linh', 'Lark: Linh', '` +
			DefaultBotID + `', 1, 1)`,
		`INSERT INTO chats (id, title, bot_id, channel_group, created_at, updated_at) VALUES ('cht_group', 'Lark: Ops', '` +
			DefaultBotID + `', 1, 1, 1)`,
		`INSERT INTO channel_chats (external_chat_id, channel, person_id, chat_id, created_at)
			VALUES ('oc_owner', 'main', 'ou_owner', 'cht_owner', 1)`,
		`INSERT INTO channel_chats (external_chat_id, channel, person_id, chat_id, created_at)
			VALUES ('oc_linh', 'main', 'ou_linh', 'cht_linh', 1)`,
		`INSERT INTO channel_chats (external_chat_id, channel, person_id, chat_id, created_at)
			VALUES ('oc_group', 'main', '', 'cht_group', 1)`,
		`INSERT INTO chat_messages (chat_id, role, text, created_at) VALUES ('cht_owner', 'owner', 'mine', 2)`,
		`INSERT INTO chat_messages (chat_id, role, text, created_at) VALUES ('cht_linh', 'owner', '[Lark message from Linh]', 2)`,
		`INSERT INTO chat_messages (chat_id, role, text, created_at) VALUES ('cht_group', 'owner', '[Owner]: hi', 2)`,
	} {
		_, err = db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	s := New(db, &fakeClock{now: t0}, func(prefix string) string { return prefix + "_new" })

	for chat, want := range map[string]bool{"cht_owner": true, "cht_linh": false, "cht_group": false} {
		mine, err := s.OwnerTurn(ctx, chat)
		require.NoError(t, err)
		assert.Equal(t, want, mine, chat)
	}

	linh, err := s.ListChatMessages(ctx, "cht_linh", 0, 0)
	require.NoError(t, err)
	require.Len(t, linh, 1)
	assert.Equal(t, &ChatSender{ID: "ou_linh", Name: "Linh"}, linh[0].Sender)

	_, err = s.AddChatMessages(ctx, "cht_group", NewChatMessage{Role: ChatOwner, Text: "[Owner]: now me"})
	require.NoError(t, err)

	mine, err := s.OwnerTurn(ctx, "cht_group")
	require.NoError(t, err)
	assert.True(t, mine, "the owner's next message in the group is his")
}
