package vault_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// provider stands in for a subscription's token endpoint with a rotating
// refresh token: each refresh token works once, and using a spent one is
// refused for good, as OpenAI's does.
type provider struct {
	mu     sync.Mutex
	issued int
	valid  string
	calls  atomic.Int32
	// before, when set, runs at the start of each exchange.
	before func()
}

func newProvider() *provider { return &provider{valid: "refresh-0"} }

func (p *provider) exchange(_ context.Context, current vault.Payload) (vault.Refreshed, error) {
	p.calls.Add(1)

	if p.before != nil {
		p.before()
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if current[vault.KeyRefreshToken] != p.valid {
		return vault.Refreshed{}, fmt.Errorf("%w: refresh_token_reused", vault.ErrLoginRejected)
	}

	p.issued++
	p.valid = fmt.Sprintf("refresh-%d", p.issued)

	return vault.Refreshed{
		Payload: vault.Payload{
			vault.KeyRefreshToken: p.valid, vault.KeyAccessToken: fmt.Sprintf("access-%d", p.issued),
		},
		AccessTokenExpiresAt: fixedClock{}.Now().Add(time.Hour),
	}, nil
}

func newTokenSet(t *testing.T, v *vault.Service) {
	t.Helper()

	_, err := v.Create(t.Context(), vault.NewSecret{
		Name: "codex", Kind: vault.KindOAuthTokenSet,
		Payload: vault.Payload{vault.KeyRefreshToken: "refresh-0", vault.KeyAccessToken: "access-0"},
	})
	require.NoError(t, err)
}

func count(t *testing.T, db *sql.DB, table string) int {
	t.Helper()

	var n int

	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n))

	return n
}

// Scenario: the access token ended. A refresh stores the new token set as the
// version in use with its expiry, and of the spent versions only the one
// before it is kept.
func TestRefreshStoresTheNewTokenSet(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	v := newVault(db, keyring(t, "k1", newKey(t, "k1")), nil)
	p := newProvider()
	newTokenSet(t, v)

	for round := 1; round <= 3; round++ {
		got, err := v.Refresh(t.Context(), "codex", round, p.exchange)
		require.NoError(t, err)
		assert.Equal(t, round+1, got.Version)
		assert.Equal(t, fmt.Sprintf("access-%d", round), got.Payload[vault.KeyAccessToken])
		assert.Equal(t, fmt.Sprintf("refresh-%d", round), got.Payload[vault.KeyRefreshToken])
		require.NotNil(t, got.AccessTokenExpiresAt)
		assert.Equal(t, fixedClock{}.Now().Add(time.Hour), *got.AccessTokenExpiresAt)
	}

	opened, err := v.Open(t.Context(), "codex")
	require.NoError(t, err)
	assert.Equal(t, "refresh-3", opened.Payload[vault.KeyRefreshToken])

	assert.Equal(t, 2, count(t, db, "secret_versions"))
	assert.Zero(t, count(t, db, "secret_refresh_leases"))
}

// Scenario: several requests find the same access token expired at once. The
// refresh token is exchanged once, and every caller gets the new set.
func TestConcurrentCallersShareOneExchange(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	v := newVault(db, keyring(t, "k1", newKey(t, "k1")), nil)
	p := newProvider()
	newTokenSet(t, v)

	// The exchange waits until every caller has asked.
	const callers = 6

	asked := make(chan struct{})
	p.before = func() { <-asked }

	results := make(chan vault.Opened, callers)
	errs := make(chan error, callers)

	for range callers {
		go func() {
			got, err := v.Refresh(t.Context(), "codex", 1, p.exchange)
			results <- got
			errs <- err
		}()
	}

	time.Sleep(100 * time.Millisecond)
	close(asked)

	for range callers {
		require.NoError(t, <-errs)
		assert.Equal(t, "refresh-1", (<-results).Payload[vault.KeyRefreshToken])
	}

	assert.EqualValues(t, 1, p.calls.Load())
}

// Scenario: a caller arrives with a version that someone already refreshed
// past (it read the secret before the refresh finished). It gets the newer
// set, and the refresh token is not exchanged again.
func TestAStaleCallerAdoptsTheNewerTokenSet(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	v := newVault(db, keyring(t, "k1", newKey(t, "k1")), nil)
	p := newProvider()
	newTokenSet(t, v)

	_, err := v.Refresh(t.Context(), "codex", 1, p.exchange)
	require.NoError(t, err)

	got, err := v.Refresh(t.Context(), "codex", 1, p.exchange)
	require.NoError(t, err)
	assert.Equal(t, 2, got.Version)
	assert.Equal(t, "refresh-1", got.Payload[vault.KeyRefreshToken])
	assert.EqualValues(t, 1, p.calls.Load())
}

