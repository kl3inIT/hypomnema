package coordinator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario (issue #789, decision 5): the rules every Bot gets keep how a Bot works with the owner, and leave the
// tools' own guidance to the "Tools" block, which workers get too (agenttool.ToolFamilies and Def.WhenToUse): the
// Browser, Routines and Files and artifacts parts are gone, and the skills of the commands with them. The owner's
// standing rules that are a Bot's alone stay: approvals on a card, the owner's own Lark account on his turn, the
// MEDIA line, and where he finds the Bot's tab.
func TestEveryBotsRulesKeepOnlyHowItWorksWithTheOwner(t *testing.T) {
	t.Parallel()

	raw, err := Files.ReadFile("AGENTS.md")
	require.NoError(t, err)

	rules := string(raw)

	for _, heading := range []string{"## Approvals", "## Tools and the owner", "## Wake-ups and the menu",
		"## Checking claims", "## Research before a design", "## Answering"} {
		assert.Contains(t, rules, "\n"+heading+"\n")
	}

	for _, moved := range []string{"## Browser", "## Routines", "## Files and artifacts", "## Skills and accounts",
		"`google-workspace` skill", "`github` skill", "`gitlab` skill", "`superset` skill", "`routines` tool",
		"show_artifact", "agent_browser_", "100,000 bytes"} {
		assert.NotContains(t, rules, moved)
	}

	assert.Contains(t, rules, "held for the owner's approval, which he gives on a card")
	assert.Contains(t, rules, "`lark-cli --account me`")
	assert.Contains(t, rules, "`MEDIA:<path>`")
	assert.Contains(t, rules, "\"Tabs of this pane's chats\"")
	assert.Contains(t, rules, "`hypomnema-coordinator` skill")
	assert.Contains(t, rules, "\"Tools\" block", "it points to where the tools' guidance went")
}
