package claude

import "strings"

// Thinking budgets per effort level (9router-go claudeLevelBudget).
var thinkingBudget = map[string]int{
	"minimal": 512, "low": 1024, "medium": 8192, "high": 24576, "xhigh": 32768, "max": 128000,
}

const (
	// minThinkingBudget is the smallest budget_tokens Anthropic accepts.
	minThinkingBudget = 1024
	// thinkingHeadroom is the room left for the answer when max_tokens is
	// raised to fit a budget.
	thinkingHeadroom = 1024
)

// adaptiveModels are the parts of a model's name that mark a model taking
// only adaptive thinking (`type: adaptive` with `output_config.effort`); the
// rest take a budget. The list is 9router's claude-adaptive rows
// (open-sse/providers/capabilities.js, MODEL_CAPABILITIES and the opencode-go
// and Anthropic claude-haiku-5-5 pins): Haiku 5.5, Sonnet 4.6+ and 5.x, Opus
// 4.6+ and 5.x, Fable 5.x. A budget sent to one is refused with a 400
// ("thinking.type.enabled" is not supported for this model).
var adaptiveModels = []string{
	"haiku-5-5",
	"sonnet-4-6", "sonnet-4-7", "sonnet-5",
	"opus-4-6", "opus-4-7", "opus-4-8", "opus-5",
	"fable-5",
}

// adaptive reports whether a model takes adaptive thinking, by its name at
// the upstream, dotted (claude-haiku-5.5) or dashed (claude-haiku-5-5).
func adaptive(model string) bool {
	name := strings.ReplaceAll(strings.ToLower(model), ".", "-")

	for _, part := range adaptiveModels {
		if strings.Contains(name, part) {
			return true
		}
	}

	return false
}

// hasXHigh reports whether an adaptive model offers the xhigh effort: the 4.6
// models lack it (9router-go thinking_levels.go levelClaudeNoXHigh).
func hasXHigh(model string) bool {
	name := strings.ReplaceAll(strings.ToLower(model), ".", "-")

	return !strings.Contains(name, "opus-4-6") && !strings.Contains(name, "sonnet-4-6")
}

// applyThinking gives the body a Messages `thinking` object when the client
// asked for reasoning with `reasoning_effort`, as 9router-go does
// (applyClaudeThinkingType, enforceClaudeThinkingSampling). An adaptive model
// gets `type: adaptive` and the effort in `output_config.effort`, a client's
// own effort kept; any other gets the budget form (`type: enabled`,
// `budget_tokens`), with max_tokens raised to leave room. When thinking is on
// and the client sent a sampling setting, temperature is pinned to 1.
func applyThinking(body item, model, effort string, outputConfig item, hasSampling bool) {
	effort = strings.ToLower(strings.TrimSpace(effort))

	switch effort {
	case "", "none", "off", "minimal_off":
		return
	case "auto":
		effort = "high"
	}

	if adaptive(model) {
		switch effort {
		case "minimal":
			effort = "low"
		case "xhigh":
			if !hasXHigh(model) {
				effort = "high"
			}
		}

		config := item{}
		for key, value := range outputConfig {
			config[key] = value
		}

		if _, set := config["effort"]; !set {
			config["effort"] = effort
		}

		body["thinking"] = item{"type": "adaptive"}
		body["output_config"] = config
	} else if !applyBudget(body, effort) {
		return
	}

	// A sampling setting and thinking cannot both stand: Anthropic wants
	// temperature 1 and no top_p then.
	if hasSampling {
		body["temperature"] = 1.0
		delete(body, "top_p")
	}
}

// applyBudget gives the body the budget form of thinking for an effort, and
// reports false when max_tokens leaves no room for the smallest budget.
func applyBudget(body item, effort string) bool {
	budget, ok := thinkingBudget[effort]
	if !ok {
		budget = thinkingBudget["medium"]
	}

	if budget < minThinkingBudget {
		budget = minThinkingBudget
	}

	maxTok, _ := body["max_tokens"].(int)

	if want := budget + thinkingHeadroom; maxTok < want {
		maxTok = want
		body["max_tokens"] = maxTok
	}

	if budget >= maxTok {
		budget = maxTok - 1
	}

	if budget < minThinkingBudget {
		return false
	}

	body["thinking"] = item{"type": "enabled", "budget_tokens": budget}

	return true
}
