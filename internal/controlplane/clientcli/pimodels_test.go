package clientcli

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/coordinator"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/prices"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// runningPi stands in for the supervisor: a pi runs for one chat and holds
// that chat's gateway key.
type runningPi struct {
	chat string
	key  string
}

func (runningPi) Enabled() bool         { return true }
func (runningPi) Working(_ string) bool { return false }

func (runningPi) Watch(_ string) (<-chan coordinator.Frame, func()) { return nil, func() {} }

func (runningPi) Abort(_ context.Context, _ string) error { return nil }

func (runningPi) Compact(_ context.Context, _ string) error { return nil }

func (p runningPi) Key(chatID string) (string, bool) { return p.key, chatID == p.chat && p.key != "" }

// Scenario: the launcher starts pi for a chat (issue #204). It writes pi's
// models.json from what the control plane says: every provider that is the
// model gateway, with its models, and never one pi cannot reach through the
// gateway. pi then runs the key command the file names, and gets the key of
// its own chat.
func TestPiStartsWithTheGatewaysModelsAndItsChatsKey(t *testing.T) { //nolint:paralleltest // sets HYPOMNEMA_CHAT_ID
	pi := &runningPi{}
	w := newWorldWith(t, pi)
	w.login(t, bearertoken.Coordinator)
	ctx := t.Context()

	_, err := w.cmd(t, "pi-models")
	require.ErrorIs(t, err, errNoGatewayProvider, "pi would start with no model it can use")

	for _, source := range []string{
		"name: model-gateway\nkind: model_gateway\nmodels: [gpt-sol, claude-sonnet]\n",
		"name: gateway\nkind: openai_compatible\nbase_url: " + w.url + "/v1\napi_key_ref: file:pi.key\nmodels: [gpt-sol]\n",
		"name: ninerouter\nkind: openai_compatible\nbase_url: http://172.22.0.1:20128/v1\n" +
			"api_key_ref: file:ninerouter.key\nmodels: [cx/gpt-sol]\n",
	} {
		_, err := w.svc.ApplyProvider(ctx, "", source)
		require.NoError(t, err)
	}

	out, err := w.cmd(t, "pi-models")
	require.NoError(t, err)

	var file piModelsFile
	require.NoError(t, json.Unmarshal([]byte(out), &file))
	require.Len(t, file.Providers, 1, "a provider pi would call past the gateway is left out, and one at its address "+
		"that is not of its kind")

	workers := file.Providers["model-gateway"]
	assert.Equal(t, w.url+"/v1", workers.BaseURL)
	assert.Equal(t, "openai-completions", workers.API)
	assert.Equal(t, "!hypomnema model-key", workers.APIKey, "pi runs the command at each request; no key is in the file")
	assert.Equal(t, []piModel{
		{ID: "claude-sonnet", Name: "claude-sonnet (gateway)", Input: []string{"text", "image"}},
		{ID: "gpt-sol", Name: "gpt-sol (gateway)", Input: []string{"text", "image"}},
	}, workers.Models, "a model the gateway does not know of has pi's defaults, but reads pictures")

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = w.cmd(t, "model-key")
	require.ErrorIs(t, err, ErrUsage, "the command is for pi, which runs for a chat")

	t.Setenv("HYPOMNEMA_CHAT_ID", chat.ID)

	_, err = w.cmd(t, "model-key")
	require.Error(t, err, "the coordinator token is not the token of the chat's pi")

	// pi calls with its own token, bound to its chat (issue #260).
	secret, err := w.svc.AddBotToken(ctx, "cli-coordinator", chat.ID, chat.Bot)
	require.NoError(t, err)
	w.loginWith(t, secret)

	_, err = w.cmd(t, "model-key")
	require.Error(t, err, "no pi runs for the chat yet")

	pi.chat, pi.key = chat.ID, "hyp_gateway_v1_of_this_pi"

	out, err = w.cmd(t, "model-key")
	require.NoError(t, err)
	assert.Equal(t, "hyp_gateway_v1_of_this_pi\n", out, "the key alone on its line, as pi reads a command's output")
}

