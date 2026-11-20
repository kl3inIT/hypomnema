package controlplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

type refusalVendor struct {
	subscription.Vendor
	runtime   string
	err       error
	before    func(context.Context) error
	refreshes atomic.Int32
}

func (v *refusalVendor) Runtime() string { return v.runtime }

func (v *refusalVendor) Refresh(ctx context.Context, _ vault.Payload) (vault.Refreshed, error) {
	v.refreshes.Add(1)
	if v.before != nil {
		if err := v.before(ctx); err != nil {
			return vault.Refreshed{}, err
		}
	}
	return vault.Refreshed{
		Payload: vault.Payload{vault.KeyAccessToken: "renewed", vault.KeyRefreshToken: "renewal"},
	}, v.err
}

// Models lists the model the Codex cases ask for: a Codex request goes only to an account whose own list has its model
// (issue #814).
func (v *refusalVendor) Models(context.Context, subscription.Token) ([]subscription.ServedModel, error) {
	return []subscription.ServedModel{{Name: codexRefusalModel, Remaining: -1}}, nil
}

// codexRefusalModel is the model the Codex cases ask for, which every account's list has.
const codexRefusalModel = "gpt-5.6-sol"

// listedCodex is Codex whose accounts' lists have codexRefusalModel, without asking chatgpt.com.
type listedCodex struct{ *subscription.Codex }

func (listedCodex) Models(context.Context, subscription.Token) ([]subscription.ServedModel, error) {
	return []subscription.ServedModel{{Name: codexRefusalModel, Remaining: -1}}, nil
}

func (v *refusalVendor) Identify(vault.Payload) (subscription.Identity, error) {
	return subscription.Identity{}, errors.New("the fake token carries no identity")
}

