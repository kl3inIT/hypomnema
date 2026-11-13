package modelgateway_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
)

// Scenario: the owner opens the Usage page after a day of Claude Code on the
// Claude subscription: one answer, and one Anthropic refused. Each request
// records the account that served it, the refused one Anthropic's message;
// the page's day counts both, a bar an hour, and by model, key and account;
// the requests list shows the refused one alone when asked for failures.
func TestTheUsagePageCountsWhatTheGatewayServed(t *testing.T) {
	t.Parallel()

	calls := 0
	backend, _, _ := anthropicBackend(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long"}}`)

			return
		}

		stream(claudeHello...)(w, r)
	})
	g := newClaudeGateway(t, backend, &accounts{})

	status, _, body := g.send(t, "/v1/messages", claudeCodeAsk, claudeCodeHeaders(g.key)...)
	require.Equal(t, http.StatusOK, status, body)

	status, _, _ = g.send(t, "/v1/messages", claudeCodeAsk, claudeCodeHeaders(g.key)...)
	require.Equal(t, http.StatusBadRequest, status)

	report, err := g.svc.Usage(t.Context(), "24h", 0)
	require.NoError(t, err)
	assert.Equal(t, int64(2), report.Totals.Requests)
	assert.Equal(t, int64(1), report.Totals.Failed)
	assert.Equal(t, int64(16), report.Totals.Prompt)
	assert.Equal(t, int64(4), report.Totals.Cached)
	assert.Len(t, report.Buckets, 24, "a bar an hour")
	assert.Equal(t, int64(2), report.Buckets[23].Requests, "both are in the last hour")

	require.Len(t, report.ByModel, 1)
	assert.Equal(t, "sol", report.ByModel[0].Name)
	require.Len(t, report.ByKey, 1)
	assert.Equal(t, "pi", report.ByKey[0].Name)
	require.Len(t, report.ByAccount, 1)
	assert.Equal(t, "acc_1", report.ByAccount[0].ID)
	assert.Equal(t, int64(2), report.ByAccount[0].Requests)

	all, more, err := g.svc.UsageRequests(t.Context(), "24h", false, 50, 0)
	require.NoError(t, err)
	assert.False(t, more)
	require.Len(t, all, 2)
	assert.Equal(t, "acc_1", all[0].AccountID)

	failed, _, err := g.svc.UsageRequests(t.Context(), "24h", true, 50, 0)
	require.NoError(t, err)
	require.Len(t, failed, 1)
	assert.Equal(t, "prompt is too long", failed[0].Error)
	assert.Equal(t, http.StatusBadRequest, failed[0].Status)

	page, more, err := g.svc.UsageRequests(t.Context(), "24h", false, 1, 0)
	require.NoError(t, err)
	assert.Len(t, page, 1)
	assert.True(t, more, "an older request follows")

	_, err = g.svc.Usage(t.Context(), "1y", 0)
	require.Error(t, err)
}

// Scenario: a request served by an API provider records no account.
func TestAnAPIProviderRequestRecordsNoAccount(t *testing.T) {
	t.Parallel()

	p, _ := provider(t, plain(http.StatusOK, answer))
	g := newChatGateway(t, modelgateway.Config{}, p)

	status, _, body := g.call(t, g.key, `{"model":"gpt","messages":[]}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.Empty(t, g.lastUsage(t).AccountID)
}
