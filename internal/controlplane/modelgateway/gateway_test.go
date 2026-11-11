package modelgateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC) }

// newGateway returns a gateway over a migrated database that knows two
// providers: `router`, OpenAI-compatible with two models, and `codex`, a
// subscription the gateway has no adapter for yet.
func newGateway(t *testing.T) *modelgateway.Service {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	orch := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	_, err = orch.ApplyProvider(t.Context(), "", "name: router\nkind: openai_compatible\n"+
		"base_url: https://router.example/v1\napi_key_ref: vault:router\nmodels: [cx/gpt-6.1-sol, small]\n")
	require.NoError(t, err)

	// anthropic is a provider kind the gateway has no chat adapter for: a
	// model that targets it cannot be served.
	_, err = orch.ApplyProvider(t.Context(), "",
		"name: anth\nkind: anthropic\nbase_url: https://api.anthropic.example/v1\napi_key_ref: vault:anth\n"+
			"models: [opus-x]\n")
	require.NoError(t, err)

	n := 0

	return modelgateway.New(db, fixedClock{}, func(prefix string) string {
		n++

		return fmt.Sprintf("%s_%04d", prefix, n)
	}, func(ctx context.Context, name string) (modelgateway.ProviderInfo, error) {
		p, err := orch.GetProvider(ctx, name)
		if err != nil {
			return modelgateway.ProviderInfo{}, fmt.Errorf("%w: %w", modelgateway.ErrNotFound, err)
		}

		return modelgateway.ProviderInfo{Name: p.Name, Kind: p.Kind, BaseURL: p.BaseURL, Models: p.Models}, nil
	}, func(context.Context, string) (string, error) { return "", nil })
}

const solModel = "name: gpt-sol\ntargets:\n  - provider: router\n    model: cx/gpt-6.1-sol\n"

// Scenario: the owner declares a model under a public name, then changes
// where it is served from. The table holds what was applied last, in the
// order the targets are tried.
func TestTheOwnerDeclaresAModelAndItsTargets(t *testing.T) {
	t.Parallel()

	g := newGateway(t)

	m, err := g.ApplyModel(t.Context(), solModel)
	require.NoError(t, err)
	assert.Equal(t, "gpt-sol", m.Name)
	assert.Equal(t, modelgateway.CapabilityChat, m.Capability)
	assert.Equal(t, []modelgateway.Target{{Provider: "router", UpstreamModel: "cx/gpt-6.1-sol"}}, m.Targets)

	m, err = g.ApplyModel(t.Context(), "name: gpt-sol\ncapability: chat\ntargets:\n"+
		"  - {provider: router, model: small}\n  - {provider: router, model: cx/gpt-6.1-sol}\n")
	require.NoError(t, err)
	assert.Equal(t, []modelgateway.Target{
		{Provider: "router", UpstreamModel: "small"}, {Provider: "router", UpstreamModel: "cx/gpt-6.1-sol"},
	}, m.Targets)

	listed, err := g.ListModels(t.Context())
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, m.Targets, listed[0].Targets)

	require.NoError(t, g.DeleteModel(t.Context(), "gpt-sol"))
	require.ErrorIs(t, g.DeleteModel(t.Context(), "gpt-sol"), modelgateway.ErrNotFound)

	_, err = g.GetModel(t.Context(), "gpt-sol")
	require.ErrorIs(t, err, modelgateway.ErrNotFound)
}

// Scenario: a model that could not be served is refused when it is applied,
// not at the first request.
func TestAModelThatCannotBeServedIsRefused(t *testing.T) {
	t.Parallel()

	g := newGateway(t)

	cases := map[string]string{
		"a provider of a kind without an adapter": "name: m\ntargets: [{provider: anth, model: opus-x}]\n",
		"a provider nobody applied":               "name: m\ntargets: [{provider: missing, model: small}]\n",
		"a model the provider does not offer":     "name: m\ntargets: [{provider: router, model: large}]\n",
		"no target":                               "name: m\ntargets: []\n",
		"a target without its model":              "name: m\ntargets: [{provider: router}]\n",
		"a name with a space":                     "name: my model\ntargets: [{provider: router, model: small}]\n",
		"a capability the gateway lacks":          "name: m\ncapability: speech\ntargets: [{provider: router, model: small}]\n",
		"a field the document does not have":      "name: m\nfallback: true\ntargets: [{provider: router, model: small}]\n",
	}

	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := g.ApplyModel(t.Context(), source)
			require.ErrorIs(t, err, modelgateway.ErrInvalid)
		})
	}

	listed, err := g.ListModels(t.Context())
	require.NoError(t, err)
	assert.Empty(t, listed)
}

