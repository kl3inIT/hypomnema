package orchestration

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
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
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 46)
	require.NoError(t, err)

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

	channels, err := s.LarkChannels(ctx)
	require.NoError(t, err)
	require.Len(t, channels, 1)

	main := channels[0]
	//nolint:gosec // G101: the name of a vault secret, not a credential
	assert.Equal(t, LarkChannel{
		Name: "main", AppID: "cli_a1b2", Secret: "lark-bot-app", Bot: DefaultBot, Mode: LarkChannelAllowlist,
		OwnerOpenID: "ou_owner", Enabled: true, CreatedAt: main.CreatedAt, UpdatedAt: main.UpdatedAt,
	}, main)

	people, err := s.LarkChannelPeople(ctx, "main")
	require.NoError(t, err)
	require.Len(t, people, 1)
	assert.Equal(t, "linh@example.com", people[0].Email)

	chats, err := s.LarkChannelChats(ctx, "main")
	require.NoError(t, err)
	assert.Equal(t, []LarkChannelChat{{
		Channel: "main", LarkChatID: "oc_owner_dm", OpenID: "ou_owner", ChatID: "cht_1", Bot: DefaultBot, SentSeq: 4,
	}}, chats)

	first, err := s.TakeLarkMessage(ctx, "main", "om_1")
	require.NoError(t, err)
	assert.False(t, first, "a message taken before the migration is not taken again")

	card, err := s.LarkQuestionCardByMessage(ctx, "main", "om_card_1")
	require.NoError(t, err)
	assert.Equal(t, LarkQuestionCard{QuestionID: "q_1", Channel: "main", CardID: "card_1", MessageID: "om_card_1", Seq: 2},
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

	_, err := w.s.PutBot(ctx, Bot{Name: "vmap"})
	require.NoError(t, err)

	//nolint:gosec // G101: the name of a vault secret, not a credential
	main := LarkChannel{
		Name: "main", AppID: "cli_main", Secret: "lark-bot-app", Bot: DefaultBot, Mode: LarkChannelOwner,
		OwnerOpenID: "ou_owner",
	}
	maps := LarkChannel{
		Name: "maps", AppID: "cli_maps", Secret: "lark-channel-maps", Bot: "vmap", Mode: LarkChannelAllowlist,
		OwnerOpenID: "ou_owner_in_maps", Enabled: true,
	}

	for _, c := range []LarkChannel{main, maps} {
		_, err := w.s.SetLarkChannel(ctx, c)
		require.NoError(t, err)
	}

	for want, c := range map[string]LarkChannel{
		"lowercase":        {Name: "Maps 2", AppID: "cli_x", Secret: "s", Bot: DefaultBot, Mode: LarkChannelOwner, OwnerOpenID: "ou_o"},
		"no bot is named":  {Name: "third", AppID: "cli_x", Secret: "s", Bot: "nobody", Mode: LarkChannelOwner, OwnerOpenID: "ou_o"},
		"one app is one":   {Name: "third", AppID: "cli_maps", Secret: "s", Bot: DefaultBot, Mode: LarkChannelOwner, OwnerOpenID: "ou_o"},
		"the mode is":      {Name: "third", AppID: "cli_x", Secret: "s", Bot: DefaultBot, Mode: "anyone", OwnerOpenID: "ou_o"},
		"starts with ou_":  {Name: "third", AppID: "cli_x", Secret: "s", Bot: DefaultBot, Mode: LarkChannelOwner, OwnerOpenID: "linh"},
		"names the bot it": {Name: "third", AppID: "cli_x", Secret: "s", Mode: LarkChannelOwner, OwnerOpenID: "ou_o"},
	} {
		_, err := w.s.SetLarkChannel(ctx, c)
		require.ErrorContains(t, err, want)
	}

	_, err = w.s.SetLarkChannel(ctx, LarkChannel{
		Name: "third", AppID: "cli_maps", Secret: "s", Bot: DefaultBot, Mode: LarkChannelOwner, OwnerOpenID: "ou_o",
	})
	require.ErrorIs(t, err, ErrConflict, "an app that is another Channel's")

	// The same person is another open_id in each app, so on each Channel apart.
	_, err = w.s.AddLarkChannelPerson(ctx, "maps", LarkChannelPerson{OpenID: "ou_linh_in_maps", Name: "Linh"})
	require.NoError(t, err)
	_, err = w.s.LarkChannelPerson(ctx, "main", "ou_linh_in_maps")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = w.s.AddLarkChannelPerson(ctx, "nowhere", LarkChannelPerson{OpenID: "ou_linh"})
	require.ErrorIs(t, err, ErrNotFound)

	chat, err := w.s.LarkChannelChatFor(ctx, "maps", "oc_dm_in_maps", "ou_owner_in_maps", "")
	require.NoError(t, err)
	assert.Equal(t, "vmap", chat.Bot, "a chat opened through a Channel is with the Channel's Bot")

	fromMaps, err := w.s.CreateRun(ctx, NewRun{Objective: "add the depot", CreatedBy: "vmap", ChatID: chat.ChatID})
	require.NoError(t, err)

	via, err := w.s.LarkChannelForRun(ctx, fromMaps.ID)
	require.NoError(t, err)
	assert.Equal(t, "maps", via, "the Channel its chat came in through")

	via, err = w.s.LarkChannelForRun(ctx, w.run.ID)
	require.NoError(t, err)
	assert.Equal(t, "maps", via, "main is off, so the oldest Channel that is on")

	main.Enabled = true
	_, err = w.s.SetLarkChannel(ctx, main)
	require.NoError(t, err)

	via, err = w.s.LarkChannelForRun(ctx, w.run.ID)
	require.NoError(t, err)
	assert.Equal(t, "main", via, "a Run started elsewhere asks through the first Channel")

	err = w.s.DeleteBot(ctx, "vmap")
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorContains(t, err, "Lark channels talk to bot vmap")

	require.NoError(t, w.s.RemoveLarkChannel(ctx, "maps"))
	require.ErrorIs(t, w.s.RemoveLarkChannel(ctx, "maps"), ErrNotFound)

	people, err := w.s.LarkChannelPeople(ctx, "maps")
	require.NoError(t, err)
	assert.Empty(t, people, "its people go with it")

	kept, err := w.s.GetChat(ctx, chat.ChatID)
	require.NoError(t, err)
	assert.Equal(t, "vmap", kept.Bot, "the chat stays")

	via, err = w.s.LarkChannelForRun(ctx, fromMaps.ID)
	require.NoError(t, err)
	assert.Equal(t, "main", via, "its Channel is gone, so the first one")

	require.ErrorIs(t, w.s.DeleteBot(ctx, "vmap"), ErrConflict, "it still has the chat")
}
