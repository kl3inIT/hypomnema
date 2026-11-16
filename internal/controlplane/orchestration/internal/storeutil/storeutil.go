// Package storeutil holds what the orchestration's stores share: the one
// write helper over the generated queries and the helpers of their rows, as
// Omnara's storage/internal/storeutil serves its stores.
package storeutil

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// Write runs fn in one transaction and calls signal after a commit that
// changed a row (issue #586): a commit that wrote nothing tells no reader to
// read again, so a loop woken by signals cannot wake itself with empty
// writes, as the scheduler's Loop already guarded against. The connection opens every transaction with BEGIN IMMEDIATE
// (internal/shared/sqlite), so fn reads the latest state. fn must not call
// Write again: with one connection, a nested write waits for it forever; a
// method that writes inside another's transaction ends in Tx and takes q.
func Write(
	ctx context.Context, db *sql.DB, clock platform.Clock, signal func(),
	fn func(q *store.Queries, now int64) error,
) error {
	return WriteTx(ctx, db, clock, signal, func(_ *sql.Tx, q *store.Queries, now int64) error { return fn(q, now) })
}

// WriteTx is Write that gives fn the transaction too, for a package that owns
// other tables to write them in it with its own queries (issue #405), as
// Omnara's stores take the caller's pgx.Tx.
func WriteTx(
	ctx context.Context, db *sql.DB, clock platform.Clock, signal func(),
	fn func(tx *sql.Tx, q *store.Queries, now int64) error,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning a transaction: %w", err)
	}

	before, err := totalChanges(ctx, tx)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}

	if err := fn(tx, store.New(tx), clock.Now().UnixMilli()); err != nil {
		return errors.Join(err, tx.Rollback())
	}

	after, err := totalChanges(ctx, tx)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing: %w", err)
	}

	if after != before {
		signal()
	}

	return nil
}

// totalChanges is SQLite's count of the rows the transaction's connection has
// inserted, updated or deleted since it opened, those of triggers included
// (https://sqlite.org/lang_corefunc.html#total_changes). The transaction holds
// its connection, so two reads in it differ exactly when it changed a row.
func totalChanges(ctx context.Context, tx *sql.Tx) (int64, error) {
	var n int64
	if err := tx.QueryRowContext(ctx, "SELECT total_changes()").Scan(&n); err != nil {
		return 0, fmt.Errorf("reading the changes of the transaction: %w", err)
	}

	return n, nil
}

// MaxChatText bounds one chat message, and a Routine's prompt and script,
// which are bounded as a message is.
const MaxChatText = 100_000

// NamePattern is what a name the owner gives (a profile, a Bot, a user) is:
// lowercase letters, digits and dashes.
var NamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// NotFound turns a missing row into storeerr.ErrNotFound naming what was read.
func NotFound(err error, kind, id string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s %s", storeerr.ErrNotFound, kind, id)
	}

	return fmt.Errorf("reading %s %s: %w", kind, id, err)
}

// Millis is a stored time, milliseconds since the epoch, in UTC.
func Millis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// OptionalMillis is a stored time that may be missing; nil when it is.
func OptionalMillis(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}

	t := Millis(v.Int64)

	return &t
}
