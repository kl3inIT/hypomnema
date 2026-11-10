// Package codex translates between OpenAI's chat completions format and the
// Responses format that the ChatGPT backend of Codex takes
// (chatgpt.com/backend-api/codex/responses), in both directions. It holds no
// network code: the gateway's adapter sends what this package builds and
// feeds it the events that come back (issue #20, step 3).
//
// Everything here is specific to that backend, and follows three sources read
// on 2026-11-10, with the commit read:
//
//   - 9router-go (MIT), commit 64c156d: internal/proxy/executor/transform.go
//     (chat to Responses), codex_token_params.go (the keys the backend takes),
//     codex_responses_lite.go (the shape of the gpt-6 models), stream.go
//     (Responses events to chat chunks);
//   - CLIProxyAPI (MIT, Copyright (c) 2025 Luis Pater, Router-For.ME), commit
//     c5251b5: internal/translator/codex/openai/chat-completions (tool names
//     that fit the backend, an answer that was cut off, a client that does
//     not stream);
//   - the Codex CLI, commit f5ac7a7: codex-rs/core/src/client.rs
//     (build_responses_request), the request the backend is made for.
package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	// maxNameBytes bounds a tool's name and a call's id at the backend.
	maxNameBytes = 64
	// liteDefaultEffort is the effort a lite model gets when the client
	// named none (9router-go, codex_responses_lite.go).
	liteDefaultEffort = "medium"
	// additionalTools is the input item that carries a lite model's tools.
	additionalTools = "additional_tools"
)

// liteModels take the "Responses Lite" shape: tools and instructions travel
// at the head of the input, and reasoning has no summary. The Codex CLI reads
// this from the backend's model list (use_responses_lite); 9router-go keeps
// the set in code, and so does this package until the list is fetched.
var liteModels = []string{"gpt-6.1-sol", "gpt-6-sol", "gpt-6-luna"}

// Lite reports whether a model takes the lite shape.
func Lite(model string) bool { return slices.Contains(liteModels, model) }

// unicodeProperty finds \p{…} in a regular expression: the backend refuses a
// schema whose pattern uses one (9router-go, transform.go).
var unicodeProperty = regexp.MustCompile(`(^|[^\\])(\\\\)*\\[pP]\{`)

// Options are what the gateway knows beside the client's body.
type Options struct {
	// Model is the model's name at the backend.
	Model string
	// CacheKey is the prompt cache key for a client that sent none. It is
	// also the session the request is sent under.
	CacheKey string
}

// Request is a body for the backend, with what is needed to read its answer.
type Request struct {
	Body []byte
	// Lite says the body has the lite shape, which the backend is told in a header.
	Lite bool
	// CacheKey is the prompt cache key in the body.
	CacheKey string
	// Names maps the tool names in the body back to the client's.
	Names Names
}

// Names maps a tool's name as sent to the backend to the name the client
// gave it.
type Names map[string]string

// Original returns the client's name for a tool the backend named.
func (n Names) Original(sent string) string {
	if original, ok := n[sent]; ok {
		return original
	}

	return sent
}

type chatMessage struct {
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

type chatTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      *bool           `json:"strict"`
	Function    *struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
		Strict      *bool           `json:"strict"`
	} `json:"function"`
}

type chatBody struct {
	Messages          []chatMessage   `json:"messages"`
	Instructions      string          `json:"instructions"`
	ReasoningEffort   string          `json:"reasoning_effort"`
	Reasoning         map[string]any  `json:"reasoning"`
	Tools             []chatTool      `json:"tools"`
	ToolChoice        json.RawMessage `json:"tool_choice"`
	ResponseFormat    json.RawMessage `json:"response_format"`
	ServiceTier       json.RawMessage `json:"service_tier"`
	PromptCacheKey    string          `json:"prompt_cache_key"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls"`
}

type item = map[string]any

