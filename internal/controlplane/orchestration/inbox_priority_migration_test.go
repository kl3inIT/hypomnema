package orchestration

import (
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// inboxPriorityVersions are the version of the migration of issue #1157 and of the one before it, found by its name:
// it is named by its time until it is numbered right before its merge.
func inboxPriorityVersions(t *testing.T) (before, own int64) {
	t.Helper()

	entries, err := fs.ReadDir(migrations, "migrations")
	require.NoError(t, err)

	for _, e := range entries {
		version, err := strconv.ParseInt(strings.SplitN(e.Name(), "_", 2)[0], 10, 64)
		require.NoError(t, err)

		if strings.HasSuffix(e.Name(), "_inbox_priority_and_projects.sql") {
			return before, version
		}

		before = version
	}

	require.FailNow(t, "no inbox_priority_and_projects migration")

	return 0, 0
}

// Scenario: issue #1157. The migration gives every item there no priority, no reason and no project; the checks keep
// a priority to Linear's 0-4, a color to #rrggbb and a project's name to one in any case; the way back keeps every
// item and drops the projects.
func TestMigrationAddsPriorityAndProjects(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	before, own := inboxPriorityVersions(t)
	db, p := databaseAt(t, before)

	_, err := db.ExecContext(ctx, `INSERT INTO todos (id, title, state, source_kind, created_by_kind, created_at,
		updated_at) VALUES ('tdo_a', 'a', 'open', 'owner', 'owner', 1, 1)`)
	require.NoError(t, err)

	_, err = p.UpTo(ctx, own)
	require.NoError(t, err)

	var (
		priority int
		reason   string
		project  *string
	)

	require.NoError(t, db.QueryRowContext(ctx, `SELECT priority, priority_reason, project_id FROM todos
		WHERE id = 'tdo_a'`).Scan(&priority, &reason, &project))
	assert.Zero(t, priority)
	assert.Empty(t, reason)
	assert.Nil(t, project)

	for _, stmt := range []string{
		`INSERT INTO inbox_projects (id, name, color, created_at, updated_at) VALUES ('prj_a', 'VMAP', '#2f80ed', 1, 1)`,
		`UPDATE todos SET priority = 1, priority_reason = 'today', project_id = 'prj_a' WHERE id = 'tdo_a'`,
	} {
		_, err = db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	for name, stmt := range map[string]string{
		"a priority past low": `UPDATE todos SET priority = 5 WHERE id = 'tdo_a'`,
		"a color by name": `INSERT INTO inbox_projects (id, name, color, created_at, updated_at)
			VALUES ('prj_b', 'BP', 'green', 1, 1)`,
		"a name in another case": `INSERT INTO inbox_projects (id, name, color, created_at, updated_at)
			VALUES ('prj_c', 'vmap', '#000000', 1, 1)`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.Error(t, err, name)
	}

	_, err = p.DownTo(ctx, before)
	require.NoError(t, err)

	var title string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT title FROM todos WHERE id = 'tdo_a'`).Scan(&title))
	assert.Equal(t, "a", title, "the item stays")

	_, err = db.ExecContext(ctx, `SELECT priority FROM todos`)
	require.Error(t, err, "its priority is gone")

	_, err = db.ExecContext(ctx, `SELECT id FROM inbox_projects`)
	require.Error(t, err, "the projects are gone")
}
