package claude

// Prompt-cache breakpoints. A chat client speaks OpenAI, which has no
// cache_control, so the gateway places the breakpoints Claude Code places, or
// the subscription pays full input tokens every turn. Three ephemeral markers,
// the same ones pi and omp set on an Anthropic request
// (openai-completions.ts applyAnthropicCacheControl → system prompt, last
// tool, last conversation message): the first caches the tools and the system
// prefix, the last caches the conversation so far.

// applyCache marks the cache breakpoints on a built Messages body. It runs
// after the disguise, so the breakpoints land on the final tool list (the
// decoys included) and the final system (Claude Code's prefix).
func applyCache(body item) {
	markLast(body["system"])
	markLast(body["tools"])

	if messages, ok := body["messages"].([]any); ok && len(messages) > 0 {
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
}

// markLast puts an ephemeral cache_control on the last block of a list.
func markLast(blocks any) {
	list, ok := blocks.([]any)
	if !ok || len(list) == 0 {
		return
	}

	markBlock(list[len(list)-1])
}

// markBlock puts an ephemeral cache_control on one block, unless it has one.
func markBlock(block any) {
	b, ok := block.(item)
	if !ok {
		return
	}

	if _, has := b["cache_control"]; !has {
		b["cache_control"] = item{"type": "ephemeral"}
	}
}