// BuildRequest turns a chat completions body into the body the backend
// takes. Sampling settings, token limits, user, metadata and stream_options
// have no place there and are left out: the backend refuses a body that
// carries them.
func BuildRequest(chat []byte, opt Options) (Request, error) {
	var in chatBody

	decoder := json.NewDecoder(bytes.NewReader(chat))
	decoder.UseNumber()

	if err := decoder.Decode(&in); err != nil {
		return Request{}, fmt.Errorf("reading the chat request: %w", err)
	}

	if opt.Model == "" {
		return Request{}, errors.New("the model's name at the backend is missing")
	}

	names := sendNames(in)
	input, instructions := convertMessages(in, names)

	body := item{"model": opt.Model, "input": input, "stream": true, "store": false}
	if instructions != "" {
		body["instructions"] = instructions
	}

	if reasoning := reasoningOf(in); reasoning != nil {
		body["reasoning"] = reasoning
	}

	if format, ok := textFormat(in.ResponseFormat); ok {
		body["text"] = item{"format": format}
	}

	if tools := convertTools(in.Tools, names); len(tools) > 0 {
		body["tools"] = tools
	}

	if choice, ok := toolChoice(in.ToolChoice, names); ok {
		body["tool_choice"] = choice
	}

	if tier, ok := serviceTier(in.ServiceTier); ok {
		body["service_tier"] = tier
	}

	if in.ParallelToolCalls != nil {
		body["parallel_tool_calls"] = *in.ParallelToolCalls
	}

	cacheKey := in.PromptCacheKey
	if cacheKey == "" {
		cacheKey = opt.CacheKey
	}

	if cacheKey != "" {
		body["prompt_cache_key"] = cacheKey
	}

	lite := Lite(opt.Model)
	if lite {
		applyLite(body)
	}

	// The backend returns the reasoning it may be handed back only when
	// asked (9router-go, injectCodexReasoningInclude).
	if reasoning, ok := body["reasoning"].(item); ok {
		if effort, _ := reasoning["effort"].(string); effort != "" && effort != "none" {
			body["include"] = []string{"reasoning.encrypted_content"}
		}
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return Request{}, fmt.Errorf("encoding the request for the backend: %w", err)
	}

	return Request{Body: encoded, Lite: lite, CacheKey: cacheKey, Names: names.reversed()}, nil
}

// convertMessages turns chat messages into input items. System and developer
// messages become the instructions; a tool call and its result become items
// of their own, tied by a call id.
func convertMessages(in chatBody, names sentNames) (input []item, instructions string) {
	input = []item{}
	instructions = in.Instructions

	var pending []string

	for i, msg := range in.Messages {
		switch msg.Role {
		case "system", "developer":
			if text := firstText(msg.Content); text != "" {
				if instructions != "" {
					instructions += "\n\n"
				}

				instructions += text
			}
		case "user":
			input = append(input, item{"type": "message", "role": "user", "content": userContent(msg.Content)})
		case "assistant":
			if content := assistantContent(msg.Content); len(content) > 0 {
				input = append(input, item{"type": "message", "role": "assistant", "content": content})
			}

			for j, call := range msg.ToolCalls {
				name := strings.TrimSpace(call.Function.Name)
				if name == "" {
					continue // The backend refuses a call with no name.
				}

				id := call.ID
				if id == "" {
					id = fmt.Sprintf("call_%d_%d", i, j)
				}

				pending = append(pending, id)
				input = append(input, item{
					"type": "function_call", "call_id": clampID(id), "name": names.sent(name),
					"arguments": call.Function.Arguments,
				})
			}
		case "tool":
			id := msg.ToolCallID

			switch at := slices.Index(pending, id); {
			case id != "" && at >= 0:
				pending = slices.Delete(pending, at, at+1)
			case id == "" && len(pending) > 0:
				// A result with no id answers the oldest open call.
				id, pending = pending[0], pending[1:]
			case id == "":
				id = fmt.Sprintf("call_tool_%d", i)
			}

			input = append(input, item{
				"type": "function_call_output", "call_id": clampID(id), "output": allText(msg.Content),
			})
		}
	}

	return input, instructions
}

// contentParts reads a message's content as a string or as a list of parts.
func contentParts(raw json.RawMessage) (text string, parts []map[string]json.RawMessage) {
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}

	_ = json.Unmarshal(raw, &parts)

	return "", parts
}

func partText(part map[string]json.RawMessage) string {
	var text string

	_ = json.Unmarshal(part["text"], &text)

	return text
}

// firstText is a message's text: the string, or the first part that has some.
func firstText(raw json.RawMessage) string {
	text, parts := contentParts(raw)
	if text != "" {
		return text
	}

	for _, part := range parts {
		if t := partText(part); t != "" {
			return t
		}
	}

	return ""
}

// allText is every text of a message, one part after another: a tool's
// result may come in several parts.
func allText(raw json.RawMessage) string {
	text, parts := contentParts(raw)
	if text != "" {
		return text
	}

	var all []string

	for _, part := range parts {
		if t := partText(part); t != "" {
			all = append(all, t)
		}
	}

	return strings.Join(all, "\n")
}

