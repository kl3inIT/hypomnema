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

	once.Do(func() { template, errTemplate = build(nil, migrate) })

	if errTemplate != nil {
		t.Fatalf("making the migrated template database: %v", errTemplate)
	}

	return copyOf(t, template)
}

// Chain gives a test a database at one version of a chain of migrations, for a test of a migration that starts
// before it. Each version a test asks for is made once in a test process, from the copy of the nearest version
// below it that was made before, so the migrations before a test's version run about once in the process instead
// of once in each test (issue #1152). It is what Coder's TestMigrationChain does with one database it steps
// through (coderd/database/migrations/migrate_test.go), with a copy of each step's file so that every test keeps
// a database of its own.
type Chain struct {
	// up migrates a database at a lower version, 0 for an empty one, up to version.
	up func(ctx context.Context, db *sql.DB, version int64) error

	mu sync.Mutex
	// made holds the file of each version made so far.
	made map[int64][]byte
}

// NewChain is a Chain whose versions up makes.
func NewChain(up func(ctx context.Context, db *sql.DB, version int64) error) *Chain {
	return &Chain{up: up, made: map[int64][]byte{}}
}

// At returns the path of a new database file in the test's own temporary directory, migrated up to version.
func (c *Chain) At(t testing.TB, version int64) string {
	t.Helper()

	file, err := c.file(version)
	if err != nil {
		t.Fatalf("making the database at version %d: %v", version, err)
	}

	return copyOf(t, file)
}

func (c *Chain) file(version int64) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if file, ok := c.made[version]; ok {
		return file, nil
	}

	var (
		below int64
		from  []byte
	)

	for v, file := range c.made {
		if v < version && v > below {
			below, from = v, file
		}
	}

	file, err := build(from, func(ctx context.Context, db *sql.DB) error { return c.up(ctx, db, version) })
	if err != nil {
		return nil, err
	}

	c.made[version] = file

	return file, nil
}

// copyOf writes file to a new database file in the test's own temporary directory.
func copyOf(t testing.TB, file []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "hypomnema.db")
	// Private to the user, as sqlite.Open makes a database and refuses any other.
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatalf("copying the template database: %v", err)
	}

	return path
}

// build migrates one database, from the file from or from an empty one when it is nil, and returns its file's
// bytes.
func build(from []byte, migrate func(context.Context, *sql.DB) error) ([]byte, error) {
	dir, err := os.MkdirTemp("", "hypomnema-template-")
	if err != nil {
		return nil, fmt.Errorf("making a directory: %w", err)
	}

	defer os.RemoveAll(dir) //nolint:errcheck // a leftover temporary directory harms nothing

	ctx := context.Background()
	path := filepath.Join(dir, "template.db")

	if from != nil {
		if err := os.WriteFile(path, from, 0o600); err != nil {
			return nil, fmt.Errorf("copying the database to start from: %w", err)
		}
	}

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
