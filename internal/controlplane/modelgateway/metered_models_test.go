package modelgateway_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// chatSecrets is the chats' running pis, by chat.
type chatSecrets map[string]string

func (c chatSecrets) Key(chatID string) (string, bool) {
	s, ok := c[chatID]

	return s, ok
}

// Scenario (issue #1306): a tool asks a gateway model for its caller, social listening's review of its shortlist. The
// call is recorded on the worker's Dispatch key, or on the key of the Bot's chat's pi, whichever model that key lets
// its own client name, since the tool chose the model; a caller with no key is not asked for, and says so.
func TestAToolAsksAModelOnItsCallersKey(t *testing.T) {
	t.Parallel()

	healthy, _ := provider(t, plain(http.StatusOK, answer))
	g := newChatGateway(t, modelgateway.Config{}, healthy)
	ctx := t.Context()
	logger := platform.NewLogger(t.Output(), platform.LogConfig{}, "test")
	body := []byte(`{"model":"gpt","messages":[{"role":"user","content":"score these"}]}`)

	_, err := g.svc.AddDispatchKey(ctx, "dsp_1", "claude-subscription/claude-opus-5-5")
	require.NoError(t, err)

	dispatchKey, err := g.svc.DispatchKey(ctx, "dsp_1")
	require.NoError(t, err)

	worker := modelgateway.WorkerModels{Server: g.server, Gateway: g.svc, Logger: logger}

	got, err := worker.Complete(ctx, "dsp_1", body)
	require.NoError(t, err)
	assert.Equal(t, answer, string(got)) //nolint:testifylint // byte for byte: the answer is passed on
	assert.Equal(t, dispatchKey.ID, g.lastUsage(t).KeyID)
	assert.Equal(t, "gpt", g.lastUsage(t).Model)

	_, err = worker.Complete(ctx, "dsp_none", body)
	require.ErrorIs(t, err, modelgateway.ErrNoKey)

	// The chat's pi runs with the test gateway's key.
	chatKey, err := g.svc.Authenticate(ctx, g.key)
	require.NoError(t, err)

	bot := modelgateway.BotModels{Server: g.server, Gateway: g.svc, Chats: chatSecrets{"cht_1": g.key}}

	_, err = bot.Complete(ctx, "cht_1", body)
	require.NoError(t, err)
	assert.Equal(t, chatKey.ID, g.lastUsage(t).KeyID)

	_, err = bot.Complete(ctx, "cht_gone", body)
	require.ErrorIs(t, err, modelgateway.ErrNoKey)

	_, err = bot.Complete(ctx, "cht_1", []byte(`{"model":"nope","messages":[]}`))
	require.ErrorIs(t, err, modelgateway.ErrNotFound)
}
