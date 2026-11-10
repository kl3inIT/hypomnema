// Package sqlitetest gives a test a migrated database without migrating one
// for every test (issue #129). The migrations run once in a test process,
// into a template file; a test gets a copy of that file. It is what Coder's
// dbtestutil and Omnara's integrationdb do with a Postgres template database
// (docs/research/2026-11-10-ci-and-tests.md), with a file's copy for SQLite.
//
// Measured on CI on 2026-11-10: migrating a fresh database took 71 to 81 ms,
// and 1.5 to 1.6 s under the race detector, for each of several hundred tests.
package sqlitetest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

var (
	once        sync.Once
	template    []byte
	errTemplate error
)

// Migrated returns the path of a new database file in the test's own
// temporary directory, with every migration applied. migrate is run once in
// the process; every test of a process passes the same one.
func Migrated(t testing.TB, migrate func(context.Context, *sql.DB) error) string {
	t.Helper()

	once.Do(func() { template, errTemplate = build(migrate) })

	if errTemplate != nil {
		t.Fatalf("making the migrated template database: %v", errTemplate)
	}

	path := filepath.Join(t.TempDir(), "hypomnema.db")
	// Private to the user, as sqlite.Open makes a database and refuses any other.
	if err := os.WriteFile(path, template, 0o600); err != nil {
		t.Fatalf("copying the migrated template database: %v", err)
	}

	return path
}

// build migrates one database and returns its file's bytes.
func build(migrate func(context.Context, *sql.DB) error) ([]byte, error) {
	dir, err := os.MkdirTemp("", "hypomnema-template-")
	if err != nil {
		return nil, fmt.Errorf("making a directory: %w", err)
	}

	defer os.RemoveAll(dir) //nolint:errcheck // a leftover temporary directory harms nothing

	ctx := context.Background()
	path := filepath.Join(dir, "template.db")

	db, err := sqlite.Open(ctx, sqlite.Config{Path: path})
	if err != nil {
		return nil, err
	}

	if err := migrate(ctx, db); err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("migrating: %w", err)
	}

	// Everything goes into the one file before it is read: nothing is left in the write-ahead log.
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("checkpointing: %w", err)
	}

	if err := db.Close(); err != nil {
		return nil, fmt.Errorf("closing: %w", err)
	}

	return os.ReadFile(path) //nolint:gosec // the file this function just wrote
}
