package claude_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/claude"
)

// decode reads a built Messages body back into a map.
func decode(t *testing.T, built claude.Request) map[string]any {
	t.Helper()

	var body map[string]any
	require.NoError(t, json.Unmarshal(built.Body, &body))

	return body
}

// cached reports whether a block carries an ephemeral cache_control kept an
// hour (issue #397).
func cached(block any) bool {
	b, ok := block.(map[string]any)
	if !ok {
		return false
	}

	cc, ok := b["cache_control"].(map[string]any)

	return ok && cc["type"] == "ephemeral" && cc["ttl"] == "1h"
}

// lastOf returns the last element of a list value, or nil.
func lastOf(t *testing.T, v any) any {
	t.Helper()

	list, ok := v.([]any)
	require.True(t, ok)
	require.NotEmpty(t, list)

	return list[len(list)-1]
}

// Scenario: the billing block is the same across two requests of the same
// conversation (so Anthropic's prompt cache can hit) and differs when the
// conversation's first message differs; it carries no per-request cch or build.
func TestBuildRequestBillingIsStableForCaching(t *testing.T) {
	t.Parallel()

	chat := `{"model":"m","messages":[{"role":"user","content":"explain the build cache please"}]}`

	billingOf := func(c string) string {
		built, err := claude.BuildRequest([]byte(c), claude.Options{Model: "claude-sonnet-5-5", Token: "tok"})
		require.NoError(t, err)
		body := decode(t, built)
		system, ok := body["system"].([]any)
		require.True(t, ok)
		first, ok := system[0].(map[string]any)
		require.True(t, ok)
		text, ok := first["text"].(string)
		require.True(t, ok)

		return text
	}

	first := billingOf(chat)
	assert.Equal(t, first, billingOf(chat), "the same conversation yields the same billing block")
	assert.NotContains(t, first, "cch=", "no per-request body hash")

	other := billingOf(`{"model":"m","messages":[{"role":"user","content":"a different opening message entirely"}]}`)
	assert.NotEqual(t, first, other, "a different first message changes the fingerprint")
}

// Scenario: the prompt-cache breakpoints Claude Code sets are placed — on the
// last system block, the last tool and the last message — so the subscription
// does not pay full input tokens every turn.
func TestBuildRequestPlacesCacheBreakpoints(t *testing.T) {
	t.Parallel()

	chat := `{"model":"m","messages":[{"role":"user","content":"hi"}],` +
		`"tools":[{"type":"function","function":{"name":"read"}}]}`

	built, err := claude.BuildRequest([]byte(chat), claude.Options{Model: "claude-sonnet-5-5", Token: "tok"})
	require.NoError(t, err)

	body := decode(t, built)
	assert.True(t, cached(lastOf(t, body["system"])), "the system prefix is cached")
	assert.True(t, cached(lastOf(t, body["tools"])), "the tool list is cached")

	last, ok := lastOf(t, body["messages"]).(map[string]any)
	require.True(t, ok)
	content, ok := last["content"].([]any)
	require.True(t, ok)
	assert.True(t, cached(content[len(content)-1]), "the conversation so far is cached")
}

// ttlsInOrder lists the ttl of every breakpoint in the order Anthropic reads
// them, tools then system then messages; "" for none.
func ttlsInOrder(t *testing.T, body map[string]any) []string {
	t.Helper()

	var out []string

	walk := func(v any) {
		list, _ := v.([]any)
		for _, block := range list {
			b, _ := block.(map[string]any)
			if cc, ok := b["cache_control"].(map[string]any); ok {
				ttl, _ := cc["ttl"].(string)
				out = append(out, ttl)
			}
		}
	}

	walk(body["tools"])
	walk(body["system"])

	messages, _ := body["messages"].([]any)
	for _, m := range messages {
		msg, _ := m.(map[string]any)
		walk(msg["content"])
	}

	return out
}

// Scenario: the client marked its own tool for the cache (issue #409). A mark
// with no ttl is kept an hour like the gateway's; one the client gave five
// minutes stays so, and no one-hour mark follows it, which Anthropic refuses.
func TestTheClientsCacheBreakpointsAgreeWithTheGateways(t *testing.T) {
	t.Parallel()

	build := func(tool string) []string {
		chat := `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[` + tool + `]}`
		built, err := claude.BuildRequest([]byte(chat), claude.Options{Model: "claude-sonnet-5-5", Token: "tok"})
		require.NoError(t, err)

		return ttlsInOrder(t, decode(t, built))
	}

	bare := build(`{"type":"function","function":{"name":"read"},"cache_control":{"type":"ephemeral"}}`)
	require.Len(t, bare, 4, "the client's mark and the gateway's three")
	assert.Equal(t, []string{"1h", "1h", "1h", "1h"}, bare)

	short := build(`{"type":"function","function":{"name":"read"},"cache_control":{"type":"ephemeral","ttl":"5m"}}`)
	require.Len(t, short, 4)
	assert.Equal(t, []string{"5m", "", "", ""}, short, "after a five-minute mark, none is kept an hour")
}