// Scenario: the owner makes a key for a tool, limited to one model, and
// later revokes it. The secret is a gateway key, authenticates until it is
// revoked, and a client API token never passes for one.
func TestAKeyAuthenticatesUntilItIsRevoked(t *testing.T) {
	t.Parallel()

	g := newGateway(t)

	k, secret, err := g.AddKey(t.Context(), "laptop", []string{"gpt-sol"})
	require.NoError(t, err)
	assert.Regexp(t, `^hyp_gateway_v1_`, secret)
	assert.True(t, k.Allows("gpt-sol"))
	assert.False(t, k.Allows("small"))

	got, err := g.Authenticate(t.Context(), secret)
	require.NoError(t, err)
	assert.Equal(t, k.ID, got.ID)

	everything, _, err := g.AddKey(t.Context(), "pi", nil)
	require.NoError(t, err)
	assert.True(t, everything.Allows("any-model"))

	_, _, err = g.AddKey(t.Context(), "laptop", nil)
	require.ErrorIs(t, err, modelgateway.ErrConflict)

	_, _, err = g.AddKey(t.Context(), "Lap Top", nil)
	require.ErrorIs(t, err, modelgateway.ErrInvalid)

	ownerToken, err := bearertoken.Generate(bearertoken.Owner)
	require.NoError(t, err)

	for name, bad := range map[string]string{
		"a client API token":            ownerToken,
		"a well-formed key nobody made": mustGenerate(t),
		"anything else":                 "sk-not-a-gateway-key",
	} {
		_, err := g.Authenticate(t.Context(), bad)
		require.ErrorIs(t, err, modelgateway.ErrUnauthorized, name)
	}

	require.NoError(t, g.RevokeKey(t.Context(), k.ID))
	require.ErrorIs(t, g.RevokeKey(t.Context(), k.ID), modelgateway.ErrConflict)
	require.ErrorIs(t, g.RevokeKey(t.Context(), "gwk_missing"), modelgateway.ErrNotFound)

	_, err = g.Authenticate(t.Context(), secret)
	require.ErrorIs(t, err, modelgateway.ErrUnauthorized)

	keys, err := g.ListKeys(t.Context())
	require.NoError(t, err)
	require.Len(t, keys, 2)
	assert.NotNil(t, keys[0].RevokedAt, "laptop is revoked and still listed")
}

func mustGenerate(t *testing.T) string {
	t.Helper()

	secret, err := bearertoken.Generate(bearertoken.Gateway)
	require.NoError(t, err)

	return secret
}

// Scenario: an OpenAI client lists the models with its key. It gets OpenAI's
// list shape, holding only the models its key may use; without a valid key it
// gets OpenAI's error shape and 401.
func TestListModelsOverHTTP(t *testing.T) {
	t.Parallel()

	g := newGateway(t)

	_, err := g.ApplyModel(t.Context(), solModel)
	require.NoError(t, err)
	_, err = g.ApplyModel(t.Context(), "name: small\ntargets: [{provider: router, model: small}]\n")
	require.NoError(t, err)

	_, limited, err := g.AddKey(t.Context(), "laptop", []string{"gpt-sol"})
	require.NoError(t, err)
	_, everything, err := g.AddKey(t.Context(), "pi", nil)
	require.NoError(t, err)

	mux := http.NewServeMux()
	modelgateway.NewServer(g, platform.NewLogger(t.Output(), platform.LogConfig{}, "test"),
		modelgateway.Config{}).Register(mux)

	get := func(key string) (int, string) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/models", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

		return rec.Code, rec.Body.String()
	}

	created := fixedClock{}.Now().Unix()

	status, body := get(limited)
	assert.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, fmt.Sprintf(
		`{"object":"list","data":[{"id":"gpt-sol","object":"model","created":%d,"owned_by":"hypomnema"}]}`, created),
		body)

	status, body = get(everything)
	assert.Equal(t, http.StatusOK, status)

	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	require.NoError(t, json.Unmarshal([]byte(body), &list))
	require.Len(t, list.Data, 2)
	assert.Equal(t, "gpt-sol", list.Data[0].ID)
	assert.Equal(t, "small", list.Data[1].ID)

	status, body = get("")
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.JSONEq(t, `{"error":{"message":"a gateway key is required: Authorization: Bearer <key>",`+
		`"type":"authentication_error","code":"missing_api_key"}}`, body)

	status, body = get("sk-wrong")
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Contains(t, body, `"code":"invalid_api_key"`)
}

// Scenario: a Dispatch's worker gets a key of its own. It opens the gateway
// for the Dispatch's model and no other; an offer sent again brings a new key
// and the old one stops; when the Dispatch settles none of its keys works,
// and the owner's keys are untouched.
func TestADispatchHasAKeyOfItsOwn(t *testing.T) {
	t.Parallel()

	g := newGateway(t)
	ctx := t.Context()

	_, owner, err := g.AddKey(ctx, "pi", nil)
	require.NoError(t, err)

	first, err := g.AddDispatchKey(ctx, "dsp_1", "gpt-sol")
	require.NoError(t, err)

	k, err := g.Authenticate(ctx, first)
	require.NoError(t, err)
	assert.Equal(t, "dsp_1", k.DispatchID)
	assert.True(t, k.Allows("gpt-sol"))
	assert.False(t, k.Allows("small"))

	second, err := g.AddDispatchKey(ctx, "dsp_1", "gpt-sol")
	require.NoError(t, err)
	assert.NotEqual(t, first, second)

	_, err = g.Authenticate(ctx, first)
	require.ErrorIs(t, err, modelgateway.ErrUnauthorized, "only the key of the last offer works")

	_, err = g.AddDispatchKey(ctx, "dsp_2", "small")
	require.NoError(t, err)

	with, err := g.DispatchesWithKeys(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"dsp_1", "dsp_2"}, with)

	require.NoError(t, g.RevokeDispatchKeys(ctx, "dsp_1"))
	require.NoError(t, g.RevokeDispatchKeys(ctx, "dsp_1"), "revoking again is no error")

	_, err = g.Authenticate(ctx, second)
	require.ErrorIs(t, err, modelgateway.ErrUnauthorized)

	with, err = g.DispatchesWithKeys(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"dsp_2"}, with)

	_, err = g.Authenticate(ctx, owner)
	require.NoError(t, err)

	_, err = g.AddDispatchKey(ctx, "dsp_3", "")
	require.ErrorIs(t, err, modelgateway.ErrInvalid, "a key is for one model")
}
