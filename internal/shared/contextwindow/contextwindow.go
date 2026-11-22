// Package contextwindow tells from an error's text that a prompt did not fit
// its model's context window (issue #1052), the one test the node's runtimes
// and the model gateway share.
//
// The phrases are omp's evidence of an overflow, each a provider's own
// wording or one that names the context (MIT License, Copyright (c) 2025
// Mario Zechner, https://github.com/can1357/oh-my-pi, c2f787e):
// packages/ai/src/error/flags.ts:91-115, CONTEXT_OVERFLOW_EVIDENCE_PATTERNS.
// Left out are its generic fallbacks ("too many tokens", "token limit
// exceeded", request_too_large, "exceeds the limit of N tokens", a message
// count cap), which a rate limit or a size cap also says, and OmniRoute's
// "input is too long", which a tool's parameter check says too: a wrong
// answer here ends a Conversation. Added is Codex's own message
// (codex-rs/protocol/src/error.rs:146-149). A tokens-per-minute limit is never
// an overflow, as OmniRoute's isTokenRateLimitWording (MIT License, Copyright
// (c) 2026 Cheaper Inference, https://github.com/diegosouzapw/OmniRoute,
// 4265bb2a; open-sse/services/accountFallback.ts:357-366).
package contextwindow

import (
	"regexp"
	"strings"
)

// rateWording is a tokens-per-minute limit, which is never an overflow.
var rateWording = regexp.MustCompile(`(?i)maximum input tokens?\s+per\s+(?:minute|second|hour|day)\b|` +
	`\btokens?\s+per\s+(?:minute|second|hour|day)\b|\btpm\b|\brpm\b`)

// evidence are the phrases of an overflow, each with whose it is.
var evidence = []string{
	`ran out of room in the model's context window`, // Codex
	`prompt is too long`,                            // Anthropic
	`input is too long for requested model`,         // Amazon Bedrock
	`exceeds the context window`,                    // OpenAI (Completions and Responses)
	`input token count.*exceeds the maximum`,        // Google (Gemini)
	`maximum prompt length is \d+`,                  // xAI (Grok)
	`reduce the length of the messages`,             // Groq
	`maximum context length is \d+ tokens`,          // OpenRouter
	`exceeds the available context size`,            // llama.cpp server
	`requested tokens?.*exceed.*context (?:window|length|size)`,
	`context (?:window|length|size).*(?:exceeded|overflow|too small)`,
	`(?:prompt|input).*(?:too long|too large).*(?:context|n_ctx)`,
	`requested tokens?.*(?:exceeds?|greater than).*(?:n_ctx|context)`,
	`greater than the context length`, // LM Studio
	`context window exceeds limit`,    // MiniMax
	`exceeded model token limit`,      // Kimi For Coding
	`context[_ ]length[_ ]exceeded`,
	`model_context_window_exceeded`,    // z.ai
	`prompt filled the context window`, // Ollama
}

var overflow = regexp.MustCompile(`(?i)` + strings.Join(evidence, "|"))

// Exceeded reports whether an error's text says the prompt did not fit.
func Exceeded(text string) bool {
	return text != "" && !rateWording.MatchString(text) && overflow.MatchString(text)
}
