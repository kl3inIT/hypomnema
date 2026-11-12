package subscription_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

var t0 = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// openAI stands in for auth.openai.com: device codes, the token endpoint and
// refresh tokens that are spent by their first use.
type openAI struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// deviceCodesOff makes the server answer 404 to a request for a device code.
	deviceCodesOff bool
	// pendingPolls is how many polls find the owner not done yet.
	pendingPolls int
	// challenge is the PKCE challenge of the browser login in progress.
	challenge string
	// plan is the plan the next id token claims.
	plan string
	// refreshFailure, when set, is the status and body every refresh gets.
	refreshFailure struct {
		status int
		body   string
	}

	issued       int
	validRefresh string
	polls        int
	refreshes    int
	redirects    []string
}

const (
	chatGPTAccount = "0d9d6a2c-chatgpt-account"
	ownerEmail     = "owner@example.com"
)

func newOpenAI(t *testing.T) *openAI {
	t.Helper()

	o := &openAI{t: t, plan: "plus"}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/accounts/deviceauth/usercode", o.userCode)
	mux.HandleFunc("POST /api/accounts/deviceauth/token", o.deviceToken)
	mux.HandleFunc("POST /oauth/token", o.token)
	o.srv = httptest.NewServer(mux)
	t.Cleanup(o.srv.Close)

	return o
}

func (o *openAI) userCode(w http.ResponseWriter, _ *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.deviceCodesOff {
		http.NotFound(w, nil)

		return
	}

	_, _ = io.WriteString(w, `{"device_auth_id":"dev-1","user_code":"ABCD-1234","interval":"5"}`)
}

func (o *openAI) deviceToken(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	var in map[string]string
	if json.NewDecoder(r.Body).Decode(&in) != nil || in["device_auth_id"] != "dev-1" || in["user_code"] != "ABCD-1234" {
		http.Error(w, `{"error":"bad device"}`, http.StatusBadRequest)

		return
	}

	o.polls++
	if o.polls <= o.pendingPolls {
		http.Error(w, `{"error":"authorization_pending"}`, http.StatusForbidden)

		return
	}

	// The server made the PKCE pair itself and hands the verifier over.
	sum := sha256.Sum256([]byte("device-verifier"))
	o.challenge = base64.RawURLEncoding.EncodeToString(sum[:])

	_, _ = io.WriteString(w, `{"authorization_code":"the-code","code_verifier":"device-verifier","code_challenge":"x"}`)
}

func (o *openAI) token(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if err := r.ParseForm(); err != nil || r.PostForm.Get("client_id") != "app_EMoamEEZ73f0CkXaXp7hrann" {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusBadRequest)

		return
	}

	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if r.PostForm.Get("code") != "the-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != o.challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)

			return
		}

		o.redirects = append(o.redirects, r.PostForm.Get("redirect_uri"))
	case "refresh_token":
		o.refreshes++

		if o.refreshFailure.status != 0 {
			w.WriteHeader(o.refreshFailure.status)
			_, _ = io.WriteString(w, o.refreshFailure.body)

			return
		}

		if r.PostForm.Get("refresh_token") != o.validRefresh {
			http.Error(w, `{"error":{"message":"used","code":"refresh_token_reused"}}`, http.StatusBadRequest)

			return
		}
	default:
		http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)

		return
	}

	o.issued++
	o.validRefresh = fmt.Sprintf("refresh-%d", o.issued)

	_, _ = fmt.Fprintf(w, `{"access_token":"access-%d","refresh_token":%q,"id_token":%q,"expires_in":3600}`,
		o.issued, o.validRefresh, idToken(o.plan))
}

func (o *openAI) set(change func(*openAI)) {
	o.mu.Lock()
	defer o.mu.Unlock()

	change(o)
}

func (o *openAI) refreshCalls() int {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.refreshes
}

// idToken is an unsigned JWT with the claims the Codex CLI reads.
func idToken(plan string) string {
	claims := fmt.Sprintf(`{"email":%q,"https://api.openai.com/auth":{"chatgpt_account_id":%q,"chatgpt_plan_type":%q}}`,
		ownerEmail, chatGPTAccount, plan)

	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".sig"
}

