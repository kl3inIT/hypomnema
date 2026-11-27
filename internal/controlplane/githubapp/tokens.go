package githubapp

import (
	"context"
	"sync"
	"time"
)

// TokenMargin is how long a token handed out still has at least: one closer to its end is made again. As Omnara
// hands out git credentials with 15 minutes left at least (git_credentials.go:16, gitCredentialExpiryMargin), so a
// gh or git call that starts with it ends before it does.
const TokenMargin = 15 * time.Minute

// Tokens keeps each installation's token until TokenMargin before its end, so a worker that asks at each gh or git
// call costs GitHub one request an installation in 45 minutes, as Omnara's installation token cache
// (installation_token_cache.go): one maker at a time a key, the others wait for its token.
type Tokens struct {
	Client Client

	mu      sync.Mutex
	entries map[tokenKey]*tokenEntry
}

type tokenKey struct {
	app          int64
	installation int64
}

type tokenEntry struct {
	gate  chan struct{}
	token Token
}

// Of is a token of the installation, from the cache or made now.
func (t *Tokens) Of(ctx context.Context, cred Credential, installation int64) (Token, error) {
	key := tokenKey{app: cred.AppID, installation: installation}

	t.mu.Lock()
	if t.entries == nil {
		t.entries = map[tokenKey]*tokenEntry{}
	}

	e, ok := t.entries[key]
	if !ok {
		e = &tokenEntry{gate: make(chan struct{}, 1)}
		t.entries[key] = e
	}
	t.mu.Unlock()

	select {
	case e.gate <- struct{}{}:
		defer func() { <-e.gate }()
	case <-ctx.Done():
		return Token{}, ctx.Err()
	}

	if e.token.Value != "" && e.token.ExpiresAt.After(t.Client.now().Add(TokenMargin)) {
		return e.token, nil
	}

	fresh, err := t.Client.mint(ctx, cred, installation)
	if err != nil {
		return Token{}, err
	}

	e.token = fresh

	return fresh, nil
}

// Forget drops every token kept, as a new App's credential replaces the old one's.
func (t *Tokens) Forget() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.entries = nil
}
