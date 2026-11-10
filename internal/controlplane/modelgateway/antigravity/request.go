// Package antigravity translates between OpenAI's chat completions format and
// the Gemini-shaped format of Google's Cloud Code backend
// (v1internal:streamGenerateContent), which serves an Antigravity account its
// Gemini, Claude and gpt-oss models, in both directions. It holds no network
// code (issue #20, step 4).
//
// Six implementations were read on 2026-11-10
// (docs/research/2026-11-10-antigravity-subscription-adapter.md). Where they
// differ this package does what most do and what Google's own Gemini CLI
// does: tools keep their names, nothing is added to pass for the IDE, no
// requestType is sent, and a tool call with no thought signature carries the
// sentinel Google's client writes. The code followed, with the commit read:
//
//   - 9router-go (MIT), 64c156d: internal/translator/gemini.go (chat to
//     Gemini and back), schema.go (the schema cleaner);
//   - CLIProxyAPI (MIT, Copyright (c) 2025 Luis Pater, Router-For.ME), c5251b5:
//     internal/translator/antigravity/openai/chat-completions (tool choice,
//     the usage counts) and internal/runtime/executor/antigravity_executor_request.go
//     (the envelope);
//   - Google's Gemini CLI, 1930f25: packages/core/src/code_assist/converter.ts
//     (the envelope) and utils/historyHardening.ts (the sentinel).
package antigravity

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	// SkipSignature is what a tool call carries in place of the thought
	// signature a chat client never sends back. Google's Gemini CLI writes
	// it on the first call of a model turn that has none.
	SkipSignature = "skip_thought_signature_validator"
	// maxNameBytes bounds a function's name at the backend.
	maxNameBytes = 64
	// maxOutputTokens is the most the backend takes for any model
	// (9router-go, hardenAntigravityRequest).
	maxOutputTokens = 64000
)

// Options are what the gateway knows beside the client's body.
type Options struct {
	// Model is the model's name at the backend.
	Model string
	// Project is the Cloud Code project of the account.
	Project string
	// RequestID names this request.
	RequestID string
}

// Request is a body for the backend, with what is needed to read its answer.
type Request struct {
	Body []byte
	// Names maps the function names in the body back to the client's.
	Names Names
}

// Names maps a function's name as sent to the backend to the client's.
type Names map[string]string

// Original returns the client's name for a function the backend named.
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
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type chatBody struct {
	Messages        []chatMessage   `json:"messages"`
	Tools           []chatTool      `json:"tools"`
	ToolChoice      json.RawMessage `json:"tool_choice"`
	ResponseFormat  json.RawMessage `json:"response_format"`
	ReasoningEffort string          `json:"reasoning_effort"`
	Temperature     *float64        `json:"temperature"`
	TopP            *float64        `json:"top_p"`
	MaxTokens       *int64          `json:"max_tokens"`
	MaxCompletion   *int64          `json:"max_completion_tokens"`
}

type object = map[string]any

// content is one turn of the conversation as the backend takes it.
type content struct {
	Role  string   `json:"role"`
	Parts []object `json:"parts"`
}

// BuildRequest turns a chat completions body into the envelope the backend
// takes.
func BuildRequest(chat []byte, opt Options) (Request, error) {
	var in chatBody

	decoder := json.NewDecoder(bytes.NewReader(chat))
	decoder.UseNumber()

	if err := decoder.Decode(&in); err != nil {
		return Request{}, fmt.Errorf("reading the chat request: %w", err)
	}

	if opt.Model == "" || opt.Project == "" {
		return Request{}, errors.New("the model's name at the backend or the account's project is missing")
	}

	names := sendNames(in)
	contents, system := convertMessages(in, names)

	request := object{"contents": contents, "sessionId": sessionID(in)}
	if len(system) > 0 {
		// The backend takes the instruction as a turn of the user's.
		request["systemInstruction"] = content{Role: "user", Parts: system}
	}

	choice := toolChoice(in.ToolChoice, names)

	if len(in.Tools) > 0 && choice["mode"] != "NONE" {
		declarations := make([]object, 0, len(in.Tools))

		for _, tool := range in.Tools {
			if tool.Function.Name == "" {
				continue
			}

			declaration := object{
				"name": names.sent(tool.Function.Name), "parameters": cleanSchema(tool.Function.Parameters),
			}
			if tool.Function.Description != "" {
				declaration["description"] = tool.Function.Description
			}

			declarations = append(declarations, declaration)
		}

		if len(declarations) > 0 {
			request["tools"] = []object{{"functionDeclarations": declarations}}
			request["toolConfig"] = object{"functionCallingConfig": choice}
		}
	}

	if config := generationConfig(in, opt.Model); len(config) > 0 {
		request["generationConfig"] = config
	}

	// requestType is left out on purpose: three references record that
	// sending "agent" draws 429s with quota left, and Google's client has no
	// such field.
	body, err := json.Marshal(object{
		"project": opt.Project, "model": opt.Model, "userAgent": "antigravity", "requestId": opt.RequestID,
		"request": request,
	})
	if err != nil {
		return Request{}, fmt.Errorf("encoding the request for the backend: %w", err)
	}

	return Request{Body: body, Names: names.reversed()}, nil
}

