package orchestration

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// Scenario: a profile keeps the web's tools and turns one off (issue #536,
// step 7). Invalid presets and entries are refused. A Dispatch reads the switches of the version it runs with:
// a later version changes the next Dispatch, not this one.
func TestAProfilesToolSwitchesReachItsDispatch(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	const head = "name: implementer\ndescription: d\nruntime: claude\nmodel: {provider: model-gateway}\n"

	for _, bad := range []string{
		"tools: [web_serch]\n",
		"tools_off: [mail_*]\n",
		"preset: custom\n",
		"tools: [web_*, web_*]\n",
		"tools: [agent_*_open]\n",
	} {
		_, _, err := w.s.ApplyProfile(ctx, "", head+bad)
		require.ErrorIs(t, err, ErrInvalid, bad)
	}

	p, _, err := w.s.ApplyProfile(ctx, "", head+"preset: report\ntools: [phone]\ntools_off: [web_fetch]\n")
	require.NoError(t, err)
	assert.Equal(t, agenttool.PresetReport, p.Preset)
	assert.Equal(t, []string{"phone"}, p.Tools)
	assert.Equal(t, []string{"web_fetch"}, p.ToolsOff)

	d := w.dispatch(w.tasks(NewTask{Ref: "a", Spec: "do a"})[0].ID)

	_, _, err = w.s.ApplyProfile(ctx, "", head)
	require.NoError(t, err)

	switches, err := w.s.ToolSwitchesOfDispatch(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, agenttool.ToolSwitches{Preset: agenttool.PresetReport, On: []string{"phone"},
		Off: []string{"web_fetch"}}, switches)
	assert.True(t, switches.Allows(agenttool.ToolWebSearch))
	assert.False(t, switches.Allows(agenttool.ToolWebFetch))
	assert.True(t, switches.Allows(agenttool.ToolMemory))
	assert.True(t, switches.Allows(devicetool.VideoStart))
	assert.False(t, switches.Allows("agent_browser_open"))
}

func TestDispatchToolAccessIsCappedOnlyForBotRuns(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	_, _, err := w.s.ApplyProfile(ctx, "", "name: implementer\ndescription: d\nruntime: claude\n"+
		"model: {provider: model-gateway}\ntools: [phone]\ntools_off: [web_fetch]\n")
	require.NoError(t, err)
	bot, err := w.s.PutBot(ctx, Bot{Name: "reporter", Preset: agenttool.PresetReport, ToolsOff: []string{"web_search"}})
	require.NoError(t, err)

	for _, botID := range []string{"", bot.ID} {
		run, err := w.s.CreateRun(ctx, NewRun{Objective: "report", CreatedBy: "test", Bot: botID})
		require.NoError(t, err)
		tasks, err := w.s.CreateTasks(ctx, run.ID, []NewTask{{Ref: "a", Spec: "report"}})
		require.NoError(t, err)
		dispatch := w.dispatch(tasks[0].ID)
		switches, err := w.s.ToolSwitchesOfDispatch(ctx, dispatch.ID)
		require.NoError(t, err)
		assert.Equal(t, botID == "", switches.Allows("agent_browser_open"))
		assert.Equal(t, botID == "", switches.Allows(devicetool.VideoStart))
		assert.Equal(t, botID == "", switches.Allows(agenttool.ToolWebSearch))
		assert.False(t, switches.Allows(agenttool.ToolWebFetch), "the profile's denial still applies")
		assert.True(t, switches.Allows(agenttool.ToolShowArtifact))

		if botID != "" {
			bot.ToolsOff = append(bot.ToolsOff, "artifacts")
			_, err := w.s.PutBot(ctx, bot)
			require.NoError(t, err)
			switches, err := w.s.ToolSwitchesOfDispatch(ctx, dispatch.ID)
			require.NoError(t, err)
			assert.False(t, switches.Allows(agenttool.ToolShowArtifact), "Bot changes reach a running Dispatch")
		}
	}
}

