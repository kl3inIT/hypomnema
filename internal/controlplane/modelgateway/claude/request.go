// Package claude turns a chat completions request into Anthropic's Messages
// format and back, for a gateway serving a Claude subscription (issue #20,
// step 5). The translation and the disguise are 9router-go's, ported
// (docs/research/2026-11-11-claude-subscription-at-a-gateway.md): Anthropic
// gates a Pro/Max OAuth token to Claude Code, so a request under one must
// carry Claude Code's identity (headers in the adapter; a billing system
// block and a metadata.user_id here) or the API answers 429 even to a valid
// body, and it reroutes a request whose tools look like a third party's, so
// the client's tools are renamed and Claude Code's own are added as decoys.
//
// This is the owner's own subscription for the owner's own agents, which the
// owner chose against Anthropic's terms (owner, 2026-11-11); it is not built
// to serve anyone else.
package claude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// item is one JSON object on the way through the translation.
type item = map[string]any

// defaultMaxTokens is the Messages API's required max_tokens when the client
// named none (9router-go ensureMessagesFor).
const defaultMaxTokens = 4096

// Options are what the gateway knows of a request.
type Options struct {
	// Model is the model's name at Anthropic.
	Model string
	// Token is the account's access token, from which the stable device and
	// account ids of metadata.user_id are derived.
	Token string
	// Session is the request's session, which keeps its prompt cache and is
	// the session id of metadata.user_id; a value for a client that names none.
	Session string
}

// Request is a Messages body with what is needed to read its answer.
type Request struct {
	Body []byte
	// Names maps a tool's name as sent to Anthropic back to the client's, to
	// undo the disguise in the answer.
	Names Names
	// Session is the session the request carries.
	Session string
}

// Names maps a tool's name as sent to Anthropic to the name the client gave it.
type Names map[string]string

// Original returns the client's name for a tool Anthropic named.
func (n Names) Original(sent string) string {
	if original, ok := n[sent]; ok {
		return original
	}

	return strings.TrimSuffix(sent, toolSuffix)
}

// chatBody is the part of a chat completions request the translation reads.
type chatBody struct {
	Messages          []json.RawMessage `json:"messages"`
	Tools             []json.RawMessage `json:"tools"`
	ToolChoice        json.RawMessage   `json:"tool_choice"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls"`
	MaxTokens         json.Number       `json:"max_tokens"`
	MaxCompletion     json.Number       `json:"max_completion_tokens"`
	Temperature       *float64          `json:"temperature"`
	TopP              *float64          `json:"top_p"`
	Stop              json.RawMessage   `json:"stop"`
	ReasoningEffort   string            `json:"reasoning_effort"`
	System            json.RawMessage   `json:"system"`
}

// BuildRequest turns a chat completions body into a Messages body under an
// account, disguised as Claude Code.
func BuildRequest(chat []byte, opt Options) (Request, error) {
	if opt.Model == "" {
		return Request{}, errors.New("the model's name is missing")
	}

	decoder := json.NewDecoder(bytes.NewReader(chat))
	decoder.UseNumber()

	var in chatBody
	if err := decoder.Decode(&in); err != nil {
		return Request{}, fmt.Errorf("reading the chat request: %w", err)
	}

	body := item{"model": opt.Model, "stream": true, "max_tokens": maxTokens(in)}

	system, messages := convertMessages(in.Messages)
	body["messages"] = messages

	if system != "" {
		body["system"] = system
	}

	names := Names{}

	if tools := convertTools(in.Tools, names); len(tools) > 0 {
		body["tools"] = tools
	}

	if choice := convertToolChoice(in.ToolChoice, in.ParallelToolCalls, len(names) > 0); choice != nil {
		body["tool_choice"] = choice
	}

	applyThinking(body, in.ReasoningEffort, in.Temperature != nil || in.TopP != nil)

	if in.Temperature != nil {
		if _, pinned := body["temperature"]; !pinned {
			body["temperature"] = *in.Temperature
		}
	}

	if in.TopP != nil {
		if _, thinking := body["thinking"]; !thinking {
			body["top_p"] = *in.TopP
		}
	}

	if len(in.Stop) > 0 {
		body["stop_sequences"] = stopSequences(in.Stop)
	}

	// The disguise: the client's tools become Claude Code's, then the billing
	// block and the fake user id go on, over the body as it then stands.
	cloakTools(body, names)
	cloak(body, opt.Token, opt.Session)

	encoded, err := json.Marshal(body)
	if err != nil {
		return Request{}, fmt.Errorf("encoding the Messages request: %w", err)
	}

	return Request{Body: encoded, Names: names, Session: opt.Session}, nil
}

// maxTokens is the Messages API's required field: the client's, or its legacy
// name, or a default; raised later to fit a thinking budget.
func maxTokens(in chatBody) int {
	for _, n := range []json.Number{in.MaxTokens, in.MaxCompletion} {
		if v, err := n.Int64(); err == nil && v > 0 {
			return int(v)
		}
	}

	return defaultMaxTokens
}

