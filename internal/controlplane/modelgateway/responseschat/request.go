// Package responseschat serves a Responses request from a model that speaks Chat Completions only (issue #1144):
// the Codex CLI speaks /v1/responses alone, and an OpenCode chat model or an OpenAI-compatible provider answers
// chat. The request becomes a chat completions request, and the chat stream becomes Responses events, as
// CLIProxyAPI's openai→openai-responses translator does (internal/translator/openai/openai/responses:
// openai_openai-responses_request.go ConvertOpenAIResponsesRequestToOpenAIChatCompletions,
// openai_openai-responses_response.go ConvertOpenAIChatCompletionsResponseToOpenAIResponses,
// openai_openai-responses_tools.go and responses_tool_index.go for the tools' names), adapted from its ojson
// documents to encoding/json. LiteLLM bridges its Responses API to chat the same way (litellm/responses/main.py).
//
// Adapted from CLIProxyAPI (https://github.com/router-for-me/CLIProxyAPI, 8522538), MIT License:
//
//	Copyright (c) 2025-2005.9 Luis Pater
//	Copyright (c) 2025.9-present Router-For.ME
//
//	Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated
//	documentation files (the "Software"), to deal in the Software without restriction, including without limitation
//	the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and
//	to permit persons to whom the Software is furnished to do so, subject to the following conditions:
//
//	The above copyright notice and this permission notice shall be included in all copies or substantial portions
//	of the Software.
//
//	THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO
//	THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
//	AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF
//	CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS
//	IN THE SOFTWARE.
//
// Left out of CLIProxyAPI's, as no Codex model the gateway serves on chat needs them yet: the local shell, tool
// search and apply_patch grammar bridges (shell_tool.go, openai_openai-responses_tool_search.go, apply-patch), audio
// and video parts, re-pairing tool outputs that name no call (NormalizeResponsesToolCallOutputs) and moving a tool's
// output next to its call (AlignOpenAIToolCallMessages). A Responses tool of another type (web_search,
// image_generation, a hosted MCP server) has no chat function and is not sent.
package responseschat

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type item = map[string]any

// reasoningUnavailable stands for reasoning the client did not keep: a chat provider that reasons (DeepSeek) refuses
// a tool call's turn without its reasoning_content (CLIProxyAPI's fallbackToolReasoning).
const reasoningUnavailable = "[reasoning unavailable]"

// toolOutputMediaNote is a tool message's text when its output was only media, sent in the next user message
// (CLIProxyAPI setFunctionCallOutputContentDoc, relayToolFiles).
const toolOutputMediaNote = "Attached media from tool output follows in the user message."

// Request is a chat completions body and the client's tools, by which its answer is read back.
type Request struct {
	Body  []byte
	Tools *Tools
}

type responsesIn struct {
	Model             string            `json:"model"`
	Instructions      *string           `json:"instructions"`
	Input             json.RawMessage   `json:"input"`
	Tools             []json.RawMessage `json:"tools"`
	ToolChoice        json.RawMessage   `json:"tool_choice"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls"`
	MaxOutputTokens   json.RawMessage   `json:"max_output_tokens"`
	Reasoning         json.RawMessage   `json:"reasoning"`
	ReasoningEffort   *string           `json:"reasoning_effort"`
	Text              *struct {
		Format json.RawMessage `json:"format"`
	} `json:"text"`
}

type inputItem struct {
	Type             string            `json:"type"`
	Role             string            `json:"role"`
	Content          json.RawMessage   `json:"content"`
	CallID           string            `json:"call_id"`
	Name             string            `json:"name"`
	Namespace        string            `json:"namespace"`
	Arguments        json.RawMessage   `json:"arguments"`
	Input            string            `json:"input"`
	Output           json.RawMessage   `json:"output"`
	ReasoningContent string            `json:"reasoning_content"`
	Tools            []json.RawMessage `json:"tools"`
	Summary          []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
}

type contentPart struct {
	Type     string          `json:"type"`
	Text     *string         `json:"text"`
	ImageURL json.RawMessage `json:"image_url"`
	Detail   json.RawMessage `json:"detail"`
	FileID   string          `json:"file_id"`
	FileData string          `json:"file_data"`
	Filename string          `json:"filename"`
}

