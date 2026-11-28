package modelgateway_test

import (
	"context"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
)

// Scenario: a Bot's model whose only target is out of capacity, a 529 and then a 503 (issue #1335, rows 1 and 2): the
// Bot's first fallback serves each request, and the usage row says the request moved on for an overload (row 6). The
// Bot's fallbacks used to be tried only after a 401, 402, 403 or 429.
func TestABotFallsBackWhenItsOnlyTargetIsOverloaded(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	var calls atomic.Int32

	primary, primaryCalls := provider(t, func(w http.ResponseWriter, r *http.Request) {
		status := 529
		if calls.Add(1) > 1 {
			status = http.StatusServiceUnavailable
		}

		plain(status, `{"error":{"message":"overloaded"}}`)(w, r)
	})
	backup, backupCalls := provider(t, plain(http.StatusOK, answer))
	g := newChatGateway(t, modelgateway.Config{
		ChatFallbacks: func(context.Context, string, string) ([]string, error) {
			return []string{"backup"}, nil
		},
	}, primary, backup)

	for _, source := range []string{
		"name: primary\ntargets: [{provider: p1, model: up-1}]\n",
		"name: backup\ntargets: [{provider: p2, model: up-2}]\n",
	} {
		_, err := g.svc.ApplyModel(ctx, source)
		require.NoError(t, err)
	}

	g.chat(t, "chat_bot", "")
	_, key, err := g.svc.AddChatKey(ctx, "chat_bot", "", "primary")
	require.NoError(t, err)

	for i := range 2 {
		status, _, body := g.call(t, key, `{"model":"primary","messages":[]}`)
		require.Equal(t, http.StatusOK, status, "request %d: %s", i+1, body)
		assert.JSONEq(t, answer, body)

		usage := g.lastUsage(t)
		assert.Equal(t, "backup", usage.Model)
		assert.Equal(t, modelgateway.OutcomeOK, usage.Outcome)
		assert.Equal(t, modelgateway.CoolingOverloaded, usage.CoolingCode, "the code the request moved on for")
	}

	assert.Equal(t, 2, primaryCalls.calls())
	assert.Equal(t, 2, backupCalls.calls())
}

