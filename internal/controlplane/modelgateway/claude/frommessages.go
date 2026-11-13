package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// The other direction (issue #273): a client that speaks Messages, served by
// a model whose target is not a Claude subscription. Its request becomes a chat
// completions request, and the chat answer becomes a Messages answer, as
// 9router-go does it (translator/request.go TranslateClaudeToOpenAI,
// translator/response.go TranslateOpenAIToClaude and
// TranslateOpenAIToClaudeStreamSession). Left out of 9router-go's: repairing a
// truncated tool argument, rejoining a JSON object split across events, and
// its fix-ups for particular providers.

// messagesIn is the part of a Messages request the translation reads.
type messagesIn struct {
	Model         string          `json:"model"`
	MaxTokens     *int64          `json:"max_tokens"`
	Temperature   *float64        `json:"temperature"`
	TopP          *float64        `json:"top_p"`
	Stream        bool            `json:"stream"`
	System        json.RawMessage `json:"system"`
	Messages      []messageIn     `json:"messages"`
	Tools         []toolIn        `json:"tools"`
	ToolChoice    json.RawMessage `json:"tool_choice"`
	StopSequences []string        `json:"stop_sequences"`
	Thinking      *thinkingIn     `json:"thinking"`
	OutputConfig  *outputConfig   `json:"output_config"`
}

type outputConfig struct {
	Effort string `json:"effort"`
}

type messageIn struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type toolIn struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type thinkingIn struct {
	Type   string `json:"type"`
	Budget int    `json:"budget_tokens"`
}

type blockIn struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	Source    *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"`
}

// ChatFromMessages turns a Messages request into a chat completions request
// for the gateway's own model name. A stream asks for the usage frame, which a
// Messages client cannot ask for and needs (9router-go chat.go).
func ChatFromMessages(raw []byte) ([]byte, error) {
	var in messagesIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("reading the Messages request: %w", err)
	}

	out := item{"model": in.Model, "stream": in.Stream}

	if in.Stream {
		out["stream_options"] = item{"include_usage": true}
	}

	if in.MaxTokens != nil {
		// gpt-5 and the o-series take max_completion_tokens (9router-go
		// requiresMaxCompletionTokens); which model serves is not known here,
		// so the name the client asked for decides, as there.
		if takesMaxCompletion(in.Model) {
			out["max_completion_tokens"] = *in.MaxTokens
		} else {
			out["max_tokens"] = *in.MaxTokens
		}
	}

	if in.Temperature != nil {
		out["temperature"] = *in.Temperature
	}

	if in.TopP != nil {
		out["top_p"] = *in.TopP
	}

	if len(in.StopSequences) > 0 {
		out["stop"] = in.StopSequences
	}

	messages := []any{}
	if system := systemText(in.System); system != "" {
		messages = append(messages, item{"role": "system", "content": system})
	}

	for i, m := range in.Messages {
		converted, err := chatMessages(m)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", i, err)
		}

		messages = append(messages, converted...)
	}

	out["messages"] = answerEveryToolCall(messages)

	if len(in.Tools) > 0 {
		tools := make([]any, 0, len(in.Tools))
		for _, t := range in.Tools {
			function := item{"name": t.Name}
			if t.Description != "" {
				function["description"] = t.Description
			}

			if len(t.InputSchema) > 0 {
				function["parameters"] = t.InputSchema
			}

			tools = append(tools, item{"type": "function", "function": function})
		}

		out["tools"] = tools
	}

	if len(in.ToolChoice) > 0 {
		choice, parallel := chatToolChoice(in.ToolChoice)
		out["tool_choice"] = choice

		if !parallel {
			out["parallel_tool_calls"] = false
		}
	}

	if effort := chatEffort(in); effort != "" {
		out["reasoning_effort"] = effort
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encoding the chat request: %w", err)
	}

	return encoded, nil
}

func takesMaxCompletion(model string) bool {
	lower := strings.ToLower(model)

	for _, name := range []string{"gpt-5", "gpt-6", "o1-", "o3-", "o4-"} {
		if strings.Contains(lower, name) {
			return true
		}
	}

	return false
}