// ChatFromResponses turns a Responses request into a streamed chat completions request under the client's own model
// name, which the chat route replaces with the provider's.
func ChatFromResponses(raw []byte) (Request, error) {
	var in responsesIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return Request{}, fmt.Errorf("reading the Responses request: %w", err)
	}

	var items []inputItem
	if len(in.Input) > 0 && in.Input[0] == '[' {
		if err := json.Unmarshal(in.Input, &items); err != nil {
			return Request{}, fmt.Errorf("reading the input: %w", err)
		}
	}

	tools := newTools(in.Tools, items)
	out := item{"model": in.Model, "stream": true}

	if in.Text != nil {
		if format := responseFormat(in.Text.Format); format != nil {
			out["response_format"] = format
		}
	}

	if len(in.MaxOutputTokens) > 0 && string(in.MaxOutputTokens) != "null" {
		out["max_tokens"] = in.MaxOutputTokens
	}

	b := &builder{tools: tools, awaiting: map[string]bool{}, mergeable: -1}
	if in.Instructions != nil {
		b.messages = append(b.messages, item{"role": "system", "content": *in.Instructions})
	}

	effort := effortOf(in)

	switch {
	case items != nil:
		b.reasoningInSession = effort != "" && effort != "none" && effort != "0" && effort != "false"
		for _, it := range items {
			if it.Type == "reasoning" || it.ReasoningContent != "" {
				b.reasoningInSession = true
			}
		}

		for _, it := range items {
			b.add(it)
		}

		b.flushCalls()
		b.flushMedia()
		b.pendingReasoningMessage()
	case len(in.Input) > 0 && in.Input[0] == '"':
		var text string
		if err := json.Unmarshal(in.Input, &text); err != nil {
			return Request{}, fmt.Errorf("reading the input: %w", err)
		}

		b.messages = append(b.messages, item{"role": "user", "content": text})
	}

	if b.err != nil {
		return Request{}, b.err
	}

	out["messages"] = b.messages

	if chat := tools.chatTools(); len(chat) > 0 {
		out["tools"] = chat

		if in.ParallelToolCalls != nil {
			out["parallel_tool_calls"] = *in.ParallelToolCalls
		}

		if len(in.ToolChoice) > 0 && string(in.ToolChoice) != "null" {
			out["tool_choice"] = tools.choice(in.ToolChoice)
		}
	}

	if effort != "" {
		out["reasoning_effort"] = effort
	}

	body, err := json.Marshal(out)
	if err != nil {
		return Request{}, fmt.Errorf("encoding the chat request: %w", err)
	}

	return Request{Body: body, Tools: tools}, nil
}

// effortOf is the reasoning effort the client asked for, lower-cased: reasoning.effort, else reasoning_effort.
func effortOf(in responsesIn) string {
	var reasoning struct {
		Effort *string `json:"effort"`
	}

	if len(in.Reasoning) > 0 && json.Unmarshal(in.Reasoning, &reasoning) == nil && reasoning.Effort != nil {
		return strings.ToLower(strings.TrimSpace(*reasoning.Effort))
	}

	if in.ReasoningEffort != nil {
		return strings.ToLower(strings.TrimSpace(*in.ReasoningEffort))
	}

	return ""
}

// builder turns the input's items into chat messages in order, as CLIProxyAPI's loop does: consecutive tool calls
// are one assistant message, a reasoning item is the next assistant message's reasoning_content, and a tool's
// media follows its outputs as a user message.
type builder struct {
	tools    *Tools
	messages []any

	calls     []any
	callIDs   []string
	awaiting  map[string]bool
	mergeable int

	pendingReasoning   string
	latestReasoning    string
	reasoningInSession bool

	media []any
	err   error
}

func (b *builder) add(it inputItem) {
	kind := it.Type
	if kind == "" && it.Role != "" {
		kind = "message"
	}

	if kind != "function_call" && kind != "custom_tool_call" {
		b.flushCalls()
	}

	if kind != "function_call_output" && kind != "custom_tool_call_output" {
		b.flushMedia()
	}

	switch kind {
	case "message":
		b.message(it)
	case "reasoning":
		text := summaryText(it)
		b.pendingReasoning = combineReasoning(b.pendingReasoning, text)
		b.noteReasoning(text)
	case "function_call", "custom_tool_call":
		b.pendingReasoning = combineReasoning(b.pendingReasoning, it.ReasoningContent)
		b.noteReasoning(it.ReasoningContent)

		args := stringOrRaw(it.Arguments)
		if kind == "custom_tool_call" {
			wrapped, _ := json.Marshal(item{"input": it.Input}) //nolint:errchkjson // A string always encodes.
			args = string(wrapped)
		}

		b.calls = append(b.calls, item{
			"id": it.CallID, "type": "function",
			"function": item{"name": b.tools.chatName(it.Namespace, it.Name), "arguments": args},
		})

		if id := strings.TrimSpace(it.CallID); id != "" {
			b.callIDs = append(b.callIDs, id)
		}
	case "function_call_output", "custom_tool_call_output":
		b.mergeable = -1
		b.output(it, kind == "custom_tool_call_output")
	default:
		b.mergeable = -1
	}
}

