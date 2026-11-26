package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// Scenario: a control plane with two Bots, each with a chat, a Run, a Routine, a Lark Channel, an artifact and a
// setup change, takes the migration that gives Bots ids (issue #466). The Bot that was always there gets the main
// Bot's fixed id and the other a new one; each keeps its name; every record names the Bot's id; the foreign keys
// of Routines and Channels point at the id and no row breaks one; and a removed Bot's routines go with nothing.
// Taken back, everything names the Bot by its name again.
func TestMigrationGivesBotsIDs(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, p := databaseAt(t, 84)
	var err error

	for _, stmt := range []string{
		`INSERT INTO bots (name, created_at, updated_at) VALUES ('vmap', 100, 100)`,
		`INSERT INTO chats (id, title, bot, created_at, updated_at) VALUES ('cht_main', 'main', 'coordinator', 200, 200)`,
		`INSERT INTO chats (id, title, bot, created_at, updated_at) VALUES ('cht_vmap', 'maps', 'vmap', 200, 200)`,
		`INSERT INTO runs (id, objective, status, created_by, chat_id, bot, created_at, updated_at)
			VALUES ('run_vmap', 'tiles', 'open', 'vmap', 'cht_vmap', 'vmap', 300, 300)`,
		`INSERT INTO routines (id, bot, name, kind, script, timeout_seconds, state, created_by, created_at, updated_at)
			VALUES ('rtn_vmap', 'vmap', 'sync', 'script', 'sync.sh', 60, 'on', 'owner', 300, 300)`,
		`INSERT INTO routine_runs (id, routine_id, started_by, state, started_at)
			VALUES ('rrn_vmap', 'rtn_vmap', 'manual', 'succeeded', 400)`,
		`INSERT INTO channels (name, kind, app_id, secret, bot, mode, owner_id, created_at, updated_at)
			VALUES ('maps', 'lark', 'cli_maps', 's', 'vmap', 'owner', 'ou_owner', 300, 300)`,
		`INSERT INTO artifacts (id, chat_id, bot, message_seq, revision, created_at, updated_at)
			VALUES ('art_vmap', 'cht_vmap', 'vmap', 1, 1, 300, 300)`,
		`INSERT INTO setup_changes (id, chat_id, kind, name, action, after, created_at)
			VALUES ('chg_bot', 'cht_main', 'bot', 'vmap', 'made', 'rev', 300)`,
		`INSERT INTO setup_changes (id, chat_id, kind, name, action, after, created_at)
			VALUES ('chg_profile', 'cht_main', 'profile', 'vmap', 'made', 'pfv', 300)`,
	} {
		_, err = db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	var vmapID string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM bots WHERE name = 'vmap'`).Scan(&vmapID))
	assert.True(t, platform.IsID("bot", vmapID), "an id of NewID's shape: %s", vmapID)

	var mainName string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT name FROM bots WHERE id = ?`, DefaultBotID).Scan(&mainName))
	assert.Equal(t, "coordinator", mainName, "the Bot that was always there is the main one, by the same name")
	assert.True(t, platform.IsID("bot", DefaultBotID))

	for query, want := range map[string]string{
		`SELECT bot_id FROM chats WHERE id = 'cht_main'`:            DefaultBotID,
		`SELECT bot_id FROM chats WHERE id = 'cht_vmap'`:            vmapID,
		`SELECT bot_id FROM runs WHERE id = 'run_vmap'`:             vmapID,
		`SELECT bot_id FROM routines WHERE id = 'rtn_vmap'`:         vmapID,
		`SELECT bot_id FROM channels WHERE name = 'maps'`:           vmapID,
		`SELECT bot_id FROM artifacts WHERE id = 'art_vmap'`:        vmapID,
		`SELECT name FROM setup_changes WHERE id = 'chg_bot'`:       vmapID,
		`SELECT name FROM setup_changes WHERE id = 'chg_profile'`:   "vmap",
		`SELECT routine_id FROM routine_runs WHERE id = 'rrn_vmap'`: "rtn_vmap",
	} {
		var got string
		require.NoError(t, db.QueryRowContext(ctx, query).Scan(&got), query)
		assert.Equal(t, want, got, query)
	}

	for _, table := range []string{"routines", "channels"} {
		var parent, to string
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT "table", "to" FROM pragma_foreign_key_list(?) WHERE "table" = 'bots'`, table).Scan(&parent, &to))
		assert.Equal(t, "id", to, table)
	}

	assert.Zero(t, foreignKeyBreaks(t, db), "no row breaks a foreign key")

	_, err = db.ExecContext(ctx, `INSERT INTO bots (id, name, created_at, updated_at) VALUES ('bot_x', 'vmap', 1, 1)`)
	require.ErrorContains(t, err, "UNIQUE constraint failed: bots.name", "a name is one Bot's")

	_, err = p.DownTo(ctx, 84)
	require.NoError(t, err)

	for query, want := range map[string]string{
		`SELECT bot FROM chats WHERE id = 'cht_main'`:         "coordinator",
		`SELECT bot FROM routines WHERE id = 'rtn_vmap'`:      "vmap",
		`SELECT bot FROM channels WHERE name = 'maps'`:        "vmap",
		`SELECT name FROM setup_changes WHERE id = 'chg_bot'`: "vmap",
	} {
		var got string
		require.NoError(t, db.QueryRowContext(ctx, query).Scan(&got), query)
		assert.Equal(t, want, got, "taken back: "+query)
	}

	assert.Zero(t, foreignKeyBreaks(t, db), "no row breaks a foreign key taken back")
}
