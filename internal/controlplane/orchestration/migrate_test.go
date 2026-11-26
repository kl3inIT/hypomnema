package orchestration

import (
	"context"
	"database/sql"
	"io/fs"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// migrationVersion is the version of the one migration whose file name ends with suffix ("_inbox.sql"). A test
// that migrates to just before or after a migration finds it by name, so the migration keeps its test whether it is
// still named by its time on a branch or numbered by `make migration-fix` (issue #954).
func migrationVersion(t *testing.T, suffix string) int64 {
	t.Helper()

	entries, err := fs.ReadDir(migrations, "migrations")
	require.NoError(t, err)

	var found []int64

	for _, e := range entries {
		if strings.HasSuffix(e.Name(), suffix) {
			version, err := goose.NumericComponent(e.Name())
			require.NoError(t, err)

			found = append(found, version)
		}
	}

	require.Len(t, found, 1, "one migration ends with %s", suffix)

	return found[0]
}

// chain makes the databases the migration tests start from, each at a version before the migration it tests, so
// that the migrations before it run about once in the package's tests and not once in each (issue #1152).
var chain = sqlitetest.NewChain(func(ctx context.Context, db *sql.DB, version int64) error {
	p, err := provider(db)
	if err != nil {
		return err
	}

	_, err = p.UpTo(ctx, version)

	return err
})

// databaseAt opens a database of the test's own, migrated up to version as goose's UpTo leaves one, and gives the
// migrations to take it on.
func databaseAt(t *testing.T, version int64) (*sql.DB, *goose.Provider) {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: chain.At(t, version)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)

	return db, p
}