func (b *builder) noteReasoning(text string) {
	if usableReasoning(text) {
		b.latestReasoning = text
	}
}

// message is a message item: its role kept, developer included, as CLIProxyAPI keeps it.
func (b *builder) message(it inputItem) {
	b.mergeable = -1

	if it.Role != "assistant" {
		b.pendingReasoningMessage()
		b.latestReasoning = ""
	}

	msg := item{"role": it.Role}

	var text string
	switch {
	case len(it.Content) > 0 && it.Content[0] == '"':
		_ = json.Unmarshal(it.Content, &text)
		msg["content"] = text
	case len(it.Content) > 0 && it.Content[0] == '[':
		var parts []contentPart
		if err := json.Unmarshal(it.Content, &parts); err != nil {
			b.fail(fmt.Errorf("a message's content is a string or a list of parts: %w", err))

			return
		}

		msg["content"] = collapse(chatParts(parts))
	default:
		msg["content"] = []any{}
	}

	if it.Role == "assistant" {
		if reasoning := combineReasoning(b.takeReasoning(), it.ReasoningContent); reasoning != "" {
			msg["reasoning_content"] = reasoning
			b.noteReasoning(reasoning)
		}
	}

	b.messages = append(b.messages, msg)
	if it.Role == "assistant" {
		b.mergeable = len(b.messages) - 1
	}
}

// chatParts are a message's parts in chat's shape: text, an image as image_url, a file by id or inline bytes; a
// part of another kind is not sent.
func chatParts(parts []contentPart) []any {
	out := make([]any, 0, len(parts))

	for _, p := range parts {
		kind := p.Type
		if kind == "" {
			kind = "input_text"
		}

		switch kind {
		case "input_text", "output_text", "text":
			text := ""
			if p.Text != nil {
				text = *p.Text
			}

			out = append(out, item{"type": "text", "text": text})
		case "input_image":
			if image := imagePart(p.ImageURL, p.Detail); image != nil {
				out = append(out, image)
			}
		case "input_file":
			if file := filePart(p); file != nil {
				out = append(out, file)
			}
		}
	}

	return out
}

func imagePart(rawURL, rawDetail json.RawMessage) item {
	var url string
	if json.Unmarshal(rawURL, &url) != nil || strings.TrimSpace(url) == "" {
		return nil
	}

	image := item{"url": strings.TrimSpace(url)}
	if detail := chatDetail(rawDetail); detail != "" {
		image["detail"] = detail
	}

	return item{"type": "image_url", "image_url": image}
}

// chatDetail is an image's detail as chat takes it: Codex's original is chat's high (normalizeChatImageDetail).
func chatDetail(raw json.RawMessage) string {
	var detail string
	if json.Unmarshal(raw, &detail) != nil {
		return ""
	}

	switch d := strings.ToLower(strings.TrimSpace(detail)); d {
	case "auto", "low", "high":
		return d
	case "original":
		return "high"
	default:
		return ""
	}
}

// filePart is a file by its id or its inline bytes; chat has no field for a bare URL (responsesInputFileToChatPart).
func filePart(p contentPart) item {
	if p.FileID == "" && p.FileData == "" {
		return nil
	}

	file := item{}
	if p.FileID != "" {
		file["file_id"] = p.FileID
	}

	if p.FileData != "" {
		file["file_data"] = p.FileData
	}

	if p.Filename != "" {
		file["filename"] = p.Filename
	}

	return item{"type": "file", "file": file}
}

// collapse is text-only content as one string, which every chat provider takes, as the gateway's Messages
// translation sends it (claude/frommessages.go collapse, from 9router-go); content with media stays a list.
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

