package claude

import (
	"encoding/json"
	"strings"
)

// Prompt-cache breakpoints. A chat client speaks OpenAI, which has no
// cache_control, so the gateway places the breakpoints Claude Code places, or
// the subscription pays full input tokens every turn. Three ephemeral markers,
// the same ones pi and omp set on an Anthropic request
// (openai-completions.ts applyAnthropicCacheControl → system prompt, last
// tool, last conversation message): the first caches the tools and the system
// prefix, the last caches the conversation so far.
//
// Each is kept an hour, as a subscription's cache is everywhere: Claude Code's
// own main conversation, omp's default for an OAuth token (getCacheControl),
// CLIProxyAPI's upgrade of a cloaked request's breakpoints under an OAuth
// token (claude_executor_execute.go), 9router-go's AnchorClaudeCache (issue
// #397). The request carries extended-cache-ttl-2025-04-11 with it.

// applyCache marks the cache breakpoints on a built Messages body. It runs
// after the disguise, so the breakpoints land on the final tool list (the
// decoys included) and the final system (Claude Code's prefix).
func applyCache(body item) {
	markLast(body["system"])
	markLast(body["tools"])
	markLastMessage(body["messages"])
	limitBreakpoints(body)
	settleTTLs(body)
}

// maxBreakpoints is how many cache_control blocks Anthropic takes in one
// request; a top-level cache_control (its automatic caching) takes one of them
// ("You can define up to 4 cache breakpoints", prompt caching docs, read on
// 2026-11-15). More is a 400, "A maximum of 4 blocks with cache_control may be
// provided" (issue #504, seen on zm).
const maxBreakpoints = 4

// limitBreakpoints removes the breakpoints past four, the least useful first,
// as CLIProxyAPI does after it places its own (MIT, Copyright (c) 2025 Luis
// Pater, Router-For.ME; commit e96d4c48, read on 2026-11-15:
// internal/runtime/executor/claude_executor_cloaking.go,
// enforceCacheControlLimitDoc): the system's but its last, earliest first; the
// tools' but the last; the messages', earliest first; then the system's last;
// then the tools' last; then a top-level one. A tool result's own blocks carry
// none (hoistToolCache), so only a message's blocks are counted.
func limitBreakpoints(body item) {
	marked := func(list any) []item {
		var out []item

		blocks, _ := list.([]any)
		for _, b := range blocks {
			if block, ok := b.(item); ok {
				if _, has := block["cache_control"]; has {
					out = append(out, block)
				}
			}
		}

		return out
	}

	tools := marked(body["tools"])
	system := marked(body["system"])

	var messages []item

	list, _ := body["messages"].([]any)
	for _, m := range list {
		if msg, ok := m.(item); ok {
			messages = append(messages, marked(msg["content"])...)
		}
	}

	var top []item
	if _, has := body["cache_control"]; has {
		top = []item{body}
	}

	excess := len(tools) + len(system) + len(messages) + len(top) - maxBreakpoints

	drop := func(blocks []item) {
		for _, b := range blocks {
			if excess <= 0 {
				return
			}

			delete(b, "cache_control")
			excess--
		}
	}

	if len(system) > 1 {
		drop(system[:len(system)-1])
	}

	if len(tools) > 1 {
		drop(tools[:len(tools)-1])
	}

	drop(messages)

	if len(system) > 0 {
		drop(system[len(system)-1:])
	}

	if len(tools) > 0 {
		drop(tools[len(tools)-1:])
	}

	drop(top)
}

// markLastMessage marks the last block of the last message that has one.
func markLastMessage(list any) {
	messages, ok := list.([]any)
	if !ok {
		return
	}

	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(item)
		if !ok {
			continue
		}

		blocks := blocksOf(msg["content"])
		if len(blocks) == 0 {
			continue
		}

		markBlock(blocks[len(blocks)-1])
		msg["content"] = blocks

		return
	}
}

// settleTTLs makes the client's breakpoints agree with the gateway's (issue
// #409), as CLIProxyAPI does after it places its own
// (claude_executor_execute.go): a breakpoint with no ttl is kept an hour
// (upgradeClaudeCacheControlTTLDoc), one whose ttl the client wrote stays,
// and, since Anthropic reads the breakpoints tools, then system, then
// messages, and refuses a one-hour one after a shorter one, a one-hour
// breakpoint after a shorter one loses its ttl (normalizeCacheControlTTLDoc).
func settleTTLs(body item) {
	var marks []item

	collect := func(blocks any) {
		list, ok := blocks.([]any)
		if !ok {
			return
		}

		for _, block := range list {
			b, ok := block.(item)
			if !ok {
				continue
			}

			if cc, ok := b["cache_control"].(item); ok {
				marks = append(marks, cc)
			}

			// A tool result's own blocks come before the next block of its message.
			if nested, ok := b["content"].([]any); ok {
				for _, n := range nested {
					if nb, ok := n.(item); ok {
						if cc, ok := nb["cache_control"].(item); ok {
							marks = append(marks, cc)
						}
					}
				}
			}
		}
	}

	collect(body["tools"])
	collect(body["system"])

	if messages, ok := body["messages"].([]any); ok {
		for _, m := range messages {
			if msg, ok := m.(item); ok {
				collect(msg["content"])
			}
		}
	}

	shorter := false

	for _, cc := range marks {
		if _, has := cc["ttl"]; !has {
			cc["ttl"] = cacheTTL
		}

		if cc["ttl"] != cacheTTL {
			shorter = true

			continue
		}

		if shorter {
			delete(cc, "ttl")
		}
	}
}

