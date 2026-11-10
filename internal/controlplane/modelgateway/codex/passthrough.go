package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// A Responses request a client already speaks (the Codex CLI) on its way to
// the backend. The body is the client's, with what the backend refuses taken
// out, as 9router-go does it (internal/proxy/executor: transform.go
// buildResponsesBody, codex_token_params.go stripCodexUnsupportedTokenParams,
// codex_responses_lite.go, providers.go injectCodexPromptCacheKey). Nothing is
// translated: the backend's answer goes back to the client as it comes.

// passKept are the only fields the backend takes (9router-go's allowlist).
var passKept = map[string]bool{
	"model": true, "input": true, "instructions": true, "tools": true, "tool_choice": true, "stream": true,
	"store": true, "reasoning": true, "service_tier": true, "include": true, "prompt_cache_key": true,
	"client_metadata": true, "text": true, "parallel_tool_calls": true,
}

// storedItemID is the id of an item the backend stored for an earlier answer.
// With store off it kept none, and refuses a request that names one.
var storedItemID = regexp.MustCompile(`^(rs|fc|resp|msg)_`)

// PassOptions are what the gateway knows of a passed-on request.
type PassOptions struct {
	// Model is the model's name at the backend.
	Model string
	// Session is the client's session, which keeps its prompt cache; it is
	// the prompt cache key when the body names none.
	Session string
}

// PassThrough prepares a client's Responses body for the backend.
func PassThrough(raw []byte, opt PassOptions) (Request, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var body item
	if err := decoder.Decode(&body); err != nil {
		return Request{}, fmt.Errorf("reading the request: %w", err)
	}

	if opt.Model == "" {
		return Request{}, errors.New("the model's name at the backend is missing")
	}

	body["model"] = opt.Model
	body["stream"] = true
	body["store"] = false
	body["input"] = passInput(body["input"])

	if tools, ok := body["tools"].([]any); ok {
		for _, tool := range tools {
			if t, ok := tool.(item); ok && t["parameters"] != nil {
				t["parameters"] = stripPatterns(t["parameters"])
			}
		}
	}

	lite := Lite(opt.Model) && passLite(body)

	cacheKey, _ := body["prompt_cache_key"].(string)
	if cacheKey == "" && opt.Session != "" {
		cacheKey = opt.Session
		body["prompt_cache_key"] = cacheKey
	}

	switch tier, _ := body["service_tier"].(string); tier {
	case "fast":
		body["service_tier"] = "priority"
	default:
		delete(body, "service_tier")
	}

	// The backend returns the reasoning it may be handed back only when asked.
	if _, asked := body["include"]; !asked {
		if reasoning, ok := body["reasoning"].(item); ok {
			if effort, _ := reasoning["effort"].(string); effort != "" && effort != "none" {
				body["include"] = []string{"reasoning.encrypted_content"}
			}
		}
	}

	for field := range body {
		if !passKept[field] {
			delete(body, field)
		}
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return Request{}, fmt.Errorf("encoding the request for the backend: %w", err)
	}

	return Request{Body: encoded, Lite: lite, CacheKey: cacheKey}, nil
}

// passInput makes the input a list of items the backend takes: a string
// becomes a user message, a reference to a stored item is dropped, an item
// loses the id of a stored one, and a call id is kept within the limit.
func passInput(input any) []any {
	items, ok := input.([]any)
	if !ok {
		text, _ := input.(string)
		if text == "" {
			text = "..."
		}

		return []any{item{
			"type": "message", "role": "user", "content": []any{item{"type": "input_text", "text": text}},
		}}
	}

	out := make([]any, 0, len(items))

	for _, entry := range items {
		it, ok := entry.(item)
		if !ok {
			out = append(out, entry)

			continue
		}

		if kind, _ := it["type"].(string); kind == "item_reference" {
			continue
		}

		if id, _ := it["id"].(string); storedItemID.MatchString(id) {
			delete(it, "id")
		}

		if id, _ := it["call_id"].(string); id != "" {
			it["call_id"] = clampID(id)
		}

		out = append(out, it)
	}

	return out
}

// passLite gives a body the lite shape and reports whether it has it. A body
// that carries the tools' item already is left as it is, and one that asks
// for the backend's own web search keeps the regular shape: lite cannot run it.
func passLite(body item) bool {
	input, _ := body["input"].([]any)

	for _, entry := range input {
		if it, ok := entry.(item); ok && it["type"] == additionalTools {
			passLiteReasoning(body)

			return true
		}
	}

	tools, _ := body["tools"].([]any)
	if tools == nil {
		tools = []any{}
	}

	for _, tool := range tools {
		if t, ok := tool.(item); ok {
			if kind, _ := t["type"].(string); strings.HasPrefix(kind, "web_search") {
				return false
			}
		}
	}

	prefix := []any{item{"type": additionalTools, "role": "developer", "tools": tools}}

	if instructions, _ := body["instructions"].(string); strings.TrimSpace(instructions) != "" {
		prefix = append(prefix, item{
			"type": "message", "role": "developer",
			"content": []any{item{"type": "input_text", "text": instructions}},
		})
	}

	body["input"] = append(prefix, input...)
	body["instructions"] = ""
	body["tools"] = nil
	body["parallel_tool_calls"] = false

	if choice, _ := body["tool_choice"].(string); choice == "" {
		body["tool_choice"] = "auto"
	}

	passLiteReasoning(body)

	return true
}

func passLiteReasoning(body item) {
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