type world struct {
	t      *testing.T
	openai *openAI
	clock  *fakeClock
	orch   *orchestration.Service
	vault  *vault.Service
	subs   *subscription.Service
	codex  *subscription.Codex
}

func newWorld(t *testing.T) *world {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	entry, err := vault.GenerateKey("k1")
	require.NoError(t, err)
	keys, err := vault.ParseKeyring(entry, "k1")
	require.NoError(t, err)

	w := &world{t: t, openai: newOpenAI(t), clock: &fakeClock{now: t0}}
	w.orch = orchestration.New(db, w.clock, platform.NewID)
	w.vault = vault.New(db, w.clock, platform.NewID, keys, w.orch.AccountsUsingSecretRef)

	w.codex = subscription.NewCodex(w.openai.srv.URL, w.clock)
	w.codex.PollWithoutWaiting()
	w.subs = subscription.New(w.orch, w.vault, w.clock, slog.New(slog.DiscardHandler), w.codex)

	return w
}

// login logs the owner's account in with a device code.
func (w *world) login() subscription.Account {
	w.t.Helper()

	attempt, err := w.subs.Begin(w.t.Context(), "codex")
	require.NoError(w.t, err)

	a, err := w.subs.Complete(w.t.Context(), attempt, "", "")
	require.NoError(w.t, err)

	return a
}

func (w *world) account(id string) subscription.Account {
	w.t.Helper()

	a, err := w.subs.Get(w.t.Context(), id)
	require.NoError(w.t, err)

	return a
}

// tokens opens the account's secret, as an adapter of the gateway will.
func (w *world) tokens(a subscription.Account) vault.Payload {
	w.t.Helper()

	name, ok := vault.NameFromRef(a.SecretRef)
	require.True(w.t, ok)

	opened, err := w.vault.Open(w.t.Context(), name)
	require.NoError(w.t, err)

	return opened.Payload
}

// Scenario: the owner logs a Codex subscription in from a server without a
// browser. The command shows a link and a code; once the owner has typed the
// code elsewhere, the account exists with who it is at OpenAI, and its tokens
// are in the vault with the time the access token ends.
func TestOwnerLogsACodexAccountInWithADeviceCode(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.openai.set(func(o *openAI) { o.pendingPolls = 2 })

	attempt, err := w.subs.Begin(t.Context(), "codex")
	require.NoError(t, err)
	assert.Equal(t, w.openai.srv.URL+"/codex/device", attempt.URL)
	assert.Equal(t, "ABCD-1234", attempt.UserCode)
	assert.False(t, attempt.Paste)

	a, err := w.subs.Complete(t.Context(), attempt, "", "")
	require.NoError(t, err)

	assert.Equal(t, "codex", a.Runtime)
	assert.Equal(t, ownerEmail, a.Label, "an account with no label given is named by its email")
	assert.Equal(t, chatGPTAccount, a.ExternalID)
	assert.Equal(t, "plus", a.Plan)
	assert.Equal(t, subscription.StateActive, a.State)
	require.NotNil(t, a.AccessTokenExpiresAt)
	assert.Equal(t, t0.Add(time.Hour), *a.AccessTokenExpiresAt)

	tokens := w.tokens(a)
	assert.Equal(t, "access-1", tokens[vault.KeyAccessToken])
	assert.Equal(t, "refresh-1", tokens[vault.KeyRefreshToken])
	assert.Equal(t, []string{w.openai.srv.URL + "/deviceauth/callback"}, w.openai.redirects)

	_, err = w.subs.Begin(t.Context(), "antigravity")
	require.ErrorIs(t, err, subscription.ErrUnknownVendor)
}