func TestGatewayMovesPastUnusableCredentials(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		runtime      string
		kind         vault.Kind
		expiring     bool
		missing      bool
		refreshErr   error
		refreshHTTP  int
		rotate       bool
		wantBearers  []string
		wantRefresh  int32
		wantSignIn   bool
		wantFallback bool
	}{
		{name: "401 and a rejected grant", refreshErr: vault.ErrLoginRejected,
			wantBearers: []string{"first", "second"}, wantRefresh: 1, wantSignIn: true, wantFallback: true},
		{name: "401 and a temporary 503 during refresh", refreshHTTP: http.StatusServiceUnavailable,
			wantBearers: []string{"first", "second"}, wantRefresh: 1, wantFallback: true},
		{name: "401 and a temporary 429 during refresh", refreshHTTP: http.StatusTooManyRequests,
			wantBearers: []string{"first", "second"}, wantRefresh: 1, wantFallback: true},
		{name: "preflight refresh gets 503", runtime: "codex", expiring: true, refreshHTTP: http.StatusServiceUnavailable,
			wantBearers: []string{"second"}, wantRefresh: 1, wantFallback: true},
		{name: "preflight refresh gets 429", runtime: "codex", expiring: true, refreshHTTP: http.StatusTooManyRequests,
			wantBearers: []string{"second"}, wantRefresh: 1, wantFallback: true},
		{name: "preflight rejected grant lost its lease", expiring: true, rotate: true, refreshErr: vault.ErrLoginRejected,
			wantBearers: []string{"second"}, wantRefresh: 1, wantFallback: true},
		{name: "401 again after refresh", wantBearers: []string{"first", "renewed", "second"},
			wantRefresh: 1, wantSignIn: true, wantFallback: true},
		{name: "not a token set", runtime: "codex", kind: vault.KindGeneric, expiring: true,
			wantBearers: []string{"second"}, wantSignIn: true, wantFallback: true},
		{name: "missing access token", missing: true, wantBearers: []string{"second"}, wantSignIn: true, wantFallback: true},
		{name: "setup token is sent unchanged", kind: vault.KindGeneric,
			wantBearers: []string{"first", "second"}, wantFallback: true},
		{name: "transport error during refresh", refreshErr: &net.OpError{Op: "dial", Err: errors.New("network unavailable")},
			wantBearers: []string{"first"}, wantRefresh: 1},
		{name: "canceled refresh", refreshErr: context.Canceled, wantBearers: []string{"first"}, wantRefresh: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			runtime, kind := tc.runtime, tc.kind
			if runtime == "" {
				runtime = "claude"
			}
			if kind == "" {
				kind = vault.KindOAuthTokenSet
			}
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
			vendor := &refusalVendor{runtime: runtime, err: tc.refreshErr}
			if tc.rotate {
				vendor.before = func(ctx context.Context) error {
					_, err := secrets.Rotate(ctx, "first", vault.Payload{
						vault.KeyAccessToken: "new-login", vault.KeyRefreshToken: "new-renewal",
					})
					return err
				}
			}

			add := func(name string, kind vault.Kind, payload vault.Payload, expires time.Time) orchestration.Account {
				account, err := orch.SaveAccountLogin(ctx,
					orchestration.AccountLogin{Runtime: runtime, ExternalID: name, Label: name},
					func(string) string { return vault.Ref(name) })
				require.NoError(t, err)
				_, err = secrets.Create(ctx, vault.NewSecret{
					Name: name, Kind: kind, Payload: payload, AccessTokenExpiresAt: expires,
				})
				require.NoError(t, err)
				return account
			}
			payload := vault.Payload{vault.KeyRefreshToken: "renewal"}
			if kind == vault.KindGeneric {
				payload = vault.Payload{vault.KeyValue: "first"}
			} else if !tc.missing {
				payload[vault.KeyAccessToken] = "first"
			}
			var expires time.Time
			if tc.expiring {
				expires = clock.Now().Add(time.Second)
			}
			first := add("first", kind, payload, expires)
			second := add("second", vault.KindOAuthTokenSet,
				vault.Payload{vault.KeyAccessToken: "second", vault.KeyRefreshToken: "renewal"}, time.Time{})

			sent := make(chan string, 4)
			var tokenCalls atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/oauth/token" || r.URL.Path == "/oauth/token" {
					tokenCalls.Add(1)
					w.WriteHeader(tc.refreshHTTP)
					_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
					return
				}
				bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				sent <- bearer
				if bearer != "second" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = io.WriteString(w, `{"error":{"type":"authentication_error","message":"refused"}}`)
					return
				}
				if runtime == "codex" {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"ok\"}}\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"input_tokens":42}`)
			}))
			t.Cleanup(backend.Close)
			var exchange subscription.Vendor = vendor
			switch {
			case tc.refreshHTTP != 0 && runtime == "codex":
				exchange = listedCodex{subscription.NewCodex(backend.URL, clock)}
			case tc.refreshHTTP != 0:
				exchange = subscription.NewClaude(subscription.ClaudeOptions{TokenBase: backend.URL}, clock)
			}
			subs := subscription.New(orch, secrets, clock, logger, exchange)
			gateway := modelgateway.New(db, clock, newID, gatewayProviders(orch),
				func(context.Context, string) (string, error) { return "", nil })
			_, key, err := gateway.AddKey(ctx, "test", []string{"*"}, nil)
			require.NoError(t, err)
			mux := http.NewServeMux()
			modelgateway.NewServer(gateway, logger, modelgateway.Config{
				Subscriptions: gatewayAccounts{subs: subs}, ClaudeBaseURL: backend.URL, CodexBaseURL: backend.URL,
			}).Register(mux)

			path := "/v1/messages/count_tokens"
			body := `{"model":"claude-subscription/claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`
			if runtime == "codex" {
				path = "/v1/responses"
				body = `{"model":"codex-subscription/` + codexRefusalModel + `","input":"hi","prompt_cache_key":"a","stream":true}`
			}
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("X-Claude-Code-Session-Id", "a")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if tc.wantFallback {
				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			} else {
				assert.NotEqual(t, http.StatusOK, rec.Code, rec.Body.String())
			}
			var bearers []string
			for len(sent) > 0 {
				bearers = append(bearers, <-sent)
			}
			assert.Equal(t, tc.wantBearers, bearers)
			assert.Equal(t, tc.wantRefresh, vendor.refreshes.Load()+tokenCalls.Load())

			account, err := subs.Get(ctx, first.ID)
			require.NoError(t, err)
			if tc.wantSignIn {
				assert.Equal(t, subscription.StateLoginRequired, account.State)
				picked, err := subs.Token(ctx, runtime, "a")
				require.NoError(t, err)
				assert.Equal(t, second.ID, picked.AccountID)
			} else {
				assert.Equal(t, subscription.StateActive, account.State)
			}
			if tc.expiring && kind == vault.KindGeneric {
				assert.Contains(t, account.Refresh.Error, "is not a token set")
			}
			if tc.refreshHTTP != 0 {
				assert.Contains(t, account.Refresh.Error, fmt.Sprintf("status %d", tc.refreshHTTP))
				assert.NotNil(t, account.Refresh.RetryAt)
			}
			if tc.rotate {
				assert.Empty(t, account.Refresh.Error)
			}
			if kind == vault.KindGeneric && runtime == "claude" {
				assert.Empty(t, account.Refresh.Error)
			}
		})
	}
}
