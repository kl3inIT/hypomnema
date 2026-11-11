package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

func jsonLine(t *testing.T, v any) string {
	t.Helper()

	raw, err := json.Marshal(v)
	require.NoError(t, err)

	return string(raw) + "\n"
}

func assistant(t *testing.T, blocks ...map[string]any) string {
	t.Helper()

	return jsonLine(t, map[string]any{"type": "assistant", "message": map[string]any{"content": blocks}})
}

func toolResult(t *testing.T, id string, content any, failed bool) string {
	t.Helper()

	return jsonLine(t, map[string]any{"type": "user", "message": map[string]any{"content": []map[string]any{
		{"type": "tool_result", "tool_use_id": id, "content": content, "is_error": failed},
	}}})
}

// Each kind of journal line becomes the items every runtime shares; lines
// that are not the worker's doing become none.
func TestJournalLinesBecomeItems(t *testing.T) {
	t.Parallel()

	said := itemsOf([]byte(assistant(t,
		map[string]any{"type": "thinking", "thinking": "The test fails on nil."},
		map[string]any{"type": "text", "text": "I will fix the nil check."},
		map[string]any{"type": "tool_use", "id": "call_1", "name": "Edit", "input": map[string]any{"file_path": "a.go"}},
	)))
	require.Len(t, said, 3)
	assert.Equal(t, nodeproto.Item{Kind: nodeproto.ItemReasoning, Text: "The test fails on nil."}, said[0])
	assert.Equal(t, nodeproto.Item{Kind: nodeproto.ItemMessage, Text: "I will fix the nil check."}, said[1])
	assert.Equal(t, nodeproto.ItemToolCall, said[2].Kind)
	assert.Equal(t, "call_1", said[2].CallID)
	assert.Equal(t, "Edit", said[2].Tool)
	assert.JSONEq(t, `{"file_path":"a.go"}`, string(said[2].Input))

	plain := itemsOf([]byte(toolResult(t, "call_1", "edited a.go", false)))
	assert.Equal(t, []nodeproto.Item{{Kind: nodeproto.ItemToolResult, CallID: "call_1", Text: "edited a.go"}}, plain)

	mixed := itemsOf([]byte(toolResult(t, "call_2", []map[string]any{
		{"type": "text", "text": "a screenshot:"}, {"type": "image", "source": map[string]any{"data": "aGVsbG8="}},
	}, true)))
	assert.Equal(t, []nodeproto.Item{
		{Kind: nodeproto.ItemToolResult, CallID: "call_2", Text: "a screenshot:\n[image]", Failed: true},
	}, mixed)

	limited := itemsOf([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1794300000}}`))
	require.Len(t, limited, 1)
	assert.Equal(t, nodeproto.ItemNotice, limited[0].Kind)
	assert.Contains(t, limited[0].Text, "Rate limited until 2026-11-10T")

	for name, line := range map[string]string{
		"the instruction":     jsonLine(t, map[string]any{"type": "user", "message": map[string]any{"content": "do the Task"}}),
		"the session's start": `{"type":"system","subtype":"init","session_id":"s"}`,
		"a permission prompt": `{"type":"control_request","request_id":"p","request":{"subtype":"can_use_tool"}}`,
		"the final result":    `{"type":"result","subtype":"success","result":"done"}`,
		"an allowed rate":     `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`,
		"empty text":          assistant(t, map[string]any{"type": "text", "text": "  "}),
		"what is not JSON":    "claude: warning\n",
		"an empty line":       "\n",
	} {
		assert.Empty(t, itemsOf([]byte(line)), name)
	}

	bare := itemsOf([]byte(assistant(t, map[string]any{"type": "tool_use", "id": "c", "name": "TodoRead"})))
	require.Len(t, bare, 1)
	assert.JSONEq(t, `{}`, string(bare[0].Input))
}

// A file's whole content in a tool call or a long output is cut to the item
// limit, on a rune boundary, and says how much was left out; short fields of
// the same input are kept.
func TestLongItemsAreCut(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é<", 200_000)

	written := itemsOf([]byte(assistant(t, map[string]any{
		"type": "tool_use", "id": "call_1", "name": "Write",
		"input": map[string]any{"file_path": "docs/long.md", "content": long},
	})))
	require.Len(t, written, 1)

	raw, err := json.Marshal(written[0])
	require.NoError(t, err)
	assert.LessOrEqual(t, len(raw), nodeproto.MaxItemBytes)

	var input map[string]string
	require.NoError(t, json.Unmarshal(written[0].Input, &input))
	assert.Equal(t, "docs/long.md", input["file_path"])
	assert.True(t, strings.HasPrefix(input["content"], "é<é<"))
	assert.Contains(t, input["content"], "more bytes not kept]")

	printed := itemsOf([]byte(toolResult(t, "call_1", long, false)))
	require.Len(t, printed, 1)

	raw, err = json.Marshal(printed[0])
	require.NoError(t, err)
	assert.LessOrEqual(t, len(raw), nodeproto.MaxItemBytes)
	assert.True(t, strings.HasPrefix(printed[0].Text, "é<é<"))
	assert.Contains(t, printed[0].Text, "more bytes not kept]")

	// Many large blocks in one line still fit one batch together.
	blocks := make([]map[string]any, 0, 12)
	for range 12 {
		blocks = append(blocks, map[string]any{"type": "text", "text": long})
	}

	total := 0

	for _, it := range itemsOf([]byte(assistant(t, blocks...))) {
		raw, err := json.Marshal(it)
		require.NoError(t, err)

		total += len(raw)
	}

	assert.LessOrEqual(t, total, nodeproto.MaxBatchBytes)
}
