package modelgateway_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
)

// moderated is OpenRouter's 403 for an input its moderation flagged (openrouter.ai/docs/api_reference/
// errors-and-debugging, "Moderation errors": error.metadata reasons, flagged_input, provider_name, model_slug).
const moderated = `{"error":{"code":403,"message":"openai/gpt-4o requires moderation on OpenAI. Your input was ` +
	`flagged for \"violence\". No credits were charged.","metadata":{"reasons":["violence"],` +
	`"flagged_input":"how do I...","provider_name":"OpenAI","model_slug":"openai/gpt-4o"}}}`

// Scenario (review of PR #1342): a content-policy refusal ends the turn (the owner chose that over a fallback,
// 2026-11-22, issue #1335 "Left out"): a 403 that says the input was flagged is not tried on the next target, where
// a 403 that refuses the key still moves on. The refusal reads unknown: neither the record nor a rest says "Sign
// in again".
func TestAContentPolicyRefusalEndsTheTurn(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		body  string
		moves bool
		code  modelgateway.CoolingCode
	}{
		"moderation":      {moderated, false, ""},
		"azure filter":    {`{"error":{"code":"content_filter","message":"The response was filtered"}}`, false, ""},
		"policy code":     {`{"error":{"code":"content_policy_violation","message":"rejected"}}`, false, ""},
		"a refused key":   {`{"error":{"code":403,"message":"Key is disabled"}}`, true, modelgateway.CoolingAuth},
		"a guarded model": {`{"error":{"message":"permission denied for this model"}}`, true, modelgateway.CoolingAuth},
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
				assert.Equal(t, http.StatusForbidden, status)
				assert.Equal(t, c.body, body)
				assert.Zero(t, nextGot.calls(), "the next target is not asked")
			}

			assert.Equal(t, c.code, g.lastUsage(t).CoolingCode)
		})
	}
}

// streamOrPlain answers a chat request, whose body was body, as it asked: a stream for a stream, else one completion.
func streamOrPlain(body string) http.HandlerFunc {
	if strings.Contains(body, `"stream":true`) {
		return stream(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"}}]}`+"\n\n",
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},`+
				`"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`+"\n\n",
			"data: [DONE]\n\n",
		)
	}

	return plain(http.StatusOK, answer)
}

