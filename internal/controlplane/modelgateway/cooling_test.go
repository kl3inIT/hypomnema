package modelgateway_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
)

func TestCoolingRefusalNamesTheFirstAccountOnEachModelRoute(t *testing.T) {
	t.Parallel()
	for _, route := range []string{"/v1/responses", "/v1/messages", "/v1/messages/count_tokens", "/v1/chat/completions"} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			firstReset := time.Now().Add(time.Hour).Truncate(time.Second)
			refuse := func(w http.ResponseWriter, req *http.Request) {
				reset := firstReset
				if strings.Contains(req.Header.Get("Authorization"), "acc_2") {
					reset = reset.Add(23 * time.Hour)
				}

				w.Header().Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(reset.Unix(), 10))
				plain(http.StatusTooManyRequests, fmt.Sprintf(
					`{"error":{"type":"usage_limit_reached","message":"vendor limit","resets_at":%d}}`, reset.Unix()))(w, req)
			}

			subs := &accounts{ids: []string{"acc_1", "acc_2"}}
			var (
				gateway chatGateway
				request string
				calls   func() int
			)
			switch route {
			case "/v1/messages", "/v1/messages/count_tokens":
				backend, seen, _ := anthropicBackend(t, refuse)
				gateway, request, calls = newClaudeGateway(t, backend, subs), claudeCodeAsk, seen.calls
			case "/v1/responses":
				backend, seen := codexBackend(t, refuse)
				gateway, request, calls = newCodexGateway(t, backend, subs), codexAsks, seen.calls
			default:
				backend, seen := codexBackend(t, refuse)
				gateway, request, calls = newCodexGateway(t, backend, subs), askSol+`}`, seen.calls
			}

			for range 2 {
				status, headers, body := gateway.send(t, route, request, "Authorization", "Bearer "+gateway.key)
				require.Equal(t, http.StatusTooManyRequests, status, body)
				var answer struct {
					Type  string `json:"type"`
					Error struct {
						Code     string `json:"code"`
						ResetsAt int64  `json:"resets_at"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal([]byte(body), &answer))
				assert.Equal(t, "account_unavailable", answer.Error.Code)
				assert.Equal(t, firstReset.Unix(), answer.Error.ResetsAt)
				wait, err := strconv.Atoi(headers.Get("Retry-After"))
				require.NoError(t, err)
				assert.InDelta(t, time.Until(firstReset).Seconds(), wait, 2)
				if strings.HasPrefix(route, "/v1/messages") {
					assert.Equal(t, "error", answer.Type)
				}
				if route != "/v1/messages/count_tokens" {
					usage := gateway.lastUsage(t)
					assert.Equal(t, modelgateway.OutcomeNoAccount, usage.Outcome)
					assert.Empty(t, usage.AccountID)
				}
			}
			assert.Equal(t, 2, calls(), "once the two accounts cool, neither is called again")
		})
	}
}

func TestNoAccountUsageIsSeparateInExactAndLedgerPerformance(t *testing.T) {
	t.Parallel()
	subs := &accounts{}
	backend, seen := codexBackend(t, stream(helloEvents...))
	gateway := newCodexGateway(t, backend, subs)
	status, _, body := gateway.respond(t, gateway.key, codexAsks)
	require.Equal(t, http.StatusOK, status, body)
	require.NoError(t, subs.CoolDown(t.Context(), "codex", "acc_1", time.Now().Add(time.Hour), "usage_limit",
		"usage limit"))
	status, _, body = gateway.respond(t, gateway.key, codexAsks)
	require.Equal(t, http.StatusTooManyRequests, status, body)
	assert.Equal(t, 1, seen.calls())

	for _, period := range []string{"24h", "90d"} {
		report, err := gateway.svc.Usage(t.Context(), period, 0)
		require.NoError(t, err)
		require.Len(t, report.ByProvider, 1)
		performance := report.ByProvider[0]
		assert.Equal(t, int64(2), performance.Requests)
		assert.Equal(t, int64(1), performance.NoAccount)
		assert.Zero(t, performance.Failed)
		answered, defined := performance.SuccessRate()
		assert.True(t, defined)
		assert.InDelta(t, 1, answered, 0)
	}
	failed, _, err := gateway.svc.UsageRequests(t.Context(), "24h", true, 10, 0)
	require.NoError(t, err)
	assert.Empty(t, failed)
}