// systemText joins the system's text, without Claude Code's billing block,
// which means nothing to another provider (9router-go parseSystemPrompt).
func systemText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var text string
	if json.Unmarshal(raw, &text) == nil {
		return withoutBilling(text)
	}

	var blocks []blockIn
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}

	parts := make([]string, 0, len(blocks))

	for _, b := range blocks {
		if text := withoutBilling(b.Text); text != "" {
			parts = append(parts, text)
		}
	}

	return strings.Join(parts, "\n")
}

func withoutBilling(text string) string {
	if strings.HasPrefix(strings.TrimSpace(text), "x-anthropic-billing-header:") {
		return ""
	}

	return text
}

// chatMessages turns one Messages turn into the chat messages it stands for:
// tool results become tool messages, tool uses an assistant's tool calls,
// images data URLs, thinking the assistant's reasoning (9router-go
// convertClaudeMessage).
func chatMessages(m messageIn) ([]any, error) {
	role := "user"
	if m.Role == "assistant" {
		role = "assistant"
	}

	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		return []any{item{"role": role, "content": text}}, nil
	}

	var blocks []blockIn
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return nil, fmt.Errorf("content is a string or a list of blocks: %w", err)
	}

	var (
		parts     []any
		calls     []any
		results   []any
		reasoning strings.Builder
	)

	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, item{"type": "text", "text": b.Text})
		case "thinking":
			reasoning.WriteString(b.Thinking)
		case "image":
			if b.Source != nil && b.Source.Type == "base64" {
				parts = append(parts, item{"type": "image_url", "image_url": item{
					"url": "data:" + b.Source.MediaType + ";base64," + b.Source.Data,
				}})
			}
		case "document":
			if b.Source != nil && b.Source.Type == "base64" {
				parts = append(parts, item{"type": "file", "file": item{
					"file_data": "data:" + b.Source.MediaType + ";base64," + b.Source.Data,
				}})
			}
		case "tool_use":
			args := string(b.Input)
			if args == "" || args == "null" {
				args = "{}"
			}

			calls = append(calls, item{
				"id": b.ID, "type": "function", "function": item{"name": b.Name, "arguments": args},
			})
		case "tool_result":
			resultText, images := toolResult(b)
			results = append(results, item{"role": "tool", "tool_call_id": b.ToolUseID, "content": resultText})

			if len(images) > 0 {
				parts = append(parts, item{"type": "text", "text": "[Image from tool result " + b.ToolUseID + "]"})
				parts = append(parts, images...)
			}
		}
	}

	switch {
	case len(results) > 0:
		if len(parts) > 0 {
			results = append(results, item{"role": "user", "content": collapse(parts)})
		}

		return results, nil
	case len(calls) > 0:
		msg := item{"role": "assistant", "tool_calls": calls}
		if len(parts) > 0 {
			msg["content"] = collapse(parts)
		}

		if reasoning.Len() > 0 {
			msg["reasoning_content"] = reasoning.String()
		}

		return []any{msg}, nil
	case len(parts) > 0:
		return []any{item{"role": role, "content": collapse(parts)}}, nil
	case len(blocks) == 0:
		return []any{item{"role": role, "content": ""}}, nil
	default:
		return nil, nil
	}
}

// toolResult is a tool result's text, and its images as chat parts.
func toolResult(b blockIn) (string, []any) {
	var text string
	if json.Unmarshal(b.Content, &text) == nil {
		return text, nil
	}

	var blocks []blockIn
	if json.Unmarshal(b.Content, &blocks) != nil {
		return string(b.Content), nil
	}

	var (
		texts  []string
		images []any
	)

	for _, c := range blocks {
		switch {
		case c.Type == "text":
			texts = append(texts, c.Text)
		case c.Type == "image" && c.Source != nil && c.Source.Type == "base64":
			media := c.Source.MediaType
			if media == "" {
				media = "image/png"
			}

			images = append(images, item{"type": "image_url", "image_url": item{
				"url": "data:" + media + ";base64," + c.Source.Data,
			}})
		}
	}

	return strings.Join(texts, "\n"), images
}

// collapse makes parts that are all text one string; any other part keeps the
// list, so an image reaches a model that sees.
func collapse(parts []any) any {
	texts := make([]string, 0, len(parts))

	for _, p := range parts {
		part, _ := p.(item)
		if part["type"] != "text" {
			return parts
		}

		text, _ := part["text"].(string)
		texts = append(texts, text)
	}

	return strings.Join(texts, "\n")
}

