package modelgateway_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
)

// Scenario (review round 2 of PR #1342): OpenRouter's typed refusal, a 403 whose error.metadata says error_type
// "refusal" and the provider's code (bio_policy), as OpenRouter publishes it for a safety policy block
// (openrouter.ai/docs/cookbook/evaluate-and-optimize/model-migrations/gpt-6, "Safety Policy Blocks": "Treat a 403
// refusal as a prompt problem ... Retrying the same input returns the same block"). It ends the turn on any skin; a
// 403 that refuses the key still moves on.
func TestRoundTwoOpenRouterTypedRefusal(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		body  string
		moves bool
		code  modelgateway.CoolingCode
	}{
		"chat completions": {`{"error":{"code":403,"message":"This content was flagged for possible biological ` +
			`risk. If this seems wrong, try rephrasing your request.","metadata":{"error_type":"refusal",` +
			`"provider_code":"bio_policy"}}}`, false, ""},
		"provider code only": {`{"error":{"code":403,"message":"Declined.","metadata":{"provider_code":` +
			`"cyber_policy"}}}`, false, ""},
		"messages skin": {`{"type":"error","error":{"type":"permission_error","error_type":"refusal",` +
			`"message":"Declined."}}`, false, ""},
		"a refused key": {`{"error":{"code":403,"message":"Key is disabled","metadata":{"error_type":` +
			`"forbidden"}}}`, true, modelgateway.CoolingAuth},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			refusing, _ := provider(t, plain(http.StatusForbidden, c.body))
			next, nextGot := provider(t, plain(http.StatusOK, answer))
			g := newChatGateway(t, modelgateway.Config{}, refusing, next)

			status, _, body := g.call(t, g.key, `{"model":"gpt","messages":[]}`)
			if c.moves {
				require.Equal(t, http.StatusOK, status, body)
				assert.Equal(t, 1, nextGot.calls())
			} else {
				assert.Equal(t, http.StatusForbidden, status, body)
				assert.Zero(t, nextGot.calls(), "the next target is not asked")
			}

			assert.Equal(t, c.code, g.lastUsage(t).CoolingCode)
		})
	}
}

// Scenario (review round 2 of PR #1342, P2): Claude refuses the request's content on a 403 with two accounts. The
// refusal is the request's, not the account's: the second account is not asked, and neither account rests (Bifrost's
// caller_fault marks no key; OpenRouter's block "does not count against the provider's health").
func TestAContentRefusalIsNotTriedOnTheNextAccount(t *testing.T) {
	t.Parallel()

	backend, got := claudeBackend(t, plain(http.StatusForbidden, `{"type":"error","error":{"type":"permission_error",`+
		`"message":"Output blocked by content filtering policy"}}`))
	acc := &accounts{ids: []string{"acc_1", "acc_2"}}
	g := newClaudeGateway(t, backend, acc)

	status, _, body := g.call(t, g.key, askClaude+`,"stream":true}`)
	require.Equal(t, http.StatusForbidden, status, body)
	assert.Equal(t, 1, got.calls(), "the other account is not asked")
	assert.True(t, acc.cooledUntil().IsZero(), "no account rests for the request's content")
}

// Scenario (review round 2 of PR #1342, P2): Antigravity's chat model answers 429 with the credits balance spent and
// a retryDelay of 10 min, past the long wait that reads a used-up quota. Spent credits still rest the usage wait, an
// hour (issue #1335, row 4).
func TestAnAntigravityCreditsSpentWithALongDelayRestsTheUsageWait(t *testing.T) {
	t.Parallel()

	backend, _ := cloudCode(t, plain(http.StatusTooManyRequests, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED",`+
		`"message":"Credit balance is unavailable","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo",`+
		`"reason":"INSUFFICIENT_G1_CREDITS_BALANCE"},{"@type":"type.googleapis.com/google.rpc.RetryInfo",`+
		`"retryDelay":"600s"}]}}`))
	g := newAntigravityGateway(t, backend, &googleAccount{})

	_, _, first := g.call(t, g.key, askFlash+`}`)

	status, headers, body := g.call(t, g.key, askFlash+`}`)
	require.Equal(t, http.StatusTooManyRequests, status, first+"\n"+body)
	assert.Contains(t, body, `"cooling_code":"billing"`)
	assert.GreaterOrEqual(t, atoi(t, headers.Get("Retry-After")), 3590.0, "the usage wait, not the 600 s delay")
}
