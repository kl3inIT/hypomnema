package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// A worker's transcript (issue #43): what the worker said and did during one
// Dispatch, kept as whole items in the order the node numbered them, as
// Omnara's agent_events and Omnigent's conversation_items
// (docs/research/2026-11-10-worker-transcripts.md). The node sends an item until
// it is acknowledged, so an item that comes again is ignored.

// ItemKind names what an item is.
type ItemKind string

// Item kinds.
const (
	ItemMessage    ItemKind = "message"
	ItemReasoning  ItemKind = "reasoning"
	ItemToolCall   ItemKind = "tool_call"
	ItemToolResult ItemKind = "tool_result"
	ItemNotice     ItemKind = "notice"
)

// Bounds of the transcript. The node cuts an item to 64 KiB and a batch to 32
// items; the control plane refuses what is far beyond that.
const (
	ItemBytesMax    = 128 << 10
	ItemBatchMax    = 256
	ItemPageDefault = 100
	ItemPageMax     = 500
	activityRunes   = 120
)

// Item is one entry of a worker's transcript. A message, reasoning or notice has
// Text; a tool call has CallID, Tool and Input; a tool result has CallID, Text
// and Failed.
type Item struct {
	Seq       int64           `json:"-"`
	Kind      ItemKind        `json:"-"`
	Text      string          `json:"text,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Failed    bool            `json:"failed,omitempty"`
	CreatedAt time.Time       `json:"-"`
}

// ItemPage is one page of a transcript, oldest first. HasMore says whether more
// items lie beyond it in the direction read.
type ItemPage struct {
	Items   []Item
	HasMore bool
}

func validItemKind(k ItemKind) bool {
	switch k {
	case ItemMessage, ItemReasoning, ItemToolCall, ItemToolResult, ItemNotice:
		return true
	default:
		return false
	}
}

// AddItems stores the next items of a Dispatch's transcript, sent by the node
// that runs it, and returns the newest seq held. Items already held are
// ignored. It takes items in any status of the Dispatch: the last ones arrive
// after the worker ended.
func (s *Service) AddItems(ctx context.Context, nodeID, dispatchID string, items []Item) (int64, error) {
	if len(items) > ItemBatchMax {
		return 0, fmt.Errorf("%w: a batch holds at most %d items", ErrInvalid, ItemBatchMax)
	}

	rows := make([]store.InsertDispatchItemParams, 0, len(items))

	for _, it := range items {
		if it.Seq <= 0 || !validItemKind(it.Kind) {
			return 0, fmt.Errorf("%w: an item needs a positive seq and a known kind", ErrInvalid)
		}

		if len(it.Input) > 0 && json.Unmarshal(it.Input, &map[string]json.RawMessage{}) != nil {
			return 0, fmt.Errorf("%w: the input of item %d is not an object", ErrInvalid, it.Seq)
		}

		data, err := json.Marshal(it)
		if err != nil {
			return 0, fmt.Errorf("%w: encoding item %d: %w", ErrInvalid, it.Seq, err)
		}

		if len(data) > ItemBytesMax {
			return 0, fmt.Errorf("%w: item %d is larger than %d bytes", ErrInvalid, it.Seq, ItemBytesMax)
		}

		rows = append(rows, store.InsertDispatchItemParams{
			DispatchID: dispatchID, Seq: it.Seq, Kind: string(it.Kind), Data: string(data),
		})
	}

	var last int64

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		d, err := q.GetDispatch(ctx, dispatchID)
		if err != nil {
			return notFound(err, "dispatch", dispatchID)
		}

		if d.NodeID != nodeID {
			return fmt.Errorf("%w: dispatch %s is not on node %s", ErrConflict, dispatchID, nodeID)
		}

		activity := ""

		for i, row := range rows {
			row.Now = now

			n, err := q.InsertDispatchItem(ctx, row)
			if err != nil {
				return fmt.Errorf("storing item %d of %s: %w", row.Seq, dispatchID, err)
			}

			if a := activityOf(items[i]); n == 1 && a != "" {
				activity = a
			}
		}

		if activity != "" {
			if err := q.SetDispatchActivity(ctx, store.SetDispatchActivityParams{
				Activity: nullString(activity), ID: dispatchID,
			}); err != nil {
				return fmt.Errorf("recording the activity of %s: %w", dispatchID, err)
			}
		}

		last, err = q.LastDispatchItemSeq(ctx, dispatchID)
		if err != nil {
			return fmt.Errorf("reading the last item of %s: %w", dispatchID, err)
		}

		return nil
	})
	if err != nil {
		return 0, err
	}

	return last, nil
}

// ListItems returns one page of a Dispatch's transcript, oldest first: the items
// after a seq, or with before set the newest items older than it. The limit
// defaults to ItemPageDefault (Omnara's history page) and is capped at
// ItemPageMax.
func (s *Service) ListItems(ctx context.Context, dispatchID string, after, before int64, limit int) (ItemPage, error) {
	if limit <= 0 {
		limit = ItemPageDefault
	}

	limit = min(limit, ItemPageMax)
	q := s.read()

	if _, err := q.GetDispatch(ctx, dispatchID); err != nil {
		return ItemPage{}, notFound(err, "dispatch", dispatchID)
	}

	var (
		rows []store.DispatchItem
		err  error
	)

	if before > 0 {
		rows, err = q.ListDispatchItemsBefore(ctx, store.ListDispatchItemsBeforeParams{
			DispatchID: dispatchID, Before: before, Lim: int64(limit) + 1,
		})
	} else {
		rows, err = q.ListDispatchItemsAfter(ctx, store.ListDispatchItemsAfterParams{
			DispatchID: dispatchID, After: after, Lim: int64(limit) + 1,
		})
	}

	if err != nil {
		return ItemPage{}, fmt.Errorf("listing the items of %s: %w", dispatchID, err)
	}

	page := ItemPage{Items: make([]Item, 0, min(len(rows), limit)), HasMore: len(rows) > limit}

	for _, r := range rows[:min(len(rows), limit)] {
		it := Item{Seq: r.Seq, Kind: ItemKind(r.Kind), CreatedAt: millis(r.CreatedAt)}
		if err := json.Unmarshal([]byte(r.Data), &it); err != nil {
			return ItemPage{}, fmt.Errorf("decoding item %d of %s: %w", r.Seq, dispatchID, err)
		}

		page.Items = append(page.Items, it)
	}

	if before > 0 {
		for i, j := 0, len(page.Items)-1; i < j; i, j = i+1, j-1 {
			page.Items[i], page.Items[j] = page.Items[j], page.Items[i]
		}
	}

	return page, nil
}

// activityHints are the inputs that say what a tool call is about, in the
// order tried; the tools of Claude Code, Codex and pi name them so.
var activityHints = []string{"command", "file_path", "path", "pattern", "url", "query", "description", "prompt"}

// activityOf is one line about an item for a card: a message's first line, or
// a tool call's name and what it is about. Other kinds say nothing new.
func activityOf(it Item) string {
	switch it.Kind {
	case ItemMessage:
		return oneLine(it.Text)
	case ItemToolCall:
		var input map[string]any

		_ = json.Unmarshal(it.Input, &input)

		for _, key := range activityHints {
			if hint, ok := input[key].(string); ok && strings.TrimSpace(hint) != "" {
				return oneLine(it.Tool + ": " + oneLine(hint))
			}
		}

		return oneLine(it.Tool)
	case ItemReasoning, ItemToolResult, ItemNotice:
		return ""
	default:
		return ""
	}
}

func oneLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	line = strings.TrimSpace(line)

	if utf8.RuneCountInString(line) <= activityRunes {
		return line
	}

	return string([]rune(line)[:activityRunes-1]) + "…"
}