// Scenario: a probe (Claude Code's own "quota" check) stays minimal — no decoy
// tools, no cache breakpoints — so a tiny key check is not dressed up.
func TestBuildRequestLeavesAProbeMinimal(t *testing.T) {
	t.Parallel()

	built, err := claude.BuildRequest(
		[]byte(`{"model":"m","messages":[{"role":"user","content":"quota"}]}`),
		claude.Options{Model: "claude-sonnet-5-5", Token: "tok"})
	require.NoError(t, err)

	body := decode(t, built)
	_, hasTools := body["tools"]
	assert.False(t, hasTools, "a probe adds no decoy tools")
	assert.False(t, cached(lastOf(t, body["system"])), "a probe is not cached")
}

// Scenario: a chat request with a system message, a user message and one tool
// becomes a Messages body carrying Claude Code's disguise: the system message
// is lifted and the billing block goes first, a metadata.user_id is added, and
// the tool is renamed with the _ide suffix among the decoy tools. Names maps
// the sent name back.
func TestBuildRequestDisguisesAsClaudeCode(t *testing.T) {
	t.Parallel()

	chat := `{"model":"ignored","messages":[` +
		`{"role":"system","content":"Be brief."},` +
		`{"role":"user","content":"hi"}],` +
		`"tools":[{"type":"function","function":{"name":"read","description":"Read a file",` +
		`"parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}`

	built, err := claude.BuildRequest([]byte(chat), claude.Options{
		Model: "claude-sonnet-5-5", Token: "tok-for-device-id", Session: "claude:key-1",
	})
	require.NoError(t, err)

	body := decode(t, built)
	assert.Equal(t, "claude-sonnet-5-5", body["model"])
	assert.Equal(t, true, body["stream"])

	// The top-level system is exactly Claude Code's: the billing header, then
	// the Claude Code identity. The client's own system text is not here.
	system, ok := body["system"].([]any)
	require.True(t, ok, "system is a list")
	require.Len(t, system, 2, "only the billing block and the identity")
	first, ok := system[0].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, first["text"], "x-anthropic-billing-header:")
	assert.Contains(t, first["text"], "cc_entrypoint=sdk-cli")
	identity, ok := system[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "You are Claude Code, Anthropic's official CLI for Claude.", identity["text"])

	// The client's "Be brief." moved into the first user turn as a reminder.
	messages, ok := body["messages"].([]any)
	require.True(t, ok)
	user, ok := messages[0].(map[string]any)
	require.True(t, ok)
	userBlocks, ok := user["content"].([]any)
	require.True(t, ok)
	reminder, ok := userBlocks[0].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, reminder["text"], "<system-reminder>")
	assert.Contains(t, reminder["text"], "Be brief.")

	// metadata.user_id names a device, an account and the session.
	meta, ok := body["metadata"].(map[string]any)
	require.True(t, ok)
	userID, ok := meta["user_id"].(string)
	require.True(t, ok)
	assert.Contains(t, userID, `"session_id":"key-1"`)
	assert.Contains(t, userID, `"device_id"`)
	assert.Contains(t, userID, `"account_uuid"`)

	// The client tool is renamed and the decoys are appended.
	tools, ok := body["tools"].([]any)
	require.True(t, ok)
	names := toolNames(tools)
	assert.Contains(t, names, "read_ide", "the client tool carries the suffix")
	assert.NotContains(t, names, "read", "its bare name is gone")
	assert.Contains(t, names, "Bash", "a decoy tool is present")
	assert.Contains(t, names, "Read", "a decoy tool is present")

	// The disguise is undone for the answer.
	assert.Equal(t, "read", built.Names.Original("read_ide"))
}

func toolNames(tools []any) []string {
	var out []string

	for _, t := range tools {
		if tool, ok := t.(map[string]any); ok {
			if name, ok := tool["name"].(string); ok {
				out = append(out, name)
			}
		}
	}

	return out
}

// Scenario: with no tools the decoys are not added, so a plain chat is not
// made to look like a tool-using Claude Code session, and nothing leaks back
// to the client.
func TestBuildRequestWithoutToolsAddsNoDecoys(t *testing.T) {
	t.Parallel()

	built, err := claude.BuildRequest(
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		claude.Options{Model: "claude-opus-5-5"})
	require.NoError(t, err)

	body := decode(t, built)
	_, has := body["tools"]
	assert.False(t, has, "no tools were sent")
	assert.Empty(t, built.Names)
}

// Scenario: max_tokens is the Messages API's required field; a client that
// names none still gets a body Anthropic accepts.
func TestBuildRequestFillsMaxTokens(t *testing.T) {
	t.Parallel()

	built, err := claude.BuildRequest(
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		claude.Options{Model: "claude-opus-5-5"})
	require.NoError(t, err)

	body := decode(t, built)
	assert.Positive(t, body["max_tokens"], "max_tokens is filled")
}

