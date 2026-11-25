package modelgateway_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// Scenario (issue #1147): a request the gateway answers before asking any provider. /v1/responses asked for a model
// with no target it passes on to is refused 400 and, as an image or audio request refused before any provider, not
// recorded; /v1/messages for a model none of whose targets can be used is answered 502 and recorded with the targets
// it counted, as the chat route counts them. Neither leaves a row the database refuses for `attempts > 0`.
func TestARequestNoProviderWasAskedForLeavesNoRowTheDatabaseRefuses(t *testing.T) {
	t.Parallel()

	router, reached := provider(t, plain(http.StatusOK, answer))
	g := newChatGateway(t, modelgateway.Config{}, router)

	_, err := g.svc.ApplyModel(t.Context(),
		"name: opus\ntargets:\n  - {provider: claude-subscription, model: claude-opus-5-5}\n")
	require.NoError(t, err)

	_, key, err := g.svc.AddKey(t.Context(), "cc", []string{"gpt", "opus"}, nil)
	require.NoError(t, err)

	var logs bytes.Buffer

	mux := http.NewServeMux()
	// No accounts: the Claude subscription's target cannot be used.
	modelgateway.NewServer(g.svc, platform.NewLogger(&logs, platform.LogConfig{}, "test"), modelgateway.Config{}).
		Register(mux)

	send := func(path, body string) (int, string) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		return rec.Code, rec.Body.String()
	}

	status, body := send("/v1/responses", `{"model":"gpt","input":"hi"}`)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, body, "model_not_served_here")
	assert.NotContains(t, logs.String(), "usage_failed")

	rows, err := g.svc.ListUsage(t.Context(), 10)
	require.NoError(t, err)
	assert.Empty(t, rows, "a request refused before any provider is not recorded")

	status, body = send("/v1/messages",
		`{"model":"opus","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	assert.Equal(t, http.StatusBadGateway, status, body)
	assert.NotContains(t, logs.String(), "usage_failed")
	assert.Zero(t, reached.calls())

	rows, err = g.svc.ListUsage(t.Context(), 10)
	require.NoError(t, err)
	require.Len(t, rows, 1, logs.String())
	assert.Equal(t, "opus", rows[0].Model)
	assert.Equal(t, 1, rows[0].Attempts)
	assert.Equal(t, modelgateway.OutcomeTransportError, rows[0].Outcome)
	assert.Equal(t, "claude-subscription", rows[0].Provider)
}
