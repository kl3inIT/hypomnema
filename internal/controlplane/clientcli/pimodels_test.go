package clientcli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/coordinator"
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
	require.Len(t, file.Providers, 2, "a provider pi would call past the gateway is left out")

	workers := file.Providers["model-gateway"]
	assert.Equal(t, w.url+"/v1", workers.BaseURL)
	assert.Equal(t, "openai-completions", workers.API)
	assert.Equal(t, "!hypomnema model-key", workers.APIKey, "pi runs the command at each request; no key is in the file")
	assert.Equal(t, []piModel{
		{ID: "claude-sonnet", Name: "claude-sonnet (gateway)", Reasoning: true},
		{ID: "gpt-sol", Name: "gpt-sol (gateway)", Reasoning: true},
	}, workers.Models)

	assert.Equal(t, w.url+"/v1", file.Providers["gateway"].BaseURL, "the provider pi had before is still one of pi's")

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
