package acp

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/shim"
	"github.com/kl3inIT/hypomnema/internal/node/transcript"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// upd is a `session/update` line of an agent's journal.
func upd(t *testing.T, kind string, fields map[string]any) string {
	t.Helper()

	fields["sessionUpdate"] = kind

	return line(t, map[string]any{
		"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "s", "update": fields},
	})
}

func line(t *testing.T, v any) string {
	t.Helper()

	raw, err := json.Marshal(v)
	require.NoError(t, err)

	return string(raw)
}

func chunk(t *testing.T, kind, text string) string {
	t.Helper()

	return upd(t, kind, map[string]any{"messageId": "m", "content": map[string]any{"type": "text", "text": text}})
}

func text(s string) []any {
	return []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": s}}}
}

func ended(t *testing.T, reason string) string {
	t.Helper()

	return line(t, map[string]any{"jsonrpc": "2.0", "id": 3, "result": map[string]any{"stopReason": reason}})
}

// feed gives lines to a decoder and returns, for each, its items and what the
// decoder holds open after it.
func feed(d transcript.Decoder, lines ...string) (out [][]nodeproto.Item, open []int) {
	for _, l := range lines {
		out = append(out, d.Line([]byte(l+"\n")))
		open = append(open, d.OpenSince())
	}

	return out, open
}

// Scenario: an opencode turn as its journal records it (2.2.7, 2026-11-10).
// A call is named with an empty input and gets it in its first update: it is one
// item then, and until then the decoder holds the line that named it. Its later
// updates repeat the input and add nothing; the result is an item when the call
// ends. The worker's `finish` is named as the web knows it. The words of the
// answer arrive a few at a time and are one message, which the turn's result
// closes; the usage in between closes nothing.
func TestAnOpencodeTurnBecomesItems(t *testing.T) {
	t.Parallel()

	d := NewDecoder()
	got, open := feed(d,
		upd(t, "available_commands_update", map[string]any{"availableCommands": []any{}}),
		upd(t, "tool_call", map[string]any{
			"toolCallId": "c1", "name": "patch", "title": "patch", "kind": "edit", "status": "pending", "rawInput": map[string]any{},
		}),
		upd(t, "tool_call_update", map[string]any{
			"toolCallId": "c1", "status": "in_progress", "rawInput": map[string]any{"patchText": "*** Add File: hello.txt"},
		}),
		upd(t, "tool_call_update", map[string]any{
			"toolCallId": "c1", "status": "in_progress", "rawInput": map[string]any{"patchText": "*** Add File: hello.txt"},
		}),
		upd(t, "tool_call_update", map[string]any{
			"toolCallId": "c1", "status": "completed", "content": append(text("Success. Updated: A hello.txt"),
				map[string]any{"type": "diff", "path": "hello.txt", "newText": "hi"}),
		}),
		upd(t, "tool_call", map[string]any{
			"toolCallId": "c2", "name": "hypomnema_finish", "kind": "other", "status": "pending", "rawInput": map[string]any{},
		}),
		upd(t, "tool_call_update", map[string]any{
			"toolCallId": "c2", "status": "in_progress", "rawInput": map[string]any{"summary": "Made hello.txt."},
		}),
		upd(t, "tool_call_update", map[string]any{
			"toolCallId": "c2", "status": "completed", "content": text("Recorded. End your turn now."),
		}),
		chunk(t, "agent_message_chunk", "Created `hello.txt` containing only:\n"),
		chunk(t, "agent_message_chunk", "```text\nhi\n```"),
		upd(t, "usage_update", map[string]any{"used": 4506, "size": 128000}),
		ended(t, "end_turn"),
	)

	assert.Equal(t, []int{-1, 1, -1, -1, -1, 5, -1, -1, 8, 8, 8, -1}, open)

	assert.Empty(t, got[0])
	assert.Empty(t, got[1], "a call with no input yet is held")

	require.Len(t, got[2], 1)
	assert.Equal(t, nodeproto.ItemToolCall, got[2][0].Kind)
	assert.Equal(t, "c1", got[2][0].CallID)
	assert.Equal(t, "patch", got[2][0].Tool)
	assert.JSONEq(t, `{"patchText":"*** Add File: hello.txt"}`, string(got[2][0].Input))
	assert.Empty(t, got[3], "its second update repeats the input")

	assert.Equal(t, []nodeproto.Item{{
		Kind: nodeproto.ItemToolResult, CallID: "c1", Text: "Success. Updated: A hello.txt\n[diff of hello.txt]",
	}}, got[4])

	require.Len(t, got[6], 1)
	assert.Equal(t, "mcp__hypomnema__finish", got[6][0].Tool)
	assert.Equal(t, "Recorded. End your turn now.", got[7][0].Text)

	assert.Empty(t, got[8])
	assert.Empty(t, got[10])
	assert.Equal(t, []nodeproto.Item{{
		Kind: nodeproto.ItemMessage, Text: "Created `hello.txt` containing only:\n```text\nhi\n```",
	}}, got[11])
}

