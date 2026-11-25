package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
)

// Scenario: a database from when there was one Lark bot, with Linh let in,
// the owner's chat and a question's card. After the migration the bot is the
// Channel "main": the same app, the same vault secret, so it connects without
// the secret being given again; it talks to the first Bot; and its people,
// its chat, the messages it took and the card are the Channel's. The way back
// restores the one bot.
func TestMigrationTurnsTheLarkBotIntoTheChannelMain(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, p := databaseAt(t, 46)
	var err error

	for _, stmt := range []string{
		`INSERT INTO lark_bot (id, app_id, secret, mode, owner_open_id, enabled, updated_at)
			VALUES (1, 'cli_a1b2', 'lark-bot-app', 'allowlist', 'ou_owner', 1, 500)`,
		`INSERT INTO lark_bot_people (open_id, email, name, created_at) VALUES ('ou_linh', 'linh@example.com', 'Linh', 600)`,
		`INSERT INTO chats (id, title, created_at, updated_at) VALUES ('cht_1', 'Fix the login page', 700, 700)`,
		`INSERT INTO lark_bot_chats (lark_chat_id, open_id, chat_id, sent_seq, created_at)
			VALUES ('oc_owner_dm', 'ou_owner', 'cht_1', 4, 700)`,
		`INSERT INTO lark_bot_messages (message_id, created_at) VALUES ('om_1', 700)`,
		`INSERT INTO lark_bot_questions (question_id, card_id, message_id, seq, created_at)
			VALUES ('q_1', 'card_1', 'om_card_1', 2, 800)`,
	} {
		_, err = db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	s := New(db, &fakeClock{now: t0}, func(prefix string) string { return prefix + "_new" })

	channels, err := s.Channels().ListChannels(ctx)
	require.NoError(t, err)
	require.Len(t, channels, 1)

	main := channels[0]
	//nolint:gosec // G101: the name of a vault secret, not a credential
	assert.Equal(t, channelstore.Channel{
		Name: "main", Kind: channelstore.KindLark, AppID: "cli_a1b2", Secret: "lark-bot-app", Bot: DefaultBotID,
		Mode: channelstore.ChannelAllowlist, OwnerID: "ou_owner", Enabled: true, CreatedAt: main.CreatedAt,
		UpdatedAt: main.UpdatedAt,
		// A Channel that existed keeps the link under its replies (migration 62).
		WebLink: true,
	}, main)

	people, err := s.Channels().ChannelPeople(ctx, "main")
	require.NoError(t, err)
	require.Len(t, people, 1)
	assert.Equal(t, "linh@example.com", people[0].Email)

	chats, err := s.Channels().ChannelChats(ctx, "main")
	require.NoError(t, err)
	assert.Equal(t, []channelstore.ChannelChat{{
		Channel: "main", ExternalChatID: "oc_owner_dm", PersonID: "ou_owner", ChatID: "cht_1", Bot: DefaultBotID, SentSeq: 4,
	}}, chats)

	first, err := s.Channels().TakeChannelMessage(ctx, "main", "om_1")
	require.NoError(t, err)
	assert.False(t, first, "a message taken before the migration is not taken again")

	card, err := s.Channels().QuestionCardByMessage(ctx, "main", "om_card_1")
	require.NoError(t, err)
	assert.Equal(t, channelstore.QuestionCard{
		QuestionID: "q_1", Channel: "main", CardID: "card_1", MessageID: "om_card_1", Seq: 2,
	},
		card)

	// The way back: the Channel main is the one bot again.
	_, err = p.DownTo(ctx, 46)
	require.NoError(t, err)

	var app, secret, linh, chat string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT app_id, secret FROM lark_bot WHERE id = 1`).Scan(&app, &secret))
	assert.Equal(t, [2]string{"cli_a1b2", "lark-bot-app"}, [2]string{app, secret})
	require.NoError(t, db.QueryRowContext(ctx, `SELECT open_id FROM lark_bot_people`).Scan(&linh))
	assert.Equal(t, "ou_linh", linh)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT chat_id FROM lark_bot_chats`).Scan(&chat))
	assert.Equal(t, "cht_1", chat)
}

