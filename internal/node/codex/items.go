package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/node/transcript"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Format names the journal of `codex exec --json` (transcript.SetFormat).
const Format = "codex"

// A Codex worker's transcript (issue #157): the lines of `codex exec --json`
// become the items every runtime shares. What each is, is read in
// codex-rs/exec/src/exec_events.rs and in a real run's journal. The tools take
// the names and the input the web already shows for Codex (`shell`,
// `apply_patch`; Omnigent's formats): a command is a call of `shell` when it
// starts and its result when it completes. Not items: the turn's usage (the
// Dispatch holds it), the plan (`todo_list`) and the calls of collab tools.

// execEvent is the part of a line the decoder reads.
type execEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
	Item *struct {
		ID               string `json:"id"`
		Type             string `json:"type"`
		Text             string `json:"text"`
		Command          string `json:"command"`
		AggregatedOutput string `json:"aggregated_output"`
		ExitCode         *int   `json:"exit_code"`
		Status           string `json:"status"`
		Changes          []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"changes"`
		Server    string          `json:"server"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
		Result    *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		ItemError *struct {
			Message string `json:"message"`
		} `json:"error"`
		Query   string            `json:"query"`
		Results []json.RawMessage `json:"results"`
		Message string            `json:"message"`
	} `json:"item"`
}

// decoder keeps nothing between lines: each of Codex's items says all of itself.
type decoder struct{}

// NewDecoder is the transcript decoder of a Codex worker's journal.
func NewDecoder() transcript.Decoder { return decoder{} }

func (decoder) OpenSince() int { return -1 }

func (decoder) Flush() []nodeproto.Item { return nil }

func (decoder) Line(raw []byte) []nodeproto.Item {
	var e execEvent
	if json.Unmarshal(raw, &e) != nil {
		return nil
	}

	switch e.Type {
	case "item.started", "item.completed":
		return transcript.Fit(itemsOf(e))
	case "turn.failed":
		if e.Error != nil {
			return []nodeproto.Item{{Kind: nodeproto.ItemNotice, Text: "The turn failed: " + e.Error.Message}}
		}
	case "error":
		return []nodeproto.Item{{Kind: nodeproto.ItemNotice, Text: e.Message}}
	}

	return nil
}

// itemsOf is the items of an item.started or an item.completed execEvent.
func itemsOf(e execEvent) []nodeproto.Item {
	it, done := e.Item, e.Type == "item.completed"
	if it == nil {
		return nil
	}

	switch it.Type {
	case "agent_message":
		if done && strings.TrimSpace(it.Text) != "" {
			return []nodeproto.Item{{Kind: nodeproto.ItemMessage, Text: it.Text}}
		}
	case "reasoning":
		if done && strings.TrimSpace(it.Text) != "" {
			return []nodeproto.Item{{Kind: nodeproto.ItemReasoning, Text: it.Text}}
		}
	case "command_execution":
		if !done {
			return []nodeproto.Item{toolCall(it.ID, "shell", map[string]any{"command": it.Command})}
		}

		failed := it.Status == "failed" || it.Status == "declined" || (it.ExitCode != nil && *it.ExitCode != 0)
		text := it.AggregatedOutput

		if it.ExitCode != nil && *it.ExitCode != 0 {
			text = strings.TrimRight(text, "\n") + fmt.Sprintf("\n[exit code %d]", *it.ExitCode)
		}

		return []nodeproto.Item{toolResult(it.ID, text, failed)}
	case "file_change":
		// Only emitted once it is done.
		if !done {
			return nil
		}

		changes := make([]map[string]string, 0, len(it.Changes))
		lines := make([]string, 0, len(it.Changes))

		for _, c := range it.Changes {
			changes = append(changes, map[string]string{"path": c.Path, "kind": c.Kind})
			lines = append(lines, c.Kind+" "+c.Path)
		}

		return []nodeproto.Item{
			toolCall(it.ID, "apply_patch", map[string]any{"changes": changes}),
			toolResult(it.ID, strings.Join(lines, "\n"), it.Status == "failed"),
		}
	case "mcp_tool_call":
		name := "mcp__" + it.Server + "__" + it.Tool
		if !done {
			input := it.Arguments
			if len(input) == 0 || string(input) == "null" {
				input = json.RawMessage(`{}`)
			}

			return []nodeproto.Item{{Kind: nodeproto.ItemToolCall, CallID: it.ID, Tool: name, Input: input}}
		}

		if it.ItemError != nil {
			return []nodeproto.Item{toolResult(it.ID, it.ItemError.Message, true)}
		}

		var parts []string

		if it.Result != nil {
			for _, c := range it.Result.Content {
				if c.Type == "text" {
					parts = append(parts, c.Text)
				} else {
					parts = append(parts, "["+c.Type+"]")
				}
			}
		}

		return []nodeproto.Item{toolResult(it.ID, strings.Join(parts, "\n"), it.Status == "failed")}
	case "web_search":
		if !done {
			return []nodeproto.Item{toolCall(it.ID, "web_search", map[string]any{"query": it.Query})}
		}

		text := ""
		if it.Results != nil {
			text = fmt.Sprintf("%d results", len(it.Results))
		}

		return []nodeproto.Item{toolResult(it.ID, text, false)}
	case "error":
		if done {
			return []nodeproto.Item{{Kind: nodeproto.ItemNotice, Text: it.Message}}
		}
	}

	return nil
}

func toolCall(id, tool string, input any) nodeproto.Item {
	raw, err := json.Marshal(input)
	if err != nil {
		raw = json.RawMessage(`{}`)
	}

	return nodeproto.Item{Kind: nodeproto.ItemToolCall, CallID: id, Tool: tool, Input: raw}
}

func toolResult(id, text string, failed bool) nodeproto.Item {
	return nodeproto.Item{Kind: nodeproto.ItemToolResult, CallID: id, Text: text, Failed: failed}
}