// answerEveryToolCall adds a placeholder result after an assistant's tool
// call the conversation never answered, which a chat provider refuses
// (9router-go fixMissingToolResponsesOpenAI).
func answerEveryToolCall(messages []any) []any {
	out := make([]any, 0, len(messages))

	for i := 0; i < len(messages); i++ {
		msg, _ := messages[i].(item)
		out = append(out, messages[i])

		calls, _ := msg["tool_calls"].([]any)
		if msg["role"] != "assistant" || len(calls) == 0 {
			continue
		}

		answered := map[string]bool{}

		for i+1 < len(messages) {
			next, _ := messages[i+1].(item)
			if next["role"] != "tool" {
				break
			}

			id, _ := next["tool_call_id"].(string)
			answered[id] = true
			out = append(out, messages[i+1])
			i++
		}

		for _, c := range calls {
			call, _ := c.(item)
			if id, _ := call["id"].(string); !answered[id] {
				out = append(out, item{"role": "tool", "tool_call_id": id, "content": "[No response received]"})
			}
		}
	}

	return out
}

// chatToolChoice turns a Messages tool_choice into chat's, and says whether
// parallel calls are allowed (9router-go convertToolChoice).
func chatToolChoice(raw json.RawMessage) (any, bool) {
	var choice struct {
		Type                   string `json:"type"`
		Name                   string `json:"name"`
		DisableParallelToolUse bool   `json:"disable_parallel_tool_use"`
	}

	if json.Unmarshal(raw, &choice) != nil {
		return "auto", true
	}

	parallel := !choice.DisableParallelToolUse

	switch choice.Type {
	case "any":
		return "required", parallel
	case "none":
		return "none", parallel
	case "tool":
		return item{"type": "function", "function": item{"name": choice.Name}}, parallel
	default:
		return "auto", parallel
	}
}

// chatEffort is the reasoning effort a Messages request's thinking stands for
// (9router-go TranslateClaudeToOpenAI and thinking.BudgetToLevel). The levels
// above high, which a chat provider does not take, are high, as 9router-go
// does for an adaptive request.
func chatEffort(in messagesIn) string {
	asked := ""
	if in.OutputConfig != nil {
		asked = strings.ToLower(in.OutputConfig.Effort)
	}

	effort := asked

	if in.Thinking != nil {
		switch in.Thinking.Type {
		case "enabled":
			effort = budgetLevel(in.Thinking.Budget)
		case "disabled":
			effort = "none"
		case "adaptive":
			if effort == "" {
				effort = "high"
			}
		}
	}

	switch effort {
	case "xhigh", "max", "auto":
		return "high"
	default:
		return effort
	}
}

func budgetLevel(budget int) string {
	switch {
	case budget <= 0:
		return "medium"
	case budget <= 768:
		return "minimal"
	case budget <= 4096:
		return "low"
	case budget <= 16384:
		return "medium"
	default:
		return "high"
	}
}

// chatAnswer is the part of a chat completion the translation reads.
type chatAnswer struct {
	ID      string `json:"id"`
	Choices []struct {
		Message      chatDelta `json:"message"`
		Delta        chatDelta `json:"delta"`
		FinishReason *string   `json:"finish_reason"`
	} `json:"choices"`
	Usage *ChatUsage `json:"usage"`
}