// convertMessages turns chat messages into the backend's turns. System and
// developer messages become the instruction; an assistant's tool call becomes
// a functionCall part and a tool's result a functionResponse part of a user
// turn, tied by the call's id and name.
func convertMessages(in chatBody, names sentNames) (contents []content, system []object) {
	// open are the calls no result has answered yet, oldest first.
	var open []openCall

	used := map[string]int{}

	for i, msg := range in.Messages {
		switch msg.Role {
		case "system", "developer":
			if text := allText(msg.Content); text != "" {
				system = append(system, object{"text": text})
			}
		case "user":
			contents = add(contents, "user", userParts(msg.Content)...)
		case "assistant":
			var parts []object
			if text := allText(msg.Content); text != "" {
				parts = append(parts, object{"text": text})
			}

			for j, call := range msg.ToolCalls {
				name := strings.TrimSpace(call.Function.Name)
				if name == "" {
					continue
				}

				id := uniqueID(call.ID, fmt.Sprintf("call_%d_%d", i, j), used)
				open = append(open, openCall{clientID: call.ID, id: id, name: name})

				part := object{"functionCall": object{
					"id": id, "name": names.sent(name), "args": arguments(call.Function.Arguments),
				}}
				if j == 0 {
					// The first call of a turn must carry a signature; the
					// backend takes this word for one it cannot check.
					part["thoughtSignature"] = SkipSignature
				}

				parts = append(parts, part)
			}

			contents = add(contents, "model", parts...)
		case "tool":
			// A result answers the call of its id, or the oldest open one
			// when it names none; it carries that call's id and name.
			answered := openCall{id: msg.ToolCallID, name: "tool"}

			for at, call := range open {
				if msg.ToolCallID == "" || call.clientID == msg.ToolCallID {
					answered, open = call, append(open[:at:at], open[at+1:]...)

					break
				}
			}

			contents = add(contents, "user", object{"functionResponse": object{
				"id": answered.id, "name": names.sent(answered.name), "response": object{"result": allText(msg.Content)},
			}})
		}
	}

	// The backend wants the user to speak first, and last.
	if len(contents) == 0 || contents[0].Role != "user" {
		contents = append([]content{{Role: "user", Parts: []object{{"text": "..."}}}}, contents...)
	}

	if contents[len(contents)-1].Role == "model" {
		contents = append(contents, content{Role: "user", Parts: []object{{"text": "Continue."}}})
	}

	return contents, system
}

// openCall is a tool call waiting for its result.
type openCall struct {
	// clientID is the id the client gave the call; id is the one sent.
	clientID string
	id       string
	name     string
}

// add appends parts to the conversation, into the last turn when it is of the
// same role: the backend refuses two turns of one role in a row.
func add(contents []content, role string, parts ...object) []content {
	if len(parts) == 0 {
		return contents
	}

	if last := len(contents) - 1; last >= 0 && contents[last].Role == role {
		contents[last].Parts = append(contents[last].Parts, parts...)

		return contents
	}

	return append(contents, content{Role: role, Parts: parts})
}

// uniqueID gives a call an id no other call of the conversation has: the
// backend refuses a repeated one.
func uniqueID(id, fallback string, used map[string]int) string {
	if id == "" {
		id = fallback
	}

	used[id]++
	if n := used[id]; n > 1 {
		return id + "-" + strconv.Itoa(n)
	}

	return id
}

// arguments are a call's arguments as an object; text that is not one is
// kept under a key, as CLIProxyAPI does.
func arguments(raw string) any {
	var parsed map[string]any

	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()

	if strings.TrimSpace(raw) == "" {
		return object{}
	}

	if decoder.Decode(&parsed) != nil || parsed == nil {
		return object{"params": raw}
	}

	return parsed
}

func contentParts(raw json.RawMessage) (text string, parts []map[string]json.RawMessage) {
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}

	_ = json.Unmarshal(raw, &parts)

	return "", parts
}

// allText is every text of a message, one part after another.
func allText(raw json.RawMessage) string {
	text, parts := contentParts(raw)
	if text != "" {
		return text
	}

	var all []string

	for _, part := range parts {
		var t string
		if json.Unmarshal(part["text"], &t) == nil && t != "" {
			all = append(all, t)
		}
	}

	return strings.Join(all, "\n")
}

