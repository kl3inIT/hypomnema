package coordinator

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// pi's activity, kept and relayed as Omnara does (issue #41, PR 4): each
// finished reply is kept with its text and thinking, each finished tool call
// is kept with its arguments and result, and what happens in between (text
// as it is written, a tool starting, pi working or idle) goes to the chat's
// watchers from memory only. pi's records are described in its docs/json.md.

// MaxToolOutput caps a kept tool result.
const MaxToolOutput = 16 << 10

// Part is one piece of a kept message, stored as JSON in its parts.
type Part struct {
	// Type is text, thinking or tool_result.
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// ID, Name and Args name a tool call.
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
	// Output is a tool's result text, cut at MaxToolOutput when Truncated.
	Output     string `json:"output,omitempty"`
	Error      bool   `json:"error,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// record is the part of a pi stdout record the supervisor reads.
type record struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
	Message *struct {
		Role       string `json:"role"`
		StopReason string `json:"stopReason"`
		Error      string `json:"errorMessage"`
		Content    []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"content"`
	} `json:"message"`
	Event *struct {
		Type         string `json:"type"`
		ContentIndex int    `json:"contentIndex"`
		Delta        string `json:"delta"`
	} `json:"assistantMessageEvent"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args"`
	Result     *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	IsError    bool  `json:"isError"`
	DurationMs int64 `json:"durationMs"`
}

func (p *proc) handle(ctx context.Context, line []byte) {
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		return
	}

	switch rec.Type {
	case "response":
		p.mu.Lock()
		ch := p.waiting[rec.ID]
		p.mu.Unlock()

		if ch != nil {
			ch <- response{Success: rec.Success, Error: rec.Error, Data: rec.Data}
		}
	case "agent_start":
		p.setWorking(true)
	case "agent_settled":
		p.setWorking(false)
		p.s.settled()
	case "message_start":
		if rec.Message != nil && rec.Message.Role == "assistant" {
			p.s.publish(p.chatID, Frame{Event: FrameReset})
		}
	case "message_update":
		p.relayDelta(rec)
	case "message_end":
		p.keepReply(ctx, rec)
	case "tool_execution_start":
		p.mu.Lock()
		p.calls[rec.ToolCallID] = rec.Args
		p.mu.Unlock()

		p.s.publish(p.chatID, Frame{Event: FrameTool, Data: map[string]any{
			"phase": "start", "id": rec.ToolCallID, "name": rec.ToolName, "args": rec.Args,
		}})
	case "tool_execution_end":
		p.keepTool(ctx, rec)
	}
}

// relayDelta sends text and thinking as pi writes them.
func (p *proc) relayDelta(rec record) {
	e := rec.Event
	if e == nil {
		return
	}

	var kind string

	switch e.Type {
	case "text_delta":
		kind = "text"
	case "thinking_delta":
		kind = "thinking"
	default:
		return
	}

	p.s.publish(p.chatID, Frame{Event: FrameDelta, Data: map[string]any{
		"kind": kind, "index": e.ContentIndex, "delta": e.Delta,
	}})
}

// keepReply keeps an assistant message's text and thinking, and its error.
// Tool calls are kept when they end, with their results.
func (p *proc) keepReply(ctx context.Context, rec record) {
	m := rec.Message
	if m == nil || m.Role != "assistant" {
		return
	}

	var (
		parts []Part
		texts []string
	)

	for _, c := range m.Content {
		switch {
		case c.Type == "text" && strings.TrimSpace(c.Text) != "":
			parts = append(parts, Part{Type: "text", Text: c.Text})
			texts = append(texts, c.Text)
		case c.Type == "thinking" && strings.TrimSpace(c.Thinking) != "":
			parts = append(parts, Part{Type: "thinking", Text: c.Thinking})
		}
	}

	if len(parts) > 0 {
		p.keep(ctx, orchestration.ChatCoordinator, strings.Join(texts, "\n\n"), parts)
	}

	if m.StopReason == "error" {
		p.keep(ctx, orchestration.ChatSystem, "pi's model call failed: "+m.Error, nil)
	}
}

// keepTool keeps a finished tool call with its arguments and result.
func (p *proc) keepTool(ctx context.Context, rec record) {
	p.mu.Lock()
	args := p.calls[rec.ToolCallID]
	delete(p.calls, rec.ToolCallID)
	p.mu.Unlock()

	var out []string

	if rec.Result != nil {
		for _, c := range rec.Result.Content {
			if c.Type == "text" {
				out = append(out, c.Text)
			}
		}
	}

	output, truncated := capText(strings.Join(out, "\n"), MaxToolOutput)

	p.keep(ctx, orchestration.ChatTool, toolLine(rec.ToolName, args), []Part{{
		Type: "tool_result", ID: rec.ToolCallID, Name: rec.ToolName, Args: args, Output: output,
		Error: rec.IsError, Truncated: truncated, DurationMs: rec.DurationMs,
	}})

	p.s.publish(p.chatID, Frame{Event: FrameTool, Data: map[string]any{"phase": "end", "id": rec.ToolCallID}})
}

// capText cuts s to at most limit bytes on a character boundary.
func capText(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}

	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}

	return s[:cut], true
}

func (p *proc) keep(ctx context.Context, role orchestration.ChatRole, text string, parts []Part) {
	var raw json.RawMessage

	if parts != nil {
		var err error

		if raw, err = json.Marshal(parts); err != nil {
			return
		}
	} else if strings.TrimSpace(text) == "" {
		return
	}

	if _, err := p.s.svc.AddChatMessageParts(ctx, p.chatID, role, text, raw); err != nil && ctx.Err() == nil {
		p.s.cfg.Logger.ErrorContext(ctx, "keeping a chat message failed", slog.String("chat_id", p.chatID),
			slog.Any("error", err))
	}
}
