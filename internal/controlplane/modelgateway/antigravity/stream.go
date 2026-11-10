package antigravity

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The backend answers with a stream of Gemini chunks, each wrapped as
// {"response": …}. Stream turns them into chat completion chunks as they
// come and keeps the whole answer for a client that did not ask for a stream.
// It follows 9router-go's TranslateGeminiChunkToOpenAI
// (internal/translator/gemini.go) and CLIProxyAPI's
// ConvertAntigravityResponseToOpenAI, whose usage counts include the thought
// tokens.

// Reasons an account or a model cannot be used, as the backend names them.
const (
	ReasonQuotaExhausted = "QUOTA_EXHAUSTED"
	ReasonRateLimited    = "RATE_LIMIT_EXCEEDED"
	ReasonNoCapacity     = "MODEL_CAPACITY_EXHAUSTED"
	// ReasonTermsViolation says Google has suspended the account.
	ReasonTermsViolation = "TOS_VIOLATION"
	// ReasonVerification says Google wants the account verified first.
	ReasonVerification = "VALIDATION_REQUIRED"
)

// Failure is an error of the backend, from a refused request or from inside
// a stream.
type Failure struct {
	// Status is the HTTP status the error stands for.
	Status  int
	Code    string
	Message string
	// Reason is the backend's own word for it, when it gave one.
	Reason string
	// RetryAfter is how long the backend asks to wait; zero when it did not say.
	RetryAfter time.Duration
	// URL is where Google sends the owner: to verify the account, or to appeal.
	URL string
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
	id, name, arguments string
}

// Stream translates one answer of the backend.
type Stream struct {
	id      string
	created int64
	model   string
	names   Names

	roleSent  bool
	calls     []toolCall
	content   strings.Builder
	reasoning strings.Builder
	finish    string
	usage     *Usage
	failure   *Failure
}

// NewStream returns a translator for one answer. id, created and model are
// what the chunks carry; names maps function names back to the client's.
func NewStream(id string, created int64, model string, names Names) *Stream {
	return &Stream{id: id, created: created, model: model, names: names}
}

// Finished reports whether the backend ended the answer.
func (s *Stream) Finished() bool { return s.finish != "" }

// Failed returns the error the backend reported, or nil.
func (s *Stream) Failed() *Failure { return s.failure }

