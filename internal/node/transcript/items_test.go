package transcript

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// A file's whole content in a tool call or a long output is cut to the item
// limit, on a rune boundary, and says how much was left out; short fields of
// the same input are kept. Every runner's decoder cuts its items with Fit.
func TestLongItemsAreCut(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é<", 200_000)

	input, err := json.Marshal(map[string]any{"file_path": "docs/long.md", "content": long})
	require.NoError(t, err)

	cut := Fit([]nodeproto.Item{
		{Kind: nodeproto.ItemToolCall, CallID: "call_1", Tool: "Write", Input: input},
		{Kind: nodeproto.ItemToolResult, CallID: "call_1", Text: long},
	})
	require.Len(t, cut, 2)

	for _, it := range cut {
		raw, err := json.Marshal(it)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(raw), nodeproto.MaxItemBytes)
	}

	var written map[string]string
	require.NoError(t, json.Unmarshal(cut[0].Input, &written))
	assert.Equal(t, "docs/long.md", written["file_path"])
	assert.True(t, strings.HasPrefix(written["content"], "é<é<"))
	assert.Contains(t, written["content"], "more bytes not kept]")

	assert.True(t, strings.HasPrefix(cut[1].Text, "é<é<"))
	assert.Contains(t, cut[1].Text, "more bytes not kept]")

	// Many large items of one line still fit one batch together.
	many := make([]nodeproto.Item, 12)
	for i := range many {
		many[i] = nodeproto.Item{Kind: nodeproto.ItemMessage, Text: long}
	}

	total := 0

	for _, it := range Fit(many) {
		raw, err := json.Marshal(it)
		require.NoError(t, err)

		total += len(raw)
	}

	assert.LessOrEqual(t, total, nodeproto.MaxBatchBytes)
}
