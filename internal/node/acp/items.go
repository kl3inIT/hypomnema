package acp

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/node/transcript"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Format names the journal of an ACP agent (transcript.SetFormat).
const Format = "acp"

// An ACP worker's transcript (issue #157): the agent's lines in the journal are
// JSON-RPC, and what the worker did is in its `session/update` notifications,
// as omp 18.18.6 and opencode 2.2.7 send them in real runs:
//
//   - `agent_message_chunk` and `agent_thought_chunk` arrive a few words at a
//     time; a run of them is one message or one thought, closed by whatever
//     comes after it;
//   - `tool_call` names a call, with its input at once (omp) or empty until
//     its first update (opencode); `tool_call_update` goes through in_progress
//     to completed or failed and carries the output. One call is one item when
//     its input is known, and its result is one when it ends. opencode ends a
//     shell call whose command did not exit cleanly as completed, and says how
//     it ended in the raw output's metadata: that result is a failed one too;
//   - the result of `session/prompt` says how the turn ended.
//
// Not items: usage, the commands the agent offers, session information, the plan.
// A run of chunks and a call whose input is not known are open: the forwarder
// holds the journal's cursor before the line that opened them (Decoder.OpenSince).

// rpcLine is the part of a JSON-RPC line the decoder reads.
type rpcLine struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Update sessionUpdate `json:"update"`
	} `json:"params"`
	Result struct {
		StopReason string `json:"stopReason"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// sessionUpdate is a `session/update` notification's update.
type sessionUpdate struct {
	Kind       string          `json:"sessionUpdate"`
	ToolCallID string          `json:"toolCallId"`
	Name       string          `json:"name"`
	Title      string          `json:"title"`
	Status     string          `json:"status"`
	RawInput   json.RawMessage `json:"rawInput"`
	Content    json.RawMessage `json:"content"`
	RawOutput  struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		// Metadata is how opencode's shell tool says a command ended
		// (packages/core/src/shell/result.ts metadata, sent on by
		// packages/cli/src/acp/tool.ts completedToolUpdate).
		Metadata struct {
			Exit    *int   `json:"exit"`
			Signal  string `json:"signal"`
			Timeout bool   `json:"timeout"`
		} `json:"metadata"`
	} `json:"rawOutput"`
}

// A textRun is a message or a thought whose chunks are still coming.
type textRun struct {
	kind  string
	text  strings.Builder
	since int
}

// call is a tool call whose input was not known when it was named.
type openCall struct {
	name  string
	input json.RawMessage
	since int
}

type decoder struct {
	lines int
	run   *textRun
	// calls are the calls named but not yet sent as items.
	calls map[string]*openCall
}

// NewDecoder is the transcript decoder of an ACP agent's journal.
func NewDecoder() transcript.Decoder { return &decoder{calls: map[string]*openCall{}} }

func (d *decoder) OpenSince() int {
	since := -1

	if d.run != nil {
		since = d.run.since
	}

	for _, c := range d.calls {
		if since < 0 || c.since < since {
			since = c.since
		}
	}

	return since
}

// Flush closes what is open, in the order the journal named it.
func (d *decoder) Flush() []nodeproto.Item {
	type opened struct {
		since int
		items []nodeproto.Item
	}

	var all []opened

	if d.run != nil {
		since := d.run.since // Read before closing the run, which forgets it.
		all = append(all, opened{since, d.closeRun()})
	}

	for id, c := range d.calls {
		all = append(all, opened{c.since, []nodeproto.Item{toolCall(id, c.name, c.input)}})
		delete(d.calls, id)
	}

	slices.SortFunc(all, func(a, b opened) int { return a.since - b.since })

	var out []nodeproto.Item
	for _, o := range all {
		out = append(out, o.items...)
	}

	return transcript.Fit(out)
}

func (d *decoder) Line(raw []byte) []nodeproto.Item {
	index := d.lines
	d.lines++

	var m rpcLine
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}

	var out []nodeproto.Item

	switch {
	case m.Method == "session/update":
		out = d.onUpdate(index, m.Params.Update)
	case m.Method != "":
		// A request or a notification of another kind.
	case m.Error != nil:
		out = append(d.closeRun(), nodeproto.Item{Kind: nodeproto.ItemNotice, Text: "Error: " + m.Error.Message})
	case m.Result.StopReason != "":
		out = d.closeRun()
		if m.Result.StopReason != "end_turn" {
			out = append(out, nodeproto.Item{Kind: nodeproto.ItemNotice, Text: "The turn ended: " + m.Result.StopReason})
		}
	}

	return transcript.Fit(out)
}

func (d *decoder) onUpdate(index int, u sessionUpdate) []nodeproto.Item {
	switch u.Kind {
	case "agent_message_chunk", "agent_thought_chunk":
		kind := nodeproto.ItemMessage
		if u.Kind == "agent_thought_chunk" {
			kind = nodeproto.ItemReasoning
		}

		var out []nodeproto.Item

		if d.run != nil && d.run.kind != kind {
			out = d.closeRun()
		}

		if d.run == nil {
			d.run = &textRun{kind: kind, since: index}
		}

		var c struct {
			Text string `json:"text"`
		}

		_ = json.Unmarshal(u.Content, &c)
		d.run.text.WriteString(c.Text)

		// One item holds so much; a longer text goes on in the next.
		if d.run.text.Len() > nodeproto.MaxItemBytes/2 {
			out = append(out, d.closeRun()...)
		}

		return out
	case "tool_call":
		out := d.closeRun()
		name := toolName(first(u.Name, u.Title))
		input := object(u.RawInput)

		if input == nil {
			d.calls[u.ToolCallID] = &openCall{name: name, since: index}

			return out
		}

		return append(out, toolCall(u.ToolCallID, name, input))
	case "tool_call_update":
		out := d.closeRun()

		// A call this decoder holds is sent once its input is known. An update of one it does not
		// hold belongs to a call sent before the cursor: opencode repeats the input in each update,
		// and only its result is new.
		c := d.calls[u.ToolCallID]

		if input := object(u.RawInput); c != nil && input != nil {
			c.input = input
		}

		ended := u.Status == "completed" || u.Status == "failed"

		if c != nil && (c.input != nil || ended) {
			out = append(out, toolCall(u.ToolCallID, c.name, c.input))
			delete(d.calls, u.ToolCallID)
		}

		if ended {
			out = append(out, nodeproto.Item{
				Kind: nodeproto.ItemToolResult, CallID: u.ToolCallID, Text: output(u), Failed: failed(u),
			})
		}

		return out
	}

	return nil
}

// closeRun returns the message or thought a run of chunks made, and closes the run.
func (d *decoder) closeRun() []nodeproto.Item {
	r := d.run
	d.run = nil

	if r == nil || strings.TrimSpace(r.text.String()) == "" {
		return nil
	}

	return []nodeproto.Item{{Kind: r.kind, Text: r.text.String()}}
}

// toolCall is a call as an item; an input not known is empty.
func toolCall(id, name string, input json.RawMessage) nodeproto.Item {
	if input == nil {
		input = json.RawMessage(`{}`)
	}

	return nodeproto.Item{Kind: nodeproto.ItemToolCall, CallID: id, Tool: name, Input: input}
}

// object returns a tool's input when it is a JSON object with something in it, else nil.
func object(raw json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || len(m) == 0 {
		return nil
	}

	return raw
}

// toolName names a tool as the web knows it: the worker's own tools, which
// omp calls `mcp__hypomnema_finish` and opencode `hypomnema_finish`, are
// `mcp__hypomnema__finish`, as Claude Code names them.
func toolName(name string) string {
	bare := strings.TrimPrefix(name, "mcp__")

	for _, prefix := range []string{"hypomnema__", "hypomnema_"} {
		if rest, ok := strings.CutPrefix(bare, prefix); ok {
			return "mcp__hypomnema__" + rest
		}
	}

	return name
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}

// output is what a tool call printed: the text of its content (opencode and
// omp both send it), or else of its raw output; a diff is named by its file.
func output(u sessionUpdate) string {
	var blocks []struct {
		Type    string `json:"type"`
		Path    string `json:"path"`
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}

	var parts []string

	if json.Unmarshal(u.Content, &blocks) == nil {
		for _, b := range blocks {
			switch {
			case b.Type == "diff":
				parts = append(parts, fmt.Sprintf("[diff of %s]", b.Path))
			case b.Content.Text != "":
				parts = append(parts, b.Content.Text)
			}
		}
	}

	if len(parts) == 0 {
		for _, b := range u.RawOutput.Content {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
	}

	return strings.Join(parts, "\n")
}

// failed reports whether a call that ended did not do what it was asked: the
// agent said so, or its command exited with another code than zero, was
// killed by a signal or ran out of time. opencode reports those three as a
// completed call (its notice in packages/core/src/shell/result.ts treats them
// alike); a Codex command's exit code is read the same way (codex/items.go),
// as t3code reads an ACP agent's (packages/provider-grok xaiAcpExtension.ts).
func failed(u sessionUpdate) bool {
	m := u.RawOutput.Metadata

	return u.Status == "failed" || (m.Exit != nil && *m.Exit != 0) || m.Signal != "" || m.Timeout
}
