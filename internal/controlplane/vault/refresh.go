package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/vault/internal/store"
)

// Refreshing a token set (issue #66). A rotating refresh token is spent by
// its first use, so the vault lets one refresher at a time exchange it and
// stores the answer before anyone else looks:
//
//   - in this process, callers that arrive during a refresh share its result;
//   - across processes, a lease row names the refresher and the version it
//     read, as Omnara's secret_oauth_refresh_leases: the result is stored only
//     while the secret is still at that version;
//   - the exchange and the write that follows do not stop when the caller
//     stops waiting, as omp's broker: by then the provider may have spent the
//     old token, and giving up would lose the login;
//   - a failure that may pass keeps the token set and frees the lease, so a
//     later call sends the same token again; a rejection for good marks the
//     secret as needing a new login, as the Codex CLI treats the two.

const (
	// exchangeTimeout bounds waiting for the lease and the provider call.
	exchangeTimeout = 60 * time.Second
	// saveTimeout bounds the retries of storing a new token set.
	saveTimeout = 60 * time.Second
	// leaseTTL outlives the exchange and its save, so a live refresher is
	// not taken over; a refresher that died is, once it passes.
	leaseTTL = exchangeTimeout + saveTimeout + 30*time.Second
	// leasePoll is how often a refresher looks again at a lease another
	// process holds.
	leasePoll = 200 * time.Millisecond
	// saveRetry is the pause between attempts to store a new token set.
	saveRetry = 250 * time.Millisecond
)

// Refreshed is what a provider gave for a refresh token, or for a login.
type Refreshed struct {
	// Payload is the whole new token set. A provider that did not send a new
	// refresh token keeps the old one: the RefreshFunc copies it over.
	Payload Payload
	// AccessTokenExpiresAt is when the new access token ends; zero when the
	// provider did not say.
	AccessTokenExpiresAt time.Time
}

// RefreshFunc exchanges a token set's refresh token with its provider. An
// error that wraps ErrLoginRejected says the provider refused the token for
// good; any other error is a failure that may pass. Its errors must not
// contain a token.
type RefreshFunc func(ctx context.Context, current Payload) (Refreshed, error)

type refreshCall struct {
	done   chan struct{}
	opened Opened
	err    error
}

// Refresh replaces a token set through fn and returns the set in use. The
// caller names the version it found stale (expired, or refused with a 401):
// when the secret has moved past it, that newer set is returned and fn is not
// called. When ctx ends first, Refresh returns its error and the refresh
// carries on, so its result is there for the next call.
func (s *Service) Refresh(ctx context.Context, name string, staleVersion int, fn RefreshFunc) (Opened, error) {
	if s.keyring == nil {
		return Opened{}, ErrNotConfigured
	}

	s.mu.Lock()

	call, running := s.refreshing[name]
	if !running {
		call = &refreshCall{done: make(chan struct{})}
		s.refreshing[name] = call

		go func() {
			call.opened, call.err = s.refresh(context.WithoutCancel(ctx), name, staleVersion, fn)

			s.mu.Lock()
			delete(s.refreshing, name)
			s.mu.Unlock()

			close(call.done)
		}()
	}

	s.mu.Unlock()

	select {
	case <-call.done:
		return call.opened, call.err
	case <-ctx.Done():
		return Opened{}, fmt.Errorf("waiting for the refresh of secret %q: %w", name, ctx.Err())
	}
}

// claim is what a refresher holds once it has the lease.
type claim struct {
	row     store.Secret
	version store.SecretVersion
	owner   string
	current Opened
}

func (s *Service) refresh(ctx context.Context, name string, staleVersion int, fn RefreshFunc) (Opened, error) {
	exchangeCtx, cancel := context.WithTimeout(ctx, exchangeTimeout)
	defer cancel()

	c, adopted, err := s.claimRefresh(exchangeCtx, name, staleVersion)
	if err != nil || adopted != nil {
		if adopted != nil {
			return *adopted, nil
		}

		return Opened{}, err
	}

	refreshed, err := fn(exchangeCtx, c.current.Payload)
	if err != nil {
		return Opened{}, s.refreshFailed(ctx, c, err)
	}

	if err := validPayload(KindOAuthTokenSet, refreshed.Payload); err != nil {
		return Opened{}, errors.Join(
			fmt.Errorf("%w: the provider's answer for secret %q is not a token set: %w", ErrRefreshFailed, name, err),
			s.releaseLease(ctx, c))
	}

	// Past this point the provider may have spent the old refresh token, so
	// the write is retried on its own time, whatever happened to the exchange's.
	saveCtx, cancelSave := context.WithTimeout(ctx, saveTimeout)
	defer cancelSave()

	return s.saveRefreshed(saveCtx, c, refreshed)
}

