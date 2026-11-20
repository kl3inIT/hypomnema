package orchestration

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Scenario: the owner sorts his Bots into departments (issue #471), as OpenMausBot's sidebar does. A department is
// made with a Bot in it, goes last, is renamed and reordered; a name another department has, a blank one or one
// too long is refused; the main Bot stays in none; a Bot moves between departments and out of every one; a change
// of a Bot's settings keeps its department; and a removed department leaves no gap and sends its Bots back.
func TestTheOwnerSortsHisBotsIntoDepartments(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	vmap, err := w.s.PutBot(ctx, Bot{Name: "vmap"})
	require.NoError(t, err)
	staff, err := w.s.PutBot(ctx, Bot{Name: "staff"})
	require.NoError(t, err)

	maps, err := w.s.CreateDepartment(ctx, "  VMAP  ", []string{vmap.ID, staff.ID})
	require.NoError(t, err)
	assert.Equal(t, Department{ID: maps.ID, Name: "VMAP", Position: 0}, maps, "its name is trimmed, first goes 0")

	got, err := w.s.GetBot(ctx, staff.ID)
	require.NoError(t, err)
	assert.Equal(t, maps.ID, got.Department, "made with the Bots named in it")

	pilot, err := w.s.CreateDepartment(ctx, "BeyondPilot", nil)
	require.NoError(t, err)
	assert.Equal(t, 1, pilot.Position, "a new department goes last")

	for name, bad := range map[string]string{"blank": "  ", "too long": strings.Repeat("x", 61), "control": "a\tb"} {
		_, err := w.s.CreateDepartment(ctx, bad, nil)
		require.ErrorIs(t, err, ErrInvalid, name)
	}

	_, err = w.s.CreateDepartment(ctx, "VMAP", nil)
	require.ErrorIs(t, err, ErrConflict, "a name is one department's")
	_, err = w.s.RenameDepartment(ctx, pilot.ID, "VMAP")
	require.ErrorIs(t, err, ErrConflict)

	renamed, err := w.s.RenameDepartment(ctx, maps.ID, "Maps")
	require.NoError(t, err)
	assert.Equal(t, "Maps", renamed.Name)

	order, err := w.s.ReorderDepartments(ctx, []string{pilot.ID, maps.ID})
	require.NoError(t, err)
	assert.Equal(t, []string{pilot.ID, maps.ID}, order)

	_, err = w.s.SetBotDepartment(ctx, DefaultBotID, maps.ID)
	require.ErrorIs(t, err, ErrInvalid, "the main Bot is always first, in no department")
	_, err = w.s.SetBotDepartment(ctx, vmap.ID, "dep_nobodyaaaaaaaaaaaaaaaaaaaa")
	require.ErrorIs(t, err, ErrNotFound)

	moved, err := w.s.SetBotDepartment(ctx, vmap.ID, pilot.ID)
	require.NoError(t, err)
	assert.Equal(t, pilot.ID, moved.Department)

	changed, err := w.s.PutBot(ctx, Bot{ID: vmap.ID, Persona: "You keep the maps."})
	require.NoError(t, err)
	assert.Equal(t, pilot.ID, changed.Department, "a change of its settings keeps its department")

	out, err := w.s.SetBotDepartment(ctx, vmap.ID, "")
	require.NoError(t, err)
	assert.Empty(t, out.Department, "out of every department")

	require.NoError(t, w.s.DeleteDepartment(ctx, pilot.ID))
	require.ErrorIs(t, w.s.DeleteDepartment(ctx, pilot.ID), ErrNotFound)

	left, err := w.s.ListDepartments(ctx)
	require.NoError(t, err)
	assert.Equal(t, []Department{{ID: maps.ID, Name: "Maps", Position: 0}}, left, "the ones after move up")

	require.NoError(t, w.s.DeleteDepartment(ctx, maps.ID))

	got, err = w.s.GetBot(ctx, staff.ID)
	require.NoError(t, err)
	assert.Empty(t, got.Department, "a removed department's Bots go back to the list")
}

// Scenario: a control plane whose owner pinned a Bot (issue #470) takes the migration that brings departments. The
// pinned Bot goes into a department named Pinned, so it stays where it was in the sidebar; one not pinned is in
// none; a control plane with no pinned Bot gets no department. Taken back, the Bot is pinned again.
func TestMigrationTurnsPinsIntoADepartment(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 148)
	require.NoError(t, err)

	for _, stmt := range []string{
		`INSERT INTO bots (id, name, position, pinned, created_at, updated_at)
			VALUES ('bot_vmapaaaaaaaaaaaaaaaaaaaaaa', 'vmap', 1, 1, 100, 100)`,
		`INSERT INTO bots (id, name, position, pinned, created_at, updated_at)
			VALUES ('bot_pilotaaaaaaaaaaaaaaaaaaaaa', 'pilot', 2, 0, 100, 100)`,
	} {
		_, err = db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	var name string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT d.name FROM bots b JOIN departments d ON d.id = b.department WHERE b.name = 'vmap'`).Scan(&name))
	assert.Equal(t, "Pinned", name)

	var none int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM bots WHERE department IS NULL`).Scan(&none))
	assert.Equal(t, 2, none, "the main Bot and the Bot not pinned are in none")

	_, err = db.ExecContext(ctx, `DELETE FROM departments`)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM bots WHERE department IS NULL`).Scan(&none))
	assert.Equal(t, 3, none, "a removed department's Bots go back to none")

	_, err = db.ExecContext(ctx,
		`UPDATE bots SET department = NULL; INSERT INTO departments (id, name, position, created_at)
			VALUES ('dep_pinnedaaaaaaaaaaaaaaaaaaaa', 'Pinned', 0, 1);
		 UPDATE bots SET department = 'dep_pinnedaaaaaaaaaaaaaaaaaaaa' WHERE name = 'vmap'`)
	require.NoError(t, err)

	_, err = p.DownTo(ctx, 148)
	require.NoError(t, err)

	var pinned int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT pinned FROM bots WHERE name = 'vmap'`).Scan(&pinned))
	assert.Equal(t, 1, pinned, "taken back, it is pinned again")
}
