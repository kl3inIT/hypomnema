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

// Write runs fn in one transaction and calls signal after the commit. The
// connection opens every transaction with BEGIN IMMEDIATE
// (internal/shared/sqlite), so fn reads the latest state. fn must not call
// Write again: with one connection, a nested write waits for it forever; a
// method that writes inside another's transaction ends in Tx and takes q.
func Write(
	ctx context.Context, db *sql.DB, clock platform.Clock, signal func(),
	fn func(q *store.Queries, now int64) error,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning a transaction: %w", err)
	}

	if err := fn(store.New(tx), clock.Now().UnixMilli()); err != nil {
		return errors.Join(err, tx.Rollback())
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing: %w", err)
	}

	signal()

	return nil
}

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
