package modelgateway_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
)

// Scenario: the control plane asks a chat model itself, for a meeting's notes (issue #721). The model's next target
// answers when the first is overloaded, as for a client; the call is recorded on the meetings' key, which is made
// once and found again; a provider's refusal comes back as its status and message, and a model the gateway does not
// serve is not found.
func TestTheControlPlaneAsksAChatModelInProcess(t *testing.T) {
	t.Parallel()

	overloaded, _ := provider(t, plain(http.StatusServiceUnavailable, `{"error":{"message":"overloaded"}}`))
	healthy, got := provider(t, plain(http.StatusOK, answer))
	g := newChatGateway(t, modelgateway.Config{}, overloaded, healthy)
	ctx := t.Context()

	keyID, err := g.svc.MeetingsKey(ctx)
	require.NoError(t, err)

	again, err := g.svc.MeetingsKey(ctx)
	require.NoError(t, err)
	assert.Equal(t, keyID, again, "one key for the meetings")

	body, err := g.server.Complete(ctx, keyID, []byte(`{"model":"gpt","messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	assert.Equal(t, answer, string(body)) //nolint:testifylint // byte for byte: the answer is passed on, not re-encoded

	_, sent := got.last(t)
	assert.JSONEq(t, `{"model":"up-2","messages":[{"role":"user","content":"hi"}]}`, sent)

	u := g.lastUsage(t)
	assert.Equal(t, keyID, u.KeyID)
	assert.Equal(t, 2, u.Attempts)
	assert.Equal(t, int64(12), u.Tokens.Prompt)
	assert.Equal(t, modelgateway.OutcomeOK, u.Outcome)

	refusing, _ := provider(t, plain(http.StatusUnauthorized, `{"error":{"message":"bad key"}}`))
	r := newChatGateway(t, modelgateway.Config{}, refusing)

	_, err = r.server.Complete(ctx, "", []byte(`{"model":"gpt","messages":[]}`))

	var answered *modelgateway.CompletionError
	require.ErrorAs(t, err, &answered)
	assert.Equal(t, http.StatusUnauthorized, answered.Status)
	assert.Equal(t, "bad key", answered.Message)

	_, err = g.server.Complete(ctx, keyID, []byte(`{"model":"nope","messages":[]}`))
	require.ErrorIs(t, err, modelgateway.ErrNotFound)

	_, err = g.server.Complete(ctx, keyID, []byte(`{"model":"gpt","stream":true,"messages":[]}`))
	require.ErrorIs(t, err, modelgateway.ErrInvalid)
}
