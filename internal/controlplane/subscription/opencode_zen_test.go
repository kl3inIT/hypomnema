package subscription_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// consoleModels is OpenCode's Console list as its documentation shapes it
// (console/api/models.mdx): providers keyed by id, each with its models keyed
// by id. Zen's provider is `opencode`; `opencode-go` is the Go subscription's.
const consoleModels = `{
  "opencode": {"id": "opencode", "name": "Acme / OpenCode", "models": {
    "glm-5.3": {"type": "chat", "id": "glm-5.3", "limit": {"context": 200000, "output": 131072},
      "cost": {"text": {"input": {"price": 1}, "output": {"price": 3.2}}},
      "api": {"chat-completions": {"base_url": "https://opencode.ai/inference/openai/v1"}}},
    "claude-sonnet-5-5": {"type": "chat", "id": "claude-sonnet-5-5", "limit": {"context": 1000000, "output": 128000},
      "cost": {"text": {"input": {"price": 2, "cache": {"read": 0.1, "write": 2.5}}, "output": {"price": 10}}},
      "api": {"messages": {"base_url": "https://opencode.ai/inference/anthropic/v1"}}},
    "gpt-6.1-sol": {"type": "chat", "id": "gpt-6.1-sol", "limit": {"context": 1050000, "output": 128000},
      "cost": {"text": {"input": {"price": 1.25}, "output": {"price": 10}}},
      "api": {"responses": {"base_url": "https://opencode.ai/inference/openai/v1"}}},
    "big-pickle": {"type": "chat", "id": "big-pickle", "limit": {"context": 200000, "output": 32000},
      "cost": {"text": {"input": {"price": 0}, "output": {"price": 0}}},
      "api": {"chat-completions": {"base_url": "https://opencode.ai/inference/openai/v1"}}},
    "gemini-4-argon": {"type": "chat", "id": "gemini-4-argon", "limit": {"context": 1048576, "output": 65536},
      "cost": {"text": {"input": {"price": 2}, "output": {"price": 12}}},
      "api": {"generate-content": {"base_url": "https://opencode.ai/inference/google/v1beta"}}}
  }},
  "opencode-go": {"id": "opencode-go", "name": "Acme / OpenCode Go", "models": {
    "kimi-k3": {"type": "chat", "id": "kimi-k3", "limit": {"context": 262144, "output": 65536},
      "api": {"chat-completions": {"base_url": "https://opencode.ai/inference/go/openai/v1"}}}
  }}
}`

// otherWorkspace is the Console list of a second workspace's key, which enabled kimi-k3 only.
const otherWorkspace = `{"opencode": {"id": "opencode", "name": "Other / OpenCode", "models": {
  "kimi-k3": {"type": "chat", "id": "kimi-k3", "limit": {"context": 262144, "output": 65536},
    "cost": {"text": {"input": {"price": 0.6}, "output": {"price": 2.5}}},
    "api": {"chat-completions": {"base_url": "https://opencode.ai/inference/openai/v1"}}}
}}}`