// Scenario: a target refuses the request itself, a client error (row 1): the next target is not tried, since another
// provider would refuse the same request; nor is it after a failure no code names (Bifrost's caller_fault and
// unknown, docs/features/retries-and-fallbacks.mdx). The record names no code: nothing moved on.
func TestAClientErrorDoesNotMoveToTheNextTarget(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		status int
		body   string
	}{
		"a 400":              {http.StatusBadRequest, `{"error":{"message":"messages is too long"}}`},
		"a 413":              {http.StatusRequestEntityTooLarge, `{"error":{"message":"request too large"}}`},
		"a 422":              {http.StatusUnprocessableEntity, `{"error":{"message":"unprocessable"}}`},
		"an unknown failure": {http.StatusNotFound, `{"error":{"message":"no such route"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			refusing, _ := provider(t, plain(c.status, c.body))
			never, neverGot := provider(t, plain(http.StatusOK, answer))
			g := newChatGateway(t, modelgateway.Config{}, refusing, never)

			status, _, body := g.call(t, g.key, `{"model":"gpt","messages":[]}`)
			assert.Equal(t, c.status, status)
			assert.Equal(t, c.body, body)
			assert.Zero(t, neverGot.calls())
			assert.Empty(t, g.lastUsage(t).CoolingCode)
		})
	}
}

// Scenario: a target whose key is refused, whose credits are spent, or that throttles in a 400's words (row 1): the
// next target answers, with no Bot's fallback to wait for, and the record names why the request moved on (row 6).
// Bifrost rotates on a rate limit's words at any status but 401 and 403 (docs/features/retries-and-fallbacks.mdx).
func TestARefusedKeyMovesToTheNextTarget(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		status int
		body   string
		code   modelgateway.CoolingCode
	}{
		"a 401": {http.StatusUnauthorized, `{"error":{"message":"no"}}`, modelgateway.CoolingAuth},
		"a 402": {http.StatusPaymentRequired, `{"error":{"message":"no"}}`, modelgateway.CoolingBilling},
		"a 400 that says throttle": {
			http.StatusBadRequest, `{"error":{"message":"rate limit reached for requests"}}`,
			modelgateway.CoolingRateLimit,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			refusing, _ := provider(t, plain(c.status, c.body))
			healthy, healthyGot := provider(t, plain(http.StatusOK, answer))
			g := newChatGateway(t, modelgateway.Config{}, refusing, healthy)

			status, _, body := g.call(t, g.key, `{"model":"gpt","messages":[]}`)
			require.Equal(t, http.StatusOK, status, body)
			assert.Equal(t, 1, healthyGot.calls())

			usage := g.lastUsage(t)
			assert.Equal(t, 2, usage.Attempts)
			assert.Equal(t, c.code, usage.CoolingCode)
		})
	}
}

// Scenario: Claude's 429 rests the account by its code (row 3). A throttle, which Anthropic answers without saying
// its window is rejected, rests the short doubling wait even though every answer names the window's reset; a 429
// whose window is rejected rests to that reset, or the usage wait (an hour) when none is named.
func TestAClaude429RestsByItsCode(t *testing.T) {
	t.Parallel()

	reset := time.Now().Add(90 * time.Minute).Truncate(time.Second)

	// wait is how long the account rests from the refusal, or zero when it rests to the window's reset.
	for name, c := range map[string]struct {
		rejected bool
		reset    bool
		code     modelgateway.CoolingCode
		wait     time.Duration
	}{
		"a throttle":                    {false, true, modelgateway.CoolingRateLimit, 2 * time.Second},
		"a rejected window":             {true, true, modelgateway.CoolingUsageLimit, 0},
		"a rejected window, no reset":   {true, false, modelgateway.CoolingUsageLimit, time.Hour},
		"a throttle that names no wait": {false, false, modelgateway.CoolingRateLimit, 2 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			backend, _ := claudeBackend(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Anthropic-Ratelimit-Unified-Status", "allowed")
				if c.rejected {
					w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
				}

				if c.reset {
					w.Header().Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(reset.Unix(), 10))
				}

				plain(http.StatusTooManyRequests, `{"type":"error","error":{"type":"rate_limit_error",`+
					`"message":"This request would exceed your account's rate limit. Please try again later."}}`)(w, r)
			})

			acc := &accounts{}
			g := newClaudeGateway(t, backend, acc)

			asked := time.Now()
			status, _, body := g.call(t, g.key, askClaude+`,"stream":true}`)
			require.Equal(t, http.StatusTooManyRequests, status, body)
			assert.Equal(t, c.code, acc.lastCode())

			want := reset
			if c.wait > 0 {
				want = asked.Add(c.wait)
			}

			assert.WithinDuration(t, want, acc.cooledUntil(), 3*time.Second,
				"rests until %v, not %v", want, acc.cooledUntil())
		})
	}
}

// Scenario: Anthropic refuses the sign-in, even after a refresh (row 4): the account rests for the usage wait, an
// hour, not two minutes, since nothing changes until the owner signs it in again; credits spent rest as long.
func TestARefusedSignInRestsForTheUsageWait(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		status int
		body   string
		code   modelgateway.CoolingCode
	}{
		"a 401": {
			http.StatusUnauthorized, `{"type":"error","error":{"type":"authentication_error","message":"invalid"}}`,
			modelgateway.CoolingAuth,
		},
		"a 402": {
			http.StatusPaymentRequired, `{"type":"error","error":{"type":"billing_error","message":"no credits"}}`,
			modelgateway.CoolingBilling,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			backend, _ := claudeBackend(t, plain(c.status, c.body))
			acc := &accounts{}
			g := newClaudeGateway(t, backend, acc)

			status, _, body := g.call(t, g.key, askClaude+`,"stream":true}`)
			require.Equal(t, c.status, status, body)
			assert.Equal(t, c.code, acc.lastCode())
			assert.WithinDuration(t, time.Now().Add(time.Hour), acc.cooledUntil(), time.Minute,
				"rests until %v", acc.cooledUntil())
		})
	}
}
