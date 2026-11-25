package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The opening of a stream (issue #1135). The backend may refuse a request it
// has already answered 200 to: response.created, response.in_progress, a
// response.metadata frame with the prompt's moderation flags, then an error
// event server_is_overloaded, all before any output (CLIProxyAPI #6856's
// capture). Only once output starts has the client seen anything worth
// keeping, so until then a gateway can hold the frames and send the request
// to another account. What may be held, and which refusals move on, follow
// CLIProxyAPI (internal/runtime/executor/codex_executor_terminal.go,
// isCodexBootstrapBufferableEvent and isCodexOverloadBootstrapFailure).

// Opening reports whether an event's data says nothing has happened yet, so
// it may be held back: the handshake, the moderation metadata, a keepalive, or
// the announcement of an item or part that is still empty. The list is closed,
// as CLIProxyAPI's is: an event it does not name may be output, or a tool the
// backend already runs, which another account must not run again.
func Opening(data []byte) bool {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		// An empty data frame is a heartbeat.
		return true
	}

	var ev struct {
		Type string          `json:"type"`
		Item json.RawMessage `json:"item"`
		Part json.RawMessage `json:"part"`
	}

	if json.Unmarshal(data, &ev) != nil {
		return false
	}

	switch ev.Type {
	case "response.created", "response.in_progress", "codex.rate_limits", "codex.response.metadata",
		"response.metadata", "keepalive":
		return true
	case "response.output_item.added":
		return emptyItem(ev.Item)
	case "response.content_part.added", "response.reasoning_summary_part.added":
		return emptyPart(ev.Part)
	default:
		return false
	}
}

type textEntry struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

// emptyItem says an announced output item is one the model makes itself and
// has not started: a message or reasoning with no text, a tool call with no
// arguments. Any other item, such as a web search the backend has started, is
// output (CLIProxyAPI isCodexBufferableOutputItem).
func emptyItem(raw json.RawMessage) bool {
	var item struct {
		Type             string      `json:"type"`
		Content          []textEntry `json:"content"`
		Summary          []textEntry `json:"summary"`
		EncryptedContent string      `json:"encrypted_content"`
		Arguments        string      `json:"arguments"`
		Input            string      `json:"input"`
	}

	if json.Unmarshal(raw, &item) != nil {
		return false
	}

	switch item.Type {
	case "message":
		return emptyEntries(item.Content)
	case "reasoning":
		return item.EncryptedContent == "" && emptyEntries(item.Summary) && emptyEntries(item.Content)
	case "function_call":
		return item.Arguments == ""
	case "custom_tool_call":
		return item.Input == ""
	default:
		return false
	}
}

// emptyEntries says every entry is a text the list knows and is still empty;
// an entry of another type may carry content where this cannot see it.
func emptyEntries(entries []textEntry) bool {
	return !slices.ContainsFunc(entries, func(e textEntry) bool { return !emptyEntry(e) })
}

func emptyEntry(e textEntry) bool {
	switch e.Type {
	case "output_text", "summary_text", "text", "reasoning_text":
		return e.Text == ""
	case "refusal":
		return e.Refusal == ""
	default:
		return false
	}
}

func emptyPart(raw json.RawMessage) bool {
	var part textEntry
	if json.Unmarshal(raw, &part) != nil {
		return false
	}

	return emptyEntry(part)
}

// EventFailure reads an error event or response.failed as the failure it
// reports, with the Retry-After its headers carry as ResetsAt; nil for any
// other event.
func EventFailure(data []byte, now time.Time) *Failure {
	var ev event
	if json.Unmarshal(data, &ev) != nil {
		return nil
	}

	var f *Failure

	switch ev.Type {
	case "error":
		f = failureAt(firstRaw(ev.Error, data), now)
	case "response.failed":
		f = responseFailure(ev.Response, now)
	default:
		return nil
	}

	if f.ResetsAt.IsZero() {
		if wait, ok := retryAfterOf(data, now); ok {
			f.ResetsAt = now.Add(wait)
		}
	}

	return f
}

// retryAfterPaths are where an event carries the headers of the error, the
// outer first: CLIProxyAPI's parseCodexWebsocketErrorHeaders, which reads the
// one nested in error.headers since its issue #7600.
var retryAfterPaths = [][]string{
	{"headers"}, {"body", "headers"}, {"error", "headers"}, {"body", "error", "headers"},
	{"response", "error", "headers"},
}

