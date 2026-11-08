// Package statedb is the node's own SQLite state (issue #32): the Dispatches
// it holds and an outbox of reports the control plane has not acknowledged,
// replayed after every reconnect (Omnara internal/machinedaemon/statedb and
// report_outbox.go at 0ef1711). It is the only package of the node tree that
// uses database/sql.
package statedb

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNotFound is returned when the node holds no such Dispatch or report.
var ErrNotFound = errors.New("not found")

// State is how far the node got with a Dispatch.
type State string

// States.
const (
	// StateOffered: the accept report is queued; the worker must not run yet.
	StateOffered State = "offered"
	// StateAccepted: the control plane accepted; the worker may run.
	StateAccepted State = "accepted"
	// StateEnded: the worker ended and its final report is queued.
	StateEnded State = "ended"
)

// Dispatch is one Dispatch the node holds.
type Dispatch struct {
	ID         string
	State      State
	Epoch      int
	Offer      nodeproto.Offer
	Accepted   *nodeproto.Accepted
	ProcessRef string
}

// DB is the node's state.
type DB struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens and migrates the state database at path.
func Open(ctx context.Context, path string, now func() time.Time) (*DB, error) {
	db, err := sqlite.Open(ctx, sqlite.Config{Path: path})
	if err != nil {
		return nil, err
	}

	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("reading migrations: %w", err), db.Close())
	}

	p, err := goose.NewProvider(goose.DialectSQLite3, db, sub)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("loading migrations: %w", err), db.Close())
	}

	if _, err := p.Up(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("migrating the node state: %w", err), db.Close())
	}

	return &DB{db: db, now: now}, nil
}

// Close closes the database.
func (s *DB) Close() error { return s.db.Close() }

// AddOffer records an offered Dispatch and queues its accept report, in one
// transaction. It reports false when the Dispatch is already held.
func (s *DB) AddOffer(ctx context.Context, o nodeproto.Offer, accept nodeproto.Report) (bool, error) {
	offer, err := json.Marshal(o)
	if err != nil {
		return false, fmt.Errorf("encoding offer: %w", err)
	}

	added := false

	err = s.tx(ctx, func(tx *sql.Tx, now int64) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO dispatches (id, state, offer, created_at, updated_at)
			VALUES (?, 'offered', ?, ?, ?) ON CONFLICT (id) DO NOTHING`, o.DispatchID, string(offer), now, now)
		if err != nil {
			return fmt.Errorf("recording offer %s: %w", o.DispatchID, err)
		}

		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}

		added = true

		return enqueue(ctx, tx, now, accept)
	})

	return added, err
}

// Accept records the control plane's accept response.
func (s *DB) Accept(ctx context.Context, id string, a nodeproto.Accepted) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("encoding accepted: %w", err)
	}

	return s.update(ctx, `UPDATE dispatches SET state = 'accepted', epoch = ?, accepted = ?, updated_at = ?
		WHERE id = ?`, a.Epoch, string(raw), s.now().UnixMilli(), id)
}

// SetProcess records the worker's process reference.
func (s *DB) SetProcess(ctx context.Context, id, ref string) error {
	return s.update(ctx, `UPDATE dispatches SET process_ref = ?, updated_at = ? WHERE id = ?`,
		ref, s.now().UnixMilli(), id)
}

// End marks a Dispatch ended and queues its final report, in one transaction.
func (s *DB) End(ctx context.Context, id string, final nodeproto.Report) error {
	return s.tx(ctx, func(tx *sql.Tx, now int64) error {
		res, err := tx.ExecContext(ctx, `UPDATE dispatches SET state = 'ended', updated_at = ? WHERE id = ?`, now, id)
		if err != nil {
			return fmt.Errorf("ending %s: %w", id, err)
		}

		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: dispatch %s", ErrNotFound, id)
		}

		return enqueue(ctx, tx, now, final)
	})
}

// Remove forgets a Dispatch.
func (s *DB) Remove(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM dispatches WHERE id = ?`, id); err != nil {
		return fmt.Errorf("removing %s: %w", id, err)
	}

	return nil
}