// userParts are a user message's texts and the images it carries inline.
func userParts(raw json.RawMessage) []object {
	text, parts := contentParts(raw)
	if text != "" {
		return []object{{"text": text}}
	}

	var out []object

	for _, part := range parts {
		var t string
		if json.Unmarshal(part["text"], &t) == nil && t != "" {
			out = append(out, object{"text": t})
		}

		var image struct {
			URL string `json:"url"`
		}

		if json.Unmarshal(part["image_url"], &image) == nil {
			if mime, data, ok := dataURL(image.URL); ok {
				out = append(out, object{"inlineData": object{"mimeType": mime, "data": data}})
			}
		}
	}

	return out
}

// dataURL splits a base64 data URL into its type and its data.
func dataURL(url string) (mime, data string, ok bool) {
	rest, isData := strings.CutPrefix(url, "data:")
	if !isData {
		return "", "", false
	}

	head, data, found := strings.Cut(rest, ",")
	mime, isBase64 := strings.CutSuffix(head, ";base64")

	return mime, data, found && isBase64 && mime != "" && data != ""
}

// toolChoice is the backend's function calling mode. VALIDATED is what the
// IDE and every reference send when there are tools; a choice the client
// makes wins.
func toolChoice(raw json.RawMessage, names sentNames) object {
	var word string
	if json.Unmarshal(raw, &word) == nil {
		switch word {
		case "none":
			return object{"mode": "NONE"}
		case "required":
			return object{"mode": "ANY"}
		}
	}

	var named struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}

	if json.Unmarshal(raw, &named) == nil && named.Function.Name != "" {
		return object{"mode": "ANY", "allowedFunctionNames": []string{names.sent(named.Function.Name)}}
	}

	return object{"mode": "VALIDATED"}
}

// generationConfig carries what the backend takes of the client's settings.
func generationConfig(in chatBody, model string) object {
	config := object{}

	if in.Temperature != nil {
		config["temperature"] = *in.Temperature
	}

	if in.TopP != nil {
		config["topP"] = *in.TopP
	}

	limit := in.MaxCompletion
	if limit == nil {
		limit = in.MaxTokens
	}

	if limit != nil && *limit > 0 {
		config["maxOutputTokens"] = min(*limit, maxOutputTokens)
	}

	// A Gemini model reasons at a level; a Claude or gpt-oss model's effort
	// is part of its name at the backend (9router-go, thinking/gemini.go).
	if effort := thinkingLevel(in.ReasoningEffort); effort != "" && strings.HasPrefix(model, "gemini-") {
		config["thinkingConfig"] = object{"thinkingLevel": effort, "includeThoughts": true}
	}

	var format struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Schema map[string]any `json:"schema"`
		} `json:"json_schema"`
	}

	if json.Unmarshal(in.ResponseFormat, &format) == nil {
		switch format.Type {
		case "json_object":
			config["responseMimeType"] = "application/json"
		case "json_schema":
			config["responseMimeType"] = "application/json"
			if format.JSONSchema.Schema != nil {
				config["responseJsonSchema"] = format.JSONSchema.Schema
			}
		}
	}

	return config
}

func thinkingLevel(effort string) string {
	switch effort {
	case "minimal", "low":
		return "low"
	case "medium":
		return "medium"
	case "high", "xhigh", "max":
		return "high"
	default:
		return ""
	}
}

// sessionID names the conversation to the backend: a negative number made
// from its first user text, so every turn of one conversation has the same
// (CLIProxyAPI, omp).
func sessionID(in chatBody) string {
	first := ""

	for _, msg := range in.Messages {
		if msg.Role == "user" {
			first = allText(msg.Content)

			break
		}
	}

	sum := sha256.Sum256([]byte(first))

	return "-" + strconv.FormatUint(binary.BigEndian.Uint64(sum[:8])&0x7fffffffffffffff, 10)
}

// sentNames maps a client's function name to the one sent to the backend.
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

// sendNames gives every function name of a request one the backend takes,
// each different (CLIProxyAPI, SanitizedFunctionNameMap).
func sendNames(in chatBody) sentNames {
	var all []string

	for _, tool := range in.Tools {
		all = append(all, tool.Function.Name)
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

// fitName makes a name the backend takes: letters, digits, "_" and "-",
// starting with a letter or "_", at most 64 bytes.
func fitName(name string) string {
	fitted := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}

		return '_'
	}, name)

	if fitted == "" || (fitted[0] >= '0' && fitted[0] <= '9') || fitted[0] == '-' {
		fitted = "_" + fitted
	}

	return fitted[:min(len(fitted), maxNameBytes)]
}
