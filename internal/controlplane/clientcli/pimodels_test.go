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

func (runningPi) Stop(_ string) {}

func (p runningPi) Key(chatID string) (string, bool) { return p.key, chatID == p.chat && p.key != "" }

// Scenario: the launcher starts pi for a chat (issue #204). It writes pi's
// models.json from what the control plane says: the model gateway and its
// fallbacks variant, with every model the gateway serves (issue #549), and never a
// provider pi would call past the gateway. pi then runs the key command the
// file names, and gets the key of its own chat.
func TestPiStartsWithTheGatewaysModelsAndItsChatsKey(t *testing.T) { //nolint:paralleltest // sets HYPOMNEMA_CHAT_ID
	pi := &runningPi{}
	g := newGatewayModels(t, false)
	w := newWorldWith(t, pi, func(srv *clientapi.Server) { srv.WithGatewayModels(g) })
	w.login(t, bearertoken.Coordinator)
	ctx := t.Context()

	_, err := w.cmd(t, "pi-models")
	require.ErrorIs(t, err, errNoGatewayProvider, "pi would start with no model it can use")

	_, err = g.ApplyModel(ctx, "name: duo\ntargets:\n"+
		"  - {provider: claude-subscription, model: claude-sonnet-5-5}\n  - {provider: codex-subscription, model: gpt-6.1-sol}\n")
	require.NoError(t, err)

	_, err = w.svc.ApplyProvider(ctx, "", "name: ninerouter\nkind: openai_compatible\n"+
		"base_url: http://172.22.0.1:20128/v1\napi_key_ref: file:ninerouter.key\nmodels: [cx/gpt-sol]\n")
	require.NoError(t, err)

	out, err := w.cmd(t, "pi-models")
	require.NoError(t, err)

	var file piModelsFile
	require.NoError(t, json.Unmarshal([]byte(out), &file))
	require.Len(t, file.Providers, 2, "a provider pi would call past the gateway is left out")

	workers := file.Providers["hypomnema"]
	assert.Equal(t, w.url+"/v1", workers.BaseURL)
	assert.Equal(t, "openai-completions", workers.API)
	assert.Equal(t, "!hypomnema model-key", workers.APIKey, "pi runs the command at each request; no key is in the file")
	require.Len(t, workers.Models, 1)
	assert.Equal(t, "duo", workers.Models[0].ID)
	assert.Empty(t, workers.Models[0].API+workers.Models[0].BaseURL,
		"a name over two kinds of subscription is asked in chat's format")

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
// built-in subscriptions as its providers. A gateway given its list of
// providers serves every subscription's models besides its table's.
func newGatewayModels(t *testing.T, listed bool) *modelgateway.Service {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	orch := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	info := func(p orchestration.Provider) modelgateway.ProviderInfo {
		return modelgateway.ProviderInfo{Name: p.Name, Kind: p.Kind, Models: p.Models}
	}

	g := modelgateway.New(db, platform.SystemClock{}, platform.NewID,
		func(ctx context.Context, name string) (modelgateway.ProviderInfo, error) {
			p, err := orch.GetProvider(ctx, name)
			if err != nil {
				return modelgateway.ProviderInfo{}, fmt.Errorf("%w: %w", modelgateway.ErrNotFound, err)
			}

			return info(p), nil
		}, func(context.Context, string) (string, error) { return "", nil })

	if !listed {
		return g
	}

	return g.WithProviderList(func(ctx context.Context) ([]modelgateway.ProviderInfo, error) {
		providers, err := orch.ListProviders(ctx)
		if err != nil {
			return nil, err
		}

		out := make([]modelgateway.ProviderInfo, 0, len(providers))
		for _, p := range providers {
			out = append(out, info(p))
		}

		return out, nil
	})
}

// Scenario: pi is given every subscription's models under their gateway
// names, each speaking its subscription's own wire (issue #549): a Claude
// model Anthropic Messages at the gateway's root, where pi adds /v1/messages;
// a Codex model OpenAI Responses at the provider's /v1; any other model, and a
// name over several kinds, the provider's chat.
func TestPiSpeaksEachSubscriptionsOwnWire(t *testing.T) {
	t.Parallel()

	g := newGatewayModels(t, true)
	w := newWorldWith(t, nil, func(srv *clientapi.Server) { srv.WithGatewayModels(g) })
	w.login(t, bearertoken.Coordinator)
	ctx := t.Context()

	_, err := g.ApplyModel(ctx, "name: duo\ntargets:\n"+
		"  - {provider: claude-subscription, model: claude-sonnet-5-5}\n  - {provider: codex-subscription, model: gpt-6.1-sol}\n")
	require.NoError(t, err)

	out, err := w.cmd(t, "pi-models")
	require.NoError(t, err)

	var file piModelsFile
	require.NoError(t, json.Unmarshal([]byte(out), &file))

	wires := map[string][2]string{}
	for _, m := range file.Providers["hypomnema"].Models {
		wires[m.ID] = [2]string{m.API, m.BaseURL}
	}

	assert.Equal(t, [2]string{"anthropic-messages", w.url}, wires["claude-subscription/claude-opus-5-5"])
	assert.Equal(t, [2]string{"openai-responses", ""}, wires["codex-subscription/gpt-6.1-sol"])
	assert.Equal(t, [2]string{"", ""}, wires["antigravity-subscription/gemini-3.1-pro-high"])
	assert.Equal(t, [2]string{"", ""}, wires["duo"])

	fallback := file.Providers["hypomnema-fallback"]
	assert.Equal(t, "openai-completions", fallback.API)
	assert.Equal(t, w.url+"/v1", fallback.BaseURL)
	require.Len(t, fallback.Models, len(wires))
	for _, model := range fallback.Models {
		assert.Contains(t, wires, model.ID)
		assert.Empty(t, model.API+model.BaseURL, "every model uses the fallback provider's wire")
	}
}

// Scenario: pi starts with the gateway's Codex and Claude models (issue
// #367). Each has its window, largest output, inputs and price, so pi
// compacts at the model's window and sends images to the Codex model; pi's
// thinking levels are the model's effort levels, one the gateway does not
// give being null. A window the owner set is the one pi is given.
func TestPiIsGivenEachModelsAttributes(t *testing.T) {
	t.Parallel()

	g := newGatewayModels(t, false)
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

	out, err := w.cmd(t, "pi-models")
	require.NoError(t, err)

	var file piModelsFile
	require.NoError(t, json.Unmarshal([]byte(out), &file))

	listed := file.Providers["hypomnema"].Models
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
	assert.Equal(t, int64(500000), sol.ContextWindow, "Codex's own window, not the API's that models.dev gives")
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