// output is a tool's output: a tool message for a call the conversation made, else the user's text, as an output
// no call asked for is not a tool message a chat provider takes.
func (b *builder) output(it inputItem, custom bool) {
	id := strings.TrimSpace(it.CallID)
	content, media := b.outputContent(it.Output, custom)

	if !b.awaiting[id] {
		if text, ok := content.(string); ok && strings.TrimSpace(text) == "" {
			return
		}

		if parts, ok := content.([]any); ok && len(parts) == 0 {
			return
		}

		b.messages = append(b.messages, item{"role": "user", "content": content})

		return
	}

	delete(b.awaiting, id)
	b.messages = append(b.messages, item{"role": "tool", "tool_call_id": id, "content": content})
	b.media = append(b.media, media...)

	if len(b.awaiting) == 0 {
		b.flushMedia()
	}
}

// outputContent is a tool output's text for the tool message and its images and files for the user message after
// it: a string as it is, a list of parts as their text (setFunctionCallOutputContentDoc and responsesToolOutputText).
func (b *builder) outputContent(raw json.RawMessage, custom bool) (any, []any) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}

	// A string that holds a list of parts is read as one, unless it has no media: then it is the text as sent.
	var text string
	fromString := json.Unmarshal(raw, &text) == nil
	if fromString {
		if custom || !strings.HasPrefix(strings.TrimSpace(text), "[") || !json.Valid([]byte(text)) {
			return text, nil
		}

		raw = json.RawMessage(text)
	}

	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return string(raw), nil
	}

	var (
		texts []string
		media []any
	)

	for _, rawPart := range parts {
		var s string
		if json.Unmarshal(rawPart, &s) == nil {
			texts = append(texts, s)

			continue
		}

		var p contentPart
		if json.Unmarshal(rawPart, &p) != nil {
			texts = append(texts, string(rawPart))

			continue
		}

		switch p.Type {
		case "input_text", "output_text", "text":
			if p.Text != nil {
				texts = append(texts, *p.Text)
			}
		case "input_image":
			if image := imagePart(p.ImageURL, p.Detail); image != nil {
				media = append(media, image)
			}
		case "input_file":
			if file := filePart(p); file != nil {
				media = append(media, file)
			}
		case "input_audio", "audio", "input_video", "video":
			b.fail(fmt.Errorf("a tool's output of type %s cannot be sent to a chat model", p.Type))
		default:
			texts = append(texts, string(rawPart))
		}
	}

	switch {
	case len(media) == 0 && fromString:
		return text, nil
	case len(texts) == 0 && len(media) > 0:
		return toolOutputMediaNote, media
	}

	return strings.Join(texts, "\n"), media
}

func (b *builder) fail(err error) {
	if b.err == nil {
		b.err = err
	}
}

// flushCalls sends the tool calls gathered so far as one assistant message, joined to the assistant message just
// before them when it has none of its own; a reasoning model's turn keeps its reasoning (CLIProxyAPI
// flushPendingToolCalls).
func (b *builder) flushCalls() {
	if len(b.calls) == 0 {
		return
	}

	reasoning := b.takeReasoning()
	if reasoning == "" {
		reasoning = b.fallbackReasoning()
	}

	merged := false
	if b.mergeable >= 0 && b.mergeable == len(b.messages)-1 {
		if prior, _ := b.messages[b.mergeable].(item); prior["role"] == "assistant" && prior["tool_calls"] == nil {
			prior["tool_calls"] = b.calls
			had, _ := prior["reasoning_content"].(string)
			if combined := combineReasoning(had, reasoning); combined != "" {
				prior["reasoning_content"] = combined
				b.noteReasoning(combined)
			}

			merged = true
		}
	}

	if !merged {
		msg := item{"role": "assistant", "tool_calls": b.calls}
		if reasoning != "" {
			msg["reasoning_content"] = reasoning
			b.noteReasoning(reasoning)
		}

		b.messages = append(b.messages, msg)
	}

	for _, id := range b.callIDs {
		b.awaiting[id] = true
	}

	b.calls, b.callIDs, b.mergeable = nil, nil, -1
}

func (b *builder) fallbackReasoning() string {
	if b.latestReasoning != "" {
		return b.latestReasoning
	}

	if b.reasoningInSession {
		return reasoningUnavailable
	}

	return ""
}

func (b *builder) takeReasoning() string {
	r := b.pendingReasoning
	b.pendingReasoning = ""

	return r
}

// pendingReasoningMessage sends reasoning no assistant message took as an assistant message of its own.
func (b *builder) pendingReasoningMessage() {
	reasoning := b.takeReasoning()
	if reasoning == "" {
		return
	}

	b.noteReasoning(reasoning)
	b.messages = append(b.messages, item{"role": "assistant", "content": "", "reasoning_content": reasoning})
}

