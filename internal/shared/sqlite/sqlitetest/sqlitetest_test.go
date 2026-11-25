package sqlitetest_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// Scenario: tests ask a chain of migrations for versions 3, then 1, then 2 and 3 again (issue #1152). Each gets a
// database of its own at exactly its version, never one above it; a version is made from the nearest one below it
// made before, and a version made once is not made again.
func TestAChainGivesEachVersionFromTheNearestBelow(t *testing.T) {
	t.Parallel()

	// up applies "migrations" 1..version as tables m1, m2, …, after the ones the database already has.
	var ran []string

	chain := sqlitetest.NewChain(func(ctx context.Context, db *sql.DB, version int64) error {
		var at int64
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name LIKE 'm%'`).
			Scan(&at); err != nil {
			return err
		}

		ran = append(ran, fmt.Sprintf("%d→%d", at, version))

		for v := at + 1; v <= version; v++ {
			if _, err := db.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE m%d (x)`, v)); err != nil {
				return err
			}
		}

		return nil
	})

	at := func(version int64) *sql.DB {
		t.Helper()

		db, err := sqlite.Open(t.Context(), sqlite.Config{Path: chain.At(t, version)})
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })

		return db
	}
	tables := func(db *sql.DB) int {
		t.Helper()

		var n int
		require.NoError(t, db.QueryRowContext(t.Context(),
			`SELECT count(*) FROM sqlite_schema WHERE name LIKE 'm%'`).Scan(&n))

		return n
	}

	three := at(3)
	assert.Equal(t, 1, tables(at(1)), "not the 3 made before")
	assert.Equal(t, 2, tables(at(2)))

	_, err := three.ExecContext(t.Context(), `CREATE TABLE m4 (x)`)
	require.NoError(t, err)
	assert.Equal(t, 3, tables(at(3)), "a test's own copy: another test's change is not in it")

	assert.Equal(t, []string{"0→3", "0→1", "1→2"}, ran)
}