// Get returns one Dispatch.
func (s *DB) Get(ctx context.Context, id string) (Dispatch, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, state, epoch, offer, accepted, process_ref FROM dispatches
		WHERE id = ?`, id)

	d, err := scanDispatch(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return Dispatch{}, fmt.Errorf("%w: dispatch %s", ErrNotFound, id)
	}

	return d, err
}

// List returns every Dispatch the node holds, oldest first.
func (s *DB) List(ctx context.Context) ([]Dispatch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, state, epoch, offer, accepted, process_ref FROM dispatches
		ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("listing dispatches: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []Dispatch

	for rows.Next() {
		d, err := scanDispatch(rows.Scan)
		if err != nil {
			return nil, err
		}

		out = append(out, d)
	}

	return out, rows.Err()
}

func scanDispatch(scan func(...any) error) (Dispatch, error) {
	var (
		d                 Dispatch
		state, offer      string
		accepted, process sql.NullString
	)

	if err := scan(&d.ID, &state, &d.Epoch, &offer, &accepted, &process); err != nil {
		return Dispatch{}, err
	}

	d.State, d.ProcessRef = State(state), process.String

	if err := json.Unmarshal([]byte(offer), &d.Offer); err != nil {
		return Dispatch{}, fmt.Errorf("decoding the offer of %s: %w", d.ID, err)
	}

	if accepted.Valid {
		var a nodeproto.Accepted
		if err := json.Unmarshal([]byte(accepted.String), &a); err != nil {
			return Dispatch{}, fmt.Errorf("decoding the accept of %s: %w", d.ID, err)
		}

		d.Accepted = &a
	}

	return d, nil
}

// Enqueue adds a report to the outbox.
func (s *DB) Enqueue(ctx context.Context, r nodeproto.Report) error {
	return s.tx(ctx, func(tx *sql.Tx, now int64) error { return enqueue(ctx, tx, now, r) })
}

func enqueue(ctx context.Context, tx *sql.Tx, now int64, r nodeproto.Report) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encoding report %s: %w", r.ReportID, err)
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (report_id, report, created_at) VALUES (?, ?, ?)
		ON CONFLICT (report_id) DO NOTHING`, r.ReportID, string(raw), now); err != nil {
		return fmt.Errorf("queuing report %s: %w", r.ReportID, err)
	}

	return nil
}

// Pending returns the reports not yet acknowledged, in the order queued.
func (s *DB) Pending(ctx context.Context) ([]nodeproto.Report, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT report FROM outbox ORDER BY seq`)
	if err != nil {
		return nil, fmt.Errorf("listing the outbox: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []nodeproto.Report

	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}

		var r nodeproto.Report
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, fmt.Errorf("decoding a queued report: %w", err)
		}

		out = append(out, r)
	}

	return out, rows.Err()
}

// Report returns one queued report.
func (s *DB) Report(ctx context.Context, reportID string) (nodeproto.Report, error) {
	var raw string

	err := s.db.QueryRowContext(ctx, `SELECT report FROM outbox WHERE report_id = ?`, reportID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nodeproto.Report{}, fmt.Errorf("%w: report %s", ErrNotFound, reportID)
	}

	if err != nil {
		return nodeproto.Report{}, fmt.Errorf("reading report %s: %w", reportID, err)
	}

	var r nodeproto.Report
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return nodeproto.Report{}, fmt.Errorf("decoding report %s: %w", reportID, err)
	}

	return r, nil
}

// Done removes an acknowledged report.
func (s *DB) Done(ctx context.Context, reportID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM outbox WHERE report_id = ?`, reportID); err != nil {
		return fmt.Errorf("removing report %s: %w", reportID, err)
	}

	return nil
}

func (s *DB) update(ctx context.Context, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating the node state: %w", err)
	}

	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: dispatch", ErrNotFound)
	}

	return nil
}

func (s *DB) tx(ctx context.Context, fn func(tx *sql.Tx, now int64) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning a transaction: %w", err)
	}

	if err := fn(tx, s.now().UnixMilli()); err != nil {
		return errors.Join(err, tx.Rollback())
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing: %w", err)
	}

	return nil
}
