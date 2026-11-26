package orchestration

import (
	"database/sql"
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// inboxProjectDepartmentVersions are the versions of the migration before issue #1188's and of its own, found by its
// name: it is named by its time until it is numbered right before its merge.
func inboxProjectDepartmentVersions(t *testing.T) (before, own int64) {
	t.Helper()

	entries, err := fs.ReadDir(migrations, "migrations")
	require.NoError(t, err)

	for _, e := range entries {
		version, err := strconv.ParseInt(strings.SplitN(e.Name(), "_", 2)[0], 10, 64)
		require.NoError(t, err)

		if strings.HasSuffix(e.Name(), "_inbox_project_is_department.sql") {
			return before, version
		}

		before = version
	}

	require.FailNow(t, "no inbox_project_is_department migration")

	return 0, 0
}

// Scenario: issue #1188. The migration drops the Inbox's own projects and makes an item's project a department: the
// items stay, each without a project, with every column in its order; a project is then a department that is there,
// and a department removed leaves its items without one. The way back has the empty list of projects again, and the
// migrations before it can still go back too.
func TestMigrationMakesAnItemsProjectADepartment(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	before, own := inboxProjectDepartmentVersions(t)
	db, p := databaseAt(t, before)

	columns := todoColumns(t, db)

	for _, stmt := range []string{
		`INSERT INTO inbox_projects (id, name, color, created_at, updated_at) VALUES ('prj_a', 'VMAP', '#2f80ed', 1, 1)`,
		`INSERT INTO todos (id, title, state, source_kind, created_by_kind, project_id, created_at, updated_at)
		 VALUES ('tdo_a', 'Call the bank', 'open', 'owner', 'owner', 'prj_a', 1, 1)`,
		`INSERT INTO todo_events (todo_id, at, revision, actor_kind, kind) VALUES ('tdo_a', 1, 1, 'owner', 'created')`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	_, err := p.UpTo(ctx, own)
	require.NoError(t, err)

	assert.Equal(t, columns, todoColumns(t, db), "every column, in its order")

	var (
		project sql.NullString
		events  int
	)

	require.NoError(t, db.QueryRowContext(ctx, `SELECT project_id FROM todos WHERE id = 'tdo_a'`).Scan(&project))
	assert.False(t, project.Valid, "a project of the list is no department")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM todo_events`).Scan(&events))
	assert.Equal(t, 1, events, "the events are kept")

	_, err = db.ExecContext(ctx, `SELECT id FROM inbox_projects`)
	require.Error(t, err, "the list of projects is gone")

	for _, stmt := range []string{
		`INSERT INTO departments (id, name, position, created_at) VALUES ('dep_vmap', 'VMAP', 0, 1)`,
		`UPDATE todos SET project_id = 'dep_vmap' WHERE id = 'tdo_a'`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	_, err = db.ExecContext(ctx, `UPDATE todos SET project_id = 'dep_gone' WHERE id = 'tdo_a'`)
	require.Error(t, err, "a project that is no department")

	_, err = db.ExecContext(ctx, `DELETE FROM departments WHERE id = 'dep_vmap'`)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT project_id FROM todos WHERE id = 'tdo_a'`).Scan(&project))
	assert.False(t, project.Valid, "a department removed leaves its items without one")

	_, err = db.ExecContext(ctx, `INSERT INTO departments (id, name, position, created_at) VALUES ('dep_bp', 'BP', 0, 1);
		UPDATE todos SET project_id = 'dep_bp' WHERE id = 'tdo_a'`)
	require.NoError(t, err)

	_, err = p.DownTo(ctx, before)
	require.NoError(t, err)

	assert.Equal(t, columns, todoColumns(t, db), "every column, in its order")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT project_id FROM todos WHERE id = 'tdo_a'`).Scan(&project))
	assert.False(t, project.Valid, "a department is none of the list's projects")

	_, err = db.ExecContext(ctx, `INSERT INTO inbox_projects (id, name, color, created_at, updated_at)
		VALUES ('prj_b', 'VMAP', '#2f80ed', 1, 1)`)
	require.NoError(t, err, "the list is there again")

	_, err = p.DownTo(ctx, before-1)
	require.NoError(t, err, "the migration before goes back too")
}
