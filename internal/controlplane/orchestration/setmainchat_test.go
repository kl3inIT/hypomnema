package orchestration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// oldLarkLabel is how the Lark Channel wrote another person's direct message before sender ids (channels/channels.go
// before commit 49050bf6): a label, then the text.
const oldLarkLabel = "[Lark message from Someone, who is not the owner. Questions for approval go to the owner.]\nhi"

// Scenario: the owner makes an older chat of a Bot its main chat (issue #923). The chat chosen becomes the main
// chat and the former one stays as it was; a chat of another Bot, one that is not there, a Routine's chat, a Lark
// chat mapped to a person, a Lark group's chat, one another person wrote in and another person's from before sender
// ids, titled "Lark: <name>" with the label Lark put on that person's lines (migration 00138), are refused and leave
// the main chat where it was; a Bot that is not there is not found. A web chat the owner began with "Lark: " is no Lark
// chat and is made the main chat. Each chat says what it is when read alone and in a list.
func TestTheOwnerSetsAnOlderChatAsTheMainChat(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	older, err := w.s.CreateChatWithID(ctx, "", "", "an older topic", ChatModel{})
	require.NoError(t, err)

	home, _, err := w.s.OpenMainChat(ctx, "", "", "", ChatModel{})
	require.NoError(t, err)

	routine, err := w.s.CreateChatWithID(ctx, "", "", "a Routine's run", ChatModel{})
	require.NoError(t, err)
	person, err := w.s.CreateChatWithID(ctx, "", "", "someone's Lark DM", ChatModel{})
	require.NoError(t, err)
	group, err := w.s.CreateChatWithID(ctx, "", "", "a Lark group", ChatModel{})
	require.NoError(t, err)
	wrote, err := w.s.CreateChatWithID(ctx, "", "", "a DM a /new left", ChatModel{})
	require.NoError(t, err)
	// Another person's direct chat from before migration 00087, whose mapping a /new moved: titled by Lark, its lines
	// the owner's with Lark's label.
	titled, err := w.s.CreateChatWithID(ctx, "", "", "Lark: Someone", ChatModel{})
	require.NoError(t, err)
	// A web chat, titled by its first line, which begins with "Lark: ".
	web, err := w.s.CreateChatWithID(ctx, "", "", "Lark: fix the notice", ChatModel{})
	require.NoError(t, err)
	_, err = w.s.AddChatMessage(ctx, web.ID, ChatOwner, "Lark: fix the notice")
	require.NoError(t, err)

	for _, stmt := range []string{
		`INSERT INTO bots (id, name, created_at, updated_at) VALUES ('bot_side', 'side', 1, 1)`,
		`INSERT INTO routines (id, bot_id, name, kind, prompt, timeout_seconds, state, created_by, created_at,
			updated_at) VALUES ('rtn_a', '` + DefaultBotID + `', 'digest', 'prompt', 'sum up', 60, 'on', 'owner', 1, 1)`,
		`INSERT INTO routine_runs (id, routine_id, started_by, state, started_at, chat_id)
			VALUES ('rrn_a', 'rtn_a', 'schedule', 'running', 1, '` + routine.ID + `')`,
		`INSERT INTO channels (name, kind, app_id, secret, bot_id, mode, owner_id, created_at, updated_at)
			VALUES ('lark', 'lark', 'cli_a', 's', '` + DefaultBotID + `', 'owner', 'ou_owner', 1, 1)`,
		`INSERT INTO channel_chats (external_chat_id, channel, person_id, chat_id, sent_seq, created_at)
			VALUES ('oc_other', 'lark', 'ou_other', '` + person.ID + `', 0, 1)`,
		`UPDATE chats SET channel_group = 1 WHERE id = '` + group.ID + `'`,
		`INSERT INTO chat_messages (chat_id, role, kind, text, sender_id, sender_name, created_at)
			VALUES ('` + wrote.ID + `', 'owner', '', 'hi', 'ou_other', 'Other', 1)`,
		`INSERT INTO chat_messages (chat_id, role, kind, text, created_at) VALUES ('` + titled.ID + `', 'owner', '', '` +
			oldLarkLabel + `', 1)`,
	} {
		_, err := w.db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	side, err := w.s.CreateChatWithID(ctx, "", "bot_side", "the side Bot's", ChatModel{})
	require.NoError(t, err)

	b, err := w.s.SetMainChat(ctx, DefaultBotID, older.ID)
	require.NoError(t, err)
	assert.Equal(t, older.ID, b.MainChat, "the chat chosen is the main chat")

	got, err := w.s.GetChat(ctx, home.ID)
	require.NoError(t, err)
	assert.Equal(t, home.ID, got.ID, "the former main chat stays")

	missing := "cht_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, refused := range []string{side.ID, missing, routine.ID, person.ID, group.ID, wrote.ID, titled.ID} {
		_, err := w.s.SetMainChat(ctx, DefaultBotID, refused)
		require.ErrorIs(t, err, ErrInvalid, refused)
	}

	assert.Equal(t, older.ID, mainChatOf(t, w.s, DefaultBotID), "a refused chat leaves the main chat")

	_, err = w.s.SetMainChat(ctx, "bot_none", older.ID)
	require.ErrorIs(t, err, ErrNotFound)

	type marks struct{ routine, channel bool }

	want := map[string]marks{
		older.ID: {}, home.ID: {}, routine.ID: {routine: true}, person.ID: {channel: true},
		group.ID: {channel: true}, wrote.ID: {channel: true}, titled.ID: {channel: true}, web.ID: {},
	}

	read := map[string]marks{}
	for id := range want {
		c, err := w.s.GetChat(ctx, id)
		require.NoError(t, err)

		read[id] = marks{c.Routine, c.Channel}
	}

	assert.Equal(t, want, read, "read alone")

	chats, err := w.s.ListChats(ctx, DefaultBotID, 0, 0)
	require.NoError(t, err)

	listed := map[string]marks{}
	for _, c := range chats {
		listed[c.ID] = marks{c.Routine, c.Channel}
	}

	assert.Equal(t, want, listed, "in a list")

	b, err = w.s.SetMainChat(ctx, DefaultBotID, web.ID)
	require.NoError(t, err)
	assert.Equal(t, web.ID, b.MainChat, "a web chat begun with \"Lark: \" is made the main chat")
}