// Scenario: the main Bot turns a Bot's browser off from its chat and the
// owner undoes it: the Bot has no switch again. A Bot's unknown tool is
// refused as a profile's is.
func TestABotsToolSwitchesAreKeptAndUndone(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.PutBot(ctx, Bot{Name: "vmap", ToolsOff: []string{"not-a-tool"}})
	require.ErrorIs(t, err, ErrInvalid)

	vmapBot, err := w.s.PutBot(ctx, Bot{Name: "vmap"})
	require.NoError(t, err)

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	vmapBot.Preset, vmapBot.ToolsOff = agenttool.PresetReport, []string{"agent_browser_*"}
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
	assert.Equal(t, agenttool.PresetFull, back.Preset)
}

// familiesAfterPresets are the tool families added after the presets' migration (00123), whose catalog of tools it
// froze: a Bot or profile it moved to the full preset gets them as the preset says, which no legacy list named.
var familiesAfterPresets = []string{"inbox", "requests", "meetings"}

func TestToolPresetMigrationPreservesBotAndProfileAccess(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	_, db := migratedTo(t, 121)
	type legacy struct{ on, off string }
	policies := []legacy{
		{}, {off: "device_*,agent_browser_*"}, {on: "web_*,device_*,agent_browser_eval", off: "web_fetch,video_stop"},
		{on: "web_f*,video_start", off: "node_*"}, {on: "*", off: "*"},
	}
	for index, policy := range policies {
		id := fmt.Sprintf("legacy-%d", index)
		_, err := db.ExecContext(ctx, `INSERT INTO bots (id, name, created_at, updated_at, tools, tools_off)
			VALUES (?, ?, 1, 1, ?, ?)`, id, id, policy.on, policy.off)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO profile_versions
			(id, profile, version, source, digest, runtime, created_at, tools, tools_off)
			VALUES (?, 'implementer', ?, ?, '', 'claude', 1, ?, ?)`,
			id, 900+index, "name: implementer\ndescription: d\nruntime: claude\n", policy.on, policy.off)
		require.NoError(t, err)
	}

	require.NoError(t, Migrate(ctx, db))
	for index, policy := range policies {
		id := fmt.Sprintf("legacy-%d", index)
		for _, query := range []string{
			"SELECT preset, tools, tools_off FROM bots WHERE id = ?",
			"SELECT preset, tools, tools_off FROM profile_versions WHERE id = ?",
		} {
			var preset, on, off string
			require.NoError(t, db.QueryRowContext(ctx, query, id).Scan(&preset, &on, &off))
			assert.Equal(t, agenttool.PresetFull, preset)
			switches := agenttool.ToolSwitches{Preset: preset, On: splitNames(on), Off: splitNames(off)}
			for _, family := range agenttool.ToolFamilies {
				if slices.Contains(familiesAfterPresets, family.Name) {
					continue
				}

				for _, tool := range family.Tools {
					match := func(entry string) bool {
						prefix, wild := strings.CutSuffix(entry, "*")
						return entry == tool || entry == family.Pattern || wild && strings.HasPrefix(tool, prefix)
					}
					allowed := (policy.on == "" || slices.ContainsFunc(splitNames(policy.on), match)) &&
						!slices.ContainsFunc(splitNames(policy.off), match)
					assert.Equal(t, allowed, switches.Allows(tool), "%s, %s, %s", id, query, tool)
				}
			}
		}
	}

	// The editor's source of a migrated profile says the policy it has, so the
	// owner saving it back changes nothing about what the profile may use.
	_, err := db.ExecContext(ctx, "UPDATE profiles SET current_version_id = 'legacy-4' WHERE name = 'implementer'")
	require.NoError(t, err)

	s := New(db, &fakeClock{now: t0}, func(prefix string) string { return prefix + "_1" })

	edited, err := s.GetProfile(ctx, "implementer")
	require.NoError(t, err)

	var source profileSource
	require.NoError(t, decodeStrict(edited.Source, &source))
	assert.Equal(t, edited.Preset, source.Preset)
	assert.Equal(t, edited.Tools, source.Tools)
	assert.Equal(t, edited.ToolsOff, source.ToolsOff)
	assert.Equal(t, "d", source.Description)
}