// Scenario: an omp turn (18.18.6). A call has its input when it is named, so
// it is an item at once and nothing is held; its updates carry the output in
// the raw output when they carry none as content; a failed call is a failed
// result; a thought and an answer are runs of chunks, the one closed by the
// other; the worker's tool is `mcp__hypomnema_finish` there.
func TestAnOmpTurnBecomesItems(t *testing.T) {
	t.Parallel()

	d := NewDecoder()
	got, open := feed(d,
		chunk(t, "agent_thought_chunk", "Check the "),
		chunk(t, "agent_thought_chunk", "file first."),
		upd(t, "tool_call", map[string]any{
			"toolCallId": "w1", "name": "write", "title": "Creating hello file", "kind": "edit", "status": "pending",
			"rawInput": map[string]any{"path": "hello.txt", "content": "hi\n"},
		}),
		upd(t, "tool_call_update", map[string]any{"toolCallId": "w1", "status": "in_progress"}),
		upd(t, "tool_call_update", map[string]any{
			"toolCallId": "w1", "status": "completed",
			"rawOutput": map[string]any{"content": []any{map[string]any{"type": "text", "text": "Wrote 3 bytes"}}},
		}),
		upd(t, "tool_call", map[string]any{
			"toolCallId": "r1", "name": "read", "kind": "read", "status": "pending", "rawInput": map[string]any{"path": "nope"},
		}),
		upd(t, "tool_call_update", map[string]any{
			"toolCallId": "r1", "status": "failed", "content": text("ENOENT: no such file"),
		}),
		upd(t, "tool_call", map[string]any{
			"toolCallId": "f1", "name": "mcp__hypomnema_finish", "kind": "other", "status": "pending",
			"rawInput": map[string]any{"summary": "Done."},
		}),
		chunk(t, "agent_message_chunk", "All "),
		chunk(t, "agent_message_chunk", "done."),
		ended(t, "end_turn"),
	)

	assert.Equal(t, []int{0, 0, -1, -1, -1, -1, -1, -1, 8, 8, -1}, open)

	assert.Equal(t, []nodeproto.Item{{Kind: nodeproto.ItemReasoning, Text: "Check the file first."}}, got[2][:1])
	require.Len(t, got[2], 2, "the thought, then the call that closed it")
	assert.Equal(t, "write", got[2][1].Tool)
	assert.JSONEq(t, `{"path":"hello.txt","content":"hi\n"}`, string(got[2][1].Input))

	assert.Empty(t, got[3])
	assert.Equal(t, nodeproto.Item{Kind: nodeproto.ItemToolResult, CallID: "w1", Text: "Wrote 3 bytes"}, got[4][0])

	assert.Equal(t, nodeproto.Item{
		Kind: nodeproto.ItemToolResult, CallID: "r1", Text: "ENOENT: no such file", Failed: true,
	}, got[6][0])
	assert.Equal(t, "mcp__hypomnema__finish", got[7][0].Tool)
	assert.Equal(t, "All done.", got[10][0].Text)
}