type chunk struct {
	Response *struct {
		Candidates []struct {
			Content struct {
				Parts []part `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata *struct {
			Prompt     int64 `json:"promptTokenCount"`
			Candidates int64 `json:"candidatesTokenCount"`
			Thoughts   int64 `json:"thoughtsTokenCount"`
			Cached     int64 `json:"cachedContentTokenCount"`
			Total      int64 `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	} `json:"response"`
	Error json.RawMessage `json:"error"`
}

type part struct {
	Text         string `json:"text"`
	Thought      bool   `json:"thought"`
	FunctionCall *struct {
		ID   string          `json:"id"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"functionCall"`
}

// Event takes the data of one server-sent event and returns the chat chunks
// it stands for, each as JSON. The chunk that carries the backend's finish
// reason gives the chunk with ours and then the one with the usage.
func (s *Stream) Event(data []byte) [][]byte {
	var ev chunk
	if json.Unmarshal(data, &ev) != nil || s.Finished() || s.failure != nil {
		return nil
	}

	if len(ev.Error) > 0 && string(ev.Error) != "null" {
		s.failure = failureOf(ev.Error, 0)

		return nil
	}

	if ev.Response == nil {
		return nil
	}

	var out [][]byte

	reason := ""

	for _, candidate := range ev.Response.Candidates {
		for _, p := range candidate.Content.Parts {
			switch {
			case p.FunctionCall != nil:
				out = append(out, s.call(p)...)
			case p.Text == "":
				// A part with only a signature says nothing to the client.
			case p.Thought:
				s.reasoning.WriteString(p.Text)
				out = append(out, s.chunk(object{"reasoning_content": p.Text}, ""))
			default:
				s.content.WriteString(p.Text)
				out = append(out, s.chunk(object{"content": p.Text}, ""))
			}
		}

		if candidate.FinishReason != "" {
			reason = candidate.FinishReason
		}
	}

	if u := ev.Response.UsageMetadata; u != nil {
		s.usage = &Usage{
			PromptTokens: u.Prompt, CompletionTokens: u.Candidates + u.Thoughts, TotalTokens: u.Total,
		}
		if s.usage.TotalTokens == 0 {
			s.usage.TotalTokens = s.usage.PromptTokens + s.usage.CompletionTokens
		}

		if u.Cached > 0 {
			s.usage.PromptTokensDetails = &promptDetails{CachedTokens: u.Cached}
		}

		if u.Thoughts > 0 {
			s.usage.CompletionDetails = &outputDetails{ReasoningTokens: u.Thoughts}
		}
	}

	if reason == "" {
		return out
	}

	s.finish = finishReason(reason, len(s.calls) > 0)
	out = append(out, s.chunk(object{}, s.finish))

	if s.usage != nil {
		out = append(out, mustJSON(object{
			"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model,
			"choices": []any{}, "usage": s.usage,
		}))
	}

	return out
}

// call is one whole tool call: the backend sends a call with its arguments
// complete, so the chunk that opens it also carries them.
func (s *Stream) call(p part) [][]byte {
	arguments := string(p.FunctionCall.Args)
	if arguments == "" || arguments == "null" {
		arguments = "{}"
	}

	id := p.FunctionCall.ID
	if id == "" {
		id = fmt.Sprintf("call_%s_%d", s.id, len(s.calls))
	}

	c := toolCall{id: id, name: s.names.Original(p.FunctionCall.Name), arguments: arguments}
	s.calls = append(s.calls, c)

	return [][]byte{s.chunk(object{"tool_calls": []object{{
		"index": len(s.calls) - 1, "id": c.id, "type": "function",
		"function": object{"name": c.name, "arguments": c.arguments},
	}}}, "")}
}

// finishReason says the backend's reason in OpenAI's words.
func finishReason(reason string, called bool) string {
	switch {
	case called:
		return "tool_calls"
	case reason == "MAX_TOKENS":
		return "length"
	case reason == "SAFETY" || reason == "RECITATION" || reason == "BLOCKLIST" || reason == "PROHIBITED_CONTENT" ||
		reason == "SPII" || reason == "IMAGE_SAFETY":
		return "content_filter"
	default:
		return "stop"
	}
}

func (s *Stream) chunk(delta object, finish string) []byte {
	if !s.roleSent {
		delta["role"] = "assistant"
		s.roleSent = true
	}

	choice := object{"index": 0, "delta": delta, "finish_reason": nil}
	if finish != "" {
		choice["finish_reason"] = finish
	}

	return mustJSON(object{
		"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model,
		"choices": []any{choice},
	})
}

// Completion is the whole answer as one chat completion, for a client that
// did not ask for a stream. It is meaningful once the stream has finished.
func (s *Stream) Completion() []byte {
	message := object{"role": "assistant", "content": nil}
	if s.content.Len() > 0 {
		message["content"] = s.content.String()
	}

	if s.reasoning.Len() > 0 {
		message["reasoning_content"] = s.reasoning.String()
	}

	if len(s.calls) > 0 {
		calls := make([]object, 0, len(s.calls))
		for _, c := range s.calls {
			calls = append(calls, object{
				"id": c.id, "type": "function", "function": object{"name": c.name, "arguments": c.arguments},
			})
		}

		message["tool_calls"] = calls
	}

	answer := object{
		"id": s.id, "object": "chat.completion", "created": s.created, "model": s.model,
		"choices": []any{object{"index": 0, "message": message, "finish_reason": s.finish}},
	}
	if s.usage != nil {
		answer["usage"] = s.usage
	}

	return mustJSON(answer)
}

// ParseFailure reads the body of a refused request. status is the HTTP status
// it came with.
func ParseFailure(status int, body []byte) *Failure {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}

	if json.Unmarshal(body, &envelope) != nil || len(envelope.Error) == 0 {
		return &Failure{Status: status, Code: "upstream_error", Message: "the model's provider refused the request"}
	}

	return failureOf(envelope.Error, status)
}

// failureOf reads a Google API error: its status word, its message, and in
// its details the reason, the wait asked for and a link for the owner
// (Google's Gemini CLI, utils/googleQuotaErrors.ts and utils/errors.ts).
func failureOf(raw json.RawMessage, status int) *Failure {
	var parsed struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			Type       string            `json:"@type"`
			Reason     string            `json:"reason"`
			RetryDelay string            `json:"retryDelay"`
			Metadata   map[string]string `json:"metadata"`
			Links      []struct {
				URL string `json:"url"`
			} `json:"links"`
		} `json:"details"`
	}

	_ = json.Unmarshal(raw, &parsed)

	f := &Failure{Status: status, Code: strings.ToLower(parsed.Status), Message: parsed.Message}
	if f.Status == 0 {
		f.Status = parsed.Code
	}

	if f.Status < 400 || f.Status > 599 {
		f.Status = http.StatusBadGateway
	}

	if f.Code == "" {
		f.Code = "upstream_error"
	}

	if f.Message == "" {
		f.Message = "the model's provider reported an error"
	}

	for _, detail := range parsed.Details {
		if detail.Reason != "" && f.Reason == "" {
			f.Reason = detail.Reason
		}

		for _, wait := range []string{detail.RetryDelay, detail.Metadata["quotaResetDelay"]} {
			if d, err := time.ParseDuration(wait); err == nil && d > f.RetryAfter {
				f.RetryAfter = d
			}
		}

		for _, key := range []string{"validation_url", "validationUrl", "appeal_url"} {
			if url := detail.Metadata[key]; url != "" && f.URL == "" {
				f.URL = url
			}
		}

		for _, link := range detail.Links {
			if link.URL != "" && f.URL == "" {
				f.URL = link.URL
			}
		}
	}

	// 9router-go knows a suspended account by this sentence when no reason is given.
	if f.Reason == "" && f.Status == http.StatusForbidden &&
		strings.Contains(strings.ToLower(f.Message), "disabled in this account for violation") {
		f.Reason = ReasonTermsViolation
	}

	return f
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{}`)
	}

	return b
}
