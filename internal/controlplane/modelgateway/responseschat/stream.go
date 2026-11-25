package responseschat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Stream turns one chat completions stream into the Responses events the Codex CLI reads, chunk by chunk, as
// CLIProxyAPI's ConvertOpenAIChatCompletionsResponseToOpenAIResponses does: response.created and in_progress
// first; reasoning as a reasoning item's summary; text as a message item; each tool call as a function_call, or a
// custom_tool_call for a freeform tool; the items closed when the choice finishes, and response.completed (or
// response.incomplete for an answer cut at its limit) after the provider's [DONE] or the stream's end, so a late
// usage-only chunk is still counted. A chat error frame becomes response.failed.
type Stream struct {
	id      string
	created int64
	model   string
	tools   *Tools

	seq     int
	started bool
	ended   bool
	next    int

	msg       *textItem
	segments  int
	reasoning *textItem
	thoughts  int

	calls map[string]*call
	done  []doneItem

	finish string
	usage  *usage
}

type textItem struct {
	id    string
	index int
	text  strings.Builder
}

type call struct {
	key, id, name string
	args          strings.Builder
	sent          int
	index         int
	added, done   bool
	custom        bool
}

type doneItem struct {
	index int
	item  item
}

type usage struct {
	input, cached, output, reasoning, total int64
}

// NewStream starts the translation of one answer: id and created name the response, model is the client's name for
// the model and tools the request's (Request.Tools).
func NewStream(id string, created int64, model string, tools *Tools) *Stream {
	return &Stream{id: id, created: created, model: model, tools: tools, calls: map[string]*call{}}
}

// Ended says the answer's last event was sent: response.completed, response.incomplete or response.failed.
func (s *Stream) Ended() bool { return s.ended }

type chunkIn struct {
	Object  string          `json:"object"`
	Error   json.RawMessage `json:"error"`
	Choices []choiceIn      `json:"choices"`
	Usage   *usageIn        `json:"usage"`
}

type choiceIn struct {
	Index        int             `json:"index"`
	Delta        *deltaIn        `json:"delta"`
	Message      *deltaIn        `json:"message"`
	FinishReason json.RawMessage `json:"finish_reason"`
}

type deltaIn struct {
	Content          json.RawMessage `json:"content"`
	ReasoningContent json.RawMessage `json:"reasoning_content"`
	Reasoning        json.RawMessage `json:"reasoning"`
	ReasoningDetails json.RawMessage `json:"reasoning_details"`
	ToolCalls        []toolCallIn    `json:"tool_calls"`
}

