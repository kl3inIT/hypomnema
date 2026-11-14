package claude

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
	settleTTLs(body)
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
