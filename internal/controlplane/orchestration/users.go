package orchestration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// The web's built-in account (issue #114). Each rule names the reference it
// is learned from (docs/research/2026-11-10-web-login.md):
//
//   - no password is shipped; the owner makes a user on the server, with a
//     password of at least 8 characters: Coder and Omnigent;
//   - the password is kept as bcrypt at cost 10: 9router, 9router-go,
//     new-api and CLIProxyAPI;
//   - a session is a row holding the SHA-256 of the cookie's secret: Coder,
//     and how client tokens are kept here;
//   - it lasts 30 days (the owner, 2026-11-10, with new-api's number) and is
//     extended as it is used, written at most once an hour: Coder;
//   - a new password ends every session of the user: Coder, Omnigent, new-api;
//   - an unknown name costs the same work as a wrong password: Omnigent.

const (
	// SessionLifetime is how long a web session lasts after its last use.
	SessionLifetime = 30 * 24 * time.Hour
	// sessionExtendEvery bounds how often a session in use is written.
	sessionExtendEvery = time.Hour
	// MinPasswordLength is Omnigent's; bcrypt reads no more than 72 bytes.
	MinPasswordLength = 8
	maxPasswordBytes  = 72
	passwordCost      = 10
	sessionBytes      = 32
)

// ErrBadCredentials says a name and a password do not match a user. It does
// not say which of the two is wrong.
var ErrBadCredentials = errors.New("the name or the password is wrong")

// User is someone who signs in to the web. A user acts as the owner.
type User struct {
	ID        string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Session is a signed-in browser.
type Session struct {
	ID string
	// Secret is what the cookie holds; set only by SignIn.
	Secret    string
	ExpiresAt time.Time
}

// dummyHash is compared against when no user has the name, so that the
// answer takes as long as for a wrong password.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("no user has this password"), passwordCost)

func hashPassword(password string) (string, error) {
	switch {
	case len([]rune(password)) < MinPasswordLength:
		return "", fmt.Errorf("%w: a password has at least %d characters", ErrInvalid, MinPasswordLength)
	case len(password) > maxPasswordBytes:
		return "", fmt.Errorf("%w: a password has at most %d bytes", ErrInvalid, maxPasswordBytes)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), passwordCost)
	if err != nil {
		return "", fmt.Errorf("hashing the password: %w", err)
	}

	return string(hash), nil
}

// AddUser makes a user of the web. The password is hashed; it is kept nowhere.
func (s *Service) AddUser(ctx context.Context, name, password string) (User, error) {
	if !namePattern.MatchString(name) {
		return User{}, fmt.Errorf("%w: a user's name is lowercase letters, digits and dashes", ErrInvalid)
	}

	hash, err := hashPassword(password)
	if err != nil {
		return User{}, err
	}

	id := s.newID("usr")

	var out User

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.InsertUser(ctx, store.InsertUserParams{ID: id, Name: name, PasswordHash: hash, Now: now}); err != nil {
			return fmt.Errorf("%w: inserting user %q: %w", ErrConflict, name, err)
		}

		out = User{ID: id, Name: name, CreatedAt: millis(now), UpdatedAt: millis(now)}

		return nil
	})
	if err != nil {
		return User{}, fmt.Errorf("adding a user: %w", err)
	}

	return out, nil
}

// SetUserPassword gives a user a new password and ends every session of theirs.
func (s *Service) SetUserPassword(ctx context.Context, name, password string) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		u, err := q.GetUserByName(ctx, name)
		if err != nil {
			return notFound(err, "user", name)
		}

		if _, err := q.SetUserPassword(ctx, store.SetUserPasswordParams{PasswordHash: hash, Now: now, Name: name}); err != nil {
			return fmt.Errorf("storing the password of %s: %w", name, err)
		}

		if err := q.DeleteWebSessionsOfUser(ctx, u.ID); err != nil {
			return fmt.Errorf("ending the sessions of %s: %w", name, err)
		}

		return nil
	})
}

// ListUsers returns the users by name.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.read().ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}

	out := make([]User, 0, len(rows))
	for _, r := range rows {
		out = append(out, User{ID: r.ID, Name: r.Name, CreatedAt: millis(r.CreatedAt), UpdatedAt: millis(r.UpdatedAt)})
	}

	return out, nil
}

// SignIn checks a name and a password and opens a session. A wrong name or
// password is ErrBadCredentials.
func (s *Service) SignIn(ctx context.Context, name, password string) (Session, error) {
	u, err := s.read().GetUserByName(ctx, name)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))

		return Session{}, ErrBadCredentials
	case err != nil:
		return Session{}, fmt.Errorf("reading user %s: %w", name, err)
	}

	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return Session{}, ErrBadCredentials
	}

	raw := make([]byte, sessionBytes)
	if _, err := rand.Read(raw); err != nil {
		return Session{}, fmt.Errorf("making a session's secret: %w", err)
	}

	out := Session{ID: s.newID("ses"), Secret: base64.RawURLEncoding.EncodeToString(raw)}

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		// Sessions that ended are cleared when someone signs in.
		if err := q.DeleteExpiredWebSessions(ctx, now); err != nil {
			return fmt.Errorf("clearing ended sessions: %w", err)
		}

		expires := now + SessionLifetime.Milliseconds()
		out.ExpiresAt = millis(expires)

		return q.InsertWebSession(ctx, store.InsertWebSessionParams{
			ID: out.ID, UserID: u.ID, SecretHash: bearertoken.Hash(out.Secret), Now: now, ExpiresAt: expires,
		})
	})
	if err != nil {
		return Session{}, fmt.Errorf("opening a session: %w", err)
	}

	return out, nil
}

// AuthenticateSession returns who a cookie's secret signs in, as the owner,
// and the session. An unknown or ended session is ErrNotFound. A session in
// use is extended.
func (s *Service) AuthenticateSession(ctx context.Context, secret string) (Token, Session, error) {
	if secret == "" {
		return Token{}, Session{}, fmt.Errorf("%w: no session", ErrNotFound)
	}

	row, err := s.read().GetWebSessionByHash(ctx, bearertoken.Hash(secret))
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, Session{}, fmt.Errorf("%w: no session has this secret", ErrNotFound)
	}

	if err != nil {
		return Token{}, Session{}, fmt.Errorf("authenticating a session: %w", err)
	}

	now := s.clock.Now().UnixMilli()
	if row.ExpiresAt <= now {
		return Token{}, Session{}, fmt.Errorf("%w: session %s has ended", ErrNotFound, row.ID)
	}

	expires := row.ExpiresAt

	if fresh := now + SessionLifetime.Milliseconds(); fresh-expires >= sessionExtendEvery.Milliseconds() {
		err := s.write(ctx, func(q *store.Queries, _ int64) error {
			return q.ExtendWebSession(ctx, store.ExtendWebSessionParams{ExpiresAt: fresh, ID: row.ID})
		})
		if err != nil {
			return Token{}, Session{}, fmt.Errorf("extending session %s: %w", row.ID, err)
		}

		expires = fresh
	}

	return Token{ID: row.UserID, Name: row.UserName, Kind: bearertoken.Owner},
		Session{ID: row.ID, ExpiresAt: millis(expires)}, nil
}

// SignOut ends a session; one that is gone already is no error.
func (s *Service) SignOut(ctx context.Context, sessionID string) error {
	return s.write(ctx, func(q *store.Queries, _ int64) error {
		if err := q.DeleteWebSession(ctx, sessionID); err != nil {
			return fmt.Errorf("ending session %s: %w", sessionID, err)
		}

		return nil
	})
}
