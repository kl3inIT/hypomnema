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
	// Plain leaves the disguise and the gateway's cache breakpoints out: the
	// body is the translation alone, for a Messages API that is not a Claude
	// subscription (OpenCode Go, issue #854).
	Plain bool
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
	OutputConfig      item              `json:"output_config"`
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

	if len(system) > 0 {
		body["system"] = system
	}

	names := Names{}

	if tools := convertTools(in.Tools, names); len(tools) > 0 {
		body["tools"] = tools
	}

	if choice := convertToolChoice(in.ToolChoice, in.ParallelToolCalls, len(names) > 0); choice != nil {
		body["tool_choice"] = choice
	}

	applyThinking(body, opt.Model, in.ReasoningEffort, in.OutputConfig, in.Temperature != nil || in.TopP != nil)

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

	// A probe (Claude Code's own "quota"/"ping" check) stays minimal: no decoy
	// tools, no cache breakpoints, only enough to be accepted (CLIProxyAPI
	// isClaudeProbeOrHelperRequest).
	probe := isProbe(in)

	if opt.Plain {
		encoded, err := json.Marshal(body)
		if err != nil {
			return Request{}, fmt.Errorf("encoding the Messages request: %w", err)
		}

		return Request{Body: encoded, Names: names, Session: opt.Session}, nil
	}

	// The disguise: the client's tools become Claude Code's, then the billing
	// block and the fake user id go on, over the body as it then stands.
	if !probe {
		cloakTools(body, names)
	}

	caller := cloak(body, opt.Token, opt.Session)

	// The prompt-cache breakpoints go on last, over the final tools and system.
	if !probe {
		applyCache(body, caller)
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return Request{}, fmt.Errorf("encoding the Messages request: %w", err)
	}

	return Request{Body: encoded, Names: names, Session: opt.Session}, nil
}

// probeWords are the trivial one-word messages Claude Code sends to check a
// key or quota, which should not be dressed up (CLIProxyAPI line 484).
var probeWords = map[string]bool{"quota": true, "test": true, "ping": true, ".": true, "probe": true}

// isProbe reports whether a request is one of Claude Code's own tiny checks: a
// single user message of one trivial word, with no tools.
func isProbe(in chatBody) bool {
	if len(in.Tools) > 0 {
		return false
	}

	seen := 0

	var text string

	for _, m := range in.Messages {
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}

		if json.Unmarshal(m, &msg) != nil || msg.Role == "system" {
			continue
		}

		seen++

		if msg.Role == "user" {
			text = probeText(msg.Content)
		}
	}

	return seen == 1 && probeWords[strings.ToLower(strings.TrimSpace(text))]
}

// probeText reads a message's text, a string or the first text block.
func probeText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}

	for _, b := range blocksOf(contentBlocks(content)) {
		if block, ok := b.(item); ok && block["type"] == "text" {
			if t, ok := block["text"].(string); ok {
				return t
			}
		}
	}

	return ""
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

// convertMessages lifts the system and developer messages to top-level system
// blocks and turns the rest into Messages turns, merging turns of the same
// role, as 9router-go does (convertOpenAIMessagesToClaude). A developer message
// ranks with a system one in OpenAI's instruction hierarchy, and pi sends its
// system prompt as one to a model that reasons (issue #504; CLIProxyAPI
// claude_openai_request.go, the "system", "developer" case).
func convertMessages(raw []json.RawMessage) (system []any, messages []any) {
	ids := map[string]string{}

	var turns []item

	for _, m := range raw {
		var msg struct {
			Role         string          `json:"role"`
			Content      json.RawMessage `json:"content"`
			CacheControl json.RawMessage `json:"cache_control"`
			ToolCallID   string          `json:"tool_call_id"`
			ToolCalls    []struct {
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
		case msg.Role == "system" || msg.Role == "developer":
			system = append(system, systemBlocks(msg.Content, msg.CacheControl)...)
		case msg.Role == "tool":
			result := item{
				"type": "tool_result", "tool_use_id": sanitizeID(msg.ToolCallID, ids),
				"content": contentBlocks(msg.Content), "is_error": false,
			}
			hoistToolCache(result, msg.Content, msg.CacheControl)
			turns = append(turns, item{"role": "user", "content": []any{result}})
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

			attachMessageCache(blocks, msg.CacheControl)
			turns = append(turns, item{"role": "assistant", "content": blocks})
		default:
			role := msg.Role
			if role != "user" && role != "assistant" {
				role = "user"
			}

			blocks := contentBlocks(msg.Content)
			attachMessageCache(blocks, msg.CacheControl)
			turns = append(turns, item{"role": role, "content": blocks})
		}
	}

	return system, merge(turns)
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

// systemBlocks reads a system or developer message as system text blocks,
// each keeping its part's breakpoint, and the message's own breakpoint on its
// last block unless a part's is there (issue #504; CLIProxyAPI
// claude_openai_request.go, the system blocks and their cache_control). What is
// not text is left out, and a blank part is dropped as contentBlocks drops it.
func systemBlocks(content, cacheControl json.RawMessage) []any {
	var out []any

	for _, b := range contentBlocks(content) {
		if block, ok := b.(item); ok && block["type"] == "text" {
			if text, ok := block["text"].(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, block)
			}
		}
	}

	attachMessageCache(out, cacheControl)

	return out
}

// contentBlocks turns a message's content into Messages content blocks: a
// string is one text block; a list of OpenAI parts becomes text and image blocks.
// A text or image part keeps its own breakpoint; a blank text part is dropped,
// as Anthropic refuses an empty text block, and its breakpoint goes to the
// block before it (CLIProxyAPI convertOpenAIContentPartToClaudePart).
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
			Type         string          `json:"type"`
			Text         string          `json:"text"`
			ImageURL     json.RawMessage `json:"image_url"`
			CacheControl json.RawMessage `json:"cache_control"`
			// A Messages block a client sent through: kept as it is.
			Raw item
		}

		_ = json.Unmarshal(p, &part)
		_ = json.Unmarshal(p, &part.Raw)

		switch part.Type {
		case "text", "input_text":
			if strings.TrimSpace(part.Text) == "" {
				if len(out) > 0 {
					if prev, ok := out[len(out)-1].(item); ok {
						if _, has := prev["cache_control"]; !has {
							attachCache(prev, part.CacheControl)
						}
					}
				}

				continue
			}

			out = append(out, attachCache(item{"type": "text", "text": part.Text}, part.CacheControl))
		case "image_url":
			if block := imageBlock(part.ImageURL); block != nil {
				out = append(out, attachCache(block, part.CacheControl))
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
