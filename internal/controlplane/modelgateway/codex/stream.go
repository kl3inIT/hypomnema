package codex

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// The backend always answers with a stream of Responses events. Stream turns
// them into chat completion chunks as they come, and keeps what a client that
// did not ask for a stream needs: the whole answer as one chat completion.
// It follows 9router-go's ProcessCodexEvent (internal/proxy/executor/stream.go)
// for the events and the tool calls, and CLIProxyAPI's
// ConvertCodexResponseToOpenAI for an answer that was cut off and for text
// that arrives only with its finished item.

// Failure is an error the backend reported inside its stream.
type Failure struct {
	// Status is the HTTP status the error stands for.
	Status  int
	Type    string
	Code    string
	Message string
	// ResetsAt is when a usage limit ends; zero when the backend did not say.
	ResetsAt time.Time
}

// Usage counts the tokens of one answer, in the chat format's names.
type Usage struct {
	PromptTokens        int64          `json:"prompt_tokens"`
	CompletionTokens    int64          `json:"completion_tokens"`
	TotalTokens         int64          `json:"total_tokens"`
	PromptTokensDetails *promptDetails `json:"prompt_tokens_details,omitempty"`
	CompletionDetails   *outputDetails `json:"completion_tokens_details,omitempty"`
}

type promptDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

type outputDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

type toolCall struct {
	id        string
	name      string
	arguments strings.Builder
	started   bool
}

// Stream translates one answer of the backend.
type Stream struct {
	id      string
	created int64
	model   string
	names   Names

	roleSent bool
	// calls are the tool calls in the order the backend opened them; a call
	// is found by its call id or by the id of its output item.
	calls  []*toolCall
	byCall map[string]int
	byItem map[string]int
	// streamed are the message items whose text came in deltas.
	streamed map[string]bool

	content   strings.Builder
	reasoning strings.Builder
	finish    string
	usage     *Usage
	failure   *Failure
}

// NewStream returns a translator for one answer. id, created and model are
// what the chunks carry; names maps tool names back to the client's.
func NewStream(id string, created int64, model string, names Names) *Stream {
	return &Stream{
		id: id, created: created, model: model, names: names,
		byCall: map[string]int{}, byItem: map[string]int{}, streamed: map[string]bool{},
	}
}

// Finished reports whether the backend ended the answer, and how.
func (s *Stream) Finished() bool { return s.finish != "" }

// Failed returns the error the backend reported, or nil.
func (s *Stream) Failed() *Failure { return s.failure }

type event struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta"`
	ItemID   string          `json:"item_id"`
	CallID   string          `json:"call_id"`
	Name     string          `json:"name"`
	Args     string          `json:"arguments"`
	Item     *outputItem     `json:"item"`
	Response *responseObject `json:"response"`
	Error    json.RawMessage `json:"error"`
	Code     string          `json:"code"`
	Message  string          `json:"message"`
}