type toolCallIn struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type usageIn struct {
	PromptTokens        *int64 `json:"prompt_tokens"`
	CompletionTokens    *int64 `json:"completion_tokens"`
	OutputTokens        *int64 `json:"output_tokens"`
	TotalTokens         *int64 `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens *int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens *int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens *int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

// Chunk translates the data of one chat stream event and returns the Responses events it makes, framed.
func (s *Stream) Chunk(data []byte) []byte {
	data = bytes.TrimSpace(data)
	if s.ended || len(data) == 0 {
		return nil
	}

	var out bytes.Buffer

	if bytes.Equal(data, []byte("[DONE]")) {
		if s.started {
			s.finishItems(&out)
			s.complete(&out)
		}

		return out.Bytes()
	}

	var in chunkIn
	if json.Unmarshal(data, &in) != nil {
		return nil
	}

	if len(in.Error) > 0 && string(in.Error) != "null" && len(in.Choices) == 0 {
		s.fail(&out, in.Error)

		return out.Bytes()
	}

	if in.Object != "" && in.Object != "chat.completion.chunk" && in.Object != "chat.completion" {
		return nil
	}

	if in.Choices == nil {
		return nil
	}

	s.readUsage(in.Usage)
	s.start(&out)

	for _, c := range in.Choices {
		s.choice(&out, c)
	}

	return out.Bytes()
}

// Close ends a stream the provider closed without [DONE]: an answer whose choice finished is completed; one cut
// before that is left without its end, which the client and the gateway's record read as cut.
func (s *Stream) Close() []byte {
	if s.ended || !s.started || s.finish == "" {
		return nil
	}

	var out bytes.Buffer
	s.finishItems(&out)
	s.complete(&out)

	return out.Bytes()
}

// Completion translates a whole chat completion, the answer of a provider that did not stream, as one chunk of its
// message and the stream's end.
func (s *Stream) Completion(body []byte) []byte {
	var in chunkIn
	if err := json.Unmarshal(body, &in); err != nil {
		return nil
	}

	if len(in.Error) > 0 && string(in.Error) != "null" && len(in.Choices) == 0 {
		var out bytes.Buffer
		s.fail(&out, in.Error)

		return out.Bytes()
	}

	for i := range in.Choices {
		c := &in.Choices[i]
		if c.Delta == nil {
			c.Delta = c.Message
		}

		if c.Delta != nil {
			for j := range c.Delta.ToolCalls {
				if c.Delta.ToolCalls[j].Index == nil {
					c.Delta.ToolCalls[j].Index = &j
				}
			}
		}
	}

	in.Object = ""

	chunk, err := json.Marshal(in)
	if err != nil {
		return nil
	}

	return append(s.Chunk(chunk), s.Chunk([]byte("[DONE]"))...)
}

func (s *Stream) readUsage(u *usageIn) {
	if u == nil {
		return
	}

	if s.usage == nil {
		s.usage = &usage{}
	}

	set := func(dst *int64, values ...*int64) {
		for _, v := range values {
			if v != nil {
				*dst = *v

				return
			}
		}
	}

	set(&s.usage.input, u.PromptTokens)
	set(&s.usage.output, u.CompletionTokens, u.OutputTokens)
	set(&s.usage.total, u.TotalTokens)

	if u.PromptTokensDetails != nil {
		set(&s.usage.cached, u.PromptTokensDetails.CachedTokens)
	}

	if u.OutputTokensDetails != nil {
		set(&s.usage.reasoning, u.OutputTokensDetails.ReasoningTokens)
	}

	if u.CompletionTokensDetails != nil && u.CompletionTokensDetails.ReasoningTokens != nil {
		set(&s.usage.reasoning, u.CompletionTokensDetails.ReasoningTokens)
	}
}

func (s *Stream) start(out *bytes.Buffer) {
	if s.started {
		return
	}

	s.started = true

	for _, kind := range []string{"response.created", "response.in_progress"} {
		s.emit(out, kind, item{"response": item{
			"id": s.id, "object": "response", "created_at": s.created, "status": "in_progress",
			"model": s.model, "output": []any{},
		}})
	}
}

func (s *Stream) choice(out *bytes.Buffer, c choiceIn) {
	if d := c.Delta; d != nil {
		for _, text := range reasoningTexts(d) {
			s.thought(out, c.Index, text)
		}

		forEachSegment(d.Content, func(reasoning bool, text string) {
			if reasoning {
				s.thought(out, c.Index, text)
			} else {
				s.text(out, c.Index, text)
			}
		})

		if len(d.ToolCalls) > 0 {
			s.stopReasoning(out)
			s.closeMessage(out)

			for _, tc := range d.ToolCalls {
				s.toolCall(out, c.Index, tc)
			}
		}
	}

	var finish string
	if json.Unmarshal(c.FinishReason, &finish) == nil && finish != "" {
		s.finish = finish
		s.finishItems(out)
	}
}

// thought is reasoning text: it closes an open message and opens a reasoning item on its first text.
func (s *Stream) thought(out *bytes.Buffer, choice int, text string) {
	if text == "" {
		return
	}

	s.closeMessage(out)

	if s.reasoning == nil {
		id := fmt.Sprintf("rs_%s_%d", s.id, choice)
		if s.thoughts > 0 {
			id = fmt.Sprintf("rs_%s_%d_%d", s.id, choice, s.thoughts)
		}

		s.thoughts++
		s.reasoning = &textItem{id: id, index: s.alloc()}

		s.emit(out, "response.output_item.added", item{"output_index": s.reasoning.index, "item": item{
			"id": id, "type": "reasoning", "status": "in_progress", "summary": []any{},
		}})
		s.emit(out, "response.reasoning_summary_part.added", item{
			"item_id": id, "output_index": s.reasoning.index, "summary_index": 0,
			"part": item{"type": "summary_text", "text": ""},
		})
	}

	s.reasoning.text.WriteString(text)
	s.emit(out, "response.reasoning_summary_text.delta", item{
		"item_id": s.reasoning.id, "output_index": s.reasoning.index, "summary_index": 0, "delta": text,
	})
}

func (s *Stream) stopReasoning(out *bytes.Buffer) {
	r := s.reasoning
	if r == nil {
		return
	}

	s.reasoning = nil
	text := r.text.String()

	s.emit(out, "response.reasoning_summary_text.done", item{
		"item_id": r.id, "output_index": r.index, "summary_index": 0, "text": text,
	})
	s.emit(out, "response.reasoning_summary_part.done", item{
		"item_id": r.id, "output_index": r.index, "summary_index": 0,
		"part": item{"type": "summary_text", "text": text},
	})

	// No encrypted_content: there is none to give, and an empty one would be sent back to a model that reads it.
	done := item{"id": r.id, "type": "reasoning", "summary": []any{item{"type": "summary_text", "text": text}}}
	s.emit(out, "response.output_item.done", item{"output_index": r.index, "item": done})
	s.done = append(s.done, doneItem{index: r.index, item: done})
}

// text is the answer's text: it closes open reasoning and opens a message item on its first text.
func (s *Stream) text(out *bytes.Buffer, choice int, text string) {
	if text == "" {
		return
	}

	s.stopReasoning(out)

	if s.msg == nil {
		id := fmt.Sprintf("msg_%s_%d", s.id, choice)
		if s.segments > 0 {
			id = fmt.Sprintf("msg_%s_%d_%d", s.id, choice, s.segments)
		}

		s.segments++
		s.msg = &textItem{id: id, index: s.alloc()}

		s.emit(out, "response.output_item.added", item{"output_index": s.msg.index, "item": item{
			"id": id, "type": "message", "status": "in_progress", "content": []any{}, "role": "assistant",
		}})
		s.emit(out, "response.content_part.added", item{
			"item_id": id, "output_index": s.msg.index, "content_index": 0, "part": outputText(""),
		})
	}

	s.msg.text.WriteString(text)
	s.emit(out, "response.output_text.delta", item{
		"item_id": s.msg.id, "output_index": s.msg.index, "content_index": 0, "delta": text, "logprobs": []any{},
	})
}

func outputText(text string) item {
	return item{"type": "output_text", "annotations": []any{}, "logprobs": []any{}, "text": text}
}

func (s *Stream) closeMessage(out *bytes.Buffer) {
	m := s.msg
	if m == nil {
		return
	}

	s.msg = nil
	text := m.text.String()
	status := s.itemStatus()

	s.emit(out, "response.output_text.done", item{
		"item_id": m.id, "output_index": m.index, "content_index": 0, "text": text, "logprobs": []any{},
	})
	s.emit(out, "response.content_part.done", item{
		"item_id": m.id, "output_index": m.index, "content_index": 0, "part": outputText(text),
	})

	done := item{
		"id": m.id, "type": "message", "status": status, "content": []any{outputText(text)}, "role": "assistant",
	}
	s.emit(out, "response.output_item.done", item{"output_index": m.index, "item": done})
	s.done = append(s.done, doneItem{index: m.index, item: done})
}

// toolCall gathers one tool call's part: the call is announced once its id and name are known, and its arguments
// are passed on as they come; a freeform tool's input is sent whole when the call ends.
func (s *Stream) toolCall(out *bytes.Buffer, choice int, tc toolCallIn) {
	index := 0
	if tc.Index != nil {
		index = *tc.Index
	}

	key := fmt.Sprintf("%d:%d", choice, index)

	c := s.calls[key]
	if c == nil {
		c = &call{key: key, index: s.alloc()}
		s.calls[key] = c
	}

	if tc.ID != "" && c.id == "" {
		c.id = tc.ID
	}

	if tc.Function.Name != "" && !c.added {
		c.name = tc.Function.Name
	}

	if args := stringOrRaw(tc.Function.Arguments); args != "" {
		c.args.WriteString(args)
	}

	s.announce(out, c, false)
	s.passArguments(out, c)
}

func (s *Stream) announce(out *bytes.Buffer, c *call, force bool) {
	if c.added || (!force && (c.id == "" || c.name == "")) {
		return
	}

	if c.id == "" {
		c.id = fmt.Sprintf("call_%s_%s", s.id, strings.ReplaceAll(c.key, ":", "_"))
	}

	c.added = true
	name, namespace, custom := s.tools.identityOf(c.name)
	c.custom = custom

	it := item{"id": "fc_" + c.id, "type": "function_call", "status": "in_progress", "arguments": "", "call_id": c.id}
	if custom {
		it = item{"id": "ctc_" + c.id, "type": "custom_tool_call", "status": "in_progress", "input": "", "call_id": c.id}
	}

	named(it, name, namespace)
	s.emit(out, "response.output_item.added", item{"output_index": c.index, "item": it})
}

func named(it item, name, namespace string) {
	it["name"] = name
	if namespace != "" {
		it["namespace"] = namespace
	}
}

func (s *Stream) passArguments(out *bytes.Buffer, c *call) {
	if !c.added || c.custom || c.args.Len() <= c.sent {
		return
	}

	args := c.args.String()
	s.emit(out, "response.function_call_arguments.delta", item{
		"item_id": "fc_" + c.id, "output_index": c.index, "delta": args[c.sent:],
	})
	c.sent = len(args)
}

// finishItems closes every open item in output order. A tool call whose stream ended with no finish and arguments
// that are not whole JSON is left out rather than sent as made (finalizeOpenItems).
func (s *Stream) finishItems(out *bytes.Buffer) {
	s.closeMessage(out)
	s.stopReasoning(out)

	calls := make([]*call, 0, len(s.calls))
	for _, c := range s.calls {
		if !c.done {
			calls = append(calls, c)
		}
	}

	sort.Slice(calls, func(i, j int) bool { return calls[i].index < calls[j].index })

	_, incomplete := incompleteReason(s.finish)
	status := s.itemStatus()

	for _, c := range calls {
		args := c.args.String()
		if s.finish == "" && (args == "" || !json.Valid([]byte(args))) {
			continue
		}

		s.announce(out, c, true)
		s.passArguments(out, c)

		if args == "" && !incomplete && (s.finish == "tool_calls" || s.finish == "stop") {
			args = "{}"
		}

		c.done = true
		name, namespace, _ := s.tools.identityOf(c.name)

		var done item

		if c.custom {
			input := unwrapInput(args)
			s.emit(out, "response.custom_tool_call_input.done", item{
				"item_id": "ctc_" + c.id, "output_index": c.index, "input": input,
			})

			done = item{"id": "ctc_" + c.id, "type": "custom_tool_call", "status": status, "input": input, "call_id": c.id}
		} else {
			s.emit(out, "response.function_call_arguments.done", item{
				"item_id": "fc_" + c.id, "output_index": c.index, "arguments": args,
			})

			done = item{"id": "fc_" + c.id, "type": "function_call", "status": status, "arguments": args, "call_id": c.id}
		}

		named(done, name, namespace)
		s.emit(out, "response.output_item.done", item{"output_index": c.index, "item": done})
		s.done = append(s.done, doneItem{index: c.index, item: done})
	}
}

// unwrapInput is a freeform tool's input from the {"input": …} its function was given (unwrapCustomToolInput).
func unwrapInput(args string) string {
	var wrapped map[string]json.RawMessage
	if json.Unmarshal([]byte(args), &wrapped) != nil {
		return args
	}

	v, ok := wrapped["input"]
	if !ok {
		return args
	}

	return stringOrRaw(v)
}

// complete sends the answer's end once, when it has any output: response.completed with every item and the usage,
// or response.incomplete for an answer cut at its limit or filtered (buildResponsesCompletedEvent).
func (s *Stream) complete(out *bytes.Buffer) {
	for _, c := range s.calls {
		if c.added && !c.done {
			return
		}
	}

	if len(s.done) == 0 {
		return
	}

	s.ended = true

	sort.SliceStable(s.done, func(i, j int) bool { return s.done[i].index < s.done[j].index })

	output := make([]any, 0, len(s.done))
	for _, d := range s.done {
		output = append(output, d.item)
	}

	kind, status := "response.completed", "completed"
	response := item{
		"id": s.id, "object": "response", "created_at": s.created, "model": s.model, "output": output,
	}

	if reason, incomplete := incompleteReason(s.finish); incomplete {
		kind, status = "response.incomplete", "incomplete"
		response["incomplete_details"] = item{"reason": reason}
	}

	response["status"] = status

	if u := s.usage; u != nil {
		total := u.total
		if total == 0 {
			total = u.input + u.output
		}

		usage := item{
			"input_tokens": u.input, "input_tokens_details": item{"cached_tokens": u.cached},
			"output_tokens": u.output, "total_tokens": total,
		}
		if u.reasoning > 0 {
			usage["output_tokens_details"] = item{"reasoning_tokens": u.reasoning}
		}

		response["usage"] = usage
	}

	s.emit(out, kind, item{"response": response})
}

// fail ends the answer with response.failed for a chat error frame: its code, else its type, and its message.
func (s *Stream) fail(out *bytes.Buffer, raw json.RawMessage) {
	var e struct {
		Message string          `json:"message"`
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"`
	}

	if json.Unmarshal(raw, &e) != nil {
		e.Message = stringOrRaw(raw)
	}

	code := stringOrRaw(e.Code)
	if code == "" || code == "null" {
		code = e.Type
	}

	if code == "" {
		code = "server_error"
	}

	if e.Message == "" {
		e.Message = "the provider ended its answer with an error"
	}

	s.ended = true
	s.emit(out, "response.failed", item{"response": item{
		"id": s.id, "object": "response", "created_at": s.created, "model": s.model, "status": "failed",
		"error": item{"code": code, "message": e.Message}, "output": []any{},
	}})
}

