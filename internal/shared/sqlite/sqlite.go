// Package sqlite opens a SQLite database the way every process of Hypomnema
// keeps its state: one private file, one connection, WAL, and pragmas checked
// after opening. It follows Omnara's node state database
// (internal/machinedaemon/statedb/store.go) and Orca's orchestration database.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	_ "modernc.org/sqlite" // Registers the "sqlite" driver, pure Go, so binaries build without cgo.

	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// Config is where a database lives.
type Config struct {
	Path string
}

// FromEnv reads prefix_DATABASE_PATH, which must be absolute.
func FromEnv(env *platform.Env, prefix string) Config {
	return Config{Path: env.Required(prefix + "_DATABASE_PATH")}
}

// pragmas are set on open and read back, so a driver that ignored one is
// caught before any state is written. synchronous=FULL because losing
// execution state on a crash costs more than the slower commit.
var pragmas = map[string]int{
	"busy_timeout": 5000,
	"foreign_keys": 1,
	"synchronous":  2,
	"temp_store":   2,
}

// Open opens the database at cfg.Path, creating it, and its directory, as
// private to the current user when it does not exist. An existing file that
// another user could read is refused: it may hold secrets.
func Open(ctx context.Context, cfg Config) (*sql.DB, error) {
	path := cfg.Path
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("database path %q must be absolute", path)
	}

	existed, err := prepare(path)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("opening the database: %w", err)
	}

	// One connection: every write is serialized, so no two writers race, and
	// transactions take the write lock when they begin (_txlock=immediate).
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := verify(ctx, db); err != nil {
		return nil, errors.Join(err, db.Close())
	}

	if !existed {
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, errors.Join(fmt.Errorf("securing the new database: %w", err), db.Close())
		}
	}

	if err := validatePrivateFile(path); err != nil {
		return nil, errors.Join(err, db.Close())
	}

	return db, nil
}

// prepare makes the directory private and validates an existing file.
func prepare(path string) (existed bool, err error) {
	info, err := os.Lstat(path)

	switch {
	case errors.Is(err, os.ErrNotExist):
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return false, fmt.Errorf("creating the database directory: %w", err)
		}

		// The directory is what keeps the file private between its creation
		// by the driver and the chmod below, so it is made private even when
		// it already existed.
		if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // A directory needs the execute bit; 0700 is owner-only.
			return false, fmt.Errorf("securing the database directory: %w", err)
		}

		return false, nil
	case err != nil:
		return false, fmt.Errorf("inspecting the database: %w", err)
	case !info.Mode().IsRegular():
		return false, fmt.Errorf("database %q must be a regular file, not a link or directory", path)
	}

	return true, validatePrivateFile(path)
}

func dsn(path string) string {
	slashPath := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && len(slashPath) >= 2 && slashPath[1] == ':' {
		slashPath = "/" + slashPath
	}

	query := url.Values{}
	query.Set("_txlock", "immediate")

	for _, p := range []string{
		"busy_timeout=5000",
		"foreign_keys=ON",
		"trusted_schema=OFF",
		"journal_mode=WAL",
		"synchronous=FULL",
		"temp_store=MEMORY",
	} {
		query.Add("_pragma", p)
	}

	return (&url.URL{Scheme: "file", Path: slashPath, RawQuery: query.Encode()}).String()
}

func verify(ctx context.Context, db *sql.DB) error {
	for name, want := range pragmas {
		var got int
		if err := db.QueryRowContext(ctx, "PRAGMA "+name).Scan(&got); err != nil {
			return fmt.Errorf("reading pragma %s: %w", name, err)
		}

		if got != want {
			return fmt.Errorf("pragma %s is %d, want %d", name, got, want)
		}
	}

	var journal string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		return fmt.Errorf("reading pragma journal_mode: %w", err)
	}

	if !strings.EqualFold(journal, "wal") {
		return fmt.Errorf("pragma journal_mode is %q, want wal", journal)
	}

	return nil
}

// Ready reports whether the database answers.
func Ready(db *sql.DB) func(context.Context) error {
	return func(ctx context.Context) error {
		var one int

		return db.QueryRowContext(ctx, "SELECT 1").Scan(&one)
	}
}