// Scenario: OpenAI offers this client no device code. The owner gets a link
// instead, signs in, and pastes the address the browser ended on. An address
// from another login is refused; the right one completes the login with the
// verifier the control plane kept.
func TestWithoutDeviceCodesTheOwnerPastesTheAddressTheBrowserEndedOn(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.openai.set(func(o *openAI) { o.deviceCodesOff = true })

	attempt, err := w.subs.Begin(t.Context(), "codex")
	require.NoError(t, err)
	require.True(t, attempt.Paste)
	assert.Empty(t, attempt.UserCode)

	link, err := url.Parse(attempt.URL)
	require.NoError(t, err)

	q := link.Query()
	assert.Equal(t, "/oauth/authorize", link.Path)
	assert.Equal(t, "http://localhost:1455/auth/callback", q.Get("redirect_uri"))
	assert.Equal(t, "S256", q.Get("code_challenge_method"))
	w.openai.set(func(o *openAI) { o.challenge = q.Get("code_challenge") })

	_, err = w.subs.Complete(t.Context(), attempt, "http://localhost:1455/auth/callback?code=the-code&state=other", "")
	require.ErrorIs(t, err, subscription.ErrLoginFailed)
	require.ErrorContains(t, err, "another login")

	_, err = w.subs.Complete(t.Context(), attempt,
		"http://localhost:1455/auth/callback?error=access_denied&state="+q.Get("state"), "")
	require.ErrorContains(t, err, "access_denied")

	a, err := w.subs.Complete(t.Context(), attempt,
		"  http://localhost:1455/auth/callback?code=the-code&state="+q.Get("state")+"\n", "work")
	require.NoError(t, err)
	assert.Equal(t, "work", a.Label)
	assert.Equal(t, subscription.StateActive, a.State)
	assert.Equal(t, []string{"http://localhost:1455/auth/callback"}, w.openai.redirects)
}

// Scenario: the refresh loop passes. A token far from its end is left alone;
// one that ends within the lead is exchanged, the new refresh token replaces
// the spent one, and a plan that changed at OpenAI shows on the account.
func TestATokenAboutToEndIsRefreshedAndOneThatIsNotIsLeftAlone(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	a := w.login()

	w.clock.advance(20 * time.Minute)
	require.NoError(t, w.subs.RefreshDue(t.Context()))
	assert.Zero(t, w.openai.refreshCalls(), "forty minutes are left; the lead is thirty")

	w.clock.advance(15 * time.Minute)
	w.openai.set(func(o *openAI) { o.plan = "pro" })
	require.NoError(t, w.subs.RefreshDue(t.Context()))
	assert.Equal(t, 1, w.openai.refreshCalls())

	a = w.account(a.ID)
	assert.Equal(t, "pro", a.Plan)
	assert.Equal(t, t0.Add(35*time.Minute+time.Hour), *a.AccessTokenExpiresAt)

	tokens := w.tokens(a)
	assert.Equal(t, "access-2", tokens[vault.KeyAccessToken])
	assert.Equal(t, "refresh-2", tokens[vault.KeyRefreshToken])

	require.NoError(t, w.subs.RefreshDue(t.Context()))
	assert.Equal(t, 1, w.openai.refreshCalls(), "the new token is an hour from its end")
}

// Scenario: OpenAI refuses the refresh token for good, here because it was
// already used. The account asks for a new login and its token is not sent
// again. The owner logs in again: the same account, active, with new tokens.
func TestARefusedRefreshTokenAsksForANewLoginAndIsNotSentAgain(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	a := w.login()

	w.openai.set(func(o *openAI) { o.validRefresh = "rotated-elsewhere" })

	_, err := w.subs.Refresh(t.Context(), a.ID)
	require.ErrorIs(t, err, vault.ErrLoginRequired)

	a = w.account(a.ID)
	assert.Equal(t, subscription.StateLoginRequired, a.State)
	assert.Contains(t, a.Refresh.Error, "refresh_token_reused")
	assert.Nil(t, a.Refresh.RetryAt, "a refused login is not tried again")

	w.clock.advance(2 * time.Hour)
	require.NoError(t, w.subs.RefreshDue(t.Context()))
	assert.Equal(t, 1, w.openai.refreshCalls())

	again := w.login()
	assert.Equal(t, a.ID, again.ID, "the same ChatGPT account keeps its account here")
	assert.Equal(t, subscription.StateActive, again.State)
	assert.Empty(t, again.Refresh.Error)
	assert.Equal(t, "refresh-2", w.tokens(again)[vault.KeyRefreshToken])

	all, err := w.subs.List(t.Context())
	require.NoError(t, err)
	assert.Len(t, all, 1)
}

