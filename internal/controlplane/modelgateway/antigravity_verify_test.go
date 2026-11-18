package modelgateway_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
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

// TokenAfterRefusal gives no token of an account that rests until its next try, as the control plane's accounts do.
func (a *verifyingAccounts) TokenAfterRefusal(
	ctx context.Context, refused modelgateway.AccountToken,
) (modelgateway.AccountToken, error) {
	a.mu.Lock()
	resting := a.cooled[refused.AccountID].After(time.Now())
	a.mu.Unlock()

	if resting {
		return modelgateway.AccountToken{}, fmt.Errorf("%w: account %s", modelgateway.ErrAccountResting, refused.AccountID)
	}

	return a.googleAccount.TokenAfterRefusal(ctx, refused)
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

// verifyError is Google's error for an account it wants verified (CLIProxyAPI#6182), with a help link before the
// ErrorInfo, as google.rpc errors may carry one.
const verifyError = `{"code":403,"message":"Verify your account to continue.","status":"PERMISSION_DENIED",
  "details":[{"@type":"type.googleapis.com/google.rpc.Help","links":[{"description":"Help",
  "url":"https://support.google.com/accounts/answer/help"}]},
  {"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"VALIDATION_REQUIRED",
  "metadata":{"validation_url":"https://accounts.google.com/signin/continue?plt=1"}}]}`

const verifyAnswer = `{"error":` + verifyError + `}`

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

// Scenario (issue #822, review of #826): the marked account's next try has come and Google answers 200, but the
// answer does not end well: the stream is empty, carries an error, or asks again for the account to be verified.
// None of these is a success: the mark stays, and the last one marks the account again.
func TestAFailedStreamDoesNotClearTheMark(t *testing.T) {
	t.Parallel()

	for name, events := range map[string][]string{
		"empty": {},
		"an error inside": {
			event(`{"error":{"code":503,"status":"UNAVAILABLE","message":"The service is currently unavailable."}}`),
		},
		"verification asked inside": {event(`{"error":` + strings.ReplaceAll(verifyError, "\n", "") + `}`)},
		"cut before its end": {
			event(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hel"}]}}]}}`),
		},
	} {
		for _, streamed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, streamed %t", name, streamed), func(t *testing.T) {
				t.Parallel()

				backend, _ := cloudCode(t, stream(events...))
				account := &verifyingAccounts{verifying: []string{"acc_1"}}
				g := newAntigravityGateway(t, backend, account)

				_, _, body := g.call(t, g.key, askFlash+fmt.Sprintf(`,"stream":%t}`, streamed))

				marks, cleared := account.marked()
				assert.Empty(t, cleared, "not a success: %s", body)

				if name == "verification asked inside" {
					assert.Equal(t, []string{"https://accounts.google.com/signin/continue?plt=1"}, marks["acc_1"])
				}
			})
		}
	}
}

// Scenario (issue #822, review of #826): acc_1's token is refused with a 401 while another request marks acc_1. The
// retry is not sent under acc_1 before its next try: the next account serves the request.
func TestTheRetryAfterARefusalGoesToTheNextAccountWhenMarked(t *testing.T) {
	t.Parallel()

	account := &verifyingAccounts{googleAccount: googleAccount{accounts: accounts{ids: []string{"acc_1", "acc_2"}}}}
	backend, got := cloudCode(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer access-0" { // acc_1's token, refused; meanwhile acc_1 is marked
			_, _ = account.NeedsVerification(r.Context(), "acc_1", "https://accounts.google.com/x", "")

			plain(http.StatusUnauthorized, `{"error":{"code":401,"status":"UNAUTHENTICATED"}}`)(w, r)

			return
		}

		stream(geminiEvents...)(w, r)
	})
	g := newAntigravityGateway(t, backend, account)

	status, _, body := g.call(t, g.key, askFlash+`}`)
	assert.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, 2, got.calls(), "acc_1 once, then acc_2: no resend under acc_1")

	header, _ := got.last(t)
	assert.Equal(t, "Bearer acc_2-access-0", header.Get("Authorization"))
}
