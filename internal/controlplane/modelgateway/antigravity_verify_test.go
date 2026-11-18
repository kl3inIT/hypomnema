package modelgateway_test

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
)

// verifyingAccounts are Antigravity accounts whose store keeps the mark of one Google wants verified (issue #822),
// as the control plane's does: a marked account is passed over until its next try, ten minutes here.
type verifyingAccounts struct {
	googleAccount

	vmu sync.Mutex
	// marks are the links each marked account was given, in order; verifying are the accounts whose next try has
	// come; cleared are the accounts Verified was called for.
	marks     map[string][]string
	verifying []string
	cleared   []string
}

func (a *verifyingAccounts) Token(
	ctx context.Context, runtime, session string, skip ...string,
) (modelgateway.AccountToken, error) {
	token, err := a.googleAccount.Token(ctx, runtime, session, skip...)

	a.vmu.Lock()
	defer a.vmu.Unlock()

	token.Verifying = slices.Contains(a.verifying, token.AccountID)

	return token, err
}

func (a *verifyingAccounts) NeedsVerification(_ context.Context, id, url, _ string) (time.Time, error) {
	a.vmu.Lock()
	if a.marks == nil {
		a.marks = map[string][]string{}
	}

	a.marks[id] = append(a.marks[id], url)
	a.vmu.Unlock()

	retry := time.Now().Add(10 * time.Minute)

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cooled == nil {
		a.cooled, a.waits = map[string]time.Time{}, map[string][]time.Duration{}
	}

	a.cooled[id] = retry

	return retry, nil
}

func (a *verifyingAccounts) Verified(_ context.Context, id string) error {
	a.vmu.Lock()
	defer a.vmu.Unlock()

	a.cleared = append(a.cleared, id)

	return nil
}

func (a *verifyingAccounts) marked() (map[string][]string, []string) {
	a.vmu.Lock()
	defer a.vmu.Unlock()

	return a.marks, slices.Clone(a.cleared)
}

const verifyAnswer = `{"error":{"code":403,"message":"Verify your account to continue.","status":"PERMISSION_DENIED",
  "details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"VALIDATION_REQUIRED",
  "metadata":{"validation_url":"https://accounts.google.com/signin/continue?plt=1"}}]}}`

// Scenario (issue #822): Google answers a chat under the first Antigravity account with 403 VALIDATION_REQUIRED.
// The account is marked with Google's link and the request is served by the next account; the next request goes to
// that account at once, without trying the marked one, and nothing is cooled as a plain 403 would be.
func TestAChatGoogleWantsVerifiedMarksTheAccountAndTheNextServes(t *testing.T) {
	t.Parallel()

	backend, got := cloudCode(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer access-0" { // acc_1's token
			plain(http.StatusForbidden, verifyAnswer)(w, r)

			return
		}

		stream(geminiEvents...)(w, r)
	})
	account := &verifyingAccounts{googleAccount: googleAccount{accounts: accounts{ids: []string{"acc_1", "acc_2"}}}}
	g := newAntigravityGateway(t, backend, account)

	status, _, body := g.call(t, g.key, askFlash+`}`)
	assert.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, "hello")
	assert.Equal(t, 2, got.calls(), "acc_1, then acc_2")

	marks, cleared := account.marked()
	assert.Equal(t, map[string][]string{"acc_1": {"https://accounts.google.com/signin/continue?plt=1"}}, marks)
	assert.Empty(t, cleared)
	assert.Empty(t, account.waited("acc_1"), "marked, not cooled as a plain 403")

	status, _, _ = g.call(t, g.key, askFlash+`}`)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, 3, got.calls(), "the marked account is not tried")

	header, _ := got.last(t)
	assert.Equal(t, "Bearer acc_2-access-0", header.Get("Authorization"))
}

// Scenario (issue #822): the only account is marked and its next try has come. A chat that works under it clears
// the mark; a chat under an account that is not marked clears nothing.
func TestAChatThatWorksClearsTheMark(t *testing.T) {
	t.Parallel()

	backend, _ := cloudCode(t, stream(geminiEvents...))
	account := &verifyingAccounts{verifying: []string{"acc_1"}}
	g := newAntigravityGateway(t, backend, account)

	status, _, body := g.call(t, g.key, askFlash+`}`)
	require.Equal(t, http.StatusOK, status, body)

	_, cleared := account.marked()
	assert.Equal(t, []string{"acc_1"}, cleared)

	plainAccount := &verifyingAccounts{}
	g = newAntigravityGateway(t, backend, plainAccount)

	status, _, _ = g.call(t, g.key, askFlash+`}`)
	require.Equal(t, http.StatusOK, status)

	_, cleared = plainAccount.marked()
	assert.Empty(t, cleared)
}
