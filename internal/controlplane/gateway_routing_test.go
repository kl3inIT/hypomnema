package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// routingWorld is the gateway over three Claude accounts, first, second and third, and two Codex accounts, cx-one and
// cx-two, whose backend is at its rate limit for every one of them: it keeps the tokens it was sent, in order.
type routingWorld struct {
	db       *sql.DB
	gateway  *modelgateway.Service
	mux      *http.ServeMux
	accounts map[string]string
	mu       sync.Mutex
	sent     []string
}

func newRoutingWorld(t *testing.T) *routingWorld {
	t.Helper()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	clock, logger := platform.SystemClock{}, slog.New(slog.DiscardHandler)
	var nextID atomic.Int64
	newID := func(prefix string) string { return fmt.Sprintf("%s_%03d", prefix, nextID.Add(1)) }
	orch := orchestration.New(db, clock, newID)
	keyring, err := vault.ParseKeyring(keygen(t, "test"), "test")
	require.NoError(t, err)
	secrets := vault.New(db, clock, newID, keyring, nil)

	w := &routingWorld{db: db, accounts: map[string]string{}}

	for _, name := range []string{"first", "second", "third", "cx-one", "cx-two"} {
		runtime := "claude"
		if strings.HasPrefix(name, "cx-") {
			runtime = "codex"
		}
		account, err := orch.SaveAccountLogin(ctx,
			orchestration.AccountLogin{Runtime: runtime, ExternalID: name, Label: name},
			func(string) string { return vault.Ref(name) })
		require.NoError(t, err)
		_, err = secrets.Create(ctx, vault.NewSecret{
			Name: name, Kind: vault.KindOAuthTokenSet,
			Payload: vault.Payload{vault.KeyAccessToken: name, vault.KeyRefreshToken: "renewal"},
		})
		require.NoError(t, err)
		w.accounts[name] = account.ID
	}

	backend := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		w.sent = append(w.sent, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.mu.Unlock()

		rw.Header().Set("Content-Type", "application/json")
		rw.Header().Set("Retry-After", "120")
		rw.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(rw, `{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`)
	}))
	t.Cleanup(backend.Close)

	subs := subscription.New(orch, secrets, clock, logger, &refusalVendor{runtime: "claude"},
		&refusalVendor{runtime: "codex"})
	w.gateway = modelgateway.New(db, clock, newID, gatewayProviders(orch),
		func(_ context.Context, _ string) (string, error) { return "", nil })
	w.mux = http.NewServeMux()
	modelgateway.NewServer(w.gateway, logger, modelgateway.Config{
		Subscriptions: gatewayAccounts{subs: subs}, ClaudeBaseURL: backend.URL, CodexBaseURL: backend.URL,
	}).Register(w.mux)

	return w
}

// ask sends a request for a Claude model with a key and returns the answer and the tokens the backend was sent.
func (w *routingWorld) ask(t *testing.T, key, model string) (*httptest.ResponseRecorder, []string) {
	t.Helper()

	w.mu.Lock()
	w.sent = nil
	w.mu.Unlock()

	body := `{"model":"` + model + `","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)

	w.mu.Lock()
	defer w.mu.Unlock()

	return rec, append([]string(nil), w.sent...)
}

const routedClaudeModel = "claude-subscription/claude-opus-5-5"

// sharedCoolings counts the rows the scheduler reads to hold back work on a cooling model (scheduler.go placeTask).
func (w *routingWorld) sharedCoolings(t *testing.T) int {
	t.Helper()

	var n int
	require.NoError(t, w.db.QueryRowContext(t.Context(), "SELECT count(*) FROM gateway_model_cooling").Scan(&n))

	return n
}

// Scenario (review of PR #1298): a key limited to two accounts gets 429 when both rest, but the third account still
// serves every other key: nothing is written of the model's cooling for all. A key over every account, refused by
// all three, writes it.
func TestALimitedKeysLimitIsNotEveryones(t *testing.T) {
	t.Parallel()

	w := newRoutingWorld(t)
	_, limited, err := w.gateway.AddRoutedKey(t.Context(), "beyondpilot", nil, nil, modelgateway.KeyRouting{
		Accounts: []string{w.accounts["first"], w.accounts["second"]},
	})
	require.NoError(t, err)

	rec, _ := w.ask(t, limited, routedClaudeModel)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	assert.Zero(t, w.sharedCoolings(t), "the key's 429 speaks for its own accounts")

	_, every, err := w.gateway.AddKey(t.Context(), "laptop", nil, nil)
	require.NoError(t, err)

	rec, _ = w.ask(t, every, routedClaudeModel)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	assert.Positive(t, w.sharedCoolings(t), "every account refused: the model cools for all")
}

// Scenario (issue #1277): a key limited to two of three Claude accounts is served only by them. When both are at
// their limit it gets 429 with when to retry, and the third account is never tried; a Codex model, whose provider has
// none of its accounts, is refused before anything is sent.
func TestAKeyLimitedToTwoAccountsUsesOnlyThem(t *testing.T) {
	t.Parallel()

	w := newRoutingWorld(t)
	_, key, err := w.gateway.AddRoutedKey(t.Context(), "beyondpilot", nil, nil, modelgateway.KeyRouting{
		Accounts: []string{w.accounts["first"], w.accounts["second"]},
	})
	require.NoError(t, err)

	rec, sent := w.ask(t, key, routedClaudeModel)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	assert.ElementsMatch(t, []string{"first", "second"}, sent, "only the key's accounts are tried")

	rec, sent = w.ask(t, key, routedClaudeModel)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	assert.Empty(t, sent, "both rest, and the third is not tried in their place")

	rec, sent = w.ask(t, key, "codex-subscription/gpt-6.1-sol")
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "none of the accounts it may use serves it")
	assert.Empty(t, sent)
}

// Scenario (issue #1277): a key with no routing set is served as before: every account is tried in turn.
func TestAKeyWithNoRoutingUsesEveryAccount(t *testing.T) {
	t.Parallel()

	w := newRoutingWorld(t)
	_, key, err := w.gateway.AddKey(t.Context(), "laptop", nil, nil)
	require.NoError(t, err)

	rec, sent := w.ask(t, key, routedClaudeModel)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	assert.ElementsMatch(t, []string{"first", "second", "third"}, sent)
}

// Scenario (review of PR #1298): a key limited to the two Codex accounts asks for an image naming no provider while
// both rest. Antigravity, which the key has no account of, is not its provider: the answer is Codex's 429 with
// Retry-After, not a 502 that mixes in a provider the key may not use.
func TestALimitedKeysImageWaitsForItsAccounts(t *testing.T) {
	t.Parallel()

	w := newRoutingWorld(t)
	_, key, err := w.gateway.AddRoutedKey(t.Context(), "artist", nil, nil, modelgateway.KeyRouting{
		Accounts: []string{w.accounts["cx-one"], w.accounts["cx-two"]},
	})
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"prompt":"a cat"}`))
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	assert.NotContains(t, rec.Body.String(), "antigravity")
}