type outputItem struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Input     string `json:"input"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type responseObject struct {
	Error             json.RawMessage `json:"error"`
	IncompleteDetails struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage *struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
		TotalTokens  int64 `json:"total_tokens"`
		InputDetails *struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		OutputDetails *struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

// Event takes the data of one server-sent event and returns the chat chunks
// it stands for, each as JSON. Most events give one chunk or none; the last
// gives the chunk with the finish reason and then the one with the usage.
func (s *Stream) Event(data []byte) [][]byte {
	var ev event
	if json.Unmarshal(data, &ev) != nil || s.Finished() || s.failure != nil {
		return nil
	}

	switch ev.Type {
	case "error":
		s.failure = failureOf(firstRaw(ev.Error, data))
	case "response.failed":
		if ev.Response != nil {
			s.failure = failureOf(ev.Response.Error)
		} else {
			s.failure = failureOf(nil)
		}
	case "response.output_text.delta", "response.text.delta":
		s.streamed[ev.ItemID] = true

		return s.text(ev.Delta)
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return s.thought(ev.Delta)
	case "response.reasoning_summary_text.done", "response.reasoning_text.done":
		// One summary has several parts; a blank line keeps them apart.
		return s.thought("\n\n")
	case "response.output_item.added":
		if ev.Item != nil && isCall(ev.Item.Type) {
			return s.openCall(ev.Item)
		}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		return s.arguments(s.callFor(ev.ItemID, ev.CallID), ev.Delta)
	case "response.function_call_arguments.done":
		return s.restOfArguments(s.callFor(ev.ItemID, ev.CallID), ev.Args)
	case "response.output_item.done":
		return s.itemDone(ev.Item)
	case "response.completed":
		reason := "stop"
		if len(s.calls) > 0 {
			reason = "tool_calls"
		}

		return s.end(reason, ev.Response)
	case "response.incomplete":
		reason := "stop"

		if ev.Response != nil {
			switch ev.Response.IncompleteDetails.Reason {
			case "max_output_tokens", "max_tokens":
				reason = "length"
			case "content_filter":
				reason = "content_filter"
			}
		}

		return s.end(reason, ev.Response)
	}

	return nil
}

func isCall(itemType string) bool {
	return itemType == "function_call" || itemType == "custom_tool_call"
}

func firstRaw(a json.RawMessage, b []byte) json.RawMessage {
	if len(a) > 0 && string(a) != "null" {
		return a
	}

	return b
}

func (s *Stream) text(delta string) [][]byte {
	if delta == "" {
		return nil
	}

	s.content.WriteString(delta)

	return s.chunk(map[string]any{"content": delta}, "")
}

func (s *Stream) thought(delta string) [][]byte {
	// A separator before any reasoning, or after one, says nothing.
	if delta == "" || (strings.TrimSpace(delta) == "" &&
		(s.reasoning.Len() == 0 || strings.HasSuffix(s.reasoning.String(), delta))) {
		return nil
	}

	s.reasoning.WriteString(delta)

	return s.chunk(map[string]any{"reasoning_content": delta}, "")
}

// openCall starts a tool call: its index, id and name, with no arguments yet.
func (s *Stream) openCall(it *outputItem) [][]byte {
	at, known := s.indexOf(it.ID, it.CallID)
	if !known {
		id := it.CallID
		if id == "" {
			id = it.ID
		}

		s.calls = append(s.calls, &toolCall{id: id, name: s.names.Original(it.Name)})
		at = len(s.calls) - 1

		if it.CallID != "" {
			s.byCall[it.CallID] = at
		}
	}

	if it.ID != "" {
		s.byItem[it.ID] = at
	}

	call := s.calls[at]
	if call.started {
		return nil
	}

	call.started = true

	return s.chunk(map[string]any{"tool_calls": []map[string]any{{
		"index": at, "id": call.id, "type": "function",
		"function": map[string]any{"name": call.name, "arguments": ""},
	}}}, "")
}

func (s *Stream) indexOf(itemID, callID string) (int, bool) {
	if at, ok := s.byItem[itemID]; ok && itemID != "" {
		return at, true
	}

	at, ok := s.byCall[callID]

	return at, ok && callID != ""
}

// callFor finds the call an event speaks of; an event that names none speaks
// of the last one opened.
func (s *Stream) callFor(itemID, callID string) int {
	if at, ok := s.indexOf(itemID, callID); ok {
		return at
	}

	return len(s.calls) - 1
}

func (s *Stream) arguments(at int, delta string) [][]byte {
	if at < 0 || delta == "" {
		return nil
	}

	s.calls[at].arguments.WriteString(delta)

	return s.chunk(map[string]any{"tool_calls": []map[string]any{{
		"index": at, "function": map[string]any{"arguments": delta},
	}}}, "")
}

// restOfArguments sends what of a call's whole arguments the deltas did not
// carry: all of them when none came, nothing when they all did.
func (s *Stream) restOfArguments(at int, whole string) [][]byte {
	if at < 0 {
		return nil
	}

	sent := s.calls[at].arguments.String()
	if !strings.HasPrefix(whole, sent) {
		return nil
	}

	return s.arguments(at, strings.TrimPrefix(whole, sent))
}

// itemDone takes a finished output item: a tool call the stream never
// opened, arguments that came only with it, or a message whose text never
// came in deltas.
func (s *Stream) itemDone(it *outputItem) [][]byte {
	switch {
	case it == nil:
		return nil
	case isCall(it.Type):
		chunks := s.openCall(it)
		whole := it.Arguments
		if whole == "" {
			whole = it.Input
		}

		return append(chunks, s.restOfArguments(s.callFor(it.ID, it.CallID), whole)...)
	case it.Type == "message" && !s.streamed[it.ID]:
		var chunks [][]byte

		for _, part := range it.Content {
			if part.Type == "output_text" {
				chunks = append(chunks, s.text(part.Text)...)
			}
		}

		return chunks
	}

	return nil
}

func (s *Stream) end(reason string, resp *responseObject) [][]byte {
	if resp != nil && resp.Usage != nil {
		u := resp.Usage
		s.usage = &Usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens, TotalTokens: u.TotalTokens}

		if s.usage.TotalTokens == 0 {
			s.usage.TotalTokens = u.InputTokens + u.OutputTokens
		}

		if u.InputDetails != nil {
			s.usage.PromptTokensDetails = &promptDetails{CachedTokens: u.InputDetails.CachedTokens}
		}

		if u.OutputDetails != nil {
			s.usage.CompletionDetails = &outputDetails{ReasoningTokens: u.OutputDetails.ReasoningTokens}
		}
	}

	chunks := s.chunk(map[string]any{}, reason)
	s.finish = reason

	if s.usage != nil {
		chunks = append(chunks, mustJSON(map[string]any{
			"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model,
			"choices": []any{}, "usage": s.usage,
		}))
	}

	return chunks
}

// chunk is one chat completion chunk. The first one of an answer names the
// assistant, as OpenAI's own stream does.
func (s *Stream) chunk(delta map[string]any, finish string) [][]byte {
	if !s.roleSent {
		delta["role"] = "assistant"
		s.roleSent = true
	}

	choice := map[string]any{"index": 0, "delta": delta, "finish_reason": nil}
	if finish != "" {
		choice["finish_reason"] = finish
	}

	return [][]byte{mustJSON(map[string]any{
		"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model,
		"choices": []any{choice},
	})}
}

// Completion is the whole answer as one chat completion, for a client that
// did not ask for a stream. It is meaningful once the stream has finished.
func (s *Stream) Completion() []byte {
	message := map[string]any{"role": "assistant", "content": nil}
	if s.content.Len() > 0 {
		message["content"] = s.content.String()
	}

	if reasoning := strings.TrimSpace(s.reasoning.String()); reasoning != "" {
		message["reasoning_content"] = reasoning
	}

	if len(s.calls) > 0 {
		calls := make([]map[string]any, 0, len(s.calls))
		for _, call := range s.calls {
			calls = append(calls, map[string]any{
				"id": call.id, "type": "function",
				"function": map[string]any{"name": call.name, "arguments": call.arguments.String()},
			})
		}

		message["tool_calls"] = calls
	}

	answer := map[string]any{
		"id": s.id, "object": "chat.completion", "created": s.created, "model": s.model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": s.finish}},
	}
	if s.usage != nil {
		answer["usage"] = s.usage
	}

	return mustJSON(answer)
}

// ParseFailure reads the body of a refused request as the error it carries.
// status is the HTTP status it came with.
func ParseFailure(status int, body []byte) *Failure {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}

	raw := json.RawMessage(body)
	if json.Unmarshal(body, &envelope) == nil && len(envelope.Error) > 0 {
		raw = envelope.Error
	}

	f := failureOf(raw)
	f.Status = status

	if f.Type == "usage_limit_reached" {
		// The backend says 429 for this, but not always (CLIProxyAPI).
		f.Status = http.StatusTooManyRequests
	}

	return f
}

// failureOf reads an error object of the backend. Its status is the one the
// error names, else the one its type or code stands for (CLIProxyAPI,
// codexTerminalFailureStatus).
func failureOf(raw json.RawMessage) *Failure {
	var parsed struct {
		Type            string      `json:"type"`
		Code            any         `json:"code"`
		Message         string      `json:"message"`
		Status          int         `json:"status"`
		StatusCode      int         `json:"status_code"`
		ResetsAt        json.Number `json:"resets_at"`
		ResetsInSeconds json.Number `json:"resets_in_seconds"`
	}

	// An "error" event may carry a plain string.
	var text string
	if json.Unmarshal(raw, &text) == nil {
		parsed.Message = text
	} else {
		_ = json.Unmarshal(raw, &parsed)
	}

	f := &Failure{Type: parsed.Type, Message: parsed.Message, Status: parsed.StatusCode}
	if code, ok := parsed.Code.(string); ok {
		f.Code = code
	}

	if f.Status == 0 {
		f.Status = parsed.Status
	}

	if f.Message == "" {
		f.Message = "the model's provider reported an error"
	}

	if at, err := parsed.ResetsAt.Int64(); err == nil && at > 0 {
		f.ResetsAt = time.Unix(at, 0)
	} else if in, err := parsed.ResetsInSeconds.Float64(); err == nil && in > 0 {
		f.ResetsAt = time.Now().Add(time.Duration(in * float64(time.Second)))
	}

	if f.Status < 400 || f.Status > 599 {
		f.Status = statusFor(strings.ToLower(f.Type + " " + f.Code))
	}

	return f
}

func statusFor(kind string) int {
	switch {
	case strings.Contains(kind, "usage_limit") || strings.Contains(kind, "rate_limit"):
		return http.StatusTooManyRequests
	case strings.Contains(kind, "auth") || strings.Contains(kind, "token_expired"):
		return http.StatusUnauthorized
	case strings.Contains(kind, "forbidden") || strings.Contains(kind, "permission"):
		return http.StatusForbidden
	case strings.Contains(kind, "not_found"):
		return http.StatusNotFound
	case strings.Contains(kind, "invalid") || strings.Contains(kind, "context_length"):
		return http.StatusBadRequest
	default:
		return http.StatusBadGateway
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{}`)
	}

	return b
}
