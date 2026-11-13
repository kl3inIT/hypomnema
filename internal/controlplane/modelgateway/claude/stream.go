package claude

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// The Messages event stream turned into OpenAI chat chunks (9router-go
// translator/claude_response.go, executor/claude_messages.go). A text block
// becomes content, a thinking block reasoning_content, a tool_use block a tool
// call whose name is the client's again. The last event carries the usage and
// the stop reason.

// Failure is an error from Anthropic, in the chat format's shape.
type Failure struct {
	Status   int
	Type     string
	Code     string
	Message  string
	ResetsAt time.Time
}

// Usage counts the tokens of one answer.
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	PromptDetails    struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	// CacheCreationInputTokens is what was written to the cache, part of the
	// prompt; outside OpenAI's shape, as LiteLLM and 9router-go carry it.
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
}

// Stream turns Anthropic's Messages events into chat chunks.
type Stream struct {
	id      string
	created int64
	model   string
	names   Names

	// blocks maps a Messages content block index to the tool-call index it
	// opened, or -1 for text and thinking.
	blocks map[int]int
	calls  int

	finish  string
	failure *Failure
	usage   *Usage
	// text and reason gather the whole answer for a non-streaming client.
	text   strings.Builder
	reason strings.Builder
	tools  []toolCall
}

type toolCall struct {
	id   string
	name string
	args strings.Builder
}

// NewStream starts a translation. names turns a tool's disguised name back
// into the client's.
func NewStream(id string, created int64, model string, names Names) *Stream {
	return &Stream{id: id, created: created, model: model, names: names, blocks: map[int]int{}}
}

// Finished reports whether Anthropic ended the answer.
func (s *Stream) Finished() bool { return s.finish != "" }

// Failed reports an error inside the stream.
func (s *Stream) Failed() *Failure { return s.failure }

// Usage is what the answer used, once known.
func (s *Stream) Usage() *Usage { return s.usage }

type messagesEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		ID    string   `json:"id"`
		Model string   `json:"model"`
		Usage rawUsage `json:"usage"`
		Role  string   `json:"role"`
	} `json:"message"`
	ContentBlock struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage rawUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type rawUsage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	CacheReadInput     int64 `json:"cache_read_input_tokens"`
	CacheCreationInput int64 `json:"cache_creation_input_tokens"`
}

// Event turns one Messages event's data into the chat chunks it stands for.
func (s *Stream) Event(data []byte) [][]byte {
	var e messagesEvent
	if json.Unmarshal(data, &e) != nil {
		return nil
	}

	switch e.Type {
	case "message_start":
		s.recordUsage(e.Message.Usage)

		return nil
	case "content_block_start":
		return s.blockStart(e)
	case "content_block_delta":
		return s.blockDelta(e)
	case "message_delta":
		s.recordUsage(e.Usage)

		if e.Delta.StopReason != "" {
			s.finish = finishReason(e.Delta.StopReason)

			return s.chunk(item{}, s.finish)
		}

		return nil
	case "message_stop":
		if s.finish == "" {
			s.finish = "stop"
		}

		return nil
	case "error":
		if e.Error != nil {
			s.failure = &Failure{Status: 502, Type: e.Error.Type, Code: e.Error.Type, Message: e.Error.Message}
		}

		return nil
	default:
		return nil
	}
}

func (s *Stream) blockStart(e messagesEvent) [][]byte {
	switch e.ContentBlock.Type {
	case "text":
		s.blocks[e.Index] = -1

		return nil
	case "thinking":
		s.blocks[e.Index] = -1

		return nil
	case "tool_use":
		at := s.calls
		s.calls++
		s.blocks[e.Index] = at

		name := s.names.Original(e.ContentBlock.Name)
		s.tools = append(s.tools, toolCall{id: e.ContentBlock.ID, name: name})

		return s.chunk(item{"tool_calls": []any{item{
			"index": at, "id": e.ContentBlock.ID, "type": "function",
			"function": item{"name": name, "arguments": ""},
		}}}, "")
	default:
		s.blocks[e.Index] = -1

		return nil
	}
}