// userContent is a user message's content: its texts and images. A message
// with neither still needs a part, which the backend demands.
func userContent(raw json.RawMessage) []item {
	text, parts := contentParts(raw)
	if text != "" {
		return []item{{"type": "input_text", "text": text}}
	}

	var content []item

	for _, part := range parts {
		if t := partText(part); t != "" {
			content = append(content, item{"type": "input_text", "text": t})
		}

		var image struct {
			URL string `json:"url"`
		}

		if json.Unmarshal(part["image_url"], &image) == nil && image.URL != "" {
			content = append(content, item{"type": "input_image", "image_url": image.URL})
		}
	}

	if len(content) == 0 {
		return []item{{"type": "input_text", "text": "..."}}
	}

	return content
}

func assistantContent(raw json.RawMessage) []item {
	text, parts := contentParts(raw)
	if text != "" {
		return []item{{"type": "output_text", "text": text}}
	}

	var content []item

	for _, part := range parts {
		if t := partText(part); t != "" {
			content = append(content, item{"type": "output_text", "text": t})
		}
	}

	return content
}

// reasoningOf is the reasoning block: the client's effort with a summary
// asked for, or the client's own block; none when it gave neither.
func reasoningOf(in chatBody) item {
	if in.ReasoningEffort == "" {
		if in.Reasoning == nil {
			return nil
		}

		return in.Reasoning
	}

	effort := in.ReasoningEffort
	if effort == "max" {
		effort = "xhigh"
	}

	reasoning := item{"summary": "auto"}
	for k, v := range in.Reasoning {
		reasoning[k] = v
	}

	reasoning["effort"] = effort

	return reasoning
}

// textFormat maps a chat response_format onto the backend's text.format.
func textFormat(raw json.RawMessage) (item, bool) {
	var format struct {
		Type       string `json:"type"`
		JSONSchema *struct {
			Name   string          `json:"name"`
			Strict *bool           `json:"strict"`
			Schema json.RawMessage `json:"schema"`
		} `json:"json_schema"`
	}

	if len(raw) == 0 || json.Unmarshal(raw, &format) != nil {
		return nil, false
	}

	switch {
	case format.Type == "json_object":
		return item{"type": "json_object"}, true
	case format.Type != "json_schema" || format.JSONSchema == nil || absent(format.JSONSchema.Schema):
		return nil, false
	}

	name := format.JSONSchema.Name
	if name == "" {
		name = "response"
	}

	// A schema with no word on strictness is strict, as chat completions has it.
	strict := format.JSONSchema.Strict == nil || *format.JSONSchema.Strict

	return item{"type": "json_schema", "name": name, "strict": strict, "schema": format.JSONSchema.Schema}, true
}

func absent(raw json.RawMessage) bool { return len(raw) == 0 || string(raw) == "null" }

// convertTools flattens chat tools into the backend's. A tool is not strict
// unless the client said so: the backend would otherwise make every optional
// parameter required.
func convertTools(tools []chatTool, names sentNames) []item {
	var out []item

	for _, tool := range tools {
		name, description, parameters, strict := tool.Name, tool.Description, tool.Parameters, tool.Strict

		if tool.Function != nil {
			name, description, parameters = tool.Function.Name, tool.Function.Description, tool.Function.Parameters
			if tool.Function.Strict != nil {
				strict = tool.Function.Strict
			}
		}

		if name == "" {
			continue
		}

		converted := item{
			"type": "function", "name": names.sent(name), "parameters": schema(parameters),
			"strict": strict != nil && *strict,
		}
		if description != "" {
			converted["description"] = description
		}

		out = append(out, converted)
	}

	return out
}

// schema is a tool's parameters as the backend takes them: an object schema,
// with no pattern that uses a Unicode property.
func schema(raw json.RawMessage) any {
	var parsed any

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	if absent(raw) || decoder.Decode(&parsed) != nil {
		return item{"type": "object", "properties": item{}}
	}

	object, ok := parsed.(map[string]any)
	if !ok {
		return item{"type": "object", "properties": item{}}
	}

	if object["type"] == nil {
		object["type"] = "object"
	}

	if object["properties"] == nil {
		object["properties"] = item{}
	}

	return stripPatterns(object)
}

func stripPatterns(node any) any {
	switch v := node.(type) {
	case map[string]any:
		for key, value := range v {
			if pattern, ok := value.(string); ok && key == "pattern" && unicodeProperty.MatchString(pattern) {
				delete(v, key)

				continue
			}

			v[key] = stripPatterns(value)
		}
	case []any:
		for i := range v {
			v[i] = stripPatterns(v[i])
		}
	}

	return node
}

