package controlplane

import (
	"context"
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

// routingWorld is the gateway over three Claude accounts, first, second and third, whose backend is at its rate
// limit for every one of them: it keeps the tokens it was sent, in order.
type routingWorld struct {
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

	w := &routingWorld{accounts: map[string]string{}}

	for _, name := range []string{"first", "second", "third"} {
		account, err := orch.SaveAccountLogin(ctx,
			orchestration.AccountLogin{Runtime: "claude", ExternalID: name, Label: name},
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

	subs := subscription.New(orch, secrets, clock, logger, &refusalVendor{runtime: "claude"})
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
