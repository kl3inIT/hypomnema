package claude

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/node/transcript"
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

	return transcript.Fit(out)
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

// Format names the journal of Claude Code's stream-JSON (transcript.SetFormat).
const Format = "claude"

// decoder is the transcript decoder of Claude Code's journal: each line says
// all of itself.
type decoder struct{}

// NewDecoder is the transcript decoder of a Claude Code worker's journal.
func NewDecoder() transcript.Decoder { return decoder{} }

func (decoder) Line(raw []byte) []nodeproto.Item { return itemsOf(raw) }

func (decoder) OpenSince() int { return -1 }

func (decoder) Flush() []nodeproto.Item { return nil }