// newZenWorld is the control plane with OpenCode Zen keys added as accounts,
// and an OpenCode stand-in answering the Console list.
func newZenWorld(t *testing.T) (*subscription.Service, func(label, key string) orchestration.Account) {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	entry, err := vault.GenerateKey("k1")
	require.NoError(t, err)
	keys, err := vault.ParseKeyring(entry, "k1")
	require.NoError(t, err)

	clock := &fakeClock{now: t0}
	orch := orchestration.New(db, clock, platform.NewID)
	secrets := vault.New(db, clock, platform.NewID, keys, orch.AccountsUsingSecretRef)

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "hypomnema/"+platform.Version, r.Header.Get("User-Agent"))

		if r.URL.Path != "/console/api/v1/models" {
			http.Error(rw, "unexpected "+r.URL.Path, http.StatusTeapot)

			return
		}

		rw.Header().Set("Content-Type", "application/json")

		switch r.Header.Get("Authorization") {
		case "Bearer sk-zen-1":
			_, _ = rw.Write([]byte(consoleModels))
		case "Bearer sk-zen-2":
			_, _ = rw.Write([]byte(otherWorkspace))
		default:
			http.Error(rw, "the key", http.StatusUnauthorized)
		}
	}))
	t.Cleanup(srv.Close)

	subs := subscription.New(orch, secrets, clock, slog.New(slog.DiscardHandler),
		subscription.NewOpenCodeGo(srv.URL, clock), subscription.NewOpenCodeZen(srv.URL, clock))

	add := func(label, key string) orchestration.Account {
		name := func(id string) string { return "account-" + strings.ReplaceAll(strings.ToLower(id), "_", "-") }

		a, err := orch.AddTokenAccount(t.Context(), orchestration.OpenCodeZenRuntime, label,
			func(id string) string { return vault.Ref(name(id)) })
		require.NoError(t, err)

		_, err = secrets.Create(t.Context(), vault.NewSecret{
			Name: name(a.ID), Kind: vault.KindGeneric, Payload: vault.Payload{vault.KeyValue: key},
		})
		require.NoError(t, err)

		return a
	}

	return subs, add
}

// Scenario: a Zen key says the models its workspace has enabled, from the
// Console list's `opencode` provider, each with its wire and window. A free
// model (priced 0), which OpenCode refuses to a client other than itself, and
// Gemini's generate-content are left out, and Go's provider is not Zen's
// (issue #965).
func TestAZenKeySaysItsPaidModelsFromTheConsoleList(t *testing.T) {
	t.Parallel()

	subs, add := newZenWorld(t)
	a := add("main", "sk-zen-1")

	models, err := subs.Models(t.Context(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, []subscription.ServedModel{
		{Name: "claude-sonnet-5-5", Remaining: -1, Window: 1000000, Output: 128000, Wire: subscription.WireMessages},
		{Name: "glm-5.3", Remaining: -1, Window: 200000, Output: 131072, Wire: subscription.WireChat},
		{Name: "gpt-6.1-sol", Remaining: -1, Window: 1050000, Output: 128000, Wire: subscription.WireResponses},
	}, models)

	wire, ok := subs.ServedWire(t.Context(), subscription.OpenCodeZenRuntime, "gpt-6.1-sol")
	assert.True(t, ok)
	assert.Equal(t, subscription.WireResponses, wire)

	names, ok := subs.ServedNames(t.Context(), subscription.OpenCodeZenRuntime)
	assert.True(t, ok)
	assert.Equal(t, []string{"claude-sonnet-5-5", "glm-5.3", "gpt-6.1-sol"}, names)

	_, ok = subs.ServedNames(t.Context(), subscription.OpenCodeGoRuntime)
	assert.False(t, ok, "a Zen key is no Go key")

	_, err = subs.Begin(t.Context(), subscription.OpenCodeZenRuntime)
	require.ErrorIs(t, err, subscription.ErrUnknownVendor, "a Zen key is pasted, never signed in")
}

// Scenario (review of PR #1000): two Zen keys of two workspaces, each with its own models enabled. Each key is served
// its own workspace's list, the runtime the union of both, and a model goes only to a key whose list has it, as a
// Codex account's does (issue #814).
func TestEachZenKeyIsServedItsOwnWorkspacesModels(t *testing.T) {
	t.Parallel()

	subs, add := newZenWorld(t)
	first := add("main", "sk-zen-1")
	second := add("other", "sk-zen-2")

	names, ok := subs.ServedNames(t.Context(), subscription.OpenCodeZenRuntime)
	assert.True(t, ok)
	assert.Equal(t, []string{"claude-sonnet-5-5", "glm-5.3", "gpt-6.1-sol", "kimi-k3"}, names)

	for model, want := range map[string]string{"kimi-k3": second.ID, "glm-5.3": first.ID} {
		token, err := subs.TokenFor(t.Context(), subscription.OpenCodeZenRuntime, model, "")
		require.NoError(t, err, model)
		assert.Equal(t, want, token.AccountID, model)
	}
}