func (b *builder) flushMedia() {
	if len(b.media) == 0 {
		return
	}

	b.messages = append(b.messages, item{"role": "user", "content": b.media})
	b.media = nil
}

// summaryText is a reasoning item's summary, or reasoningUnavailable when it has none
// (collectOpenAIResponsesReasoningContent).
func summaryText(it inputItem) string {
	var text strings.Builder

	for _, s := range it.Summary {
		if s.Type == "summary_text" {
			text.WriteString(s.Text)
		}
	}

	if text.Len() == 0 {
		return reasoningUnavailable
	}

	return text.String()
}

func combineReasoning(existing, incoming string) string {
	e, in := strings.TrimSpace(existing), strings.TrimSpace(incoming)

	switch {
	case e == "":
		return incoming
	case in == "":
		return existing
	case e == reasoningUnavailable:
		return incoming
	case in == reasoningUnavailable, e == in:
		return existing
	default:
		return existing + "\n\n" + incoming
	}
}

func usableReasoning(r string) bool {
	t := strings.TrimSpace(r)

	return t != "" && t != reasoningUnavailable
}

// stringOrRaw is a JSON string's value, else the raw JSON.
func stringOrRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	return string(raw)
}

// responseFormat is text.format in chat's response_format (convertResponsesTextFormatToChatResponseFormat).
func responseFormat(raw json.RawMessage) item {
	var format struct {
		Type        string          `json:"type"`
		Name        json.RawMessage `json:"name"`
		Description json.RawMessage `json:"description"`
		Strict      json.RawMessage `json:"strict"`
		Schema      json.RawMessage `json:"schema"`
	}

	if json.Unmarshal(raw, &format) != nil {
		return nil
	}

	switch format.Type {
	case "text", "json_object":
		return item{"type": format.Type}
	case "json_schema":
		schema := item{}
		for key, v := range map[string]json.RawMessage{
			"name": format.Name, "description": format.Description, "strict": format.Strict, "schema": format.Schema,
		} {
			if len(v) > 0 {
				schema[key] = v
			}
		}

		return item{"type": "json_schema", "json_schema": schema}
	default:
		return nil
	}
}

// Tools are the client's tools as chat functions: a function as it is, a freeform tool as a function of one input
// string, a namespace's tools under "<namespace>__<name>", every name at most 64 bytes, its tail kept
// (responsesToolIndex, walkResponsesToolDeclarations).
type Tools struct {
	declared   []declaration
	byChat     map[string]declaration
	byIdentity map[identity]string
	byLocal    map[string]string // "" when two tools have the local name
}

type identity struct{ namespace, name string }

type declaration struct {
	raw       toolIn
	chatName  string
	name      string
	namespace string
	custom    bool
}

