package transcript

import (
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

// fake is a journal format for the tests: `m <text>` is a message, `c <text>` a
// chunk of one that goes on until a line of another kind closes it, `x` a line
// that closes it and holds no item of its own.
type fake struct {
	lines int
	run   []string
	since int
}

func newFake() Decoder { return &fake{since: -1} }

func (f *fake) Line(raw []byte) []nodeproto.Item {
	index := f.lines
	f.lines++

	kind, text, _ := strings.Cut(strings.TrimRight(string(raw), "\n"), " ")

	switch kind {
	case "c":
		if f.since < 0 {
			f.since = index
		}

		f.run = append(f.run, text)

		return nil
	case "m":
		return append(f.Flush(), nodeproto.Item{Kind: nodeproto.ItemMessage, Text: text})
	case "x":
		return f.Flush()
	}

	return nil
}

func (f *fake) OpenSince() int { return f.since }

func (f *fake) Flush() []nodeproto.Item {
	if len(f.run) == 0 {
		return nil
	}

	out := []nodeproto.Item{{Kind: nodeproto.ItemMessage, Text: strings.Join(f.run, "")}}
	f.run, f.since = nil, -1

	return out
}

// plane plays the control plane for a Forwarder: it keeps what is sent and
// acknowledges when the test says so.
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

func textsOf(b nodeproto.Items) []string {
	out := make([]string, 0, len(b.Items))
	for _, it := range b.Items {
		out = append(out, it.Text)
	}

	return out
}

type rig struct {
	t   *testing.T
	f   *Forwarder
	p   *plane
	dir string
}

func newForwarder(home string, p *plane) *Forwarder {
	return &Forwarder{
		Home: home, Send: p.send, Logger: slog.New(slog.DiscardHandler),
		Decoders: map[string]func() Decoder{"fake": newFake},
	}
}

func newRig(t *testing.T) *rig {
	t.Helper()

	p := &plane{}
	f := newForwarder(t.TempDir(), p)
	dir := f.dir("dsp_1")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, SetFormat(dir, "fake"))

	return &rig{t: t, f: f, p: p, dir: dir}
}

// restart is a daemon that starts again: a new forwarder finds the journals and their cursors.
func (g *rig) restart() { g.f = newForwarder(g.f.Home, g.p) }

