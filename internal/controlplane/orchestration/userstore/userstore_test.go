package userstore_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/userstore"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

var t0 = time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// newTestStore is the users' store as the root's facade gives it, over a
// migrated database.
func newTestStore(t *testing.T) (*userstore.Store, *fakeClock) {
	t.Helper()

	s, clock, _ := newTestStoreDB(t)

	return s, clock
}

// newTestStoreDB is newTestStore with its database, to read what the store keeps.
func newTestStoreDB(t *testing.T) (*userstore.Store, *fakeClock, *sql.DB) {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	clock := &fakeClock{now: t0}
	n := 0
	ids := func(prefix string) string {
		n++

		return fmt.Sprintf("%s_%04d", prefix, n)
	}

	return orchestration.New(db, clock, ids).Users(), clock, db
}

// Scenario: the owner makes a user of the web; a wrong name or password is
// refused alike, a right one opens a session for 30 days (issue #114).
func TestAUserSignsInWithTheirPassword(t *testing.T) {
	t.Parallel()

	s, _ := newTestStore(t)
	ctx := t.Context()

	for _, bad := range []struct{ name, password string }{
		{"Linh", "correct horse"},
		{"linh", "short"},
		{"linh", strings.Repeat("x", 73)},
	} {
		_, err := s.AddUser(ctx, bad.name, bad.password)
		require.ErrorIs(t, err, storeerr.ErrInvalid, "%q / %d characters", bad.name, len(bad.password))
	}

	u, err := s.AddUser(ctx, "linh", "correct horse")
	require.NoError(t, err)
	assert.Equal(t, userstore.User{ID: u.ID, Name: "linh", CreatedAt: t0, UpdatedAt: t0}, u)

	_, err = s.AddUser(ctx, "linh", "another horse")
	require.ErrorIs(t, err, storeerr.ErrConflict, "a name is one user's")

	_, err = s.AddUser(ctx, "an", "battery staple")
	require.NoError(t, err)

	users, err := s.ListUsers(ctx)
	require.NoError(t, err)
	require.Len(t, users, 2)
	assert.Equal(t, []string{"an", "linh"}, []string{users[0].Name, users[1].Name}, "by name")

	_, err = s.SignIn(ctx, "linh", "wrong horse")
	require.ErrorIs(t, err, userstore.ErrBadCredentials)
	_, err = s.SignIn(ctx, "nobody", "correct horse")
	require.ErrorIs(t, err, userstore.ErrBadCredentials, "an unknown name is told the same")

	session, err := s.SignIn(ctx, "linh", "correct horse")
	require.NoError(t, err)
	assert.NotEmpty(t, session.Secret)
	assert.Equal(t, t0.Add(userstore.SessionLifetime), session.ExpiresAt)

	who, got, err := s.AuthenticateSession(ctx, session.Secret)
	require.NoError(t, err)
	assert.Equal(t, u.ID, who.ID)
	assert.Equal(t, "linh", who.Name)
	assert.Equal(t, userstore.Session{ID: session.ID, ExpiresAt: session.ExpiresAt}, got)

	for _, secret := range []string{"", "not a session"} {
		_, _, err := s.AuthenticateSession(ctx, secret)
		require.ErrorIs(t, err, storeerr.ErrNotFound, "%q", secret)
	}
}

// A session in use is extended, written at most once an hour; one left
// unused for 30 days has ended.
func TestASessionInUseIsExtendedAndAnUnusedOneEnds(t *testing.T) {
	t.Parallel()

	s, clock, db := newTestStoreDB(t)
	ctx := t.Context()

	_, err := s.AddUser(ctx, "linh", "correct horse")
	require.NoError(t, err)

	session, err := s.SignIn(ctx, "linh", "correct horse")
	require.NoError(t, err)

	lastUsed := func() sql.NullInt64 {
		t.Helper()

		var at sql.NullInt64
		require.NoError(t, db.QueryRowContext(ctx, "SELECT last_used_at FROM web_sessions WHERE id = ?", session.ID).
			Scan(&at))

		return at
	}

	clock.now = t0.Add(59 * time.Minute)
	_, got, err := s.AuthenticateSession(ctx, session.Secret)
	require.NoError(t, err)
	assert.Equal(t, t0.Add(userstore.SessionLifetime), got.ExpiresAt, "not written within the hour")
	assert.False(t, lastUsed().Valid, "nor its last use (issue #996)")

	clock.now = t0.Add(time.Hour)
	_, got, err = s.AuthenticateSession(ctx, session.Secret)
	require.NoError(t, err)
	assert.Equal(t, clock.now.Add(userstore.SessionLifetime), got.ExpiresAt, "extended after an hour")
	assert.Equal(t, clock.now.UnixMilli(), lastUsed().Int64, "with its last use")

	clock.now = got.ExpiresAt
	_, _, err = s.AuthenticateSession(ctx, session.Secret)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "ended")
}

// A new password ends every session of the user; signing out ends one, and
// signing out again is no error. Each ended session is kept, revoked with its
// reason (issue #996).
func TestANewPasswordAndSigningOutEndSessions(t *testing.T) {
	t.Parallel()

	s, _, db := newTestStoreDB(t)
	ctx := t.Context()

	require.ErrorIs(t, s.SetUserPassword(ctx, "nobody", "battery staple"), storeerr.ErrNotFound)

	_, err := s.AddUser(ctx, "linh", "correct horse")
	require.NoError(t, err)

	first, err := s.SignIn(ctx, "linh", "correct horse")
	require.NoError(t, err)
	second, err := s.SignIn(ctx, "linh", "correct horse")
	require.NoError(t, err)

	require.NoError(t, s.SignOut(ctx, first.ID))
	require.NoError(t, s.SignOut(ctx, first.ID), "a session that is gone already")

	_, _, err = s.AuthenticateSession(ctx, first.Secret)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, _, err = s.AuthenticateSession(ctx, second.Secret)
	require.NoError(t, err, "signing out ends only that session")

	require.ErrorIs(t, s.SetUserPassword(ctx, "linh", "short"), storeerr.ErrInvalid)
	require.NoError(t, s.SetUserPassword(ctx, "linh", "battery staple"))

	_, _, err = s.AuthenticateSession(ctx, second.Secret)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "a new password ends every session")

	reasons := map[string]string{}
	rows, err := db.QueryContext(ctx, "SELECT id, revoke_reason FROM web_sessions WHERE revoked_at IS NOT NULL")
	require.NoError(t, err)

	for rows.Next() {
		var id, reason string
		require.NoError(t, rows.Scan(&id, &reason))
		reasons[id] = reason
	}

	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	assert.Equal(t, map[string]string{
		first.ID: string(bearertoken.RevokedSignedOut), second.ID: string(bearertoken.RevokedPasswordChanged),
	}, reasons)

	_, err = s.SignIn(ctx, "linh", "correct horse")
	require.ErrorIs(t, err, userstore.ErrBadCredentials, "the old password")
	_, err = s.SignIn(ctx, "linh", "battery staple")
	require.NoError(t, err)
}
