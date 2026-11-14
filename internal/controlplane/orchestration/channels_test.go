package orchestration

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Scenario: a database from before Channels had a kind (migration 79), with
// the Lark Channel main, a person let in, her direct chat, a group the bot is
// in and its chat, a message taken and a question's card. After the
// migration every row is there under the tables' and columns' new names, the
// Channel is of the kind lark, the foreign keys name the new tables and hold,
// and nothing is named lark any more but the kind. An app id is still one
// Channel's whatever its kind: the unique app_id of migration 47 stays and is
// what refuses it, not the new (kind, app_id). The way back restores the Lark
// names with the rows.
func TestMigrationRenamesTheLarkTablesToChannels(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 79)
	require.NoError(t, err)

	for _, stmt := range []string{
		`INSERT INTO lark_channels (name, app_id, secret, bot, mode, owner_open_id, enabled, web_link, created_at,
			updated_at) VALUES ('main', 'cli_main', 'lark-channel-main', 'coordinator', 'allowlist', 'ou_owner', 1, 1,
			500, 500)`,
		`INSERT INTO lark_channel_people (channel, open_id, email, name, created_at)
			VALUES ('main', 'ou_linh', 'linh@example.com', 'Linh', 600)`,
		`INSERT INTO chats (id, title, bot, created_at, updated_at) VALUES ('cht_dm', 'Linh', 'coordinator', 700, 700)`,
		`INSERT INTO chats (id, title, bot, lark_group, created_at, updated_at)
			VALUES ('cht_group', 'Maps team', 'coordinator', 1, 700, 700)`,
		`INSERT INTO lark_channel_chats (lark_chat_id, channel, open_id, chat_id, sent_seq, created_at)
			VALUES ('oc_dm', 'main', 'ou_linh', 'cht_dm', 4, 700)`,
		`INSERT INTO lark_channel_chats (lark_chat_id, channel, open_id, chat_id, sent_seq, created_at)
			VALUES ('oc_group', 'main', '', 'cht_group', 2, 700)`,
		`INSERT INTO lark_channel_groups (channel, lark_chat_id, name, enabled, asked_at, created_at)
			VALUES ('main', 'oc_group', 'Maps team', 1, 800, 700)`,
		`INSERT INTO lark_channel_messages (message_id, channel, created_at) VALUES ('om_1', 'main', 700)`,
		`INSERT INTO lark_channel_questions (question_id, channel, card_id, message_id, seq, created_at)
			VALUES ('q_1', 'main', 'card_1', 'om_card_1', 3, 800)`,
	} {
		_, err = db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	// Every row, read by its new names.
	var kind, owner, person, email string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT kind, owner_id FROM channels WHERE name = 'main'`).
		Scan(&kind, &owner))
	assert.Equal(t, [2]string{"lark", "ou_owner"}, [2]string{kind, owner})
	require.NoError(t, db.QueryRowContext(ctx, `SELECT person_id, email FROM channel_people WHERE channel = 'main'`).
		Scan(&person, &email))
	assert.Equal(t, [2]string{"ou_linh", "linh@example.com"}, [2]string{person, email})

	type chatRow struct {
		Person, Chat string
		Sent, Group  int64
	}

	for external, want := range map[string]chatRow{
		"oc_dm":    {Person: "ou_linh", Chat: "cht_dm", Sent: 4},
		"oc_group": {Chat: "cht_group", Sent: 2, Group: 1},
	} {
		var r chatRow
		require.NoError(t, db.QueryRowContext(ctx, `SELECT cc.person_id, cc.chat_id, cc.sent_seq, c.channel_group
			FROM channel_chats cc JOIN chats c ON c.id = cc.chat_id WHERE cc.external_chat_id = ?`, external).
			Scan(&r.Person, &r.Chat, &r.Sent, &r.Group), external)
		assert.Equal(t, want, r, external)
	}

	var group, groupName string
	var asked sql.NullInt64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT external_chat_id, name, asked_at FROM channel_groups
		WHERE channel = 'main' AND enabled = 1`).Scan(&group, &groupName, &asked))
	assert.Equal(t, [2]string{"oc_group", "Maps team"}, [2]string{group, groupName})
	assert.Equal(t, int64(800), asked.Int64)

	var messages, cards int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM channel_messages WHERE message_id = 'om_1'`).
		Scan(&messages))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM channel_questions
		WHERE question_id = 'q_1' AND card_id = 'card_1' AND seq = 3`).Scan(&cards))
	assert.Equal(t, [2]int{1, 1}, [2]int{messages, cards})

	// The store reads them too.
	s := New(db, &fakeClock{now: t0}, func(prefix string) string { return prefix + "_new" })
	isGroup, err := s.Channels().IsChannelGroupChat(ctx, "cht_group")
	require.NoError(t, err)
	assert.True(t, isGroup, "a group's chat stays one")

	// The foreign keys name the new tables, and every row keeps to them.
	for _, table := range []string{"channel_people", "channel_chats", "channel_messages", "channel_questions",
		"channel_groups"} {
		var parent string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT "table" FROM pragma_foreign_key_list(?)`, table).
			Scan(&parent), table)
		assert.Equal(t, "channels", parent, table)
	}

	assert.Zero(t, foreignKeyBreaks(t, db), "no row breaks a foreign key")

	// No table or index is named lark; the old index is made again under the new name.
	var named sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT group_concat(name) FROM sqlite_schema
		WHERE name LIKE '%lark%' OR tbl_name LIKE '%lark%'`).Scan(&named))
	assert.False(t, named.Valid, "named lark: %s", named.String)

	var indexes int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'index'
		AND name IN ('channel_chats_channel', 'channels_kind_app')`).Scan(&indexes))
	assert.Equal(t, 2, indexes)

	// Another kind with the same app id is refused by the unique app_id that stays, not by (kind, app_id); one of
	// the kind with another app is kept.
	_, err = db.ExecContext(ctx, `INSERT INTO channels (name, kind, app_id, secret, bot, mode, owner_id, created_at,
		updated_at) VALUES ('zalo', 'zalo', 'cli_main', 's', 'coordinator', 'owner', 'z_owner', 900, 900)`)
	require.ErrorContains(t, err, "UNIQUE constraint failed: channels.app_id")
	assert.NotContains(t, err.Error(), "channels.kind")

	_, err = s.Channels().SetChannel(ctx, channelstore.Channel{
		Name: "maps", AppID: "cli_maps", Secret: "lark-channel-maps", Bot: DefaultBot, Mode: channelstore.ChannelOwner,
		OwnerID: "ou_owner_in_maps",
	})
	require.NoError(t, err)

	maps, err := s.Channels().Channel(ctx, "maps")
	require.NoError(t, err)
	assert.Equal(t, channelstore.KindLark, maps.Kind, "a Channel set up without a kind is Lark's")

	_, err = s.Channels().SetChannel(ctx, channelstore.Channel{
		Name: "zalo", Kind: "zalo", AppID: "zalo_1", Secret: "s", Bot: DefaultBot, Mode: channelstore.ChannelOwner,
		OwnerID: "ou_owner",
	})
	require.ErrorIs(t, err, ErrInvalid, "Lark is the one kind there is")

	// The way back: the Lark names, with the rows.
	_, err = p.DownTo(ctx, 79)
	require.NoError(t, err)

	var larkChat string
	var larkGroup int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT owner_open_id FROM lark_channels WHERE name = 'main'`).
		Scan(&owner))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT open_id FROM lark_channel_people`).Scan(&person))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lc.lark_chat_id, c.lark_group FROM lark_channel_chats lc
		JOIN chats c ON c.id = lc.chat_id WHERE lc.open_id = ''`).Scan(&larkChat, &larkGroup))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lark_chat_id FROM lark_channel_groups`).Scan(&group))
	assert.Equal(t, []any{"ou_owner", "ou_linh", "oc_group", int64(1), "oc_group"},
		[]any{owner, person, larkChat, larkGroup, group})

	assert.Zero(t, foreignKeyBreaks(t, db), "no row breaks a foreign key on the way back")
}

// foreignKeyBreaks counts the rows PRAGMA foreign_key_check reports.
func foreignKeyBreaks(t *testing.T, db *sql.DB) int {
	t.Helper()

	var n int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&n))

	return n
}
