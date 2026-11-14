package coordinator

import (
	"encoding/json"
	"maps"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// What a chat shows of its Bot's tool calls (issue #441) is decided here, before anything is sent, as OpenClaw
// drops a tool event below the session's level before delivery (src/auto-reply/reply/agent-runner-cli-dispatch.ts:
// no result unless shouldEmitToolResult, the command's details only when the level is full, the output only when
// shouldEmitToolOutput). The kept message stays whole: a Bot switched to full shows its earlier calls in full.

// ShownMessage is a kept message as the level shows it, and false when it shows none of it.
func ShownMessage(d orchestration.ToolDisplay, m orchestration.ChatMessage) (orchestration.ChatMessage, bool) {
	if m.Role != orchestration.ChatTool || d == orchestration.ToolsFull {
		return m, true
	}

	if d == orchestration.ToolsOff {
		return m, false
	}

	var parts []Part
	_ = json.Unmarshal(m.Parts, &parts)

	name := "a tool"
	shown := make([]Part, 0, len(parts))

	for _, p := range parts {
		shown = append(shown, Part{Type: p.Type, ID: p.ID, Name: p.Name, Error: p.Error})

		if p.Name != "" {
			name = p.Name
		}
	}

	m.Text = "pi used " + name
	m.Parts = nil

	if raw, err := json.Marshal(shown); err == nil && len(shown) > 0 {
		m.Parts = raw
	}

	return m, true
}

// ShownFrame is a live frame as the level shows it, and false when it shows none of it: a tool that starts names
// its tool without its arguments below full.
func ShownFrame(d orchestration.ToolDisplay, f Frame) (Frame, bool) {
	if f.Event != FrameTool || d == orchestration.ToolsFull {
		return f, true
	}

	if d == orchestration.ToolsOff {
		return f, false
	}

	data, ok := f.Data.(map[string]any)
	if !ok {
		return f, true
	}

	data = maps.Clone(data)
	delete(data, "args")

	return Frame{Event: f.Event, Data: data}, true
}