func (g *rig) journal(lines ...string) {
	g.t.Helper()

	f, err := os.OpenFile(filepath.Join(g.dir, shim.JournalFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(g.t, err)

	_, err = f.WriteString(strings.Join(lines, ""))
	require.NoError(g.t, err)
	require.NoError(g.t, f.Close())
}

func (g *rig) exit() {
	g.t.Helper()
	require.NoError(g.t, os.WriteFile(filepath.Join(g.dir, shim.ExitFile), []byte(`{"code":0}`), 0o600))
}

func (g *rig) tick() { g.f.Step(g.t.Context()) }

func (g *rig) ack(seq int64) {
	g.f.ItemsAcked(nodeproto.ItemsAck{DispatchID: "dsp_1", Seq: seq, Status: nodeproto.AckCommitted})
}

// Scenario: a worker runs, the connection drops before a batch is
// acknowledged, and the daemon restarts. The control plane is sent the same
// items with the same numbers again, then the rest, and nothing after the
// worker ended and all of it arrived.
func TestATranscriptIsSentUntilAcknowledged(t *testing.T) {
	t.Parallel()

	g := newRig(t)
	g.journal("m one\n", "m two\n", "m three\n", "m half a li")
	g.tick()

	require.Len(t, g.p.sent(), 1)
	assert.Equal(t, []int64{1, 2, 3}, seqsOf(g.p.sent()[0]), "whole lines only; the half-written one waits")

	g.tick()
	require.Len(t, g.p.sent(), 1, "one batch is on its way at a time")

	// The acknowledgement is lost and the daemon restarts.
	g.restart()
	g.tick()

	require.Len(t, g.p.sent(), 2)
	assert.Equal(t, g.p.sent()[0], g.p.sent()[1], "the same items with the same numbers")

	// The line is finished and the worker ends; the acknowledgement brings the rest at once.
	g.journal("ne\n")
	g.exit()
	g.ack(3)

	require.Len(t, g.p.sent(), 3)
	assert.Equal(t, []int64{4}, seqsOf(g.p.sent()[2]))
	assert.Equal(t, []string{"half a line"}, textsOf(g.p.sent()[2]))

	g.ack(4)
	g.tick()
	g.tick()

	require.Len(t, g.p.sent(), 3)

	_, err := os.Stat(filepath.Join(g.dir, sentDone))
	require.NoError(t, err, "a transcript the control plane has whole is marked")

	g.restart()
	g.tick()
	assert.Len(t, g.p.sent(), 3, "and is not followed again")
}

// Scenario: the control plane is away while the worker runs a long turn. Once
// it is back the transcript goes in batches of at most 32 items, each after the
// one before was acknowledged.
func TestALongTranscriptGoesInBatches(t *testing.T) {
	t.Parallel()

	g := newRig(t)
	g.p.down = true

	lines := make([]string, 0, 70)
	for range 70 {
		lines = append(lines, "m step\n")
	}

	g.journal(lines...)
	g.tick()
	assert.Empty(t, g.p.sent())

	g.p.mu.Lock()
	g.p.down = false
	g.p.mu.Unlock()

	g.tick()
	g.ack(32)
	g.ack(64)

	sent := g.p.sent()
	require.Len(t, sent, 3)
	assert.Len(t, sent[0].Items, nodeproto.MaxBatchItems)
	assert.Equal(t, int64(33), sent[1].Items[0].Seq)
	assert.Equal(t, []int64{65, 66, 67, 68, 69, 70}, seqsOf(sent[2]))
}

// Scenario: the control plane refuses a transcript for good (the Dispatch is not
// this node's any more). It is not sent again.
func TestARefusedTranscriptIsNotSentAgain(t *testing.T) {
	t.Parallel()

	g := newRig(t)
	g.journal("m step\n")
	g.tick()

	g.f.ItemsAcked(nodeproto.ItemsAck{DispatchID: "dsp_1", Status: nodeproto.AckRejected, Error: "not on this node"})
	g.journal("m more\n")
	g.tick()

	assert.Len(t, g.p.sent(), 1)
}

// Scenario: a worker prints one line far larger than the node keeps in
// memory. The transcript says a line was left out and goes on with the next.
func TestAHugeLineIsPassedOver(t *testing.T) {
	t.Parallel()

	g := newRig(t)
	g.journal("m "+strings.Repeat("x", lineBytes)+"\n", "m after\n")
	g.tick()
	g.ack(1)

	sent := g.p.sent()
	require.Len(t, sent, 2)
	assert.Equal(t, nodeproto.ItemNotice, sent[0].Items[0].Kind)
	assert.Contains(t, sent[0].Items[0].Text, "was not kept")
	assert.Equal(t, "after", sent[1].Items[0].Text)
}

// Scenario: a message arrives a few words at a time. Nothing of it is sent
// while it may go on, and the journal's cursor does not pass its first line:
// a daemon that restarts in the middle sends the whole message once, and what
// came before it is not sent again. A line that closes it brings it, in order.
func TestAMessageInChunksIsSentWhole(t *testing.T) {
	t.Parallel()

	g := newRig(t)
	g.journal("m before\n", "c Hel\n", "c lo, \n")
	g.tick()

	require.Len(t, g.p.sent(), 1)
	assert.Equal(t, []string{"before"}, textsOf(g.p.sent()[0]), "the run is held")
	g.ack(1)

	g.restart()
	g.journal("c wor\n", "c ld\n")
	g.tick()
	assert.Len(t, g.p.sent(), 1, "still open")

	g.journal("m after\n")
	g.tick()

	require.Len(t, g.p.sent(), 2)
	assert.Equal(t, []string{"Hello, world", "after"}, textsOf(g.p.sent()[1]))
	assert.Equal(t, []int64{2, 3}, seqsOf(g.p.sent()[1]))
}

// Scenario: the worker ends in the middle of a message. What it said is sent
// all the same, once the journal is read to its end.
func TestAnOpenMessageIsSentWhenTheWorkerEnds(t *testing.T) {
	t.Parallel()

	g := newRig(t)
	g.journal("c half \n", "c a thought\n")
	g.tick()
	assert.Empty(t, g.p.sent())

	g.exit()
	g.tick()

	require.Len(t, g.p.sent(), 1)
	assert.Equal(t, []string{"half a thought"}, textsOf(g.p.sent()[0]))

	g.ack(1)
	g.tick()

	_, err := os.Stat(filepath.Join(g.dir, sentDone))
	require.NoError(t, err)
}

// Scenario: a batch fills up while a run of chunks is open before the line
// that no longer fits. The next batch begins at the run, so that its message is
// neither lost nor sent twice.
func TestABatchThatFillsKeepsAnOpenRun(t *testing.T) {
	t.Parallel()

	g := newRig(t)

	lines := make([]string, 0, 40)
	for range 31 {
		lines = append(lines, "m step\n")
	}

	// The 32nd item is the run; the line that closes it adds a 33rd.
	lines = append(lines, "c one \n", "c two\n", "m last\n")
	g.journal(lines...)
	g.tick()

	require.Len(t, g.p.sent(), 1)
	assert.Equal(t, int64(31), g.p.sent()[0].Items[len(g.p.sent()[0].Items)-1].Seq, "the run and its closer wait")

	g.ack(31)

	require.Len(t, g.p.sent(), 2)
	assert.Equal(t, []string{"one two", "last"}, textsOf(g.p.sent()[1]))
	assert.Equal(t, []int64{32, 33}, seqsOf(g.p.sent()[1]))
}

// Scenario: a Dispatch's journal names its format; one that names none, or
// one that no decoder reads, is left alone; a directory being prepared, with
// no journal yet, waits for its format; a directory the node released is
// forgotten.
func TestEachJournalIsReadInItsOwnFormat(t *testing.T) {
	t.Parallel()

	g := newRig(t)
	other := g.f.dir("dsp_2")
	require.NoError(t, os.MkdirAll(other, 0o700))
	require.NoError(t, SetFormat(other, "unheard-of"))
	require.NoError(t, os.WriteFile(filepath.Join(other, shim.JournalFile), []byte("m ignored\n"), 0o600))

	plain := g.f.dir("dsp_3")
	require.NoError(t, os.MkdirAll(plain, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(plain, shim.JournalFile), []byte("m ignored too\n"), 0o600))

	preparing := g.f.dir("dsp_4")
	require.NoError(t, os.MkdirAll(preparing, 0o700))

	g.journal("m read\n")
	g.tick()

	require.Len(t, g.p.sent(), 1, "only the one whose decoder exists")
	assert.Equal(t, "dsp_1", g.p.sent()[0].DispatchID)
	assert.True(t, g.f.transcripts["dsp_3"].unknown, "a journal with no format is not read")
	assert.False(t, g.f.transcripts["dsp_4"].unknown, "a directory without a journal is not judged yet")

	require.NoError(t, os.RemoveAll(g.dir))
	g.tick()
	assert.NotContains(t, g.f.transcripts, "dsp_1", "a released Dispatch is forgotten")
}