// claimRefresh takes the lease, waiting while another process holds it. It
// returns the newer token set instead when the secret has moved past the
// version the caller found stale.
func (s *Service) claimRefresh(ctx context.Context, name string, staleVersion int) (*claim, *Opened, error) {
	owner := s.newID("lease")

	for {
		var (
			c       *claim
			adopted *Opened
		)

		err := s.write(ctx, func(q *store.Queries, now int64) error {
			row, version, err := currentVersion(ctx, q, name)
			if err != nil {
				return err
			}

			if Kind(row.Kind) != KindOAuthTokenSet {
				return fmt.Errorf("%w: secret %q is not a token set", ErrInvalid, name)
			}

			current, err := s.openVersion(row, version)
			if err != nil {
				return err
			}

			if current.Version > staleVersion {
				adopted = &current

				return nil
			}

			if row.LoginRequiredAt.Valid {
				return fmt.Errorf("%w: secret %q", ErrLoginRequired, name)
			}

			if err := q.DeleteExpiredSecretRefreshLease(ctx, store.DeleteExpiredSecretRefreshLeaseParams{
				SecretID: row.ID, Now: now,
			}); err != nil {
				return fmt.Errorf("clearing an expired lease on secret %q: %w", name, err)
			}

			taken, err := q.InsertSecretRefreshLease(ctx, store.InsertSecretRefreshLeaseParams{
				SecretID: row.ID, OwnerToken: owner, ExpectedVersionID: version.ID,
				ExpiresAt: now + leaseTTL.Milliseconds(),
			})
			if err != nil {
				return fmt.Errorf("taking the lease on secret %q: %w", name, err)
			}

			if taken == 1 {
				c = &claim{row: row, version: version, owner: owner, current: current}
			}

			return nil
		})
		if err != nil || c != nil || adopted != nil {
			return c, adopted, err
		}

		select {
		case <-ctx.Done():
			return nil, nil, fmt.Errorf("%w: another process is refreshing secret %q", ErrRefreshFailed, name)
		case <-time.After(leasePoll):
		}
	}
}

// refreshFailed frees the lease and, when the provider refused the token for
// good, marks the secret as needing a new login.
func (s *Service) refreshFailed(ctx context.Context, c *claim, cause error) error {
	if !errors.Is(cause, ErrLoginRejected) {
		return errors.Join(
			fmt.Errorf("%w: secret %q: %w", ErrRefreshFailed, c.row.Name, cause), s.releaseLease(ctx, c))
	}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.SetSecretLoginRequired(ctx, store.SetSecretLoginRequiredParams{
			Now: sql.NullInt64{Int64: now, Valid: true}, ID: c.row.ID,
		}); err != nil {
			return fmt.Errorf("marking secret %q as needing a login: %w", c.row.Name, err)
		}

		return release(ctx, q, c)
	})

	return errors.Join(fmt.Errorf("%w: secret %q: %w", ErrLoginRequired, c.row.Name, cause), err)
}

func (s *Service) releaseLease(ctx context.Context, c *claim) error {
	return s.write(ctx, func(q *store.Queries, _ int64) error { return release(ctx, q, c) })
}

func release(ctx context.Context, q *store.Queries, c *claim) error {
	if err := q.ReleaseSecretRefreshLease(ctx, store.ReleaseSecretRefreshLeaseParams{
		SecretID: c.row.ID, OwnerToken: c.owner,
	}); err != nil {
		return fmt.Errorf("releasing the lease on secret %q: %w", c.row.Name, err)
	}

	return nil
}

// saveRefreshed stores the new token set as the next version and frees the
// lease, trying again while the database refuses. It gives up only when the
// lease is another refresher's or the secret is no longer at the version this
// one read: then storing would overwrite a newer token set.
func (s *Service) saveRefreshed(ctx context.Context, c *claim, r Refreshed) (Opened, error) {
	id := identity{
		SecretID: c.row.ID, VersionID: s.newID("secv"), VersionNumber: c.version.VersionNumber + 1,
		Kind: KindOAuthTokenSet,
	}

	sealedVersion, err := seal(s.keyring, id, r.Payload)
	if err != nil {
		return Opened{}, errors.Join(err, s.releaseLease(ctx, c))
	}

	expires := knownTime(r.AccessTokenExpiresAt)

	for {
		err := s.write(ctx, func(q *store.Queries, now int64) error {
			if err := stillOurs(ctx, q, c); err != nil {
				return err
			}

			if err := insertVersion(ctx, q, id, sealedVersion, expires, now); err != nil {
				return err
			}

			if err := q.SetCurrentSecretVersion(ctx, store.SetCurrentSecretVersionParams{
				CurrentVersionID: id.VersionID, Now: now, ID: c.row.ID,
			}); err != nil {
				return fmt.Errorf("pointing secret %q at its new version: %w", c.row.Name, err)
			}

			// A refresh token is dead once it was exchanged, so only the
			// version before the new one is kept beside it.
			if err := q.DeleteSecretVersionsBefore(ctx, store.DeleteSecretVersionsBeforeParams{
				SecretID: c.row.ID, VersionNumber: c.version.VersionNumber,
			}); err != nil {
				return fmt.Errorf("removing spent versions of secret %q: %w", c.row.Name, err)
			}

			return release(ctx, q, c)
		})
		if err == nil {
			return s.Open(ctx, c.row.Name)
		}

		if errors.Is(err, ErrConflict) {
			return Opened{}, errors.Join(err, s.releaseLease(ctx, c))
		}

		select {
		case <-ctx.Done():
			return Opened{}, fmt.Errorf("storing the refreshed secret %q: %w", c.row.Name, err)
		case <-time.After(saveRetry):
		}
	}
}

// stillOurs checks that the lease is this refresher's and the secret is at
// the version it read, as Omnara's rotate step. The lease's time is not
// checked: while nobody took it over, a late answer is still the only copy of
// the new token set and is stored.
func stillOurs(ctx context.Context, q *store.Queries, c *claim) error {
	lease, err := q.GetSecretRefreshLease(ctx, c.row.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("reading the lease on secret %q: %w", c.row.Name, err)
	}

	if err != nil || lease.OwnerToken != c.owner {
		return fmt.Errorf("%w: another refresher took over secret %q", ErrConflict, c.row.Name)
	}

	row, err := q.GetSecretByName(ctx, c.row.Name)
	if err != nil {
		return notFound(err, c.row.Name)
	}

	if row.CurrentVersionID != c.version.ID {
		return fmt.Errorf("%w: secret %q changed during the refresh", ErrConflict, c.row.Name)
	}

	return nil
}
