package agenttool

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Scenario (issue #723, decisions 2.2 and 2.4): the "Tools" block names only
// the families the agent has, in the families' order, each with its
// WhenToUse; a family whose WhenToUse is empty is left out even when the
// agent has it, and an agent with none of the others gets no block. The same
// tools give the same block, whatever their order.
func TestTheToolsBlockNamesOnlyTheFamiliesTheAgentHas(t *testing.T) {
	t.Parallel()

	block := ToolGuide([]string{ToolAudioSpeak, ToolMemory, ToolWebFetch, "agent_browser_open"})

	assert.True(t, strings.HasPrefix(block, "## Tools\n\n- Web: Search the web with web_search"), block)
	assert.Contains(t, block, "\n- Audio: Turn a recording into text")
	assert.Less(t, strings.Index(block, "- Web:"), strings.Index(block, "- Audio:"))
	assert.NotContains(t, block, "- Images:", "a family the agent lacks is not named")
	assert.NotContains(t, block, "Memory", "a family with no line is left out")
	assert.NotContains(t, block, "Browser")
	assert.Equal(t, 2, strings.Count(block, "\n- "))

	assert.Equal(t, block, ToolGuide([]string{"agent_browser_open", ToolWebFetch, ToolMemory, ToolAudioSpeak}))
	assert.Empty(t, ToolGuide([]string{ToolMemory, "agent_browser_open"}))
	assert.Empty(t, ToolGuide(nil))
}

// Scenario: in this issue only the web, the images and the audio say when to
// use them; the other families wait for #789.
func TestOnlyTheWebImagesAndAudioSayWhenToUseThem(t *testing.T) {
	t.Parallel()

	var said []string

	for _, f := range ToolFamilies {
		if f.WhenToUse != "" {
			said = append(said, f.Name)
		}
	}

	assert.Equal(t, []string{"web", "images", "audio"}, said)
}