type chatDelta struct {
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
	Reasoning        string `json:"reasoning"`
	ToolCalls        []struct {
		Index    *int   `json:"index"`
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

func (d chatDelta) reasoning() string {
	if d.ReasoningContent != "" {
		return d.ReasoningContent
	}

	return d.Reasoning
}

// ChatUsage is a chat answer's usage.
type ChatUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	PromptDetails    *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// Cached is the part of the prompt read from a cache.
func (u *ChatUsage) Cached() int64 {
	if u == nil || u.PromptDetails == nil {
		return 0
	}

	return u.PromptDetails.CachedTokens
}

// messagesUsage is a chat usage as Messages counts it: input without what
// was read from the cache, which Claude Code adds back (9router-go
// claudeFinalUsage).
func messagesUsage(u *ChatUsage) item {
	if u == nil {
		return item{"input_tokens": 0, "output_tokens": 0}
	}

	cached := u.Cached()
	out := item{"input_tokens": max(u.PromptTokens-cached, 0), "output_tokens": u.CompletionTokens}

	if cached > 0 {
		out["cache_read_input_tokens"] = cached
	}

	return out
}

func stopReason(finish string) string {
	switch finish {
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "refusal"
	default:
		return "end_turn"
	}
}

func messageID(id string) string {
	if id = strings.TrimPrefix(id, "chatcmpl-"); id == "" || id == "chat" {
		return ""
	}

	return "msg_" + strings.TrimPrefix(id, "msg_")
}

// MessageFromChat turns a whole chat completion into a Messages answer under
// the model name the client asked for, and returns its usage.
func MessageFromChat(body []byte, model, fallbackID string) ([]byte, *ChatUsage, error) {
	var answer chatAnswer
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, nil, fmt.Errorf("reading the chat answer: %w", err)
	}

	if len(answer.Choices) == 0 {
		return nil, nil, fmt.Errorf("the chat answer has no choice")
	}

	choice := answer.Choices[0]
	msg := choice.Message
	content := []any{}

	if reasoning := msg.reasoning(); reasoning != "" {
		content = append(content, item{"type": "thinking", "thinking": reasoning})
	}

	if msg.Content != "" {
		content = append(content, item{"type": "text", "text": msg.Content})
	}

	for _, call := range msg.ToolCalls {
		if call.Function.Name == "" {
			continue
		}

		content = append(content, item{
			"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": toolInput(call.Function.Arguments),
		})
	}

	finish := ""
	if choice.FinishReason != nil {
		finish = *choice.FinishReason
	}

	id := messageID(answer.ID)
	if id == "" {
		id = fallbackID
	}

	encoded, err := json.Marshal(item{
		"id": id, "type": "message", "role": "assistant", "model": model, "content": content,
		"stop_reason": stopReason(finish), "stop_sequence": nil, "usage": messagesUsage(answer.Usage),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("encoding the Messages answer: %w", err)
	}

	return encoded, answer.Usage, nil
}

// toolInput is a tool call's arguments as a JSON object; arguments that are
// not JSON are kept under raw rather than lost (9router-go SafeToolCallInput).
func toolInput(args string) json.RawMessage {
	args = strings.TrimSpace(args)
	if args == "" || args == "null" {
		return json.RawMessage("{}")
	}

	if json.Valid([]byte(args)) {
		return json.RawMessage(args)
	}

	encoded, _ := json.Marshal(item{"raw": args}) //nolint:errchkjson // A map of a string always encodes.

	return encoded
}

// FromChatStream turns chat completion chunks into Messages events, one
// stream at a time (9router-go TranslateOpenAIToClaudeStreamSession).
type FromChatStream struct {
	id, model string

	started, ended bool
	next           int
	// open is the index of the text or thinking block now open, and kind
	// which it is; -1 when none is.
	open int
	kind string
	// calls maps a chat tool call's index to its block, args gathers its
	// arguments, and order keeps the calls in the order they began.
	calls map[int]int
	names map[int]string
	ids   map[int]string
	args  map[int]*strings.Builder
	order []int

	finish string
	usage  *ChatUsage
}

// NewFromChatStream starts a stream answered under model; id names the
// message when the chunks name none.
func NewFromChatStream(id, model string) *FromChatStream {
	return &FromChatStream{
		id: id, model: model, open: -1, calls: map[int]int{}, names: map[int]string{}, ids: map[int]string{},
		args: map[int]*strings.Builder{},
	}
}

// Usage is the usage the chunks reported, nil when none did.
func (s *FromChatStream) Usage() *ChatUsage { return s.usage }

// Ended says the stream sent its message_stop.
func (s *FromChatStream) Ended() bool { return s.ended }

// Chunk turns one chunk's data into the events it stands for, as SSE frames.
func (s *FromChatStream) Chunk(data []byte) []byte {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || s.ended {
		return nil
	}

	if string(data) == "[DONE]" {
		return s.Close()
	}

	var chunk chatAnswer
	if json.Unmarshal(data, &chunk) != nil {
		return nil
	}

	var out []item

	if chunk.Usage != nil {
		s.usage = chunk.Usage
	}

	if len(chunk.Choices) == 0 {
		// A usage frame after the finish closes the message with the counts.
		if s.finish != "" && s.started {
			out = append(out, s.terminal()...)
		}

		return frames(out)
	}

	if !s.started {
		if id := messageID(chunk.ID); id != "" {
			s.id = id
		}

		out = append(out, s.start())
	}

	choice := chunk.Choices[0]
	delta := choice.Delta

	if reasoning := delta.reasoning(); reasoning != "" {
		out = append(out, s.openBlock("thinking")...)
		out = append(out, item{"type": "content_block_delta", "index": s.open,
			"delta": item{"type": "thinking_delta", "thinking": reasoning}})
	}

	if delta.Content != "" {
		out = append(out, s.openBlock("text")...)
		out = append(out, item{"type": "content_block_delta", "index": s.open,
			"delta": item{"type": "text_delta", "text": delta.Content}})
	}

	for _, call := range delta.ToolCalls {
		index := 0
		if call.Index != nil {
			index = *call.Index
		}

		if _, known := s.calls[index]; !known {
			if call.Function.Name == "" {
				continue
			}

			out = append(out, s.closeBlock()...)
			s.calls[index], s.names[index], s.ids[index] = s.next, call.Function.Name, call.ID
			s.args[index] = &strings.Builder{}
			s.order = append(s.order, index)
			out = append(out, item{"type": "content_block_start", "index": s.next, "content_block": item{
				"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": item{},
			}})
			s.next++
		}

		_, _ = s.args[index].WriteString(call.Function.Arguments)
	}

	if choice.FinishReason != nil && *choice.FinishReason != "" {
		s.finish = *choice.FinishReason
		out = append(out, s.closeBlock()...)
		out = append(out, s.closeCalls()...)

		// The usage frame comes after the finish when the provider sends one;
		// the message is closed then, or at the end of the stream.
		if s.usage != nil {
			out = append(out, s.terminal()...)
		}
	}

	return frames(out)
}

// Close ends the message if the chunks did not, and reports nothing when the
// stream never began.
func (s *FromChatStream) Close() []byte {
	if s.ended || !s.started {
		return nil
	}

	out := append(s.closeBlock(), s.closeCalls()...)

	return frames(append(out, s.terminal()...))
}

func (s *FromChatStream) start() item {
	s.started = true

	return item{"type": "message_start", "message": item{
		"id": s.id, "type": "message", "role": "assistant", "model": s.model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil, "usage": item{"input_tokens": 0, "output_tokens": 0},
	}}
}

func (s *FromChatStream) openBlock(kind string) []item {
	if s.open >= 0 && s.kind == kind {
		return nil
	}

	out := s.closeBlock()
	s.open, s.kind = s.next, kind
	s.next++

	block := item{"type": kind, kind: ""}

	return append(out, item{"type": "content_block_start", "index": s.open, "content_block": block})
}

func (s *FromChatStream) closeBlock() []item {
	if s.open < 0 {
		return nil
	}

	out := []item{{"type": "content_block_stop", "index": s.open}}
	s.open = -1

	return out
}

// closeCalls sends each tool call's arguments whole and closes its block, as
// 9router-go does once the call is complete.
func (s *FromChatStream) closeCalls() []item {
	out := make([]item, 0, 2*len(s.order))

	for _, index := range s.order {
		block := s.calls[index]
		out = append(out,
			item{"type": "content_block_delta", "index": block, "delta": item{
				"type": "input_json_delta", "partial_json": string(toolInput(s.args[index].String())),
			}},
			item{"type": "content_block_stop", "index": block},
		)
	}

	s.order = nil

	return out
}

func (s *FromChatStream) terminal() []item {
	if s.ended {
		return nil
	}

	s.ended = true

	return []item{
		{"type": "message_delta", "delta": item{"stop_reason": stopReason(s.finish), "stop_sequence": nil},
			"usage": messagesUsage(s.usage)},
		{"type": "message_stop"},
	}
}

// frames writes events as Anthropic's SSE: an event line and a data line each.
func frames(events []item) []byte {
	var buf bytes.Buffer

	for _, e := range events {
		encoded, err := json.Marshal(e)
		if err != nil {
			continue
		}

		fmt.Fprintf(&buf, "event: %s\ndata: %s\n\n", e["type"], encoded)
	}

	return buf.Bytes()
}