// Scenario: reasoning_effort turns into a Messages thinking budget.
func TestBuildRequestTranslatesThinking(t *testing.T) {
	t.Parallel()

	built, err := claude.BuildRequest(
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`),
		claude.Options{Model: "claude-opus-5-5"})
	require.NoError(t, err)

	body := decode(t, built)
	thinking, ok := body["thinking"].(map[string]any)
	require.True(t, ok, "thinking is set")
	assert.Equal(t, "enabled", thinking["type"])
	assert.Positive(t, thinking["budget_tokens"])
}

// event wraps a Messages SSE payload as one data frame.
func msg(payload string) []byte { return []byte(payload) }

// Scenario: an answer's events become OpenAI chat chunks: the thinking is
// reasoning_content, the text is content, a tool call keeps the client's name,
// and the end frame carries the finish reason and the usage.
func TestStreamTranslatesAnAnswer(t *testing.T) {
	t.Parallel()

	names := claude.Names{"read_ide": "read"}
	s := claude.NewStream("chatcmpl-1", 1700000000, "claude-sonnet-5-5", names)

	var chunks []map[string]any

	feed := func(payload string) {
		for _, raw := range s.Event(msg(payload)) {
			var chunk map[string]any
			require.NoError(t, json.Unmarshal(raw, &chunk))
			chunks = append(chunks, chunk)
		}
	}

	feed(`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":12,"cache_read_input_tokens":3,` +
		`"cache_creation_input_tokens":5}}}`)
	feed(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`)
	feed(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`)
	feed(`{"type":"content_block_start","index":1,"content_block":{"type":"text"}}`)
	feed(`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hello"}}`)
	feed(`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_ide"}}`)
	feed(`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":1}"}}`)
	feed(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`)
	feed(`{"type":"message_stop"}`)

	require.True(t, s.Finished())
	require.Nil(t, s.Failed())

	assert.True(t, hasDelta(chunks, "reasoning_content", "hmm"), "the thinking is reasoning_content")
	assert.True(t, hasDelta(chunks, "content", "hello"), "the text is content")

	// The tool call keeps the client's name, not the disguised one.
	name := toolCallName(chunks)
	assert.Equal(t, "read", name)

	finish := finishReason(chunks)
	assert.Equal(t, "tool_calls", finish)

	usage := s.Usage()
	require.NotNil(t, usage)
	assert.Equal(t, int64(20), usage.PromptTokens, "input plus the cache read and written")
	assert.Equal(t, int64(9), usage.CompletionTokens)

	// The stream ends with the usage, as the client and the gateway's meter read it (issue #389).
	last := chunks[len(chunks)-1]
	assert.Equal(t, []any{}, last["choices"])
	assert.Equal(t, map[string]any{
		"prompt_tokens": 20.0, "completion_tokens": 9.0, "total_tokens": 29.0,
		"prompt_tokens_details": map[string]any{"cached_tokens": 3.0}, "cache_creation_input_tokens": 5.0,
	}, last["usage"])
}

// Scenario: an error answer of Anthropic's is read into a failure with its
// status and type.
func TestParseFailureReadsAnthropicsError(t *testing.T) {
	t.Parallel()

	f := claude.ParseFailure(429, []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`))
	assert.Equal(t, 429, f.Status)
	assert.Equal(t, "rate_limit_error", f.Type)
	assert.Equal(t, "slow down", f.Message)
}

func hasDelta(chunks []map[string]any, field, want string) bool {
	for _, c := range chunks {
		choices, _ := c["choices"].([]any)
		for _, ch := range choices {
			delta, _ := ch.(map[string]any)["delta"].(map[string]any)
			if v, ok := delta[field].(string); ok && strings.Contains(v, want) {
				return true
			}
		}
	}

	return false
}

func toolCallName(chunks []map[string]any) string {
	for _, c := range chunks {
		choices, _ := c["choices"].([]any)
		for _, ch := range choices {
			delta, _ := ch.(map[string]any)["delta"].(map[string]any)
			calls, _ := delta["tool_calls"].([]any)
			for _, call := range calls {
				fn, _ := call.(map[string]any)["function"].(map[string]any)
				if name, ok := fn["name"].(string); ok && name != "" {
					return name
				}
			}
		}
	}

	return ""
}

func finishReason(chunks []map[string]any) string {
	reasons := []string{}

	for _, c := range chunks {
		choices, _ := c["choices"].([]any)
		for _, ch := range choices {
			if r, ok := ch.(map[string]any)["finish_reason"].(string); ok && r != "" {
				reasons = append(reasons, r)
			}
		}
	}

	if len(reasons) == 0 {
		return ""
	}

	return reasons[len(reasons)-1]
}
