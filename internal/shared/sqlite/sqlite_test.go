package sqlite_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// A new database is private to its owner and in WAL mode with the pragmas
// set, because it holds execution state and, later, the vault's ciphertext.
func TestOpenCreatesAPrivateWALDatabase(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state", "hypomnema.db")

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: path})
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	var journal string
	require.NoError(t, db.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&journal))
	assert.Equal(t, "wal", journal)
	require.NoError(t, sqlite.Ready(db)(t.Context()))

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

		dir, err := os.Stat(filepath.Dir(path))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), dir.Mode().Perm())
	}
}

// Reopening keeps the data: the file is the state.
func TestOpenReopensTheSameState(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "hypomnema.db")

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: path})
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), "CREATE TABLE note (body TEXT); INSERT INTO note VALUES ('kept')")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	db, err = sqlite.Open(t.Context(), sqlite.Config{Path: path})
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	var body string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT body FROM note").Scan(&body))
	assert.Equal(t, "kept", body)
}

// A relative path would depend on the working directory of whoever started
// the process; it is refused.
func TestOpenRefusesARelativePath(t *testing.T) {
	t.Parallel()

	_, err := sqlite.Open(t.Context(), sqlite.Config{Path: "hypomnema.db"})
	require.ErrorContains(t, err, "must be absolute")
}

// An existing database that other users can read may already have leaked;
// the process refuses it instead of quietly tightening and continuing.
func TestOpenRefusesADatabaseOthersCanRead(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("permissions are enforced on Linux deployments only")
	}

	path := filepath.Join(t.TempDir(), "hypomnema.db")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	require.NoError(t, os.Chmod(path, 0o644)) //nolint:gosec // The test makes the file too open on purpose.

	_, err := sqlite.Open(t.Context(), sqlite.Config{Path: path})
	require.ErrorContains(t, err, "must be 0600")
}