// Scenario (review of PR #1342, row 6): the model's first target is a Codex subscription whose every account rests
// for a used-up window, and its next answers. Every route records the code it moved on for: /chat/completions,
// /messages, /responses and the control plane's own completions.
func TestEveryRouteRecordsTheCodeItMovedOnFor(t *testing.T) {
	t.Parallel()

	resting, _ := codexBackend(t, plain(http.StatusOK, answer))
	var asked *seen

	healthy, seenByHealthy := provider(t, func(w http.ResponseWriter, r *http.Request) {
		_, body := asked.last(t)
		streamOrPlain(body)(w, r)
	})
	asked = seenByHealthy
	subs := &accounts{
		cooled: map[string]time.Time{"acc_1": time.Now().Add(time.Hour)},
		codes:  map[string]modelgateway.CoolingCode{"acc_1": modelgateway.CoolingUsageLimit},
	}
	g := newChatGateway(t, modelgateway.Config{Subscriptions: subs, CodexBaseURL: resting.URL}, healthy)
	ctx := t.Context()

	_, err := g.svc.ApplyModel(ctx, "name: mixed\ntargets:\n"+
		"  - {provider: codex-subscription, model: gpt-6.1-sol}\n  - {provider: p1, model: up-1}\n")
	require.NoError(t, err)
	_, key, err := g.svc.AddKey(ctx, "mixed", []string{"mixed"}, nil)
	require.NoError(t, err)

	keyID, err := g.svc.MeetingsKey(ctx)
	require.NoError(t, err)

	for name, ask := range map[string]func() (int, string){
		"/chat/completions": func() (int, string) {
			status, _, body := g.call(t, key, `{"model":"mixed","messages":[{"role":"user","content":"hi"}]}`)
			return status, body
		},
		"/messages": func() (int, string) {
			status, _, body := g.send(t, "/v1/messages",
				`{"model":"mixed","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
				"Authorization", "Bearer "+key, "Anthropic-Version", "2023-06-01")
			return status, body
		},
		"/responses": func() (int, string) {
			status, _, body := g.respond(t, key, `{"model":"mixed","input":"hi"}`)
			return status, body
		},
		"completions": func() (int, string) {
			body, err := g.server.Complete(ctx, keyID,
				[]byte(`{"model":"mixed","messages":[{"role":"user","content":"hi"}]}`))
			if err != nil {
				return 0, err.Error()
			}

			return http.StatusOK, string(body)
		},
	} {
		status, body := ask()
		require.Equal(t, http.StatusOK, status, "%s: %s", name, body)

		u := g.lastUsage(t)
		assert.Equal(t, "p1", u.Provider, name)
		assert.Equal(t, modelgateway.CoolingUsageLimit, u.CoolingCode, name)
	}
}

// Scenario (review of PR #1342, row 6): the first target is overloaded (529), the second has no account at all,
// the third answers: the record keeps overloaded, which a skip that names no code does not overwrite.
func TestASkipWithNoCodeKeepsTheCodeItMovedOnFor(t *testing.T) {
	t.Parallel()

	overloaded, _ := provider(t, plain(529, `{"error":{"message":"overloaded"}}`))
	healthy, healthyGot := provider(t, plain(http.StatusOK, answer))
	none, _ := codexBackend(t, plain(http.StatusOK, answer))
	g := newChatGateway(t, modelgateway.Config{Subscriptions: &accounts{none: true}, CodexBaseURL: none.URL},
		overloaded, healthy)
	ctx := t.Context()

	_, err := g.svc.ApplyModel(ctx, "name: mixed\ntargets:\n  - {provider: p1, model: up-1}\n"+
		"  - {provider: codex-subscription, model: gpt-6.1-sol}\n  - {provider: p2, model: up-2}\n")
	require.NoError(t, err)
	_, key, err := g.svc.AddKey(ctx, "mixed", []string{"mixed"}, nil)
	require.NoError(t, err)

	status, _, body := g.call(t, key, `{"model":"mixed","messages":[]}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, 1, healthyGot.calls())

	u := g.lastUsage(t)
	assert.Equal(t, 3, u.Attempts)
	assert.Equal(t, modelgateway.CoolingOverloaded, u.CoolingCode)
}

// Scenario (review of PR #1342, row 4): Antigravity's image model answers 429 with the credits balance spent
// (INSUFFICIENT_G1_CREDITS_BALANCE). It is billing and rests the usage wait, an hour, not the throttle's 30 s the 429
// names, since only the owner can add credits.
func TestAnAntigravityImageWithCreditsSpentRestsTheUsageWait(t *testing.T) {
	t.Parallel()

	backend, _ := imageBackend(t, "/v1internal:generateContent", answerWith(http.StatusTooManyRequests,
		`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Credit balance is unavailable",`+
			`"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo",`+
			`"reason":"INSUFFICIENT_G1_CREDITS_BALANCE"}]}}`))
	subs := &imageAccounts{all: []imageAccount{{id: "g_1", runtime: "antigravity", project: "p"}}}
	g := newImageGateway(t, subs, "", backend.URL)

	status, header, body := g.post(t, "/v1/images/generations", g.key,
		`{"prompt":"a fox","model":"antigravity/auto"}`)
	require.Equal(t, http.StatusTooManyRequests, status, body)
	assert.GreaterOrEqual(t, atoi(t, header.Get("Retry-After")), 3590.0)

	rests, err := g.server.ImageRests(t.Context())
	require.NoError(t, err)
	assert.Equal(t, modelgateway.CoolingBilling, rests["antigravity"]["g_1"].Code)
	assert.WithinDuration(t, time.Now().Add(time.Hour), rests["antigravity"]["g_1"].Until, time.Minute)
}

// Scenario (review of PR #1342, P2): Anthropic refuses the sign-in with a Retry-After of 30 s. A refused sign-in
// changes only when the owner signs in again, so the account rests the usage wait whatever the header says, as a
// spent Zen balance does (opencode_adapter.go, "An hour whatever Retry-After says").
func TestARefusedClaudeSignInIgnoresAShortRetryAfter(t *testing.T) {
	t.Parallel()

	backend, _ := claudeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		plain(http.StatusUnauthorized, `{"type":"error","error":{"type":"authentication_error","message":"no"}}`)(w, r)
	})
	acc := &accounts{}
	g := newClaudeGateway(t, backend, acc)

	status, _, body := g.call(t, g.key, askClaude+`,"stream":true}`)
	require.Equal(t, http.StatusUnauthorized, status, body)
	assert.Equal(t, modelgateway.CoolingAuth, acc.lastCode())
	assert.WithinDuration(t, time.Now().Add(time.Hour), acc.cooledUntil(), time.Minute)
}

// Scenario (review of PR #1342, P2; issue #1335 row 3 "or a named window"): a Claude 429 whose words name its
// weekly or 5-hour limit is a used-up window even without the unified status, and rests the usage wait.
func TestAClaude429ThatNamesItsWindowIsAUsageLimit(t *testing.T) {
	t.Parallel()

	for _, message := range []string{
		"You have reached your weekly limit. Try again later.",
		"5-hour limit reached",
	} {
		t.Run(message, func(t *testing.T) {
			t.Parallel()

			refusal, err := json.Marshal(map[string]any{
				"type": "error", "error": map[string]string{"type": "rate_limit_error", "message": message},
			})
			require.NoError(t, err)

			backend, _ := claudeBackend(t, plain(http.StatusTooManyRequests, string(refusal)))
			acc := &accounts{}
			g := newClaudeGateway(t, backend, acc)

			status, _, body := g.call(t, g.key, askClaude+`,"stream":true}`)
			require.Equal(t, http.StatusTooManyRequests, status, body)
			assert.Equal(t, modelgateway.CoolingUsageLimit, acc.lastCode())
			assert.WithinDuration(t, time.Now().Add(time.Hour), acc.cooledUntil(), time.Minute)
		})
	}
}
