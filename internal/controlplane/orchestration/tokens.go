package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// Client tokens (issue #41): the owner's and each coordinator's key to the
// client API, as Omnara's personal access tokens. A token is shown once; only
// its SHA-256 is kept. A coordinator token coordinates the Runs it creates.

// Token is a client token without its secret.
type Token struct {
	ID        string
	Name      string
	Kind      bearertoken.Kind
	CreatedAt time.Time
	RevokedAt *time.Time
}

// AddToken records a client token and returns it with its secret, which is
// shown only here.
func (s *Service) AddToken(ctx context.Context, name string, kind bearertoken.Kind) (Token, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || !kind.Valid() {
		return Token{}, "", fmt.Errorf("%w: a token needs a name and a kind (owner or coordinator)", ErrInvalid)
	}

	secret, err := bearertoken.Generate(kind)
	if err != nil {
		return Token{}, "", err
	}

	id := s.newID("tok")

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.InsertClientToken(ctx, store.InsertClientTokenParams{
			ID: id, Name: name, Kind: string(kind), TokenHash: bearertoken.Hash(secret), Now: now,
		}); err != nil {
			return fmt.Errorf("%w: inserting token %q: %w", ErrConflict, name, err)
		}

		return nil
	})
	if err != nil {
		return Token{}, "", fmt.Errorf("adding a token: %w", err)
	}

	t, err := s.GetToken(ctx, id)

	return t, secret, err
}

// GetToken returns one token.
func (s *Service) GetToken(ctx context.Context, id string) (Token, error) {
	row, err := s.read().GetClientToken(ctx, id)
	if err != nil {
		return Token{}, notFound(err, "token", id)
	}

	return tokenFromRow(row), nil
}

// ListTokens returns every token, revoked ones included, by name.
func (s *Service) ListTokens(ctx context.Context) ([]Token, error) {
	rows, err := s.read().ListClientTokens(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing tokens: %w", err)
	}

	out := make([]Token, 0, len(rows))
	for _, r := range rows {
		out = append(out, tokenFromRow(r))
	}

	return out, nil
}

// RevokeToken stops a token from working. Revoking twice is a conflict.
func (s *Service) RevokeToken(ctx context.Context, id string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.RevokeClientToken(ctx, store.RevokeClientTokenParams{Now: sql.NullInt64{Int64: now, Valid: true}, ID: id})
		if err != nil {
			return fmt.Errorf("revoking token %s: %w", id, err)
		}

		if n == 1 {
			return nil
		}

		if _, err := q.GetClientToken(ctx, id); err != nil {
			return notFound(err, "token", id)
		}

		return fmt.Errorf("%w: token %s is already revoked", ErrConflict, id)
	})
}

// AuthenticateToken returns the token a secret belongs to. A malformed,
// unknown or revoked secret is ErrNotFound; the form is checked before the
// database is read.
func (s *Service) AuthenticateToken(ctx context.Context, secret string) (Token, error) {
	kind, err := bearertoken.Parse(secret)
	if err != nil {
		return Token{}, fmt.Errorf("%w: %w", ErrNotFound, err)
	}

	row, err := s.read().GetClientTokenByHash(ctx, bearertoken.Hash(secret))
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, fmt.Errorf("%w: no token has this secret", ErrNotFound)
	}

	if err != nil {
		return Token{}, fmt.Errorf("authenticating a token: %w", err)
	}

	t := tokenFromRow(row)

	switch {
	case t.RevokedAt != nil:
		return Token{}, fmt.Errorf("%w: token %s is revoked", ErrNotFound, t.ID)
	case t.Kind != kind:
		return Token{}, fmt.Errorf("%w: token %s has another kind", ErrNotFound, t.ID)
	}

	return t, nil
}

func tokenFromRow(r store.ClientToken) Token {
	return Token{
		ID: r.ID, Name: r.Name, Kind: bearertoken.Kind(r.Kind), CreatedAt: millis(r.CreatedAt),
		RevokedAt: optionalTime(r.RevokedAt),
	}
}

// Once makes a client's create safe to repeat (an Idempotency-Key, as
// Omnara's): the same caller and key with the same request returns the first
// result; with another request it is ErrIdempotencyConflict. The zero value
// does nothing.
type Once struct {
	// Caller is the token id; Key is the client's key.
	Caller string
	Key    string
}

// ErrIdempotencyConflict means the key was already used for another request.
var ErrIdempotencyConflict = errors.New("idempotency key was used for another request")

// once runs fn inside the open transaction unless the key has a receipt, in
// which case it returns the stored result. It shares the node calls' receipts.
func once[T any](ctx context.Context, q *store.Queries, now int64, o Once, method string, request any,
	fn func() (T, error),
) (T, error) {
	var out T

	if o.Key == "" {
		return fn()
	}

	if o.Caller == "" {
		return out, fmt.Errorf("%w: an idempotency key needs its caller", ErrInvalid)
	}

	hash, err := requestHash(method, request)
	if err != nil {
		return out, err
	}

	r, err := q.GetReceipt(ctx, store.GetReceiptParams{Caller: o.Caller, RequestID: o.Key})
	if err == nil {
		if r.Method != method || r.PayloadHash != hash {
			return out, fmt.Errorf("%w: key %q", ErrIdempotencyConflict, o.Key)
		}

		if err := json.Unmarshal([]byte(r.Response), &out); err != nil {
			return out, fmt.Errorf("decoding the stored result of %q: %w", o.Key, err)
		}

		return out, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return out, fmt.Errorf("reading receipt %q: %w", o.Key, err)
	}

	if out, err = fn(); err != nil {
		return out, err
	}

	response, err := json.Marshal(out)
	if err != nil {
		return out, fmt.Errorf("encoding the result: %w", err)
	}

	if err := q.InsertReceipt(ctx, store.InsertReceiptParams{
		Caller: o.Caller, RequestID: o.Key, Method: method, PayloadHash: hash, Response: string(response), Now: now,
	}); err != nil {
		return out, fmt.Errorf("storing receipt %q: %w", o.Key, err)
	}

	return out, nil
}

func requestHash(method string, request any) (string, error) {
	return payloadHash(method, NodeCall{}, request)
}