// Scenario: the turn does not end well. A turn that stops for another reason
// than the end of it, and an error the agent answers with, each show as a
// notice; what was open before them is closed first.
func TestATurnThatDoesNotEndWellShows(t *testing.T) {
	t.Parallel()

	got, _ := feed(NewDecoder(),
		chunk(t, "agent_message_chunk", "Half of it"),
		ended(t, "max_tokens"),
		line(t, map[string]any{
			"jsonrpc": "2.0", "id": 3, "error": map[string]any{"code": -32603, "message": "provider.rate-limit"},
		}),
		line(t, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": 1}}),
		`{"jsonrpc":"2.0","id":9,"method":"session/request_permission","params":{}}`,
		`not json`,
	)

	assert.Equal(t, []nodeproto.Item{
		{Kind: nodeproto.ItemMessage, Text: "Half of it"},
		{Kind: nodeproto.ItemNotice, Text: "The turn ended: max_tokens"},
	}, got[1])
	assert.Equal(t, []nodeproto.Item{{Kind: nodeproto.ItemNotice, Text: "Error: provider.rate-limit"}}, got[2])

	for _, rest := range got[3:] {
		assert.Empty(t, rest)
	}
}

// Scenario: the worker ends with a call whose input never came and a message
// still open. Both are sent as they are when the journal is read to its end,
// and a message too long for one item goes in pieces as it grows.
func TestWhatIsOpenWhenTheWorkerEndsIsFlushed(t *testing.T) {
	t.Parallel()

	d := NewDecoder()
	feed(d,
		upd(t, "tool_call", map[string]any{"toolCallId": "c9", "name": "read", "rawInput": map[string]any{}}),
		chunk(t, "agent_message_chunk", "the rest"),
	)

	assert.Equal(t, []nodeproto.Item{
		{Kind: nodeproto.ItemToolCall, CallID: "c9", Tool: "read", Input: json.RawMessage(`{}`)},
		{Kind: nodeproto.ItemMessage, Text: "the rest"},
	}, d.Flush(), "in the order the journal named them")
	assert.Equal(t, -1, d.OpenSince())

	long := strings.Repeat("x", nodeproto.MaxItemBytes/4)
	pieces := 0

	growing := NewDecoder()

	for range 6 {
		out, _ := feed(growing, chunk(t, "agent_message_chunk", long))
		pieces += len(out[0])
	}

	assert.Equal(t, 2, pieces, "six quarters of an item make two items, each cut as it passes half of one")
}

// plane keeps what a forwarder sends.
type plane struct {
	mu      sync.Mutex
	batches []nodeproto.Items
}

func (p *plane) send(b nodeproto.Items) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.batches = append(p.batches, b)

	return true
}

func (p *plane) items() []nodeproto.Item {
	p.mu.Lock()
	defer p.mu.Unlock()

	var out []nodeproto.Item
	for _, b := range p.batches {
		out = append(out, b.Items...)
	}

	return out
}

// Scenario: the journal of an opencode worker reaches the control plane
// through the forwarder while it is written, and the daemon restarts between
// the line that names a call and the one that gives its input. Nothing is lost
// and nothing is sent twice: the call is held back until its input is known.
func TestAJournalIsForwardedAcrossARestart(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := filepath.Join(home, "run", "dsp_1")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, transcript.SetFormat(dir, Format))

	write := func(lines ...string) {
		//nolint:gosec // the test's own directory
		f, err := os.OpenFile(filepath.Join(dir, shim.JournalFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		require.NoError(t, err)

		_, err = f.WriteString(strings.Join(lines, "\n") + "\n")
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}

	p := &plane{}

	forwarder := func() *transcript.Forwarder {
		return &transcript.Forwarder{
			Home: home, Send: p.send, Logger: slog.New(slog.DiscardHandler),
			Decoders: map[string]func() transcript.Decoder{Format: NewDecoder},
		}
	}

	f := forwarder()
	ack := func(seq int64) {
		f.ItemsAcked(nodeproto.ItemsAck{DispatchID: "dsp_1", Seq: seq, Status: nodeproto.AckCommitted})
	}

	write(
		chunk(t, "agent_message_chunk", "I will "),
		chunk(t, "agent_message_chunk", "read it."),
		upd(t, "tool_call", map[string]any{"toolCallId": "c1", "name": "read", "rawInput": map[string]any{}}),
	)
	f.Step(t.Context())
	require.Len(t, p.items(), 1, "the message, which the call closed; the call waits")
	assert.Equal(t, "I will read it.", p.items()[0].Text)
	ack(1)

	// The daemon restarts: the call is read again from its line.
	f = forwarder()

	write(
		upd(t, "tool_call_update", map[string]any{
			"toolCallId": "c1", "status": "in_progress", "rawInput": map[string]any{"path": "a"},
		}),
		upd(t, "tool_call_update", map[string]any{"toolCallId": "c1", "status": "completed", "content": text("package a")}),
		ended(t, "end_turn"),
	)
	require.NoError(t, os.WriteFile(filepath.Join(dir, shim.ExitFile), []byte(`{"code":0}`), 0o600))

	f.Step(t.Context())

	got := p.items()
	require.Len(t, got, 3)
	assert.Equal(t, []int64{1, 2, 3}, []int64{got[0].Seq, got[1].Seq, got[2].Seq})
	assert.Equal(t, "read", got[1].Tool)
	assert.JSONEq(t, `{"path":"a"}`, string(got[1].Input))
	assert.Equal(t, "package a", got[2].Text)
}
