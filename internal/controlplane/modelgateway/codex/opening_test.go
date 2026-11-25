package codex_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/codex"
)

// What may be held at a stream's opening (issue #1135): CLIProxyAPI's
// isCodexBootstrapBufferableEvent cases, in its test's words.
func TestOpeningHoldsOnlyWhatSaysNothingHappenedYet(t *testing.T) {
	t.Parallel()

	for data, want := range map[string]bool{
		``: true,
		`{"type":"response.created","response":{}}`:                                                                      true,
		`{"type":"response.in_progress"}`:                                                                                true,
		`{"type":"response.metadata","metadata":{"openai_chatgpt_moderation_metadata":{"prompt":{}}}}`:                   true,
		`{"type":"codex.rate_limits"}`:                                                                                   true,
		`{"type":"keepalive"}`:                                                                                           true,
		`{"type":"response.output_item.added","item":{"type":"message","content":[]}}`:                                   true,
		`{"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`:                                 true,
		`{"type":"response.output_item.added","item":{"type":"function_call","arguments":""}}`:                           true,
		`{"type":"response.content_part.added","part":{"type":"output_text","text":""}}`:                                 true,
		`{"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":""}}`:                      true,
		`{"type":"response.output_item.added","item":{"type":"message","content":[{"type":"output_text","text":"hi"}]}}`: false,
		`{"type":"response.output_item.added","item":{"type":"reasoning","encrypted_content":"x"}}`:                      false,
		`{"type":"response.output_item.added","item":{"type":"web_search_call","status":"in_progress"}}`:                 false,
		`{"type":"response.content_part.added","part":{"type":"output_audio"}}`:                                          false,
		`{"type":"response.output_text.delta","delta":"h"}`:                                                              false,
		`{"type":"response.completed","response":{}}`:                                                                    false,
		`{"type":"error","error":{"code":"server_is_overloaded"}}`:                                                       false,
		`[DONE]`: false,
	} {
		assert.Equal(t, want, codex.Opening([]byte(data)), data)
	}
}

// Where an error event names its Retry-After: CLIProxyAPI #7600's captures,
// the outer headers first.
func TestAnEventFailureReadsItsRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 25, 3, 0, 0, 0, time.UTC)

	for name, c := range map[string]struct {
		data string
		wait time.Duration
	}{
		"nested in error.headers": {`{"type":"error","error":{"type":"service_unavailable_error",` +
			`"code":"server_is_overloaded","message":"overloaded","headers":{"retry-after":"326"}}}`, 326 * time.Second},
		"in response.error.headers": {`{"type":"response.failed","response":{"error":{"code":"server_is_overloaded",` +
			`"message":"overloaded","headers":{"Retry-After":"45"}}}}`, 45 * time.Second},
		"top level outranks nested": {`{"type":"error","headers":{"retry-after":"180"},"error":{"code":"server_is_overloaded",` +
			`"message":"overloaded","headers":{"retry-after":"1"}}}`, 180 * time.Second},
		"a number":   {`{"type":"error","error":{"code":"server_is_overloaded","headers":{"retry-after":30}}}`, 30 * time.Second},
		"unreadable": {`{"type":"error","error":{"code":"server_is_overloaded","headers":{"retry-after":"soon"}}}`, 0},
		"none":       {`{"type":"error","error":{"code":"server_is_overloaded"}}`, 0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := codex.EventFailure([]byte(c.data), now)
			require.NotNil(t, f)
			assert.True(t, f.Overloaded())
			assert.Equal(t, http.StatusServiceUnavailable, f.Status)

			if c.wait == 0 {
				assert.True(t, f.ResetsAt.IsZero())
			} else {
				assert.Equal(t, now.Add(c.wait), f.ResetsAt)
			}
		})
	}

	assert.Nil(t, codex.EventFailure([]byte(`{"type":"response.output_text.delta"}`), now))
}

// Which refusals inside a 200 move a request to another account: CLIProxyAPI's
// isCodexOverloadBootstrapFailure.
func TestOnlyALackOfCapacityIsOverloaded(t *testing.T) {
	t.Parallel()

	for data, want := range map[string]bool{
		`{"type":"error","error":{"type":"service_unavailable_error","message":"x"}}`:                  true,
		`{"type":"error","error":{"code":"rate_limit_exceeded","message":"x"}}`:                        true,
		`{"type":"error","error":{"message":"The model is at capacity"}}`:                              true,
		`{"type":"error","error":{"type":"server_error","message":"x. You can retry your request."}}`:  true,
		`{"type":"error","error":{"type":"server_error","message":"x"}}`:                               false,
		`{"type":"error","error":{"code":"context_length_exceeded","message":"input too long"}}`:       false,
		`{"type":"response.failed","response":{"error":{"code":"usage_limit_reached","message":"x"}}}`: false,
	} {
		f := codex.EventFailure([]byte(data), time.Now())
		require.NotNil(t, f, data)
		assert.Equal(t, want, f.Overloaded(), data)
	}
}

// The moderation flags a row keeps: each flag's path and value, never more
// than the metadata says.
func TestModerationFlags(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "prompt.omnimod.protection_type=omnimod prompt.violence=true", codex.ModerationFlags([]byte(
		`{"type":"response.metadata","metadata":{"openai_chatgpt_moderation_metadata":`+
			`{"prompt":{"violence":true,"omnimod":{"protection_type":"omnimod"}}}}}`)))
	assert.Empty(t, codex.ModerationFlags([]byte(
		`{"type":"response.metadata","metadata":{"openai_chatgpt_moderation_metadata":{"prompt":{}}}}`)))
	assert.Empty(t, codex.ModerationFlags([]byte(`{"type":"response.created"}`)))
}