// retryAfterOf is the Retry-After an event's error names: seconds or an HTTP
// date (CLIProxyAPI parseRetryAfterHeader). The outer one named wins, even
// when it cannot be read, as in CLIProxyAPI.
func retryAfterOf(data []byte, now time.Time) (time.Duration, bool) {
	for _, path := range retryAfterPaths {
		headers, ok := objectAt(data, path)
		if !ok {
			continue
		}

		for name, raw := range headers {
			if !strings.EqualFold(strings.TrimSpace(name), "retry-after") {
				continue
			}

			return parseRetryAfter(raw, now)
		}
	}

	return 0, false
}

func objectAt(data []byte, path []string) (map[string]json.RawMessage, bool) {
	raw := json.RawMessage(data)

	for _, key := range path {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil {
			return nil, false
		}

		if raw = object[key]; raw == nil {
			return nil, false
		}
	}

	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}

	return object, true
}

func parseRetryAfter(raw json.RawMessage, now time.Time) (time.Duration, bool) {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		text = string(raw) // A number.
	}

	text = strings.TrimSpace(text)
	if seconds, err := strconv.ParseInt(text, 10, 64); err == nil && seconds >= 0 {
		return time.Duration(min(seconds, int64(maxRetryAfter/time.Second))) * time.Second, true
	}

	if at, err := http.ParseTime(text); err == nil {
		return max(at.Sub(now), 0), true
	}

	return 0, false
}

// maxRetryAfter keeps an absurd Retry-After from overflowing; how long an
// account is really kept out is the gateway's to bound.
const maxRetryAfter = 365 * 24 * time.Hour

// Overloaded says the failure is the backend's lack of capacity, which
// another account may well not share: an overload, a rate limit, a model at
// capacity, or a server error the backend says to retry (CLIProxyAPI
// isCodexOverloadBootstrapFailure). Only these move a request on to another
// account once the backend has answered 200; any other failure is the
// request's and reaches the client as it came.
func (f *Failure) Overloaded() bool {
	kind := strings.ToLower(f.Type)
	code := strings.ToLower(f.Code)
	message := strings.ToLower(f.Message)

	switch {
	case kind == "service_unavailable_error", code == "server_is_overloaded":
		return true
	case kind == "rate_limit_error", code == "rate_limit_exceeded", code == "turn_rate_limit_exceeded":
		return true
	case code == "model_at_capacity", code == "model_is_at_capacity",
		code == "" && strings.Contains(message, "model") && strings.Contains(message, "at capacity"):
		return true
	case (kind == "server_error" || code == "server_error") && strings.Contains(message, "you can retry your request"):
		return true
	default:
		return false
	}
}

// Text is the failure as a usage row keeps it: the code (else the type) and
// the message, and the moderation flags the backend sent before it.
func (f *Failure) Text() string {
	text := f.Message

	code := f.Code
	if code == "" {
		code = f.Type
	}

	if code != "" {
		text = code + ": " + text
	}

	if f.Moderation != "" {
		text += " (moderation: " + f.Moderation + ")"
	}

	return text
}

const (
	// maxModerationValue and maxModerationText bound what a usage row keeps of
	// the moderation metadata.
	maxModerationValue = 40
	maxModerationText  = 300
)

// ModerationFlags reads a response.metadata event's moderation metadata as
// its flags, each path with its value, such as
// "prompt.omnimod.protection_type=omnimod"; "" for another event or no flag.
// The metadata says how the prompt was classified, never the prompt itself
// (CLIProxyAPI #6856's capture).
func ModerationFlags(data []byte) string {
	if !bytes.Contains(data, []byte("openai_chatgpt_moderation_metadata")) {
		return ""
	}

	var ev struct {
		Type     string `json:"type"`
		Metadata struct {
			Moderation any `json:"openai_chatgpt_moderation_metadata"`
		} `json:"metadata"`
	}

	if json.Unmarshal(data, &ev) != nil || ev.Type != "response.metadata" {
		return ""
	}

	var flags []string

	flatten(ev.Metadata.Moderation, "", &flags)

	text := strings.Join(flags, " ")
	if utf8.RuneCountInString(text) > maxModerationText {
		text = string([]rune(text)[:maxModerationText]) + "…"
	}

	return text
}

func flatten(v any, path string, out *[]string) {
	join := func(key string) string {
		if path == "" {
			return key
		}

		return path + "." + key
	}

	switch value := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}

		slices.Sort(keys)

		for _, key := range keys {
			flatten(value[key], join(key), out)
		}
	case []any:
		for i, item := range value {
			flatten(item, join(strconv.Itoa(i)), out)
		}
	case nil:
	case string:
		if utf8.RuneCountInString(value) > maxModerationValue {
			value = string([]rune(value)[:maxModerationValue]) + "…"
		}

		*out = append(*out, path+"="+value)
	default:
		*out = append(*out, fmt.Sprintf("%s=%v", path, value))
	}
}
