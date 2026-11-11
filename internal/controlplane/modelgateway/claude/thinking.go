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

// applyThinking gives the body a Messages `thinking` object when the client
// asked for reasoning with `reasoning_effort`, as 9router-go does
// (applyClaudeThinkingType, enforceClaudeThinkingSampling). The budget form
// (`type: enabled`, `budget_tokens`) is used, which every thinking-capable
// Claude model accepts; max_tokens is raised to leave room, and when thinking
// is on and the client sent a sampling setting, temperature is pinned to 1.
func applyThinking(body item, effort string, hasSampling bool) {
	effort = strings.ToLower(strings.TrimSpace(effort))

	switch effort {
	case "", "none", "off", "minimal_off":
		return
	case "auto":
		effort = "high"
	}

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
		return
	}

	body["thinking"] = item{"type": "enabled", "budget_tokens": budget}

	// A sampling setting and thinking cannot both stand: Anthropic wants
	// temperature 1 and no top_p then.
	if hasSampling {
		body["temperature"] = 1.0
		delete(body, "top_p")
	}
}