// newGatewayModels is a model gateway over a database of its own, with the
// built-in subscriptions as its providers.
func newGatewayModels(t *testing.T) *modelgateway.Service {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	orch := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	return modelgateway.New(db, platform.SystemClock{}, platform.NewID,
		func(ctx context.Context, name string) (modelgateway.ProviderInfo, error) {
			p, err := orch.GetProvider(ctx, name)
			if err != nil {
				return modelgateway.ProviderInfo{}, fmt.Errorf("%w: %w", modelgateway.ErrNotFound, err)
			}

			return modelgateway.ProviderInfo{Name: p.Name, Kind: p.Kind, Models: p.Models}, nil
		}, func(context.Context, string) (string, error) { return "", nil })
}

// Scenario: pi starts with the gateway's Codex and Claude models (issue
// #367). Each has its window, largest output, inputs and price, so pi
// compacts at the model's window and sends images to the Codex model; pi's
// thinking levels are the model's effort levels, one the gateway does not
// give being null. A window the owner set is the one pi is given.
func TestPiIsGivenEachModelsAttributes(t *testing.T) {
	t.Parallel()

	g := newGatewayModels(t)
	w := newWorldWith(t, nil, func(srv *clientapi.Server) { srv.WithGatewayModels(g) })
	w.login(t, bearertoken.Coordinator)
	ctx := t.Context()

	_, err := g.ApplyModel(ctx, "name: sol\ntargets: [{provider: codex-subscription, model: gpt-6.1-sol}]\n")
	require.NoError(t, err)
	_, err = g.ApplyModel(ctx, "name: claude-sonnet-5-5\n"+
		"targets: [{provider: claude-subscription, model: claude-sonnet-5-5}]\ncontext_window: 400000\n")
	require.NoError(t, err)
	_, err = g.ApplyModel(ctx, "name: claude-opus-5-5\n"+
		"targets: [{provider: claude-subscription, model: claude-opus-5-5}]\n")
	require.NoError(t, err)
	_, err = w.svc.ApplyProvider(ctx, "",
		"name: model-gateway\nkind: model_gateway\nmodels: [sol, claude-sonnet-5-5, claude-opus-5-5]\n")
	require.NoError(t, err)

	out, err := w.cmd(t, "pi-models")
	require.NoError(t, err)

	var file piModelsFile
	require.NoError(t, json.Unmarshal([]byte(out), &file))

	listed := file.Providers["model-gateway"].Models
	require.Len(t, listed, 3)

	models := map[string]piModel{}
	for _, m := range listed {
		models[m.ID] = m
	}

	codex, ok := prices.Find("codex_subscription", "gpt-6.1-sol")
	require.True(t, ok)

	sol := models["sol"]
	assert.Equal(t, "sol", sol.ID)
	assert.Equal(t, []string{"text", "image"}, sol.Input, "the Codex model takes images")
	assert.Equal(t, int64(272000), sol.ContextWindow, "Codex's own window, not the API's that models.dev gives")
	assert.Equal(t, *codex.MaxOutput, sol.MaxTokens)
	assert.True(t, sol.Reasoning)
	require.NotNil(t, sol.ThinkingLevelMap["xhigh"])
	assert.Equal(t, "xhigh", *sol.ThinkingLevelMap["xhigh"])
	assert.Contains(t, sol.ThinkingLevelMap, "max")
	assert.Nil(t, sol.ThinkingLevelMap["max"], "the gateway sends Codex max as xhigh")
	assert.NotContains(t, sol.ThinkingLevelMap, "off", "off sends no effort")
	require.NotNil(t, sol.Cost)
	assert.InDelta(t, float64(*codex.Input)/1e6, sol.Cost.Input, 1e-9)

	sonnet := models["claude-sonnet-5-5"]
	assert.Equal(t, int64(400_000), sonnet.ContextWindow, "the window the owner set")
	assert.Equal(t, "max", *sonnet.ThinkingLevelMap["max"])
	assert.Nil(t, sonnet.ThinkingLevelMap["minimal"], "the gateway gives Claude no minimal of its own")

	claude, ok := prices.Find("claude_subscription", "claude-opus-5-5")
	require.True(t, ok)

	opus := models["claude-opus-5-5"]
	assert.Equal(t, "claude-opus-5-5", opus.ID)
	assert.Equal(t, *claude.Context, opus.ContextWindow, "models.dev's window, not pi's 128000")
	assert.Equal(t, *claude.MaxOutput, opus.MaxTokens)
	assert.Equal(t, []string{"text", "image"}, opus.Input)
	assert.True(t, opus.Reasoning)
	require.NotNil(t, opus.Cost)
	assert.InDelta(t, float64(*claude.Input)/1e6, opus.Cost.Input, 1e-9)
}
