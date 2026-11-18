package subscription_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

// goWorld is the control plane with OpenCode Go keys added as accounts, and
// an OpenCode stand-in answering the model list and the usage.
type goWorld struct {
	t     *testing.T
	clock *fakeClock
	orch  *orchestration.Service
	vault *vault.Service
	subs  *subscription.Service
	// usage answers GET /zen/go/v1/usage.
	usage atomic.Pointer[http.HandlerFunc]
	srv   *httptest.Server
}

func newGoWorld(t *testing.T) *goWorld {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	entry, err := vault.GenerateKey("k1")
	require.NoError(t, err)
	keys, err := vault.ParseKeyring(entry, "k1")
	require.NoError(t, err)

	w := &goWorld{t: t, clock: &fakeClock{now: t0}}
	w.orch = orchestration.New(db, w.clock, platform.NewID)
	w.vault = vault.New(db, w.clock, platform.NewID, keys, w.orch.AccountsUsingSecretRef)

	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		assert.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sk-go-"), "the key")
		assert.Equal(t, "hypomnema/"+platform.Version, r.Header.Get("User-Agent"))

		switch r.URL.Path {
		case "/inference/go/v1/models":
			rw.Header().Set("Content-Type", "application/json")
			_, _ = rw.Write([]byte(`{"object":"list","data":[
				{"id":"glm-5.3","type":"chat","limit":{"context":1000000,"output":131072},
				 "api":{"chat-completions":{"base_url":"https://opencode.ai/inference/go/openai/v1"}}},
				{"id":"minimax-m3","type":"chat","limit":{"context":1000000,"output":131072},
				 "api":{"messages":{"base_url":"https://opencode.ai/inference/go/anthropic/v1"}}},
				{"id":"gpt-6-luna","type":"chat","limit":{"context":1050000,"output":128000},
				 "api":{"responses":{"base_url":"https://opencode.ai/inference/go/openai/v1"}}},
				{"id":"gemini-4-argon","type":"chat","limit":{"context":1048576,"output":65536},
				 "api":{"generate-content":{"base_url":"https://opencode.ai/inference/go/google/v1beta"}}}
			]}`))
		case "/zen/go/v1/usage":
			(*w.usage.Load())(rw, r)
		default:
			http.Error(rw, "unexpected "+r.URL.Path, http.StatusTeapot)
		}
	}))
	t.Cleanup(w.srv.Close)

	w.subs = subscription.New(w.orch, w.vault, w.clock, slog.New(slog.DiscardHandler),
		subscription.NewOpenCodeGo(w.srv.URL, w.clock))

	return w
}

// addKey adds a Go key as the control plane's `accounts add opencode_go` does:
// the account, then its key sealed in the vault.
func (w *goWorld) addKey(label, key string) orchestration.Account {
	w.t.Helper()

	name := func(id string) string { return "account-" + strings.ReplaceAll(strings.ToLower(id), "_", "-") }

	a, err := w.orch.AddTokenAccount(w.t.Context(), orchestration.OpenCodeGoRuntime, label,
		func(id string) string { return vault.Ref(name(id)) })
	require.NoError(w.t, err)

	_, err = w.vault.Create(w.t.Context(), vault.NewSecret{
		Name: name(a.ID), Kind: vault.KindGeneric, Payload: vault.Payload{vault.KeyValue: key},
	})
	require.NoError(w.t, err)

	return a
}

func (w *goWorld) answerUsage(h http.HandlerFunc) { w.usage.Store(&h) }

