package orchestration

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: the owner makes a user of the web; a wrong name or password is
// refused alike, a right one opens a session for 30 days (issue #114).
func TestAUserSignsInWithTheirPassword(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	ctx := t.Context()

	for _, bad := range []struct{ name, password string }{
		{"Linh", "correct horse"},
		{"linh", "short"},
		{"linh", strings.Repeat("x", 73)},
	} {
		_, err := s.AddUser(ctx, bad.name, bad.password)
		require.ErrorIs(t, err, ErrInvalid, "%q / %d characters", bad.name, len(bad.password))
	}

	u, err := s.AddUser(ctx, "linh", "correct horse")
	require.NoError(t, err)
	assert.Equal(t, User{ID: u.ID, Name: "linh", CreatedAt: t0, UpdatedAt: t0}, u)

	_, err = s.AddUser(ctx, "linh", "another horse")
	require.ErrorIs(t, err, ErrConflict, "a name is one user's")

	_, err = s.AddUser(ctx, "an", "battery staple")
	require.NoError(t, err)

	users, err := s.ListUsers(ctx)
	require.NoError(t, err)
	require.Len(t, users, 2)
	assert.Equal(t, []string{"an", "linh"}, []string{users[0].Name, users[1].Name}, "by name")

	_, err = s.SignIn(ctx, "linh", "wrong horse")
	require.ErrorIs(t, err, ErrBadCredentials)
	_, err = s.SignIn(ctx, "nobody", "correct horse")
	require.ErrorIs(t, err, ErrBadCredentials, "an unknown name is told the same")

	session, err := s.SignIn(ctx, "linh", "correct horse")
	require.NoError(t, err)
	assert.NotEmpty(t, session.Secret)
	assert.Equal(t, t0.Add(SessionLifetime), session.ExpiresAt)

	tok, got, err := s.AuthenticateSession(ctx, session.Secret)
	require.NoError(t, err)
	assert.Equal(t, u.ID, tok.ID)
	assert.Equal(t, "linh", tok.Name)
	assert.Equal(t, Session{ID: session.ID, ExpiresAt: session.ExpiresAt}, got)

	for _, secret := range []string{"", "not a session"} {
		_, _, err := s.AuthenticateSession(ctx, secret)
		require.ErrorIs(t, err, ErrNotFound, "%q", secret)
	}
}

// A session in use is extended, written at most once an hour; one left
// unused for 30 days has ended.
func TestASessionInUseIsExtendedAndAnUnusedOneEnds(t *testing.T) {
	t.Parallel()

	s, _, clock := newTestService(t)
	ctx := t.Context()

	_, err := s.AddUser(ctx, "linh", "correct horse")
	require.NoError(t, err)

	session, err := s.SignIn(ctx, "linh", "correct horse")
	require.NoError(t, err)

	clock.now = t0.Add(59 * time.Minute)
	_, got, err := s.AuthenticateSession(ctx, session.Secret)
	require.NoError(t, err)
	assert.Equal(t, t0.Add(SessionLifetime), got.ExpiresAt, "not written within the hour")

	clock.now = t0.Add(time.Hour)
	_, got, err = s.AuthenticateSession(ctx, session.Secret)
	require.NoError(t, err)
	assert.Equal(t, clock.now.Add(SessionLifetime), got.ExpiresAt, "extended after an hour")

	clock.now = got.ExpiresAt
	_, _, err = s.AuthenticateSession(ctx, session.Secret)
	require.ErrorIs(t, err, ErrNotFound, "ended")
}

// A new password ends every session of the user; signing out ends one, and
// signing out again is no error.
func TestANewPasswordAndSigningOutEndSessions(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	ctx := t.Context()

	require.ErrorIs(t, s.SetUserPassword(ctx, "nobody", "battery staple"), ErrNotFound)

	_, err := s.AddUser(ctx, "linh", "correct horse")
	require.NoError(t, err)

	first, err := s.SignIn(ctx, "linh", "correct horse")
	require.NoError(t, err)
	second, err := s.SignIn(ctx, "linh", "correct horse")
	require.NoError(t, err)

	require.NoError(t, s.SignOut(ctx, first.ID))
	require.NoError(t, s.SignOut(ctx, first.ID), "a session that is gone already")

	_, _, err = s.AuthenticateSession(ctx, first.Secret)
	require.ErrorIs(t, err, ErrNotFound)
	_, _, err = s.AuthenticateSession(ctx, second.Secret)
	require.NoError(t, err, "signing out ends only that session")

	require.ErrorIs(t, s.SetUserPassword(ctx, "linh", "short"), ErrInvalid)
	require.NoError(t, s.SetUserPassword(ctx, "linh", "battery staple"))

	_, _, err = s.AuthenticateSession(ctx, second.Secret)
	require.ErrorIs(t, err, ErrNotFound, "a new password ends every session")

	_, err = s.SignIn(ctx, "linh", "correct horse")
	require.ErrorIs(t, err, ErrBadCredentials, "the old password")
	_, err = s.SignIn(ctx, "linh", "battery staple")
	require.NoError(t, err)
}
