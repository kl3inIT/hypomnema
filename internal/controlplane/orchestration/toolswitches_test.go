package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Scenario: a profile keeps the web's tools and turns one off (issue #536,
// step 7). A name no tool has, a family that names none, and one named in
// both lists are refused when the profile is applied, as a skill no one added
// is. Its worker's Dispatch reads the switches of the version it runs with:
// a later version changes the next Dispatch, not this one.
func TestAProfilesToolSwitchesReachItsDispatch(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	const head = "name: implementer\ndescription: d\nruntime: claude\nmodel: {provider: model-gateway}\n"

	for _, bad := range []string{
		"tools: [web_serch]\n",
		"tools_off: [mail_*]\n",
		"tools: [web_*]\ntools_off: [web_*]\n",
		"tools: [agent_*_open]\n",
	} {
		_, _, err := w.s.ApplyProfile(ctx, "", head+bad)
		require.ErrorIs(t, err, ErrInvalid, bad)
	}

	p, _, err := w.s.ApplyProfile(ctx, "", head+"tools: [web_*]\ntools_off: [web_fetch]\n")
	require.NoError(t, err)
	assert.Equal(t, []string{"web_*"}, p.Tools)
	assert.Equal(t, []string{"web_fetch"}, p.ToolsOff)

	d := w.dispatch(w.tasks(NewTask{Ref: "a", Spec: "do a"})[0].ID)

	_, _, err = w.s.ApplyProfile(ctx, "", head)
	require.NoError(t, err)

	switches, err := w.s.ToolSwitchesOfDispatch(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, agenttool.ToolSwitches{On: []string{"web_*"}, Off: []string{"web_fetch"}}, switches)
	assert.True(t, switches.Allows(agenttool.ToolWebSearch))
	assert.False(t, switches.Allows(agenttool.ToolWebFetch))
	assert.False(t, switches.Allows(agenttool.ToolMemory))
}

// Scenario: the main Bot turns a Bot's browser off from its chat and the
// owner undoes it: the Bot has no switch again. A Bot's unknown tool is
// refused as a profile's is.
func TestABotsToolSwitchesAreKeptAndUndone(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.PutBot(ctx, Bot{Name: "vmap", ToolsOff: []string{"browser"}})
	require.ErrorIs(t, err, ErrInvalid)

	vmapBot, err := w.s.PutBot(ctx, Bot{Name: "vmap"})
	require.NoError(t, err)

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	vmapBot.ToolsOff = []string{"agent_browser_*"}
	changed, err := w.s.PutBotFromChat(ctx, chat.ID, vmapBot)
	require.NoError(t, err)
	assert.Equal(t, []string{"agent_browser_*"}, changed.ToolsOff)
	assert.False(t, changed.ToolSwitches().Allows("agent_browser_open"))

	changes, err := w.s.ListSetupChanges(ctx, chat.ID)
	require.NoError(t, err)
	require.NotEmpty(t, changes)

	_, err = w.s.UndoSetupChange(ctx, changes[len(changes)-1].ID)
	require.NoError(t, err)

	back, err := w.s.GetBot(ctx, vmapBot.ID)
	require.NoError(t, err)
	assert.Empty(t, back.ToolsOff)
}