// convertMessages lifts the system messages to a top-level system and turns
// the rest into Messages turns, merging turns of the same role, as 9router-go
// does (convertOpenAIMessagesToClaude).
func convertMessages(raw []json.RawMessage) (system string, messages []any) {
	ids := map[string]string{}

	var (
		systemParts []string
		turns       []item
	)

	for _, m := range raw {
		var msg struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		}

		if json.Unmarshal(m, &msg) != nil {
			continue
		}

		switch {
		case msg.Role == "system":
			systemParts = append(systemParts, systemTextOf(msg.Content)...)
		case msg.Role == "tool":
			turns = append(turns, item{"role": "user", "content": []any{item{
				"type": "tool_result", "tool_use_id": sanitizeID(msg.ToolCallID, ids),
				"content": contentBlocks(msg.Content), "is_error": false,
			}}})
		case msg.Role == "assistant" && len(msg.ToolCalls) > 0:
			blocks := contentBlocks(msg.Content)
			for _, tc := range msg.ToolCalls {
				var input any = item{}
				if strings.TrimSpace(tc.Function.Arguments) != "" {
					_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
				}

				blocks = append(blocks, item{
					"type": "tool_use", "id": sanitizeID(tc.ID, ids), "name": tc.Function.Name, "input": input,
				})
			}

			turns = append(turns, item{"role": "assistant", "content": blocks})
		default:
			role := msg.Role
			if role != "user" && role != "assistant" {
				role = "user"
			}

			turns = append(turns, item{"role": role, "content": contentBlocks(msg.Content)})
		}
	}

	return strings.Join(systemParts, "\n\n"), merge(turns)
}

// merge joins consecutive turns of the same role, which the Messages API wants.
func merge(turns []item) []any {
	out := make([]any, 0, len(turns))

	for _, t := range turns {
		if len(out) > 0 {
			if prev, ok := out[len(out)-1].(item); ok && prev["role"] == t["role"] {
				prev["content"] = append(blocksOf(prev["content"]), blocksOf(t["content"])...)

				continue
			}
		}

		out = append(out, t)
	}

	return out
}

// systemTextOf reads the text of a system message, a string or text blocks.
func systemTextOf(content json.RawMessage) []string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		if strings.TrimSpace(text) == "" {
			return nil
		}

		return []string{text}
	}

	var out []string

	for _, b := range blocksOf(contentBlocks(content)) {
		if block, ok := b.(item); ok {
			if t, ok := block["text"].(string); ok && strings.TrimSpace(t) != "" {
				out = append(out, t)
			}
		}
	}

	return out
}

// contentBlocks turns a message's content into Messages content blocks: a
// string is one text block; a list of OpenAI parts becomes text and image blocks.
func contentBlocks(content json.RawMessage) []any {
	if len(content) == 0 {
		return []any{}
	}

	var text string
	if json.Unmarshal(content, &text) == nil {
		if text == "" {
			return []any{}
		}

		return []any{item{"type": "text", "text": text}}
	}

	var parts []json.RawMessage
	if json.Unmarshal(content, &parts) != nil {
		return []any{}
	}

	out := make([]any, 0, len(parts))

	for _, p := range parts {
		var part struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			ImageURL json.RawMessage `json:"image_url"`
			// A Messages block a client sent through: kept as it is.
			Raw item
		}

		_ = json.Unmarshal(p, &part)
		_ = json.Unmarshal(p, &part.Raw)

		switch part.Type {
		case "text", "input_text":
			out = append(out, item{"type": "text", "text": part.Text})
		case "image_url":
			if block := imageBlock(part.ImageURL); block != nil {
				out = append(out, block)
			}
		case "text_block", "tool_result", "tool_use", "image", "thinking":
			out = append(out, part.Raw) // Already a Messages block.
		default:
			if part.Text != "" {
				out = append(out, item{"type": "text", "text": part.Text})
			}
		}
	}

	return out
}

// imageBlock turns an OpenAI image_url part into a Messages image block, for a
// data URL only; a remote URL is passed as a URL source.
func imageBlock(raw json.RawMessage) item {
	var url string
	if json.Unmarshal(raw, &url) != nil {
		var obj struct {
			URL string `json:"url"`
		}

		if json.Unmarshal(raw, &obj) != nil {
			return nil
		}

		url = obj.URL
	}

	if strings.HasPrefix(url, "data:") {
		if media, data, ok := dataURL(url); ok {
			return item{"type": "image", "source": item{
				"type": "base64", "media_type": media, "data": data,
			}}
		}

		return nil
	}

	if url == "" {
		return nil
	}

	return item{"type": "image", "source": item{"type": "url", "url": url}}
}

func dataURL(url string) (media, data string, ok bool) {
	rest, found := strings.CutPrefix(url, "data:")
	if !found {
		return "", "", false
	}

	head, body, found := strings.Cut(rest, ",")
	if !found {
		return "", "", false
	}

	media, _, _ = strings.Cut(head, ";")

	return media, body, media != ""
}

// blocksOf reads a content value as a list of blocks.
func blocksOf(content any) []any {
	switch v := content.(type) {
	case []any:
		return v
	case string:
		if v == "" {
			return []any{}
		}

		return []any{item{"type": "text", "text": v}}
	case nil:
		return []any{}
	default:
		return []any{v}
	}
}

// sanitizeID keeps a tool id the Messages API accepts (letters, digits, _ and
// -), rewriting any other deterministically so a tool_use and its result stay
// paired (9router-go sanitizeToolUseID).
func sanitizeID(id string, mapping map[string]string) string {
	if id == "" {
		return id
	}

	if mapped, ok := mapping[id]; ok {
		return mapped
	}

	valid := true

	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			valid = false

			break
		}
	}

	if valid {
		mapping[id] = id

		return id
	}

	sum := sha256.Sum256([]byte(id))
	rewritten := "toolu_" + hex.EncodeToString(sum[:])[:24]
	mapping[id] = rewritten

	return rewritten
}

// stopSequences reads the chat `stop` field, a string or a list, as the
// Messages `stop_sequences` list.
func stopSequences(raw json.RawMessage) []string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}

	var many []string
	_ = json.Unmarshal(raw, &many)

	return many
}