// Scenario: two processes share the database (the control plane and an
// owner's command). While one refreshes, the other waits for its lease and
// then adopts its result instead of exchanging the spent token.
func TestAnotherProcessWaitsForTheLeaseAndAdopts(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	k := keyring(t, "k1", newKey(t, "k1"))
	first, second := newVault(db, k, nil), newVault(db, k, nil)
	p := newProvider()
	newTokenSet(t, first)

	started, finish := make(chan struct{}), make(chan struct{})
	p.before = func() {
		close(started)
		<-finish
	}

	firstErr := make(chan error, 1)

	go func() {
		_, err := first.Refresh(t.Context(), "codex", 1, p.exchange)
		firstErr <- err
	}()

	<-started

	secondResult := make(chan vault.Opened, 1)
	secondErr := make(chan error, 1)

	go func() {
		got, err := second.Refresh(t.Context(), "codex", 1, func(context.Context, vault.Payload) (vault.Refreshed, error) {
			return vault.Refreshed{}, errors.New("the second process must not exchange the token")
		})
		secondResult <- got
		secondErr <- err
	}()

	// The second process is now waiting on the first one's lease.
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, 1, count(t, db, "secret_refresh_leases"))
	close(finish)

	require.NoError(t, <-firstErr)
	require.NoError(t, <-secondErr)
	assert.Equal(t, "refresh-1", (<-secondResult).Payload[vault.KeyRefreshToken])
}

// Scenario: the network fails during the exchange. The token set is kept and
// the lease freed, so the next call sends the same refresh token and succeeds.
func TestATransientFailureKeepsTheTokenAndIsRetried(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	v := newVault(db, keyring(t, "k1", newKey(t, "k1")), nil)
	p := newProvider()
	newTokenSet(t, v)

	_, err := v.Refresh(t.Context(), "codex", 1, func(context.Context, vault.Payload) (vault.Refreshed, error) {
		return vault.Refreshed{}, errors.New("dial tcp: i/o timeout")
	})
	require.ErrorIs(t, err, vault.ErrRefreshFailed)
	require.NotErrorIs(t, err, vault.ErrLoginRequired)

	listed, err := v.Get(t.Context(), "codex")
	require.NoError(t, err)
	assert.Equal(t, 1, listed.Version)
	assert.False(t, listed.LoginRequired)
	assert.Zero(t, count(t, db, "secret_refresh_leases"))

	got, err := v.Refresh(t.Context(), "codex", 1, p.exchange)
	require.NoError(t, err)
	assert.Equal(t, "refresh-1", got.Payload[vault.KeyRefreshToken])
}

// Scenario: the provider refuses the refresh token for good. The secret is
// marked as needing a new login, later refreshes fail without reaching the
// provider, the access token can still be read, and rotating the secret
// after a new login clears the mark.
func TestARejectedLoginIsMarkedUntilTheOwnerLogsInAgain(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	v := newVault(db, keyring(t, "k1", newKey(t, "k1")), nil)
	p := newProvider()
	p.valid = "a token issued to another client"
	newTokenSet(t, v)

	_, err := v.Refresh(t.Context(), "codex", 1, p.exchange)
	require.ErrorIs(t, err, vault.ErrLoginRequired)

	_, err = v.Refresh(t.Context(), "codex", 1, p.exchange)
	require.ErrorIs(t, err, vault.ErrLoginRequired)
	assert.EqualValues(t, 1, p.calls.Load(), "a dead login is not sent to the provider again")

	opened, err := v.Open(t.Context(), "codex")
	require.NoError(t, err)
	assert.True(t, opened.LoginRequired)
	assert.Equal(t, "access-0", opened.Payload[vault.KeyAccessToken])
	assert.Zero(t, count(t, db, "secret_refresh_leases"))

	p.valid = "refresh-from-new-login"

	s, err := v.Rotate(t.Context(), "codex", vault.Payload{vault.KeyRefreshToken: "refresh-from-new-login"})
	require.NoError(t, err)
	assert.False(t, s.LoginRequired)

	got, err := v.Refresh(t.Context(), "codex", s.Version, p.exchange)
	require.NoError(t, err)
	assert.Equal(t, "refresh-1", got.Payload[vault.KeyRefreshToken])
}

