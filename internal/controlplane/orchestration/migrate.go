package orchestration

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrSchemaOutdated means the database schema is not the one this binary expects.
var ErrSchemaOutdated = errors.New("database schema is outdated")

func provider(db *sql.DB) (*goose.Provider, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("reading migrations: %w", err)
	}

	p, err := goose.NewProvider(goose.DialectSQLite3, db, sub)
	if err != nil {
		return nil, fmt.Errorf("loading migrations: %w", err)
	}

	return p, nil
}

// Migrate applies every pending migration. It is run by `hypomnema migrate`,
// an explicit admin command (docs/conventions.md, twelve-factor).
func Migrate(ctx context.Context, db *sql.DB) error {
	p, err := provider(db)
	if err != nil {
		return err
	}

	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrating: %w", err)
	}

	return nil
}

// CheckSchema fails unless the database is at exactly the schema this binary
// was built with, so `serve` never runs on a schema it does not know.
func CheckSchema(ctx context.Context, db *sql.DB) error {
	p, err := provider(db)
	if err != nil {
		return err
	}

	current, err := p.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("reading the schema version: %w", err)
	}

	sources := p.ListSources()
	latest := sources[len(sources)-1].Version

	switch {
	case current < latest:
		return fmt.Errorf("%w: at version %d, this binary needs %d; run `hypomnema migrate`",
			ErrSchemaOutdated, current, latest)
	case current > latest:
		return fmt.Errorf("%w: at version %d, newer than this binary's %d", ErrSchemaOutdated, current, latest)
	default:
		return nil
	}
}