// markLast puts an ephemeral cache_control on the last block of a list.
func markLast(blocks any) {
	list, ok := blocks.([]any)
	if !ok || len(list) == 0 {
		return
	}

	markBlock(list[len(list)-1])
}

// cacheTTL is how long Anthropic keeps a breakpoint the gateway sets.
const cacheTTL = "1h"

// markBlock puts an ephemeral cache_control on one block, unless it has one.
func markBlock(block any) {
	b, ok := block.(item)
	if !ok {
		return
	}

	if _, has := b["cache_control"]; !has {
		b["cache_control"] = item{"type": "ephemeral", "ttl": cacheTTL}
	}
}

// The client's own breakpoints on its messages (issue #482). A chat client may
// mark a part or a whole message with an Anthropic cache_control; the rules
// that carry them onto the Messages blocks are CLIProxyAPI's (MIT, Copyright
// (c) 2025 Luis Pater, Router-For.ME), commit c5251b5, read on 2026-11-15:
// internal/translator/common/cache_control.go (AttachCacheControl,
// AttachMessageCacheControl, AttachToolMessageCacheControl) and
// internal/translator/claude/openai/chat-completions/claude_openai_request.go.

// clientCache reads a client's cache_control: an object whose type is
// "ephemeral", or nil (CLIProxyAPI isValidCacheControl). Its ttl is left as
// the client wrote it, for settleTTLs.
func clientCache(raw json.RawMessage) item {
	var cc item
	if len(raw) == 0 || json.Unmarshal(raw, &cc) != nil || cc == nil {
		return nil
	}

	if kind, ok := cc["type"].(string); !ok || kind != "ephemeral" {
		return nil
	}

	return cc
}

// canHoldCache reports whether Anthropic takes a breakpoint on a block: not on
// thinking, nor on a blank text block (CLIProxyAPI claudeBlockCanHostCacheControl).
func canHoldCache(block item) bool {
	switch block["type"] {
	case "thinking", "redacted_thinking":
		return false
	case "text":
		text, _ := block["text"].(string)

		return strings.TrimSpace(text) != ""
	default:
		return true
	}
}

// attachCache puts a client's valid breakpoint on a block that can hold one
// and returns the block (CLIProxyAPI AttachCacheControl).
func attachCache(block item, raw json.RawMessage) item {
	if cc := clientCache(raw); cc != nil && canHoldCache(block) {
		block["cache_control"] = cc
	}

	return block
}

// attachMessageCache puts a message's own breakpoint on its last block that
// can hold one, unless a part's breakpoint is already there (CLIProxyAPI
// AttachMessageCacheControl).
func attachMessageCache(blocks []any, raw json.RawMessage) {
	cc := clientCache(raw)
	if cc == nil {
		return
	}

	for i := len(blocks) - 1; i >= 0; i-- {
		block, ok := blocks[i].(item)
		if !ok || !canHoldCache(block) {
			continue
		}

		if _, has := block["cache_control"]; !has {
			block["cache_control"] = cc
		}

		return
	}
}

// hoistToolCache puts a tool message's breakpoint on its tool_result block:
// the first valid one of its parts, else the message's own. None stays inside
// the result's content, which Anthropic refuses (CLIProxyAPI
// AttachToolMessageCacheControl, extractFirstPartCacheControl).
func hoistToolCache(result item, content, message json.RawMessage) {
	cc := firstPartCache(content)
	if cc == nil {
		cc = clientCache(message)
	}

	for _, b := range blocksOf(result["content"]) {
		if block, ok := b.(item); ok {
			delete(block, "cache_control")
		}
	}

	if cc != nil {
		result["cache_control"] = cc
	}
}

// firstPartCache reads the first valid breakpoint among a content's parts, a
// list or a single object.
func firstPartCache(content json.RawMessage) item {
	var parts []json.RawMessage
	if json.Unmarshal(content, &parts) != nil {
		parts = []json.RawMessage{content}
	}

	for _, p := range parts {
		var part struct {
			CacheControl json.RawMessage `json:"cache_control"`
		}

		if json.Unmarshal(p, &part) == nil {
			if cc := clientCache(part.CacheControl); cc != nil {
				return cc
			}
		}
	}

	return nil
}