// Scenario: the caller stops waiting (its request was canceled) after the
// provider was asked. The exchange and the write still finish, so the new
// refresh token, which the provider already issued, is not lost.
func TestACanceledCallerDoesNotLoseTheNewToken(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	v := newVault(db, keyring(t, "k1", newKey(t, "k1")), nil)
	p := newProvider()
	newTokenSet(t, v)

	started, finish := make(chan struct{}), make(chan struct{})
	p.before = func() {
		close(started)
		<-finish
	}

	ctx, cancel := context.WithCancel(t.Context())
	callerErr := make(chan error, 1)

	go func() {
		_, err := v.Refresh(ctx, "codex", 1, p.exchange)
		callerErr <- err
	}()

	<-started
	cancel()
	require.ErrorIs(t, <-callerErr, context.Canceled)

	close(finish)

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		opened, err := v.Open(t.Context(), "codex")
		require.NoError(c, err)
		assert.Equal(c, "refresh-1", opened.Payload[vault.KeyRefreshToken])
	}, 5*time.Second, 20*time.Millisecond)
}

// Scenario: a refresher died holding the lease. Once its time has passed the
// next refresher takes over; a lease still in time is waited for, not taken.
func TestALeaseLeftByADeadRefresherIsTakenOverOnceItExpires(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	v := newVault(db, keyring(t, "k1", newKey(t, "k1")), nil)
	p := newProvider()
	newTokenSet(t, v)

	past := fixedClock{}.Now().Add(-time.Second).UnixMilli()

	_, err := db.ExecContext(t.Context(), `INSERT INTO secret_refresh_leases
		SELECT id, 'lease_of_a_dead_process', current_version_id, ? FROM secrets WHERE name = 'codex'`, past)
	require.NoError(t, err)

	got, err := v.Refresh(t.Context(), "codex", 1, p.exchange)
	require.NoError(t, err)
	assert.Equal(t, "refresh-1", got.Payload[vault.KeyRefreshToken])
	assert.Zero(t, count(t, db, "secret_refresh_leases"))
}

// Scenario: the owner logs in again while a refresh of the old login is
// under way. The refresh's answer belongs to the old login and is dropped;
// the owner's new token set stays the one in use.
func TestARefreshDoesNotOverwriteANewLogin(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	v := newVault(db, keyring(t, "k1", newKey(t, "k1")), nil)
	p := newProvider()
	newTokenSet(t, v)

	p.before = func() {
		_, err := v.Rotate(t.Context(), "codex", vault.Payload{vault.KeyRefreshToken: "refresh-from-new-login"})
		assert.NoError(t, err)
	}

	_, err := v.Refresh(t.Context(), "codex", 1, p.exchange)
	require.ErrorIs(t, err, vault.ErrConflict)

	opened, err := v.Open(t.Context(), "codex")
	require.NoError(t, err)
	assert.Equal(t, "refresh-from-new-login", opened.Payload[vault.KeyRefreshToken])
	assert.Zero(t, count(t, db, "secret_refresh_leases"))
}

// Refresh is for token sets, on a configured vault, with an answer that is a
// token set.
func TestRefreshRefusesWhatItCannotDo(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	v := newVault(db, keyring(t, "k1", newKey(t, "k1")), nil)
	p := newProvider()
	newTokenSet(t, v)

	_, err := v.Create(t.Context(), generic(apiKey))
	require.NoError(t, err)

	_, err = v.Refresh(t.Context(), "router", 1, p.exchange)
	require.ErrorIs(t, err, vault.ErrInvalid)

	_, err = v.Refresh(t.Context(), "missing", 1, p.exchange)
	require.ErrorIs(t, err, vault.ErrNotFound)

	_, err = newVault(db, nil, nil).Refresh(t.Context(), "codex", 1, p.exchange)
	require.ErrorIs(t, err, vault.ErrNotConfigured)

	_, err = v.Refresh(t.Context(), "codex", 1, func(context.Context, vault.Payload) (vault.Refreshed, error) {
		return vault.Refreshed{Payload: vault.Payload{vault.KeyAccessToken: "an answer without a refresh token"}}, nil
	})
	require.ErrorIs(t, err, vault.ErrRefreshFailed)
	assert.EqualValues(t, 0, p.calls.Load())
	assert.Zero(t, count(t, db, "secret_refresh_leases"))
}