func (s *Stream) blockDelta(e messagesEvent) [][]byte {
	switch e.Delta.Type {
	case "text_delta":
		if e.Delta.Text == "" {
			return nil
		}

		s.text.WriteString(e.Delta.Text)

		return s.chunk(item{"content": e.Delta.Text}, "")
	case "thinking_delta":
		if e.Delta.Thinking == "" {
			return nil
		}

		s.reason.WriteString(e.Delta.Thinking)

		return s.chunk(item{"reasoning_content": e.Delta.Thinking}, "")
	case "input_json_delta":
		at, ok := s.blocks[e.Index]
		if !ok || at < 0 || e.Delta.PartialJSON == "" {
			return nil
		}

		if at < len(s.tools) {
			s.tools[at].args.WriteString(e.Delta.PartialJSON)
		}

		return s.chunk(item{"tool_calls": []any{item{
			"index": at, "function": item{"arguments": e.Delta.PartialJSON},
		}}}, "")
	default:
		return nil
	}
}

func (s *Stream) recordUsage(u rawUsage) {
	if s.usage == nil {
		s.usage = &Usage{}
	}

	if u.InputTokens > 0 {
		s.usage.PromptTokens = u.InputTokens + u.CacheReadInput + u.CacheCreationInput
		s.usage.PromptDetails.CachedTokens = u.CacheReadInput
		s.usage.CacheCreationInputTokens = u.CacheCreationInput
	}

	if u.OutputTokens > 0 {
		s.usage.CompletionTokens = u.OutputTokens
	}

	s.usage.TotalTokens = s.usage.PromptTokens + s.usage.CompletionTokens
}

// chunk is one chat completion chunk with the delta and, at the end, the
// finish reason.
func (s *Stream) chunk(delta item, finish string) [][]byte {
	choice := item{"index": 0, "delta": delta}
	if finish != "" {
		choice["finish_reason"] = finish
	} else {
		choice["finish_reason"] = nil
	}

	out, err := json.Marshal(item{
		"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model,
		"choices": []any{choice},
	})
	if err != nil {
		return nil
	}

	return [][]byte{out}
}

// Completion is the whole answer as one chat completion, for a client that
// did not ask for a stream.
func (s *Stream) Completion() []byte {
	message := item{"role": "assistant", "content": nil}

	if s.text.Len() > 0 {
		message["content"] = s.text.String()
	}

	if s.reason.Len() > 0 {
		message["reasoning_content"] = s.reason.String()
	}

	if len(s.tools) > 0 {
		calls := make([]any, 0, len(s.tools))
		for i, t := range s.tools {
			args := t.args.String()
			if args == "" {
				args = "{}"
			}

			calls = append(calls, item{
				"index": i, "id": t.id, "type": "function",
				"function": item{"name": t.name, "arguments": args},
			})
		}

		message["tool_calls"] = calls
	}

	finish := s.finish
	if finish == "" {
		finish = "stop"
	}

	body := item{
		"id": s.id, "object": "chat.completion", "created": s.created, "model": s.model,
		"choices": []any{item{"index": 0, "message": message, "finish_reason": finish}},
	}

	if s.usage != nil {
		body["usage"] = s.usage
	}

	out, _ := json.Marshal(body) //nolint:errchkjson // maps of simple values

	return out
}

// finishReason maps a Messages stop_reason to a chat finish_reason.
func finishReason(reason string) string {
	switch reason {
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "refusal":
		return "content_filter"
	default: // end_turn, stop_sequence
		return "stop"
	}
}

// ParseFailure reads an error answer of Anthropic's. A 429 whose reset headers
// the adapter read is given a ResetsAt there.
func ParseFailure(status int, body []byte) *Failure {
	f := &Failure{Status: status, Type: typeOf(status), Code: "upstream_error"}

	var envelope struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if json.Unmarshal(body, &envelope) == nil && envelope.Error.Type != "" {
		f.Type = envelope.Error.Type
		f.Code = envelope.Error.Type
		f.Message = envelope.Error.Message
	}

	if f.Message == "" {
		f.Message = strings.TrimSpace(string(body))
	}

	return f
}

func typeOf(status int) string {
	switch {
	case status == 429:
		return "rate_limit_error"
	case status == 401 || status == 403:
		return "authentication_error"
	case status >= 500:
		return "api_error"
	default:
		return "invalid_request_error"
	}
}

// RetrySeconds reads a Retry-After header value as whole seconds.
func RetrySeconds(header string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || n < 0 {
		return 0, false
	}

	return n, true
}
