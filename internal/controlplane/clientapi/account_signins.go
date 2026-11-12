package clientapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
)

// Signing a subscription account in from the Models page (issue #199, step
// 2). The two flows are the ones `hypomnema accounts login` has: a link whose
// last address is pasted back, or a link and a code. 9router's dashboard does
// the same on a remote host (OAuthModal.js) and CLIProxyAPI's management API
// has an auth URL and a pasted callback (server_management.go).

// maxAccountSignIns bounds the sign-ins waiting to be completed.
const maxAccountSignIns = 16

// accountSignIn is a sign-in begun and not completed. The attempt holds what
// the vendor needs to finish it, so it is kept in memory and never leaves.
type accountSignIn struct {
	attempt *subscription.Attempt
	label   string
	expires time.Time
}

type accountSignIns struct {
	mu      sync.Mutex
	waiting map[string]accountSignIn
}

func (a *accountSignIns) add(signIn accountSignIn, now time.Time) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("making a sign-in's id: %w", err)
	}

	id := hex.EncodeToString(raw)

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.waiting == nil {
		a.waiting = map[string]accountSignIn{}
	}

	for old, s := range a.waiting {
		if now.After(s.expires) {
			delete(a.waiting, old)
		}
	}

	if len(a.waiting) >= maxAccountSignIns {
		return "", fmt.Errorf("%w: too many sign-ins are waiting; complete one or wait for it to end",
			orchestration.ErrUnavailable)
	}

	a.waiting[id] = signIn

	return id, nil
}

// take returns a sign-in once, if it has not ended.
func (a *accountSignIns) take(id string, now time.Time) (accountSignIn, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	signIn, ok := a.waiting[id]
	delete(a.waiting, id)

	return signIn, ok && now.Before(signIn.expires)
}

// BeginAccountSignIn starts a sign-in at the vendor of a runtime and answers
// with the link the owner opens.
func (s *Server) BeginAccountSignIn(ctx context.Context, req openapi.BeginAccountSignInRequestObject,
) (openapi.BeginAccountSignInResponseObject, error) {
	if s.accounts == nil || s.secrets == nil {
		return nil, fmt.Errorf("%w: a sign-in's tokens are kept in the vault, which is not set up",
			orchestration.ErrUnavailable)
	}

	runtime := string(req.Body.Runtime)

	attempt, err := s.accounts.Begin(ctx, runtime)
	if err != nil {
		if errors.Is(err, subscription.ErrUnknownVendor) {
			return nil, fmt.Errorf("%w: %w", orchestration.ErrInvalid, err)
		}

		return nil, fmt.Errorf("%w: starting the sign-in: %w", orchestration.ErrUnavailable, err)
	}

	now := s.now()

	id, err := s.accountSignIns.add(accountSignIn{
		attempt: attempt, label: val(req.Body.Label), expires: now.Add(attempt.ExpiresIn),
	}, now)
	if err != nil {
		return nil, err
	}

	out := openapi.AccountSignInAttempt{
		Id: id, Runtime: runtime, Url: attempt.URL, Paste: attempt.Paste,
		ExpiresInSeconds: int(attempt.ExpiresIn.Seconds()),
	}
	if attempt.UserCode != "" {
		out.UserCode = &attempt.UserCode
	}

	return openapi.BeginAccountSignIn200JSONResponse(out), nil
}

// CompleteAccountSignIn finishes a sign-in: with the address the browser
// ended on, or by waiting for the owner to enter the code at the vendor. The
// address holds the code, so it is not logged. A sign-in is completed once:
// one that failed is begun again.
func (s *Server) CompleteAccountSignIn(ctx context.Context, req openapi.CompleteAccountSignInRequestObject,
) (openapi.CompleteAccountSignInResponseObject, error) {
	if s.accounts == nil {
		return nil, fmt.Errorf("%w: accounts are not signed in on this control plane", orchestration.ErrUnavailable)
	}

	signIn, ok := s.accountSignIns.take(req.SignInId, s.now())
	if !ok {
		return nil, fmt.Errorf("%w: this sign-in has ended or was completed; start it again", orchestration.ErrNotFound)
	}

	pasted := val(req.Body.Callback)
	if signIn.attempt.Paste && pasted == "" {
		return nil, fmt.Errorf("%w: this sign-in needs the address the browser ended on; start it again",
			orchestration.ErrInvalid)
	}

	a, err := s.accounts.Complete(ctx, signIn.attempt, pasted, signIn.label)
	if err != nil {
		if errors.Is(err, subscription.ErrLoginFailed) {
			return nil, fmt.Errorf("%w: %w", orchestration.ErrInvalid, err)
		}

		return nil, err
	}

	return openapi.CompleteAccountSignIn200JSONResponse(toAccount(a)), nil
}

// AddTokenAccount keeps an account from the token of `claude setup-token`
// the owner pastes (issue #199): the other way to add a Claude account,
// beside signing in, as 9router's dialog has "Paste token" beside its browser
// sign-in (OAuthModal.js) and OpenClaw has `models auth paste-token`. The
// token is not logged and never answered.
func (s *Server) AddTokenAccount(ctx context.Context, req openapi.AddTokenAccountRequestObject,
) (openapi.AddTokenAccountResponseObject, error) {
	if s.tokenAccounts == nil {
		return nil, fmt.Errorf("%w: a pasted token is kept in the vault, which has no key here",
			orchestration.ErrUnavailable)
	}

	id, err := s.tokenAccounts.Add(ctx, string(req.Body.Runtime), req.Body.Label, val(req.Body.Token))
	if err != nil {
		return nil, err
	}

	a, err := s.account(ctx, id)
	if err != nil {
		return nil, err
	}

	return openapi.AddTokenAccount201JSONResponse(a), nil
}

// MoveAccount moves an account one place among the accounts of its runtime:
// the gateway and placement take the first that can serve (9router moves a
// connection up and down the same way, providers/[id]/page.js).
func (s *Server) MoveAccount(ctx context.Context, req openapi.MoveAccountRequestObject,
) (openapi.MoveAccountResponseObject, error) {
	if err := s.svc.MoveAccount(ctx, req.AccountId, req.Body.Direction == openapi.Up); err != nil {
		return nil, err
	}

	a, err := s.account(ctx, req.AccountId)
	if err != nil {
		return nil, err
	}

	return openapi.MoveAccount200JSONResponse(a), nil
}
