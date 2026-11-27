package agenttool

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario (issue #723, decisions 2.2 and 2.4; issue #789): the "Tools" block
// names only the families and commands the agent has, the families first in
// their order, then the commands in the catalog's, each with its WhenToUse.
// An agent with none of them gets no block. The same tools give the same
// block, whatever their order.
func TestTheToolsBlockNamesOnlyTheFamiliesTheAgentHas(t *testing.T) {
	t.Parallel()

	block := ToolGuide([]string{ToolAudioSpeak, "gh", ToolMemory, ToolWebFetch, "gws"})

	assert.True(t, strings.HasPrefix(block, "## Tools\n\n- Memory: Keep a lasting fact"), block)
	assert.Contains(t, block, "\n- Web: Search the web with web_search")
	assert.Contains(t, block, "\n- Audio: Turn a recording into text")
	assert.Contains(t, block, "\n- gws: ")
	assert.Contains(t, block, "\n- gh: ")
	assert.Less(t, strings.Index(block, "- Memory:"), strings.Index(block, "- Web:"))
	assert.Less(t, strings.Index(block, "- Audio:"), strings.Index(block, "- gws:"), "the commands come last")
	assert.Less(t, strings.Index(block, "- gws:"), strings.Index(block, "- gh:"))
	assert.NotContains(t, block, "- Images:", "a family the agent lacks is not named")
	assert.NotContains(t, block, "- Browser:")
	assert.NotContains(t, block, "- lark-cli:", "a command the agent lacks is not named")
	assert.Equal(t, 5, strings.Count(block, "\n- "))

	assert.Equal(t, block, ToolGuide([]string{"gws", ToolWebFetch, ToolMemory, "gh", ToolAudioSpeak}))
	assert.Empty(t, ToolGuide([]string{"aws", "not_a_tool"}), "the AWS CLI has no line")
	assert.Empty(t, ToolGuide(nil))
}

// Scenario (issue #789, decision 1): every family says when to use it, and
// each line names its own tools, so that the block never points an agent at
// a tool of another family. The browser's line holds the owner's standing
// rules, since a worker has the browser too: its own tab, the sign-in there,
// and never a password.
func TestEveryFamilySaysWhenToUseIt(t *testing.T) {
	t.Parallel()

	for _, f := range ToolFamilies {
		require.NotEmpty(t, f.WhenToUse, f.Name)
		assert.True(t, strings.Contains(f.WhenToUse, f.Tools[0]) || strings.Contains(f.WhenToUse, f.Pattern),
			"%s's line names its tools", f.Name)
	}

	browser := ToolGuide([]string{"agent_browser_open"})
	assert.Contains(t, browser, "\n- Browser: ")
	assert.Contains(t, browser, "your own tab")
	assert.Contains(t, browser, "\"My browser\"")
	assert.Contains(t, browser, "\"Hand back\"")
	assert.Contains(t, browser, "Never repeat a password or code in a reply")

	assert.Contains(t, ToolGuide([]string{ToolWhatsAppChats}), "\n- WhatsApp: ", "a family's own title")
}

// Scenario (issue #789, decision 2): the command-line tools reached through
// the service proxy say when to use them and point to their skill; their
// lines live beside their pins in the catalog.
func TestTheServiceProxysCommandsPointToTheirSkills(t *testing.T) {
	t.Parallel()

	skills := map[string]string{
		"gws": "`google-workspace` skill", "lark-cli": "`lark` skill", "gh": "`github` skill", "glab": "`gitlab` skill",
	}

	for name, skill := range skills {
		def, ok := Named(name)
		require.True(t, ok, name)
		assert.Contains(t, def.WhenToUse, skill, name)
		assert.Contains(t, def.WhenToUse, "already signed in", name)
	}
}

// The Inbox's line keeps one item for each ask, from the ask to the work to done (issue #1206), and the meetings'
// line adds only what involves the owner (issue #1142): never two items for one ask, nor another person's action.
func TestTheInboxLineKeepsOneItemForEachAsk(t *testing.T) {
	t.Parallel()

	inbox := ToolGuide([]string{ToolInboxAdd})
	assert.NotContains(t, inbox, "two items")
	assert.Contains(t, inbox, "One ask is one item")
	assert.Contains(t, inbox, "why_open promised")
	assert.Contains(t, inbox, "the due he gave, else the asker's, else due_date none",
		"the order of the due is the triage's (review of PR #1243)")

	meetings := ToolGuide([]string{ToolMeetingGet})
	assert.NotContains(t, meetings, "any other as proposed")
	assert.Contains(t, meetings, "another person's own action adds nothing")
}