// Scenario: OpenAI is down when a token is due. The tokens are kept, the
// account stays active, and the reason is recorded without what the server
// sent back. The next pass waits five minutes, then ten; once OpenAI answers
// again the refresh works and the record is cleared.
func TestAFailureThatMayPassKeepsTheTokensAndIsTriedAgainLater(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	a := w.login()

	w.clock.advance(45 * time.Minute)
	w.openai.set(func(o *openAI) {
		o.refreshFailure.status = http.StatusBadGateway
		o.refreshFailure.body = `{"error":{"message":"upstream said refresh-1 is busy","code":"server_error"}}`
	})

	require.Error(t, w.subs.RefreshDue(t.Context()))

	a = w.account(a.ID)
	assert.Equal(t, subscription.StateActive, a.State)
	assert.Equal(t, 1, a.Refresh.Failures)
	assert.Contains(t, a.Refresh.Error, "status 502")
	assert.NotContains(t, a.Refresh.Error, "refresh-1")
	require.NotNil(t, a.Refresh.RetryAt)
	assert.Equal(t, w.clock.Now().Add(5*time.Minute), *a.Refresh.RetryAt)
	assert.Equal(t, "refresh-1", w.tokens(a)[vault.KeyRefreshToken])

	w.clock.advance(4 * time.Minute)
	require.NoError(t, w.subs.RefreshDue(t.Context()))
	assert.Equal(t, 1, w.openai.refreshCalls(), "the next try is not due yet")

	w.clock.advance(time.Minute)
	require.Error(t, w.subs.RefreshDue(t.Context()))
	assert.Equal(t, w.clock.Now().Add(10*time.Minute), *w.account(a.ID).Refresh.RetryAt)

	w.clock.advance(10 * time.Minute)
	w.openai.set(func(o *openAI) { o.refreshFailure.status = 0 })
	require.NoError(t, w.subs.RefreshDue(t.Context()))

	a = w.account(a.ID)
	assert.Zero(t, a.Refresh.Failures)
	assert.Empty(t, a.Refresh.Error)
	assert.Nil(t, a.Refresh.RetryAt)
	assert.Equal(t, "refresh-2", w.tokens(a)[vault.KeyRefreshToken])
}

// Scenario: the gateway asks for a token to call Codex with. It gets the
// account's access token; one about to end is refreshed first. A token the
// backend refused is exchanged once, and a second caller that held the same
// refused token gets the new one without another exchange. While the account
// waits for its usage limit no token is given, and the error says until when.
func TestTheGatewayGetsAValidTokenOfAnAccountThatCanBeUsed(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	_, err := w.subs.Token(t.Context(), "codex", "")
	require.ErrorIs(t, err, subscription.ErrNoAccount)

	a := w.login()

	first, err := w.subs.Token(t.Context(), "codex", "")
	require.NoError(t, err)
	assert.Equal(t, subscription.Token{
		AccountID: a.ID, ExternalID: chatGPTAccount, AccessToken: "access-1", Version: 1,
	}, first)

	w.clock.advance(time.Hour - 30*time.Second)

	fresh, err := w.subs.Token(t.Context(), "codex", "")
	require.NoError(t, err)
	assert.Equal(t, "access-2", fresh.AccessToken)
	assert.Equal(t, 1, w.openai.refreshCalls())

	after, err := w.subs.TokenAfterRefusal(t.Context(), fresh)
	require.NoError(t, err)
	assert.Equal(t, "access-3", after.AccessToken)

	late, err := w.subs.TokenAfterRefusal(t.Context(), fresh)
	require.NoError(t, err)
	assert.Equal(t, after, late)
	assert.Equal(t, 2, w.openai.refreshCalls(), "the token was already replaced; it is not exchanged again")

	until := w.clock.Now().Add(3 * time.Hour)
	require.NoError(t, w.subs.CoolDown(t.Context(), "codex", a.ID, until, "usage limit"))

	_, err = w.subs.Token(t.Context(), "codex", "")

	var cooling *subscription.CoolingError
	require.ErrorAs(t, err, &cooling)
	assert.Equal(t, until, cooling.Until)

	w.clock.advance(3*time.Hour + time.Second)

	_, err = w.subs.Token(t.Context(), "codex", "")
	require.NoError(t, err)
}