// Scenario: the owner keeps two Channels in the store, one for each of two
// Bots. A Channel names a Bot that exists and an app no other Channel has.
// A Bot a Channel talks to is not removed until the Channel talks to another
// Bot. A question of a Run goes out through the Channel whose Lark chat
// started the Run, else through the oldest Channel that is on. Removing a
// Channel takes its people and its Lark chats with it; the chat stays.
func TestChannelsAreKeptEachWithItsBot(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	vmapBot, err := w.s.PutBot(ctx, Bot{Name: "vmap"})
	require.NoError(t, err)

	//nolint:gosec // G101: the name of a vault secret, not a credential
	main := channelstore.Channel{
		Name: "main", AppID: "cli_main", Secret: "lark-bot-app", Bot: DefaultBotID, Mode: channelstore.ChannelOwner,
		OwnerID: "ou_owner",
	}
	maps := channelstore.Channel{
		Name: "maps", AppID: "cli_maps", Secret: "lark-channel-maps", Bot: vmapBot.ID, Mode: channelstore.ChannelAllowlist,
		OwnerID: "ou_owner_in_maps", Enabled: true,
	}

	for _, c := range []channelstore.Channel{main, maps} {
		_, err := w.s.Channels().SetChannel(ctx, c)
		require.NoError(t, err)
	}

	for want, c := range map[string]channelstore.Channel{
		"lowercase":         {Name: "Maps 2", AppID: "cli_x", Secret: "s", Bot: DefaultBotID, Mode: channelstore.ChannelOwner, OwnerID: "ou_o"},
		"no bot has the id": {Name: "third", AppID: "cli_x", Secret: "s", Bot: "nobody", Mode: channelstore.ChannelOwner, OwnerID: "ou_o"},
		"one app is one":    {Name: "third", AppID: "cli_maps", Secret: "s", Bot: DefaultBotID, Mode: channelstore.ChannelOwner, OwnerID: "ou_o"},
		"the mode is":       {Name: "third", AppID: "cli_x", Secret: "s", Bot: DefaultBotID, Mode: "anyone", OwnerID: "ou_o"},
		"starts with ou_":   {Name: "third", AppID: "cli_x", Secret: "s", Bot: DefaultBotID, Mode: channelstore.ChannelOwner, OwnerID: "linh"},
		"names the bot it":  {Name: "third", AppID: "cli_x", Secret: "s", Mode: channelstore.ChannelOwner, OwnerID: "ou_o"},
	} {
		_, err := w.s.Channels().SetChannel(ctx, c)
		require.ErrorContains(t, err, want)
	}

	_, err = w.s.Channels().SetChannel(ctx, channelstore.Channel{
		Name: "third", AppID: "cli_maps", Secret: "s", Bot: DefaultBotID, Mode: channelstore.ChannelOwner, OwnerID: "ou_o",
	})
	require.ErrorIs(t, err, ErrConflict, "an app that is another Channel's")

	// The same person is another open_id in each app, so on each Channel apart.
	_, err = w.s.Channels().AddChannelPerson(ctx, "maps",
		channelstore.ChannelPerson{PersonID: "ou_linh_in_maps", Name: "Linh"})
	require.NoError(t, err)
	_, err = w.s.Channels().ChannelPerson(ctx, "main", "ou_linh_in_maps")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = w.s.Channels().AddChannelPerson(ctx, "nowhere", channelstore.ChannelPerson{PersonID: "ou_linh"})
	require.ErrorIs(t, err, ErrNotFound)

	chat, err := w.s.ChannelChatFor(ctx, "maps", "oc_dm_in_maps", "ou_owner_in_maps", "")
	require.NoError(t, err)
	assert.Equal(t, vmapBot.ID, chat.Bot, "a chat opened through a Channel is with the Channel's Bot")

	fromMaps, err := w.s.CreateRun(ctx, NewRun{Objective: "add the depot", CreatedBy: vmapBot.ID, ChatID: chat.ChatID})
	require.NoError(t, err)

	via, err := w.s.Channels().ChannelForRun(ctx, fromMaps.ID)
	require.NoError(t, err)
	assert.Equal(t, "maps", via, "the Channel its chat came in through")

	via, err = w.s.Channels().ChannelForRun(ctx, w.run.ID)
	require.NoError(t, err)
	assert.Equal(t, "maps", via, "main is off, so the oldest Channel that is on")

	main.Enabled = true
	_, err = w.s.Channels().SetChannel(ctx, main)
	require.NoError(t, err)

	via, err = w.s.Channels().ChannelForRun(ctx, w.run.ID)
	require.NoError(t, err)
	assert.Equal(t, "main", via, "a Run started elsewhere asks through the first Channel")

	err = w.s.DeleteBot(ctx, vmapBot.ID)
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorContains(t, err, "Lark channels talk to bot vmap")

	require.NoError(t, w.s.Channels().RemoveChannel(ctx, "maps"))
	require.ErrorIs(t, w.s.Channels().RemoveChannel(ctx, "maps"), ErrNotFound)

	people, err := w.s.Channels().ChannelPeople(ctx, "maps")
	require.NoError(t, err)
	assert.Empty(t, people, "its people go with it")

	kept, err := w.s.GetChat(ctx, chat.ChatID)
	require.NoError(t, err)
	assert.Equal(t, vmapBot.ID, kept.Bot, "the chat stays")

	via, err = w.s.Channels().ChannelForRun(ctx, fromMaps.ID)
	require.NoError(t, err)
	assert.Equal(t, "main", via, "its Channel is gone, so the first one")

	require.ErrorIs(t, w.s.DeleteBot(ctx, vmapBot.ID), ErrConflict, "it still has the chat")
}