func (s *Stream) itemStatus() string {
	if _, incomplete := incompleteReason(s.finish); incomplete {
		return "incomplete"
	}

	return "completed"
}

func incompleteReason(finish string) (string, bool) {
	switch finish {
	case "length", "max_tokens":
		return "max_output_tokens", true
	case "content_filter":
		return "content_filter", true
	default:
		return "", false
	}
}

func (s *Stream) alloc() int {
	i := s.next
	s.next++

	return i
}

// emit writes one event as CLIProxyAPI frames it: its type as the event's name and in its data, with a sequence
// number.
func (s *Stream) emit(out *bytes.Buffer, kind string, fields item) {
	s.seq++
	fields["type"] = kind
	fields["sequence_number"] = s.seq

	var data bytes.Buffer

	enc := json.NewEncoder(&data)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(fields); err != nil {
		return
	}

	out.WriteString("event: " + kind + "\ndata: ")
	out.Write(bytes.TrimSuffix(data.Bytes(), []byte("\n")))
	out.WriteString("\n\n")
}

// reasoningTexts are a delta's reasoning: reasoning_content, else reasoning, else reasoning_details
// (collectOpenAIObjectReasoningTexts).
func reasoningTexts(d *deltaIn) []string {
	for _, raw := range []json.RawMessage{d.ReasoningContent, d.Reasoning, d.ReasoningDetails} {
		if texts := textsOf(raw); len(texts) > 0 {
			return texts
		}
	}

	return nil
}