type toolIn struct {
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Parameters  json.RawMessage   `json:"parameters"`
	Tools       []json.RawMessage `json:"tools"`
	Function    *struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// chatNameLimit is the function name limit strict chat providers keep (responsesChatToolNameLimit).
const chatNameLimit = 64

func newTools(top []json.RawMessage, items []inputItem) *Tools {
	t := &Tools{byChat: map[string]declaration{}, byIdentity: map[identity]string{}, byLocal: map[string]string{}}

	scan := func(list []json.RawMessage) {
		for _, raw := range list {
			var tool toolIn
			if json.Unmarshal(raw, &tool) != nil {
				continue
			}

			if strings.TrimSpace(tool.Type) != "namespace" {
				t.declare(tool, "")

				continue
			}

			namespace := strings.TrimSpace(tool.Name)
			for _, childRaw := range tool.Tools {
				var child toolIn
				if json.Unmarshal(childRaw, &child) == nil {
					t.declare(child, namespace)
				}
			}
		}
	}

	scan(top)

	// Codex's lite shape sends its tools as an input item (Responses Lite's additional_tools).
	for _, it := range items {
		if it.Type == "additional_tools" {
			scan(it.Tools)
		}
	}

	return t
}

func (t *Tools) declare(tool toolIn, namespace string) {
	custom := false

	switch strings.TrimSpace(tool.Type) {
	case "", "function":
	case "custom":
		custom = true
	default:
		return
	}

	name := strings.TrimSpace(tool.Name)
	if name == "" && tool.Function != nil {
		name = strings.TrimSpace(tool.Function.Name)
	}

	if name == "" {
		return
	}

	id := identity{namespace, name}
	if _, seen := t.byIdentity[id]; seen {
		// The same tool sent twice, in tools and in additional_tools: the first wins.
		return
	}

	chatName := t.free(capName(qualify(namespace, name)))
	d := declaration{raw: tool, chatName: chatName, name: name, namespace: namespace, custom: custom}

	t.declared = append(t.declared, d)
	t.byChat[chatName] = d
	t.byIdentity[id] = chatName

	if _, seen := t.byLocal[name]; seen {
		t.byLocal[name] = ""
	} else {
		t.byLocal[name] = chatName
	}
}

// free is a chat name no other tool has: a suffix for one that collides only once cut to the limit
// (disambiguateResponsesChatToolNames).
func (t *Tools) free(name string) string {
	if _, taken := t.byChat[name]; !taken {
		return name
	}

	for suffix := 1; ; suffix++ {
		candidate := capName(name + "_" + strconv.Itoa(suffix))
		if _, taken := t.byChat[candidate]; !taken {
			return candidate
		}
	}
}

// qualify is a namespace's tool's name in chat (rawResponsesNamespaceQualifiedName).
func qualify(namespace, name string) string {
	switch {
	case namespace == "" || strings.HasPrefix(name, "mcp__"):
		return name
	case name == namespace || strings.HasPrefix(name, namespace+"__"):
		return name
	case strings.HasSuffix(namespace, "__"):
		return namespace + name
	default:
		return namespace + "__" + name
	}
}

// capName keeps a name's last 64 bytes, without leading separators (capResponsesChatToolName).
func capName(name string) string {
	if len(name) <= chatNameLimit {
		return name
	}

	cut := name[len(name)-chatNameLimit:]
	if trimmed := strings.TrimLeft(cut, "_-"); trimmed != "" {
		return trimmed
	}

	return cut
}

// chatName is the chat name of a call to a tool by its namespace and name, as the request's tools name it.
func (t *Tools) chatName(namespace, name string) string {
	namespace, name = strings.TrimSpace(namespace), strings.TrimSpace(name)
	if namespace != "" {
		if chatName, ok := t.byIdentity[identity{namespace, name}]; ok {
			return chatName
		}

		return capName(qualify(namespace, name))
	}

	if _, ok := t.byChat[name]; ok {
		return name
	}

	if chatName := t.byLocal[name]; chatName != "" {
		return chatName
	}

	return capName(name)
}

// chatTools are the tools in chat's shape, in the request's order.
func (t *Tools) chatTools() []any {
	out := make([]any, 0, len(t.declared))

	for _, d := range t.declared {
		description, parameters := d.raw.Description, d.raw.Parameters
		if d.raw.Function != nil {
			if description == "" {
				description = d.raw.Function.Description
			}

			if len(parameters) == 0 {
				parameters = d.raw.Function.Parameters
			}
		}

		function := item{"name": d.chatName}
		if description != "" {
			function["description"] = description
		}

		switch {
		case d.custom:
			// A freeform tool is a function of one input string (convertResponsesCustomToolToOpenAIChat).
			function["parameters"] = item{
				"type": "object", "properties": item{"input": item{"type": "string"}}, "required": []string{"input"},
			}
		case len(parameters) > 0:
			function["parameters"] = parameters
		default:
			function["parameters"] = item{}
		}

		out = append(out, item{"type": "function", "function": function})
	}

	return out
}

// choice is tool_choice in chat's shape: a named function or freeform tool as chat names it, anything else as it
// is (convertResponsesToolChoiceWithIndex).
func (t *Tools) choice(raw json.RawMessage) any {
	var choice struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Function  *struct {
			Name string `json:"name"`
		} `json:"function"`
		Custom *struct {
			Name string `json:"name"`
		} `json:"custom"`
	}

	if json.Unmarshal(raw, &choice) != nil || (choice.Type != "function" && choice.Type != "custom") {
		return raw
	}

	name := choice.Name
	if choice.Function != nil && choice.Function.Name != "" {
		name = choice.Function.Name
	} else if choice.Custom != nil && choice.Custom.Name != "" {
		name = choice.Custom.Name
	}

	if name == "" {
		return raw
	}

	return item{"type": "function", "function": item{"name": t.chatName(choice.Namespace, name)}}
}

// identityOf is the client's name, namespace and freeform-ness of a tool chat called by its chat name.
func (t *Tools) identityOf(chatName string) (name, namespace string, custom bool) {
	if t == nil {
		return chatName, "", false
	}

	if d, ok := t.byChat[strings.TrimSpace(chatName)]; ok {
		return d.name, d.namespace, d.custom
	}

	return chatName, "", false
}
