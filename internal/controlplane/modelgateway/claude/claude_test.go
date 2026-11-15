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
// them, tools then system then messages, a tool result's own blocks before the
// next block of its message; "" for none.
func ttlsInOrder(t *testing.T, body map[string]any) []string {
	t.Helper()

	var out []string

	var walk func(v any)

	walk = func(v any) {
		list, _ := v.([]any)
		for _, block := range list {
			b, _ := block.(map[string]any)
			if cc, ok := b["cache_control"].(map[string]any); ok {
				ttl, _ := cc["ttl"].(string)
				out = append(out, ttl)
			}

			walk(b["content"])
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

// buildChat builds a chat body with one tool, so the gateway sets its three
// breakpoints too.
func buildChat(t *testing.T, messages string) map[string]any {
	t.Helper()

	chat := `{"model":"m","messages":[` + messages + `],"tools":[{"type":"function","function":{"name":"read"}}]}`
	built, err := claude.BuildRequest([]byte(chat), claude.Options{Model: "claude-sonnet-5-5", Token: "tok"})
	require.NoError(t, err)

	return decode(t, built)
}

// blocksOfMessage returns the content blocks of the body's i-th message.
func blocksOfMessage(t *testing.T, body map[string]any, i int) []map[string]any {
	t.Helper()

	messages, ok := body["messages"].([]any)
	require.True(t, ok)
	require.Greater(t, len(messages), i)
	msg, ok := messages[i].(map[string]any)
	require.True(t, ok)
	content, ok := msg["content"].([]any)
	require.True(t, ok)

	out := make([]map[string]any, 0, len(content))

	for _, block := range content {
		b, ok := block.(map[string]any)
		require.True(t, ok)

		out = append(out, b)
	}

	return out
}

// toolResultOf finds the tool_result block among a body's messages.
func toolResultOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()

	messages, _ := body["messages"].([]any)
	for i := range messages {
		for _, b := range blocksOfMessage(t, body, i) {
			if b["type"] == "tool_result" {
				return b
			}
		}
	}

	require.Fail(t, "no tool_result block")

	return nil
}

// nestedMarks counts the breakpoints left inside a tool_result's content.
func nestedMarks(result map[string]any) int {
	n := 0

	content, _ := result["content"].([]any)
	for _, block := range content {
		if b, _ := block.(map[string]any); b["cache_control"] != nil {
			n++
		}
	}

	return n
}

// Scenario: the client marked a block inside an earlier tool result for the
// cache (issues #409, #482). Anthropic refuses a breakpoint inside a
// tool_result's content, so the mark moves onto the tool_result block itself
// and none stays inside. A mark with no ttl is kept an hour; one the client
// gave five minutes stays so, and the gateway's mark on the message after it
// is not kept an hour, which Anthropic would refuse.
func TestACacheBreakpointInsideAToolResultAgreesWithTheGateways(t *testing.T) {
	t.Parallel()

	build := func(cacheControl string) map[string]any {
		return buildChat(t, `{"role":"user","content":"look at the screenshot"},`+
			`{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function",`+
			`"function":{"name":"read","arguments":"{\"path\":\"shot.png\"}"}}]},`+
			`{"role":"tool","tool_call_id":"call_1","content":[{"type":"image",`+
			`"source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="},`+
			`"cache_control":`+cacheControl+`}]},`+
			`{"role":"user","content":"what does it show?"}`)
	}

	bare := build(`{"type":"ephemeral"}`)
	result := toolResultOf(t, bare)
	assert.True(t, cached(result), "the mark is on the tool_result block, kept an hour")
	assert.Zero(t, nestedMarks(result), "no mark stays inside the result")
	assert.Equal(t, []string{"1h", "1h", "1h", "1h"}, ttlsInOrder(t, bare),
		"the gateway's tools and system marks, the client's, the gateway's last")

	short := build(`{"type":"ephemeral","ttl":"5m"}`)
	assert.Zero(t, nestedMarks(toolResultOf(t, short)))
	assert.Equal(t, []string{"1h", "1h", "5m", ""}, ttlsInOrder(t, short),
		"after the client's five-minute mark, none is kept an hour")
}

// Scenario: a client marked a text part of an early message for the cache
// (issue #482). The mark reaches Anthropic on that text block with its five
// minutes, and the gateway's mark on the last message after it is not kept an
// hour, which Anthropic would refuse.
func TestAClientsBreakpointOnATextPartReachesAnthropic(t *testing.T) {
	t.Parallel()

	body := buildChat(t, `{"role":"user","content":[{"type":"text","text":"a long document"},`+
		`{"type":"text","text":"the question","cache_control":{"type":"ephemeral","ttl":"5m"}}]},`+
		`{"role":"assistant","content":"an answer"},`+
		`{"role":"user","content":"and another"}`)

	first := blocksOfMessage(t, body, 0)
	require.Len(t, first, 2)
	assert.NotContains(t, first[0], "cache_control", "the unmarked part stays unmarked")
	assert.Equal(t, map[string]any{"type": "ephemeral", "ttl": "5m"}, first[1]["cache_control"])
	assert.Equal(t, []string{"1h", "1h", "5m", ""}, ttlsInOrder(t, body))
}

// Scenario: a blank text part is dropped, as Anthropic refuses an empty text
// block, and its mark goes to the block before it; a mark whose type is not
// ephemeral is not sent (issue #482).
func TestABlankTextPartPassesItsBreakpointBack(t *testing.T) {
	t.Parallel()

	body := buildChat(t, `{"role":"user","content":[{"type":"text","text":"a long document"},`+
		`{"type":"text","text":"  ","cache_control":{"type":"ephemeral"}}]},`+
		`{"role":"assistant","content":[{"type":"text","text":"an answer","cache_control":{"type":"forever"}}]},`+
		`{"role":"user","content":"and another"}`)

	first := blocksOfMessage(t, body, 0)
	require.Len(t, first, 1, "the blank part is dropped")
	assert.True(t, cached(first[0]), "its mark is on the block before it")

	answer := blocksOfMessage(t, body, 1)
	assert.NotContains(t, answer[0], "cache_control", "a mark that is not ephemeral is not sent")
}

// Scenario: a client marked a whole message for the cache (issue #482). The
// mark lands on the message's last block; a part's own mark there wins. The
// answer is the last message, so the gateway's own mark is the part's and the
// request holds four, all Anthropic takes (issue #504).
func TestAMessagesBreakpointLandsOnItsLastBlock(t *testing.T) {
	t.Parallel()

	body := buildChat(t, `{"role":"user","content":[{"type":"text","text":"a long document"},`+
		`{"type":"text","text":"the question"}],"cache_control":{"type":"ephemeral","ttl":"5m"}},`+
		`{"role":"assistant","content":[{"type":"text","text":"an answer","cache_control":{"type":"ephemeral"}}],`+
		`"cache_control":{"type":"ephemeral","ttl":"5m"}}`)

	first := blocksOfMessage(t, body, 0)
	require.Len(t, first, 2)
	assert.NotContains(t, first[0], "cache_control")
	assert.Equal(t, map[string]any{"type": "ephemeral", "ttl": "5m"}, first[1]["cache_control"])

	answer := blocksOfMessage(t, body, 1)
	assert.Equal(t, map[string]any{"type": "ephemeral"}, answer[0]["cache_control"],
		"the part's own mark wins, and is not kept an hour after a five-minute mark")
	assert.Equal(t, []string{"1h", "1h", "5m", ""}, ttlsInOrder(t, body))
}

// Scenario: a tool message marked as a whole or on a text part (issue #482).
// Either mark is on the tool_result block and none stays inside its content;
// a part's mark wins over the message's.
func TestAToolMessagesBreakpointIsOnItsToolResult(t *testing.T) {
	t.Parallel()

	build := func(tool string) map[string]any {
		return buildChat(t, `{"role":"user","content":"read it"},`+
			`{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function",`+
			`"function":{"name":"read","arguments":"{}"}}]},`+tool+`,`+
			`{"role":"user","content":"and now?"}`)
	}

	message := build(`{"role":"tool","tool_call_id":"call_1","content":"the file",` +
		`"cache_control":{"type":"ephemeral","ttl":"5m"}}`)
	result := toolResultOf(t, message)
	assert.Equal(t, map[string]any{"type": "ephemeral", "ttl": "5m"}, result["cache_control"])
	assert.Zero(t, nestedMarks(result))

	part := build(`{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"the file",` +
		`"cache_control":{"type":"ephemeral"}}],"cache_control":{"type":"ephemeral","ttl":"5m"}}`)
	result = toolResultOf(t, part)
	assert.True(t, cached(result), "the part's mark wins and is kept an hour")
	assert.Zero(t, nestedMarks(result), "no mark stays inside the result")
}

// Scenario: pi sends its system prompt as a developer message to a model that
// reasons (issue #504, seen on zm with pi 1.5.2). It reaches Anthropic as the
// client's system does, a system-reminder in the first user turn, not as plain
// text of the user.
func TestADeveloperMessageIsTheClientsSystem(t *testing.T) {
	t.Parallel()

	body := buildChat(t, `{"role":"developer","content":"You are pi."},{"role":"user","content":"hi"}`)

	messages, ok := body["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 1, "the developer message is not a turn of its own")

	first := blocksOfMessage(t, body, 0)
	require.Len(t, first, 2)
	assert.Equal(t, "<system-reminder>\nYou are pi.\n</system-reminder>", first[0]["text"])
	assert.Equal(t, "hi", first[1]["text"])
}

// Scenario: pi marks nothing for the cache, so its system prompt was written
// again for every new chat (issue #520, measured on zm). The gateway's system
// breakpoint is on the client's system, its last reminder, and not on Claude
// Code's identity line, so a new chat that starts with the same prompt reads
// it from the cache.
func TestTheGatewaysSystemBreakpointIsOnTheClientsSystem(t *testing.T) {
	t.Parallel()

	body := buildChat(t, `{"role":"system","content":"the rules"},{"role":"developer","content":"You are pi."},`+
		`{"role":"user","content":"hi"}`)

	first := blocksOfMessage(t, body, 0)
	require.Len(t, first, 3)
	assert.NotContains(t, first[0], "cache_control", "one breakpoint for the client's system")
	assert.True(t, cached(first[1]), "on its last block")
	assert.False(t, marked(lastOf(t, body["system"])), "none on Claude Code's identity line")
	assert.Len(t, ttlsInOrder(t, body), 3, "the tools', the client's system's and the last message's")
}

// Scenario: a client marked its system message for the cache, on a part or on
// the whole message (issue #504). The mark reaches Anthropic on the reminder
// that carries that text, kept an hour like the gateway's; the unmarked part
// stays unmarked.
func TestAClientsBreakpointOnItsSystemReachesAnthropic(t *testing.T) {
	t.Parallel()

	parts := buildChat(t, `{"role":"system","content":[{"type":"text","text":"the rules",`+
		`"cache_control":{"type":"ephemeral"}},{"type":"text","text":"today is Monday"}]},`+
		`{"role":"user","content":"hi"}`)

	first := blocksOfMessage(t, parts, 0)
	require.Len(t, first, 3)
	assert.Contains(t, first[0]["text"], "the rules")
	assert.True(t, cached(first[0]), "the marked part's reminder is marked")
	assert.Contains(t, first[1]["text"], "today is Monday")
	assert.NotContains(t, first[1], "cache_control")

	whole := buildChat(t, `{"role":"system","content":"the rules","cache_control":{"type":"ephemeral"}},`+
		`{"role":"user","content":"hi"}`)
	assert.True(t, cached(blocksOfMessage(t, whole, 0)[0]), "the marked message's reminder is marked")

	// The client's own mark stands for the system: the gateway adds none (issue #520).
	assert.False(t, marked(lastOf(t, parts["system"])), "none on Claude Code's identity line")
	assert.Len(t, ttlsInOrder(t, parts), 3)
}

// Scenario: a client marked four earlier messages for the cache; with the
// gateway's three that is seven, and Anthropic refuses more than four ("A
// maximum of 4 blocks with cache_control may be provided. Found 7.", issue
// #504, seen on zm). The earliest of the client's go; the tools', the
// system's and the last message's stay.
func TestNoMoreThanFourBreakpointsReachAnthropic(t *testing.T) {
	t.Parallel()

	var turns []string
	for _, note := range []string{"one", "two", "three", "four"} {
		turns = append(turns, `{"role":"user","content":[{"type":"text","text":"note `+note+`",`+
			`"cache_control":{"type":"ephemeral"}}]}`, `{"role":"assistant","content":"noted"}`)
	}

	body := buildChat(t, strings.Join(append(turns, `{"role":"user","content":"say ok"}`), ","))

	assert.Len(t, ttlsInOrder(t, body), 4)
	assert.True(t, cached(lastOf(t, body["tools"])), "the tools' mark stays")
	assert.True(t, cached(lastOf(t, body["system"])), "the system's mark stays")

	for i, want := range []bool{false, false, false, true} {
		assert.Equal(t, want, cached(blocksOfMessage(t, body, 2*i)[0]), "note %d", i+1)
	}

	last := blocksOfMessage(t, body, 8)
	assert.True(t, cached(last[len(last)-1]), "the last message's mark stays")
}

// Scenario: a client's own Messages body carries five breakpoints, a top-level
// one among them (issue #504). It leaves with four: the system's earlier one
// goes first, as CLIProxyAPI removes them.
func TestAPassedOnBodyKeepsFourBreakpoints(t *testing.T) {
	t.Parallel()

	raw := `{"model":"m","cache_control":{"type":"ephemeral"},` +
		`"tools":[{"name":"read","cache_control":{"type":"ephemeral"}}],` +
		`"system":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}},` +
		`{"type":"text","text":"b","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`

	out, err := claude.PassThrough([]byte(raw), claude.PassOptions{Model: "claude-sonnet-5-5"})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(out, &body))

	assert.Contains(t, body, "cache_control", "the top-level one stays")

	system, ok := body["system"].([]any)
	require.True(t, ok)
	assert.False(t, marked(system[0]), "the system's earlier one goes")
	assert.True(t, marked(system[1]))
	assert.True(t, marked(lastOf(t, body["tools"])))
	assert.Len(t, ttlsInOrder(t, body), 3, "with the top-level one, four")
}

// marked reports whether a block carries a cache_control.
func marked(block any) bool {
	b, ok := block.(map[string]any)

	return ok && b["cache_control"] != nil
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
