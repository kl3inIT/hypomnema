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
// whose WhenToUse is empty is left out. After the families come the
// command-line tools the agent reaches accounts with through the service
// proxy, named by their command (gws, lark-cli, gh, glab), each with its
// Def.WhenToUse, which points to the tool's skill (issue #789): OpenClaw
// gates a tool's hint on the tool being there (src/agents/system-prompt.ts:
// 598-653), and Hermes drops a line whose tool is absent
// (agent/prompt_builder.py:525-533). A tool's standing rules that only the
// Bots' AGENTS.md said until #789 (the browser's own tab and sign-in, never
// a password in a chat; the Routines') are in its family's line, so that
// every agent with the tool gets them, as OpenMausBot gives its browser's
// rules with the browser (server/browser/prompt.ts:4-5).
//
// A worker gets it in its rules, from the tool names its offer carries
// (nodeproto.Offer.Tools); a Bot's pi at the start of what `hypomnema bots
// prompt` gives it. It holds no date and nothing of a task or a chat, so a
// worker and a Bot with the same families get the same block, and the start
// of the prompt stays the same for the provider's cache (#672).

// ToolGuide is the "Tools" block for an agent with the tools named, the
// control plane's and the command-line tools' (by their command), or "" when
// none it has says when to use it.
func ToolGuide(tools []string) string {
	var lines []string

	for _, f := range ToolFamilies {
		if f.WhenToUse != "" && slices.ContainsFunc(f.Tools, func(t string) bool { return slices.Contains(tools, t) }) {
			lines = append(lines, "- "+f.title()+": "+f.WhenToUse+"\n")
		}
	}

	for _, d := range Catalog() {
		if d.WhenToUse != "" && slices.Contains(tools, d.Name) {
			lines = append(lines, "- "+d.Name+": "+d.WhenToUse+"\n")
		}
	}

	if len(lines) == 0 {
		return ""
	}

	return "## Tools\n\n" + strings.Join(lines, "")
}

// title is how the block names a family: its Title, or its name with its
// first letter in capitals.
func (f ToolFamily) title() string {
	if f.Title != "" || f.Name == "" {
		return f.Title
	}

	return strings.ToUpper(f.Name[:1]) + f.Name[1:]
}
