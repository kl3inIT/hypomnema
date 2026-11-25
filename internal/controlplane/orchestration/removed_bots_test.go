package orchestration

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Scenario: issue #1139. The owner removes the Thư ký Bot, the main Bot makes a Bot and the owner undoes it, and
// the main Bot removes another: each keeps its name with its id, so what they did is still named. The removal
// the owner undoes puts the Bot back, and it is no longer among the removed.
func TestARemovedBotKeepsItsName(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	secretary, err := w.s.PutBot(ctx, Bot{Name: "Thư ký"})
	require.NoError(t, err)
	require.NoError(t, w.s.DeleteBot(ctx, secretary.ID))

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	made, err := w.s.PutBotFromChat(ctx, chat.ID, Bot{Name: "Maps"})
	require.NoError(t, err)

	changes, err := w.s.ListSetupChanges(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	_, err = w.s.UndoSetupChange(ctx, changes[0].ID)
	require.NoError(t, err)

	vmap, err := w.s.PutBot(ctx, Bot{Name: "VMAP"})
	require.NoError(t, err)
	require.NoError(t, w.s.DeleteBotFromChat(ctx, chat.ID, vmap.ID))

	removed, err := w.s.ListRemovedBots(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []RemovedBot{
		{ID: secretary.ID, Name: "Thư ký"}, {ID: made.ID, Name: "Maps"}, {ID: vmap.ID, Name: "VMAP"},
	}, removed)

	changes, err = w.s.ListSetupChanges(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, changes, 2)

	for _, c := range changes {
		if c.Action == actionRemoved {
			_, err = w.s.UndoSetupChange(ctx, c.ID)
			require.NoError(t, err)
		}
	}

	removed, err = w.s.ListRemovedBots(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []RemovedBot{{ID: secretary.ID, Name: "Thư ký"}, {ID: made.ID, Name: "Maps"}},
		removed, "a Bot put back is not removed")
}

// Scenario: issue #1139 on zm's database: the Thư ký Bot was removed on 2026-11-23, before a removed Bot's name
// was kept, and its items still name its id. The migration gives it its name; a database where nothing names it
// gets none.
func TestTheMigrationNamesTheRemovedThuKyBot(t *testing.T) {
	t.Parallel()

	const gone = "bot_6lwjzodlaqoat2ktg47yztjfae"

	for _, named := range []bool{true, false} {
		ctx := t.Context()
		db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })

		p, err := provider(db)
		require.NoError(t, err)
		_, err = p.UpTo(ctx, migrationVersion(t, p, "_removed_bot_names.sql")-1)
		require.NoError(t, err)

		if named {
			_, err = db.ExecContext(ctx, `INSERT INTO todos (id, state, created_by_kind, created_by, assignee_kind,
				assignee, title, source_kind, closed_at, created_at, updated_at)
				VALUES ('tdo_closed', 'done', 'bot', ?1, 'me', '', 'Reply to Đức Anh', 'owner', 1, 1, 1)`, gone)
			require.NoError(t, err)
		}

		require.NoError(t, Migrate(ctx, db))
		require.NoError(t, CheckSchema(ctx, db))

		s := New(db, &fakeClock{now: t0}, func(prefix string) string { return prefix + "_new" })
		removed, err := s.ListRemovedBots(ctx)
		require.NoError(t, err)

		if named {
			assert.Equal(t, []RemovedBot{{ID: gone, Name: "Thư ký"}}, removed)
		} else {
			assert.Empty(t, removed)
		}
	}
}
