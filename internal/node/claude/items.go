package claude

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Transcript items (issue #43): a journal line of Claude Code's stream-JSON
// becomes items of the shape every runtime shares (nodeproto.Item), so the
// control plane and the web read one format. The worker's own messages and
// tool calls are in "assistant" lines, the results of its calls in "user"
// lines; the first instruction and the final report are not items, the Task
// and the Dispatch hold them.

// line is the part of a stream-JSON line that becomes items.
type line struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	RateLimit *struct {
		Status   string `json:"status"`
		ResetsAt int64  `json:"resetsAt"`
	} `json:"rate_limit_info"`
}

// block is one content block of a message.
type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// itemsOf returns the items of one journal line, without their seq, each cut
// so that the line's items fit one batch.
func itemsOf(raw []byte) []nodeproto.Item {
	var l line
	if json.Unmarshal(raw, &l) != nil {
		return nil
	}

	var out []nodeproto.Item

	switch l.Type {
	case "assistant":
		for _, b := range blocksOf(l.Message.Content) {
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) != "" {
					out = append(out, nodeproto.Item{Kind: nodeproto.ItemMessage, Text: b.Text})
				}
			case "thinking":
				if strings.TrimSpace(b.Thinking) != "" {
					out = append(out, nodeproto.Item{Kind: nodeproto.ItemReasoning, Text: b.Thinking})
				}
			case "tool_use":
				out = append(out, nodeproto.Item{Kind: nodeproto.ItemToolCall, CallID: b.ID, Tool: b.Name, Input: object(b.Input)})
			}
		}
	case "user":
		for _, b := range blocksOf(l.Message.Content) {
			if b.Type == "tool_result" {
				out = append(out, nodeproto.Item{
					Kind: nodeproto.ItemToolResult, CallID: b.ToolUseID, Text: resultText(b.Content), Failed: b.IsError,
				})
			}
		}
	case "rate_limit_event":
		if l.RateLimit != nil && l.RateLimit.Status == "rejected" {
			out = append(out, nodeproto.Item{
				Kind: nodeproto.ItemNotice,
				Text: "Rate limited until " + time.Unix(l.RateLimit.ResetsAt, 0).UTC().Format(time.RFC3339),
			})
		}
	}

	// Room for the envelope and each item's field names.
	limit := min(nodeproto.MaxItemBytes, (nodeproto.MaxBatchBytes-4096)/max(len(out), 1))
	for i := range out {
		bound(&out[i], limit)
	}

	return out
}

// blocksOf returns a message's content blocks; a content that is plain text
// (the instruction given to the worker) has none.
func blocksOf(content json.RawMessage) []block {
	var blocks []block

	_ = json.Unmarshal(content, &blocks)

	return blocks
}

// object returns a tool's input when it is a JSON object, else an empty one.
func object(raw json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return json.RawMessage(`{}`)
	}

	return raw
}

// resultText is a tool result as text: the text itself, or its text blocks
// joined, with a marker for each image or other block.
func resultText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}

	var parts []string

	for _, b := range blocksOf(content) {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		} else {
			parts = append(parts, "["+b.Type+"]")
		}
	}

	return strings.Join(parts, "\n")
}

// bound cuts an item until it encodes to at most limit bytes: its text, and
// the long strings of its input, as Omnigent's forwarder cuts an oversized
// item (harnesses/claude_native/forwarder.py at cf1021e).
func bound(it *nodeproto.Item, limit int) {
	for most := limit; ; most /= 2 {
		c := *it
		c.Text = cut(it.Text, most)
		c.Input = cutInput(it.Input, most)

		if raw, err := json.Marshal(c); (err == nil && len(raw) <= limit) || most < 64 {
			*it = c

			return
		}
	}
}

// cutInput cuts the strings of a tool's input so that it takes about most
// bytes; an input that cannot be cut that far is dropped and marked.
func cutInput(raw json.RawMessage, most int) json.RawMessage {
	if len(raw) <= most {
		return raw
	}

	var input map[string]any
	if most < 64 || json.Unmarshal(raw, &input) != nil {
		return json.RawMessage(`{"truncated":true}`)
	}

	out, err := json.Marshal(cutStrings(input, most/8))
	if err != nil {
		return json.RawMessage(`{"truncated":true}`)
	}

	return out
}

func cutStrings(v any, most int) any {
	switch x := v.(type) {
	case string:
		return cut(x, most)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = cutStrings(e, most)
		}

		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = cutStrings(e, most)
		}

		return out
	default:
		return v
	}
}

// cut keeps the first most bytes of s, on a rune boundary, and says how much
// it left out.
func cut(s string, most int) string {
	most = max(most, 64)
	if len(s) <= most {
		return s
	}

	keep := most
	for keep > 0 && !utf8.RuneStart(s[keep]) {
		keep--
	}

	return s[:keep] + fmt.Sprintf("\n… [%d more bytes not kept]", len(s)-keep)
}
