package orchestration

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seqs(items []Item) []int64 {
	out := make([]int64, 0, len(items))
	for _, it := range items {
		out = append(out, it.Seq)
	}

	return out
}

// Scenario: the node sends a worker's transcript in batches and, after a lost
// acknowledgement, sends a batch again. Every item is kept once, in order,
// and the Dispatch says what the worker did last.
func TestATranscriptIsKeptOnceAndInOrder(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0]
	d, _ := w.start(task.ID)
	ctx := t.Context()

	first := []Item{
		{Seq: 1, Kind: ItemMessage, Text: "I will run the tests first.\nThen fix what fails."},
		{Seq: 2, Kind: ItemToolCall, CallID: "call_1", Tool: "Bash", Input: json.RawMessage(`{"command":"go test ./..."}`)},
	}

	last, err := w.s.AddItems(ctx, w.node.ID, d.ID, first)
	require.NoError(t, err)
	assert.Equal(t, int64(2), last)
	assert.Equal(t, "Bash: go test ./...", w.dispatchOf(d.ID).Activity)

	// The batch comes again with the next item: the first two are not stored twice or changed.
	again := append([]Item{{Seq: 1, Kind: ItemMessage, Text: "changed"}, first[1]},
		Item{Seq: 3, Kind: ItemToolResult, CallID: "call_1", Text: "FAIL", Failed: true})

	last, err = w.s.AddItems(ctx, w.node.ID, d.ID, again)
	require.NoError(t, err)
	assert.Equal(t, int64(3), last)
	assert.Equal(t, "Bash: go test ./...", w.dispatchOf(d.ID).Activity, "a result says nothing new on the card")

	page, err := w.s.ListItems(ctx, d.ID, 0, 0, 0)
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, 3}, seqs(page.Items))
	assert.False(t, page.HasMore)
	assert.Equal(t, first[0].Text, page.Items[0].Text)
	assert.Equal(t, ItemToolCall, page.Items[1].Kind)
	assert.JSONEq(t, `{"command":"go test ./..."}`, string(page.Items[1].Input))
	assert.True(t, page.Items[2].Failed)
	assert.Equal(t, "call_1", page.Items[2].CallID)
}

// Scenario: the web reads a long transcript from its end: the newest page first,
// then the page before it, then follows what comes after.
func TestATranscriptIsReadInPages(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0]
	d, _ := w.start(task.ID)
	ctx := t.Context()

	items := make([]Item, 0, 5)
	for i := 1; i <= 5; i++ {
		items = append(items, Item{Seq: int64(i), Kind: ItemMessage, Text: "step"})
	}

	_, err := w.s.AddItems(ctx, w.node.ID, d.ID, items)
	require.NoError(t, err)

	newest, err := w.s.ListItems(ctx, d.ID, 0, 6, 2)
	require.NoError(t, err)
	assert.Equal(t, []int64{4, 5}, seqs(newest.Items))
	assert.True(t, newest.HasMore)

	older, err := w.s.ListItems(ctx, d.ID, 0, 4, 10)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2, 3}, seqs(older.Items))
	assert.False(t, older.HasMore)

	after, err := w.s.ListItems(ctx, d.ID, 5, 0, 0)
	require.NoError(t, err)
	assert.Empty(t, after.Items)

	_, err = w.s.ListItems(ctx, "dsp_missing", 0, 0, 0)
	assert.ErrorIs(t, err, ErrNotFound)
}

// Scenario: items that cannot be a transcript are refused for good: another
// node's Dispatch, an unknown Dispatch, an unknown kind, an item far too large.
func TestItemsThatCannotApplyAreRefused(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0]
	d, _ := w.start(task.ID)
	other := w.addNode("node_b", 1, "claude")
	ctx := t.Context()
	ok := []Item{{Seq: 1, Kind: ItemMessage, Text: "hello"}}

	_, err := w.s.AddItems(ctx, other.ID, d.ID, ok)
	require.ErrorIs(t, err, ErrConflict)

	_, err = w.s.AddItems(ctx, w.node.ID, "dsp_missing", ok)
	require.ErrorIs(t, err, ErrNotFound)

	for name, bad := range map[string]Item{
		"an unknown kind":   {Seq: 1, Kind: "diff", Text: "x"},
		"no seq":            {Kind: ItemMessage, Text: "x"},
		"too large":         {Seq: 1, Kind: ItemMessage, Text: strings.Repeat("x", ItemBytesMax)},
		"an input not JSON": {Seq: 1, Kind: ItemToolCall, Tool: "Bash", Input: json.RawMessage(`"ls"`)},
	} {
		_, err = w.s.AddItems(ctx, w.node.ID, d.ID, []Item{bad})
		require.ErrorIs(t, err, ErrInvalid, name)
	}

	page, err := w.s.ListItems(ctx, d.ID, 0, 0, 0)
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

// Scenario: the last items of a transcript arrive after the worker's final
// report. They are still kept; thirty days after the Dispatch settled the
// transcript is dropped and the result stays.
func TestATranscriptOutlivesTheWorkerAndIsDroppedLater(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0]
	d, c := w.start(task.ID)
	ctx := t.Context()

	require.NoError(t, w.finish(c, success("done")))

	_, err := w.s.AddItems(ctx, w.node.ID, d.ID, []Item{{Seq: 1, Kind: ItemMessage, Text: "All tests pass."}})
	require.NoError(t, err)

	w.advance(ItemRetention - time.Second)
	w.reap()

	page, err := w.s.ListItems(ctx, d.ID, 0, 0, 0)
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)

	w.advance(2 * time.Second)
	w.reap()

	page, err = w.s.ListItems(ctx, d.ID, 0, 0, 0)
	require.NoError(t, err)
	assert.Empty(t, page.Items)

	settled := w.dispatchOf(d.ID)
	require.NotNil(t, settled.Result)
	assert.Equal(t, "done", settled.Result.Summary)
	assert.Equal(t, "All tests pass.", settled.Activity)
}

// A card's line is a message's first line or a tool call with what it is
// about, cut to fit a card.
func TestActivityIsOneLine(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		item Item
		want string
	}{
		"a message's first line": {Item{Kind: ItemMessage, Text: "\nFixing the handler.\nMore."}, "Fixing the handler."},
		"a call with a path":     {Item{Kind: ItemToolCall, Tool: "Edit", Input: json.RawMessage(`{"file_path":"a/b.go","old_string":"x"}`)}, "Edit: a/b.go"},
		"a call with no hint":    {Item{Kind: ItemToolCall, Tool: "TodoWrite", Input: json.RawMessage(`{"todos":[]}`)}, "TodoWrite"},
		"a result":               {Item{Kind: ItemToolResult, Text: "ok"}, ""},
		"thinking":               {Item{Kind: ItemReasoning, Text: "hmm"}, ""},
		"a long line": {
			Item{Kind: ItemMessage, Text: strings.Repeat("é", 200)},
			strings.Repeat("é", activityRunes-1) + "…",
		},
	} {
		assert.Equal(t, tc.want, activityOf(tc.item), name)
	}
}