// Scenario: a Bot with no main chat whose chats are all older than the first page of chats (issue #923). The page
// carries the chat the Bot opens, its latest that is not a Routine's run (here the owner's Lark direct chat, which
// the approved design does not leave out), so that the sidebar opens it rather than an empty chat; not its
// Routine's chat, newer though it is, nor its older chats.
func TestTheFirstPageCarriesTheChatABotWithNoMainChatOpens(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.db.ExecContext(ctx,
		`INSERT INTO bots (id, name, created_at, updated_at) VALUES ('bot_side', 'side', 1, 1)`)
	require.NoError(t, err)

	first, err := w.s.CreateChatWithID(ctx, "", "bot_side", "first", ChatModel{})
	require.NoError(t, err)

	w.clock.now = w.clock.now.Add(time.Minute)
	latest, err := w.s.CreateChatWithID(ctx, "", "bot_side", "the owner's Lark DM", ChatModel{})
	require.NoError(t, err)

	w.clock.now = w.clock.now.Add(time.Minute)
	routine, err := w.s.CreateChatWithID(ctx, "", "bot_side", "a Routine's run", ChatModel{})
	require.NoError(t, err)

	for _, stmt := range []string{
		`INSERT INTO routines (id, bot_id, name, kind, prompt, timeout_seconds, state, created_by, created_at,
			updated_at) VALUES ('rtn_a', 'bot_side', 'digest', 'prompt', 'sum up', 60, 'on', 'owner', 1, 1)`,
		`INSERT INTO routine_runs (id, routine_id, started_by, state, started_at, chat_id)
			VALUES ('rrn_a', 'rtn_a', 'schedule', 'running', 1, '` + routine.ID + `')`,
		`INSERT INTO channels (name, kind, app_id, secret, bot_id, mode, owner_id, created_at, updated_at)
			VALUES ('lark', 'lark', 'cli_a', 's', 'bot_side', 'owner', 'ou_owner', 1, 1)`,
		`INSERT INTO channel_chats (external_chat_id, channel, person_id, chat_id, sent_seq, created_at)
			VALUES ('oc_owner', 'lark', 'ou_owner', '` + latest.ID + `', 0, 1)`,
	} {
		_, err := w.db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	newer := make([]string, 0, 2)

	for range 2 {
		w.clock.now = w.clock.now.Add(time.Minute)

		c, err := w.s.CreateChatWithID(ctx, "", "", "newer", ChatModel{})
		require.NoError(t, err)

		newer = append(newer, c.ID)
	}

	page, err := w.s.ListChats(ctx, "", 2, 0)
	require.NoError(t, err)

	ids := []string{}
	for _, c := range page {
		ids = append(ids, c.ID)
	}

	assert.ElementsMatch(t, []string{newer[1], newer[0], latest.ID}, ids,
		"the newest two and the chat the side Bot opens, not %s nor %s", first.ID, routine.ID)
}