// toolChoice carries the client's choice: a word as it is, a named function
// in the backend's flat form.
func toolChoice(raw json.RawMessage, names sentNames) (any, bool) {
	if absent(raw) {
		return nil, false
	}

	var word string
	if json.Unmarshal(raw, &word) == nil {
		return word, word != ""
	}

	var named struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}

	if json.Unmarshal(raw, &named) != nil {
		return nil, false
	}

	if name := cmpOr(named.Function.Name, named.Name); named.Type == "function" && name != "" {
		return item{"type": "function", "name": names.sent(name)}, true
	}

	var other any

	if json.Unmarshal(raw, &other) != nil {
		return nil, false
	}

	return other, true
}

// serviceTier keeps the one tier the backend knows: "fast" is its "priority".
func serviceTier(raw json.RawMessage) (string, bool) {
	var tier string

	if json.Unmarshal(raw, &tier) != nil {
		return "", false
	}

	if tier == "fast" || tier == "priority" {
		return "priority", true
	}

	return "", false
}

// applyLite rewrites a body into the lite shape: the tools and the
// instructions move to the head of the input as developer items, calls are
// made one at a time, and reasoning spans every turn with no summary.
func applyLite(body item) {
	input, _ := body["input"].([]item)

	var prefix []item

	if tools, ok := body["tools"].([]item); ok && len(tools) > 0 {
		prefix = append(prefix, item{"type": additionalTools, "role": "developer", "tools": tools})
	}

	if instructions, _ := body["instructions"].(string); strings.TrimSpace(instructions) != "" {
		prefix = append(prefix, item{
			"type": "message", "role": "developer",
			"content": []item{{"type": "input_text", "text": instructions}},
		})
	}

	body["input"] = append(prefix, input...)
	body["instructions"] = ""
	body["tools"] = nil
	body["parallel_tool_calls"] = false

	if choice, _ := body["tool_choice"].(string); choice == "" {
		body["tool_choice"] = "auto"
	}

	reasoning, _ := body["reasoning"].(item)
	if reasoning == nil {
		reasoning = item{}
	}

	if effort, _ := reasoning["effort"].(string); effort == "" {
		reasoning["effort"] = liteDefaultEffort
	}

	delete(reasoning, "summary")

	reasoning["context"] = "all_turns"
	body["reasoning"] = reasoning
}

// clampID keeps a call id within the backend's limit.
func clampID(id string) string {
	if len(id) > maxNameBytes {
		return id[:maxNameBytes]
	}

	return id
}

// sentNames maps a client's tool name to the one sent to the backend.
type sentNames map[string]string

func (n sentNames) sent(name string) string {
	if sent, ok := n[name]; ok {
		return sent
	}

	return fitName(name)
}

func (n sentNames) reversed() Names {
	out := Names{}

	for original, sent := range n {
		if original != sent {
			out[sent] = original
		}
	}

	return out
}

// sendNames gives every tool name of a request one the backend takes:
// letters, digits, "_" and "-", at most 64 bytes, each different
// (CLIProxyAPI, buildShortNameMap). Names come from the tools, the choice and
// the calls already made.
func sendNames(in chatBody) sentNames {
	var all []string

	for _, tool := range in.Tools {
		if tool.Function != nil {
			all = append(all, tool.Function.Name)
		} else {
			all = append(all, tool.Name)
		}
	}

	for _, msg := range in.Messages {
		for _, call := range msg.ToolCalls {
			all = append(all, strings.TrimSpace(call.Function.Name))
		}
	}

	names, used := sentNames{}, map[string]bool{}

	for _, name := range all {
		if _, done := names[name]; done || name == "" {
			continue
		}

		base := fitName(name)
		sent := base

		for i := 1; used[sent]; i++ {
			suffix := "_" + strconv.Itoa(i)
			sent = base[:min(len(base), maxNameBytes-len(suffix))] + suffix
		}

		names[name], used[sent] = sent, true
	}

	return names
}

// fitName replaces what the backend refuses in a name and shortens one that
// is too long, keeping an MCP tool's prefix and last segment.
func fitName(name string) string {
	fitted := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}

		return '_'
	}, name)

	if len(fitted) <= maxNameBytes {
		return fitted
	}

	if last := strings.LastIndex(fitted, "__"); strings.HasPrefix(fitted, "mcp__") && last > 0 {
		fitted = "mcp__" + fitted[last+2:]
	}

	return fitted[:min(len(fitted), maxNameBytes)]
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}

	return b
}