// textsOf is the text of a reasoning value: a string, an object's text, or a list of either
// (collectOpenAIReasoningTexts).
func textsOf(raw json.RawMessage) []string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}

	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return []string{s}
		}
	case '[':
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) != nil {
			return nil
		}

		var texts []string
		for _, v := range list {
			texts = append(texts, textsOf(v)...)
		}

		return texts
	case '{':
		var obj struct {
			Text *string `json:"text"`
		}

		if json.Unmarshal(raw, &obj) == nil && obj.Text != nil && *obj.Text != "" {
			return []string{*obj.Text}
		}
	}

	return nil
}

// forEachSegment walks a delta's content: a string is text; a list's thinking parts are reasoning and its text and
// refusal parts text (forEachOpenAIContentSegment).
func forEachSegment(raw json.RawMessage, emit func(reasoning bool, text string)) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return
	}

	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			emit(false, s)
		}
	case '[':
		var parts []json.RawMessage
		if json.Unmarshal(raw, &parts) != nil {
			return
		}

		for _, p := range parts {
			var s string
			if json.Unmarshal(p, &s) == nil {
				emit(false, s)

				continue
			}

			var part struct {
				Type    string          `json:"type"`
				Text    string          `json:"text"`
				Refusal string          `json:"refusal"`
				Think   json.RawMessage `json:"thinking"`
				Reason  json.RawMessage `json:"reasoning"`
			}

			if json.Unmarshal(p, &part) != nil {
				continue
			}

			switch part.Type {
			case "thinking", "reasoning", "thought", "reasoning.text", "reasoning_text":
				// Its thinking, else its reasoning, else its text (extractOpenAIContentPartThinkingText).
				text := part.Text
				if texts := textsOf(part.Think); len(texts) > 0 {
					text = strings.Join(texts, "")
				} else if texts := textsOf(part.Reason); len(texts) > 0 {
					text = strings.Join(texts, "")
				}

				emit(true, text)
			case "text", "output_text":
				emit(false, part.Text)
			case "refusal":
				if part.Refusal != "" {
					emit(false, part.Refusal)
				} else {
					emit(false, part.Text)
				}
			default:
				emit(false, string(p))
			}
		}
	}
}
