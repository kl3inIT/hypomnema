package claude

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

// plane plays the control plane for a Runner's transcripts: it keeps what is
// sent and acknowledges when the test says so.
type plane struct {
	mu      sync.Mutex
	batches []nodeproto.Items
	down    bool
}

func (p *plane) send(b nodeproto.Items) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.down {
		return false
	}

	p.batches = append(p.batches, b)

	return true
}

func (p *plane) sent() []nodeproto.Items {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]nodeproto.Items(nil), p.batches...)
}

func seqsOf(b nodeproto.Items) []int64 {
	out := make([]int64, 0, len(b.Items))
	for _, it := range b.Items {
		out = append(out, it.Seq)
	}

	return out
}

type sender struct {
	t   *testing.T
	rn  *Runner
	p   *plane
	dir string
}

func newSender(t *testing.T) *sender {
	t.Helper()

	p := &plane{}
	rn := &Runner{Home: t.TempDir(), Logger: slog.New(slog.DiscardHandler), Send: p.send}
	dir := rn.dir("dsp_1")
	require.NoError(t, os.MkdirAll(dir, 0o700))

	return &sender{t: t, rn: rn, p: p, dir: dir}
}

func (s *sender) journal(lines ...string) {
	s.t.Helper()

	f, err := os.OpenFile(filepath.Join(s.dir, shim.JournalFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(s.t, err)

	_, err = f.WriteString(strings.Join(lines, ""))
	require.NoError(s.t, err)
	require.NoError(s.t, f.Close())
}

// tick is one pass of Forward over every transcript.
func (s *sender) tick() {
	s.rn.smu.Lock()
	defer s.rn.smu.Unlock()

	for _, sess := range s.rn.transcripts {
		s.rn.step(s.t.Context(), sess)
	}
}

func (s *sender) ack(seq int64) {
	s.rn.ItemsAcked(nodeproto.ItemsAck{DispatchID: "dsp_1", Seq: seq, Status: nodeproto.AckCommitted})
}

// Scenario: a worker runs, the connection drops before a batch is
// acknowledged, and the daemon restarts. The control plane is sent the same
// items with the same numbers again, then the rest, and nothing after the
// worker ended and all of it arrived.
func TestATranscriptIsSentUntilAcknowledged(t *testing.T) {
	t.Parallel()

	s := newSender(t)
	s.journal(
		`{"type":"system","subtype":"init","session_id":"s"}`+"\n",
		assistant(t, map[string]any{"type": "text", "text": "Reading the handler."},
			map[string]any{"type": "tool_use", "id": "call_1", "name": "Read", "input": map[string]any{"file_path": "a.go"}}),
		toolResult(t, "call_1", "package a", false),
		`{"type":"assistant","message":{"content":[{"type":"text","text":"half a li`,
	)

	s.rn.follow("dsp_1", s.dir)
	s.tick()

	require.Len(t, s.p.sent(), 1)
	assert.Equal(t, []int64{1, 2, 3}, seqsOf(s.p.sent()[0]), "whole lines only; the half-written one waits")

	s.tick()
	require.Len(t, s.p.sent(), 1, "one batch is on its way at a time")

	// The acknowledgement is lost and the daemon restarts: a new Runner finds the journal and its cursor.
	s.rn = &Runner{Home: s.rn.Home, Logger: s.rn.Logger, Send: s.p.send}
	s.rn.discover()
	s.tick()

	require.Len(t, s.p.sent(), 2)
	assert.Equal(t, s.p.sent()[0], s.p.sent()[1], "the same items with the same numbers")

	// The line is finished and the worker ends; the acknowledgement brings the rest at once.
	s.journal(`ne"}]}}` + "\n")
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, shim.ExitFile), []byte(`{"code":0}`), 0o600))
	s.ack(3)

	require.Len(t, s.p.sent(), 3)
	assert.Equal(t, []int64{4}, seqsOf(s.p.sent()[2]))
	assert.Equal(t, "half a line", s.p.sent()[2].Items[0].Text)

	s.ack(4)
	s.tick()
	s.tick()

	require.Len(t, s.p.sent(), 3)

	_, err := os.Stat(filepath.Join(s.dir, sentDone))
	require.NoError(t, err, "a transcript the control plane has whole is marked")

	s.rn = &Runner{Home: s.rn.Home, Logger: s.rn.Logger, Send: s.p.send}
	s.rn.discover()
	assert.Empty(t, s.rn.transcripts, "and is not followed again")
}

// Scenario: the control plane is away while the worker runs a long turn. Once
// it is back the transcript goes in batches of at most 32 items, each after the
// one before was acknowledged.
func TestALongTranscriptGoesInBatches(t *testing.T) {
	t.Parallel()

	s := newSender(t)
	s.p.down = true

	lines := make([]string, 0, 70)
	for range 70 {
		lines = append(lines, assistant(t, map[string]any{"type": "text", "text": "step"}))
	}

	s.journal(lines...)
	s.rn.follow("dsp_1", s.dir)
	s.tick()
	assert.Empty(t, s.p.sent())

	s.p.mu.Lock()
	s.p.down = false
	s.p.mu.Unlock()

	s.tick()
	s.ack(32)
	s.ack(64)

	sent := s.p.sent()
	require.Len(t, sent, 3)
	assert.Len(t, sent[0].Items, nodeproto.MaxBatchItems)
	assert.Equal(t, int64(33), sent[1].Items[0].Seq)
	assert.Equal(t, []int64{65, 66, 67, 68, 69, 70}, seqsOf(sent[2]))
}

// Scenario: the control plane refuses a transcript for good (the Dispatch is not
// this node's any more). It is not sent again.
func TestARefusedTranscriptIsNotSentAgain(t *testing.T) {
	t.Parallel()

	s := newSender(t)
	s.journal(assistant(t, map[string]any{"type": "text", "text": "step"}))
	s.rn.follow("dsp_1", s.dir)
	s.tick()

	s.rn.ItemsAcked(nodeproto.ItemsAck{DispatchID: "dsp_1", Status: nodeproto.AckRejected, Error: "not on this node"})
	s.journal(assistant(t, map[string]any{"type": "text", "text": "more"}))
	s.tick()

	assert.Len(t, s.p.sent(), 1)
}
