// Package contextwindow tells from an error's text that a prompt did not fit
// its model's context window (issue #1052), the one test the node's runtimes
// and the model gateway share.
//
// Adapted from OmniRoute (MIT License, Copyright (c) 2026 Cheaper Inference,
// https://github.com/diegosouzapw/OmniRoute, 4265bb2a):
// open-sse/services/accountFallback.ts:357-374, isTokenRateLimitWording and
// STRONG_CONTEXT_OVERFLOW_RE, the phrases only a real overflow uses. Its weaker
// patterns ("max.*token", "token limit") are left out: they need its
// parameter-validation exclusions, and a wrong answer here ends a
// Conversation.
package contextwindow

import "regexp"

// rateWording is a tokens-per-minute limit, which is never an overflow.
var rateWording = regexp.MustCompile(`(?i)maximum input tokens?\s+per\s+(?:minute|second|hour|day)\b|` +
	`\btokens?\s+per\s+(?:minute|second|hour|day)\b|\btpm\b|\brpm\b`)

// overflow matches Codex's own "ran out of room in the model's context window"
// (codex-rs/protocol/src/error.rs:146-149), OpenAI's context_length_exceeded
// code, and Anthropic's "prompt is too long".
var overflow = regexp.MustCompile(`(?i)\b(?:maximum context|context (?:length|window))\b|context_length_exceeded|` +
	`\b(?:prompt is |input (?:is )?)too long\b`)

// Exceeded reports whether an error's text says the prompt did not fit.
func Exceeded(text string) bool {
	return text != "" && !rateWording.MatchString(text) && overflow.MatchString(text)
}
