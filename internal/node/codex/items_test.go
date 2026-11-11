package codex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// items runs a journal through the decoder, line by line.
func items(t *testing.T, lines ...string) []nodeproto.Item {
	t.Helper()

	d := NewDecoder()

	var out []nodeproto.Item

	for _, l := range lines {
		out = append(out, d.Line([]byte(l+"\n"))...)
		require.Equal(t, -1, d.OpenSince(), "a Codex line says all of itself")
	}

	return append(out, d.Flush()...)
}

// Scenario: the journal of a real Codex worker, as `codex exec --json` printed
// it on 2026-11-10 (0.167.0): a message, a shell command that starts and ends,
// the worker's own `finish` tool, a last message. The calls and results are
// what the web already draws for Codex: `shell` with its command, and the
// worker's tools as `mcp__hypomnema__<tool>`. The thread's start, the turn's
// start and its usage are not items.
func TestACodexJournalBecomesItems(t *testing.T) {
	t.Parallel()

	got := items(t,
		`{"type":"thread.started","thread_id":"01a1c621-e7d7-7c01-86ee-6735fb7e63a3"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"I’ll create hello.txt."}}`,
		`{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"/bin/bash -lc \"printf 'hi\\n' > hello.txt\"","aggregated_output":"","exit_code":null,"status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"/bin/bash -lc \"printf 'hi\\n' > hello.txt\"","aggregated_output":"hi\n","exit_code":0,"status":"completed"}}`,
		`{"type":"item.started","item":{"id":"item_2","type":"mcp_tool_call","server":"hypomnema","tool":"finish","arguments":{"summary":"Created hello.txt."},"result":null,"error":null,"status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"item_2","type":"mcp_tool_call","server":"hypomnema","tool":"finish","arguments":{"summary":"Created hello.txt."},"result":{"content":[{"type":"text","text":"Recorded. End your turn now, without calling more tools."}],"structured_content":null},"error":null,"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"Done."}}`,
		`{"type":"turn.completed","usage":{"input_tokens":33864,"cached_input_tokens":31360,"output_tokens":150}}`,
	)

	require.Len(t, got, 6)
	assert.Equal(t, nodeproto.Item{Kind: nodeproto.ItemMessage, Text: "I’ll create hello.txt."}, got[0])

	assert.Equal(t, nodeproto.ItemToolCall, got[1].Kind)
	assert.Equal(t, "item_1", got[1].CallID)
	assert.Equal(t, "shell", got[1].Tool)
	assert.JSONEq(t, `{"command":"/bin/bash -lc \"printf 'hi\\n' > hello.txt\""}`, string(got[1].Input))
	assert.Equal(t, nodeproto.Item{Kind: nodeproto.ItemToolResult, CallID: "item_1", Text: "hi\n"}, got[2])

	assert.Equal(t, "mcp__hypomnema__finish", got[3].Tool)
	assert.JSONEq(t, `{"summary":"Created hello.txt."}`, string(got[3].Input))
	assert.Equal(t, nodeproto.Item{
		Kind: nodeproto.ItemToolResult, CallID: "item_2", Text: "Recorded. End your turn now, without calling more tools.",
	}, got[4])
	assert.Equal(t, nodeproto.Item{Kind: nodeproto.ItemMessage, Text: "Done."}, got[5], "and the turn's usage is no item")
}

// Scenario: what goes wrong is shown. A command that exits non-zero, or that
// the worker was refused, fails with its exit code; a file change is one call
// and its result; a search, a thought, an error item, an error line and a failed
// turn each show.
func TestWhatGoesWrongInACodexJournalShows(t *testing.T) {
	t.Parallel()

	got := items(t,
		`{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"go test","aggregated_output":"FAIL\n","exit_code":2,"status":"failed"}}`,
		`{"type":"item.completed","item":{"id":"i2","type":"command_execution","command":"rm -rf /","aggregated_output":"","exit_code":null,"status":"declined"}}`,
		`{"type":"item.completed","item":{"id":"i3","type":"file_change","changes":[{"path":"a.go","kind":"update"},{"path":"b.go","kind":"add"}],"status":"completed"}}`,
		`{"type":"item.started","item":{"id":"i4","type":"web_search","query":"go sqlite wal"}}`,
		`{"type":"item.completed","item":{"id":"i4","type":"web_search","query":"go sqlite wal","results":[{},{}]}}`,
		`{"type":"item.completed","item":{"id":"i5","type":"reasoning","text":"The test fails on nil."}}`,
		`{"type":"item.completed","item":{"id":"i6","type":"error","message":"a hook failed"}}`,
		`{"type":"error","message":"Reconnecting... waiting for network"}`,
		`{"type":"turn.failed","error":{"message":"You've hit your usage limit."}}`,
		`{"type":"item.started","item":{"id":"i7","type":"mcp_tool_call","server":"gws","tool":"gmail.list","arguments":null,"result":null,"error":null,"status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"i7","type":"mcp_tool_call","server":"gws","tool":"gmail.list","arguments":null,"result":null,"error":{"message":"not signed in"},"status":"failed"}}`,
		`{"type":"item.updated","item":{"id":"i8","type":"todo_list","items":[{"text":"a","completed":false}]}}`,
		`not json`,
	)

	require.Len(t, got, 12)
	assert.Equal(t, nodeproto.Item{
		Kind: nodeproto.ItemToolResult, CallID: "i1", Text: "FAIL\n[exit code 2]", Failed: true,
	}, got[0])
	assert.Equal(t, nodeproto.Item{Kind: nodeproto.ItemToolResult, CallID: "i2", Text: "", Failed: true}, got[1])

	assert.Equal(t, "apply_patch", got[2].Tool)
	assert.JSONEq(t, `{"changes":[{"path":"a.go","kind":"update"},{"path":"b.go","kind":"add"}]}`, string(got[2].Input))
	assert.Equal(t, "update a.go\nadd b.go", got[3].Text)
	assert.Equal(t, "i3", got[3].CallID)

	assert.Equal(t, "web_search", got[4].Tool)
	assert.JSONEq(t, `{"query":"go sqlite wal"}`, string(got[4].Input))
	assert.Equal(t, "2 results", got[5].Text)

	assert.Equal(t, nodeproto.Item{Kind: nodeproto.ItemReasoning, Text: "The test fails on nil."}, got[6])
	assert.Equal(t, nodeproto.Item{Kind: nodeproto.ItemNotice, Text: "a hook failed"}, got[7])
	assert.Equal(t, nodeproto.Item{Kind: nodeproto.ItemNotice, Text: "Reconnecting... waiting for network"}, got[8])
	assert.Equal(t, nodeproto.Item{
		Kind: nodeproto.ItemNotice, Text: "The turn failed: You've hit your usage limit.",
	}, got[9])

	assert.Equal(t, "mcp__gws__gmail.list", got[10].Tool)
	assert.JSONEq(t, `{}`, string(got[10].Input), "no arguments is an empty input")
	assert.Equal(t, nodeproto.Item{
		Kind: nodeproto.ItemToolResult, CallID: "i7", Text: "not signed in", Failed: true,
	}, got[11])
}
