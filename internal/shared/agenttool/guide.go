package agenttool

import (
	"slices"
	"strings"
)

// The "Tools" block of an agent's prompt (issue #723, decisions 2.2 to 2.4):
// one short line for each family of Hypomnema's tools the agent has, which
// says when to use it and what to use instead, beside what each tool's own
// description says (2.1). It is built from the tools the agent has and
// nothing else, as Hermes builds its tool guidance only from the tools a
// session has (agent/prompt_builder.py:450, :1323-1324) and OpenClaw lists a
// line for each tool its agent has (src/agents/system-prompt-tool-list.ts:
// 21-71): a tool the agent lacks is not named. Each family's line is its
// WhenToUse (switches.go), the one source (owner, 2026-11-18); a family
// whose WhenToUse is empty is left out.
//
// A worker gets it in its rules, from the tool names its offer carries
// (nodeproto.Offer.Tools); a Bot's pi at the start of what `hypomnema bots
// prompt` gives it. It holds no date and nothing of a task or a chat, so a
// worker and a Bot with the same families get the same block, and the start
// of the prompt stays the same for the provider's cache (#672).

// ToolGuide is the "Tools" block for an agent with the tools named, or ""
// when no family it has says when to use it.
func ToolGuide(tools []string) string {
	var b strings.Builder

	for _, f := range ToolFamilies {
		if f.WhenToUse == "" || !slices.ContainsFunc(f.Tools, func(t string) bool { return slices.Contains(tools, t) }) {
			continue
		}

		if b.Len() == 0 {
			b.WriteString("## Tools\n\n")
		}

		b.WriteString("- " + titled(f.Name) + ": " + f.WhenToUse + "\n")
	}

	return b.String()
}

// titled is a family's name with its first letter in capitals.
func titled(name string) string {
	if name == "" {
		return name
	}

	return strings.ToUpper(name[:1]) + name[1:]
}