// Scenario: the owner adds two Go keys. Each is an active account whose token
// is its key; a request goes to one, and when that one rests the other serves.
func TestTwoGoKeysAreTwoAccountsThatRotate(t *testing.T) {
	t.Parallel()

	w := newGoWorld(t)
	first := w.addKey("main", "sk-go-1")
	second := w.addKey("spare", "sk-go-2")

	token, err := w.subs.Token(t.Context(), subscription.OpenCodeGoRuntime, "")
	require.NoError(t, err)
	assert.Contains(t, []string{"sk-go-1", "sk-go-2"}, token.AccessToken)

	require.NoError(t, w.subs.CoolDown(t.Context(), subscription.OpenCodeGoRuntime, first.ID,
		t0.Add(13*24*time.Hour), "OpenCode Go usage limit"))

	for range 3 {
		token, err = w.subs.Token(t.Context(), subscription.OpenCodeGoRuntime, "")
		require.NoError(t, err)
		assert.Equal(t, second.ID, token.AccountID, "the resting key is passed over")
		assert.Equal(t, "sk-go-2", token.AccessToken)
	}

	_, err = w.subs.Begin(t.Context(), subscription.OpenCodeGoRuntime)
	require.ErrorIs(t, err, subscription.ErrUnknownVendor, "a Go key is pasted, never signed in")
}

// Scenario: a Go key says the models it is served, each with the wire it is
// served on and its window; Gemini's generate-content is left out (issue
// #854). The wire is then known for the gateway.
func TestAGoKeySaysItsModelsAndTheirWires(t *testing.T) {
	t.Parallel()

	w := newGoWorld(t)
	a := w.addKey("main", "sk-go-1")

	models, err := w.subs.Models(t.Context(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, []subscription.ServedModel{
		{Name: "glm-5.3", Remaining: -1, Window: 1000000, Output: 131072, Wire: subscription.WireChat},
		{Name: "gpt-6-luna", Remaining: -1, Window: 1050000, Output: 128000, Wire: subscription.WireResponses},
		{Name: "minimax-m3", Remaining: -1, Window: 1000000, Output: 131072, Wire: subscription.WireMessages},
	}, models)

	wire, ok := w.subs.ServedWire(t.Context(), subscription.OpenCodeGoRuntime, "minimax-m3")
	assert.True(t, ok)
	assert.Equal(t, subscription.WireMessages, wire)

	_, ok = w.subs.ServedWire(t.Context(), subscription.OpenCodeGoRuntime, "gemini-4-argon")
	assert.False(t, ok)

	names, ok := w.subs.ServedNames(t.Context(), subscription.OpenCodeGoRuntime)
	assert.True(t, ok)
	assert.Equal(t, []string{"glm-5.3", "gpt-6-luna", "minimax-m3"}, names)
}

// Scenario: the owner asks a Go key's limits. Go's usage names the 5 hours,
// the week and the month, each a percent and a reset; a window Go says is
// rate-limited is used up whatever its percent.
func TestAGoKeySaysItsThreeWindows(t *testing.T) {
	t.Parallel()

	w := newGoWorld(t)
	a := w.addKey("main", "sk-go-1")

	w.answerUsage(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"usage":{
			"rolling":{"percent":42,"status":"ok","resetsAt":"2026-11-10T12:00:00Z"},
			"weekly":{"percent":97,"status":"rate-limited","resetsAt":"2026-11-16T00:00:00Z"},
			"monthly":{"percent":30,"status":"ok","resetsAt":"2026-12-01T08:00:00.000Z"}}}`))
	})

	limits, err := w.subs.Limits(t.Context(), a.ID, true)
	require.NoError(t, err)
	assert.Equal(t, []subscription.Window{
		{Name: subscription.WindowFiveHours, UsedPercent: 42, ResetsAt: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)},
		{Name: subscription.WindowWeek, UsedPercent: 100, ResetsAt: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)},
		{Name: subscription.WindowMonth, UsedPercent: 30, ResetsAt: time.Date(2026, 12, 1, 8, 0, 0, 0, time.UTC)},
	}, limits.Windows)
}

// Scenario: a key whose workspace has no Go subscription. Go answers 403 to
// its usage: the reading fails and the account is marked so, as its plan.
func TestAGoKeyWithoutASubscriptionIsMarked(t *testing.T) {
	t.Parallel()

	w := newGoWorld(t)
	a := w.addKey("main", "sk-go-1")

	w.answerUsage(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusForbidden)
		_, _ = rw.Write([]byte(`{"error":{"message":"No active Go subscription"}}`))
	})

	_, err := w.subs.Limits(t.Context(), a.ID, true)
	require.Error(t, err)

	got, err := w.subs.Get(t.Context(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, "no Go subscription", got.Plan)
}
