package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// Connections (issue #103). An Integration is a service Hypomnema works with;
// today each git host is one, named by its host. A Connection is one account
// of an Integration that the owner authorized, under a label he chose. Its
// credential is in the vault; this package holds only the secret's name.
//
// "Account" already means a runtime's subscription (Account), so an
// authorized account of a service is called a Connection.

// DefaultConnection is the label of a host's Connection made without one, and
// of the token a host had before Connections existed.
const DefaultConnection = "default"

// Connection is one authorized account of an Integration.
type Connection struct {
	Integration string
	Label       string
	// Account is the account as the service names it, when known.
	Account string
	// Secret names the vault secret that holds the credential. The
	// credential itself is never here.
	Secret    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

var connectionLabelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// ValidConnectionLabel says whether a label is one the owner can give.
func ValidConnectionLabel(label string) bool { return connectionLabelPattern.MatchString(label) }

// SetConnection records a Connection and the secret that holds its
// credential, or changes the secret and account of an existing one. The
// Integration must be a git host a Run can be on.
func (s *Service) SetConnection(ctx context.Context, integration, label, account, secret string,
) (Connection, error) {
	if !ValidConnectionLabel(label) {
		return Connection{}, fmt.Errorf("%w: a label is lower-case letters, digits and dashes, such as work", ErrInvalid)
	}

	if secret == "" {
		return Connection{}, fmt.Errorf("%w: a connection needs its secret", ErrInvalid)
	}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := gitHostOf(ctx, q, integration); err != nil {
			return err
		}

		return q.UpsertConnection(ctx, store.UpsertConnectionParams{
			Integration: integration, Label: label, Account: nullString(account), Secret: secret, Now: now,
		})
	})
	if err != nil {
		return Connection{}, fmt.Errorf("recording connection %s:%s: %w", integration, label, err)
	}

	return s.ConnectionOf(ctx, integration, label)
}

// ConnectionOf returns one Connection; ErrNotFound when there is none.
func (s *Service) ConnectionOf(ctx context.Context, integration, label string) (Connection, error) {
	row, err := s.read().GetConnection(ctx, store.GetConnectionParams{Integration: integration, Label: label})
	if errors.Is(err, sql.ErrNoRows) {
		return Connection{}, fmt.Errorf("%w: %s has no connection %s", ErrNotFound, integration, label)
	}

	if err != nil {
		return Connection{}, fmt.Errorf("reading connection %s:%s: %w", integration, label, err)
	}

	return connectionFromRow(row), nil
}

// ListConnections returns every Connection, by Integration and then by age.
func (s *Service) ListConnections(ctx context.Context) ([]Connection, error) {
	rows, err := s.read().ListConnections(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing connections: %w", err)
	}

	out := make([]Connection, 0, len(rows))
	for _, r := range rows {
		out = append(out, connectionFromRow(r))
	}

	return out, nil
}

// RemoveConnection forgets a Connection and returns it, so its secret can be
// deleted. It is refused while a registered repository names it.
func (s *Service) RemoveConnection(ctx context.Context, integration, label string) (Connection, error) {
	var removed Connection

	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		row, err := q.GetConnection(ctx, store.GetConnectionParams{Integration: integration, Label: label})
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s has no connection %s", ErrNotFound, integration, label)
		}

		if err != nil {
			return fmt.Errorf("reading connection %s:%s: %w", integration, label, err)
		}

		repos, err := q.CountReposUsingConnection(ctx, store.CountReposUsingConnectionParams{
			Host: integration, Connection: nullString(label),
		})
		if err != nil {
			return fmt.Errorf("counting the repositories that use %s:%s: %w", integration, label, err)
		}

		if repos > 0 {
			return fmt.Errorf("%w: %d registered repositories use %s:%s", ErrConflict, repos, integration, label)
		}

		_, err = q.DeleteConnection(ctx, store.DeleteConnectionParams{Integration: integration, Label: label})
		if err != nil {
			return fmt.Errorf("removing connection %s:%s: %w", integration, label, err)
		}

		removed = connectionFromRow(row)

		return nil
	})

	return removed, err
}

// ConnectionFor returns the Connection work on a repository uses: the one a
// registered repository names, else the host's only one. With several and
// none named there is no answer: nothing is guessed.
func (s *Service) ConnectionFor(ctx context.Context, host, path string) (Connection, bool, error) {
	q := s.read()

	if path != "" {
		named, err := q.ConnectionOfRepo(ctx, store.ConnectionOfRepoParams{Host: host, Path: path})
		if err == nil && named.Valid {
			row, err := q.GetConnection(ctx, store.GetConnectionParams{Integration: host, Label: named.String})
			if err == nil {
				return connectionFromRow(row), true, nil
			}

			if !errors.Is(err, sql.ErrNoRows) {
				return Connection{}, false, fmt.Errorf("reading connection %s:%s: %w", host, named.String, err)
			}
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Connection{}, false, fmt.Errorf("reading the connection of %s/%s: %w", host, path, err)
		}
	}

	rows, err := q.ListConnectionsOf(ctx, host)
	if err != nil {
		return Connection{}, false, fmt.Errorf("listing the connections of %s: %w", host, err)
	}

	if len(rows) != 1 {
		return Connection{}, false, nil
	}

	return connectionFromRow(rows[0]), true, nil
}

func connectionFromRow(r store.Connection) Connection {
	return Connection{
		Integration: r.Integration, Label: r.Label, Account: r.Account.String, Secret: r.Secret,
		CreatedAt: millis(r.CreatedAt), UpdatedAt: millis(r.UpdatedAt),
	}
}
