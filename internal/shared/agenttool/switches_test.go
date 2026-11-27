package agenttool

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

func TestToolPresetsApplyExceptionsAndDenyWins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		switches ToolSwitches
		tool     string
		allows   bool
	}{
		{"full is the default", ToolSwitches{}, ToolMemory, true},
		{"full excludes phone", ToolSwitches{}, devicetool.Screen, false},
		{"report includes web", ToolSwitches{Preset: PresetReport}, ToolWebSearch, true},
		{"report excludes browser", ToolSwitches{Preset: PresetReport}, "agent_browser_open", false},
		{"an exception adds browser", ToolSwitches{Preset: PresetReport, On: []string{"browser"}},
			"agent_browser_open", true},
		{"additions do not replace preset tools", ToolSwitches{On: []string{"browser"}}, ToolMemory, true},
		{"a name keeps itself", ToolSwitches{On: []string{ToolMemory}}, ToolMemory, true},
		{"off wins over on", ToolSwitches{On: []string{"web_*"}, Off: []string{ToolWebFetch}}, ToolWebFetch, false},
		{"off by family", ToolSwitches{Off: []string{"superset_*"}}, ToolSupersetAPI, false},
		{"off leaves the rest", ToolSwitches{Off: []string{"superset_*"}}, ToolWebSearch, true},
		{"a star alone is every tool", ToolSwitches{Off: []string{"*"}}, ToolRoutines, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.allows, tt.switches.Allows(tt.tool))
		})
	}
}

func TestAToolSwitchNamesAToolOnce(t *testing.T) {
	t.Parallel()

	require.NoError(t, CheckToolSwitches([]string{"agent_browser_*", ToolMemory}, []string{"agent_browser_eval"}))
	require.NoError(t, CheckToolSwitches(nil, nil))
	require.NoError(t, CheckToolSwitches([]string{"phone"}, []string{"phone"}))

	for _, bad := range [][2][]string{
		{{"no-such-family"}, nil},
		{nil, {"mail_*"}},
		{{"agent_*_open"}, nil},
		{{ToolMemory, ToolMemory}, nil},
		{{""}, nil},
	} {
		err := CheckToolSwitches(bad[0], bad[1])
		require.ErrorIs(t, err, ErrBadToolSwitch, bad)
	}

	err := CheckToolSwitches([]string{"no-such-family"}, nil)
	assert.ErrorContains(t, err, "agent_browser_*", "a refusal names the families")
}

// The phone's family is every tool the phone offers, so a switch of its
// family or of one of its names is never refused as unknown.
func TestThePhoneFamilyIsThePhonesTools(t *testing.T) {
	t.Parallel()

	var offered []string
	for _, tool := range devicetool.Tools() {
		offered = append(offered, tool.Name)
	}

	// video_make and video_get are the control plane's, served on its own tools route, apart from the phone
	// (issue #818), and so is video_templates (issue #1230); the phone's switch names them too.
	offered = append(offered, devicetool.VideoMake, devicetool.VideoGet, devicetool.VideoTemplates)

	phone := ToolFamilies[len(ToolFamilies)-1]
	require.Equal(t, "phone", phone.Name)
	assert.ElementsMatch(t, offered, phone.Tools)
	assert.False(t, devicetool.Routed(devicetool.VideoMake), "a render is not the phone's")
	assert.False(t, devicetool.Routed(devicetool.VideoGet))
	assert.False(t, devicetool.Routed(devicetool.VideoTemplates))
	assert.True(t, devicetool.Routed(devicetool.VideoStatus))
}

// The phone's switch is one: switching device_* off takes the video tools of
// the phone's family too (issue #817), and naming one of them alone still works.
func TestThePhonesSwitchTakesItsVideoTools(t *testing.T) {
	t.Parallel()

	off := ToolSwitches{Off: []string{devicetool.Prefix + "*"}}
	assert.False(t, off.Allows(devicetool.VideoStart))
	assert.False(t, off.Allows(devicetool.Screen))
	assert.True(t, off.Allows(ToolMemory))

	only := ToolSwitches{On: []string{devicetool.Prefix + "*"}}
	assert.True(t, only.Allows(devicetool.VideoSay))

	one := ToolSwitches{On: []string{"phone"}, Off: []string{devicetool.VideoStart}}
	assert.False(t, one.Allows(devicetool.VideoStart))
	assert.True(t, one.Allows(devicetool.VideoSay))
	require.NoError(t, CheckToolSwitches(nil, []string{devicetool.VideoStart}))
}

// The owner's rule that a Bot reading his messages never sends one as him (2026-11-17, and 2026-11-21 on #838: by
// the tools' access, not the prompt): the Inbox's family has no send, and a Routine's run that reads only is given
// the Inbox and no tool that sends as him.
func TestAReadRunKeepsTheInboxAndNoSend(t *testing.T) {
	t.Parallel()

	sends := []string{ToolZaloSend, ToolWhatsAppSend}

	for _, tool := range InboxTools {
		assert.True(t, ReadRunAllows(tool), tool)
		assert.NotContains(t, sends, tool)
	}

	for _, tool := range sends {
		assert.False(t, ReadRunAllows(tool), tool)
	}
}

// Scenario: issue #1023, section 5: the Bot that keeps the Inbox reads the owner's meetings in a run that reads
// only, and that run is given exactly the reads of his accounts, his meetings and its memory, and the Inbox, which
// writes only in the control plane: no tool of any family that writes outside, sends or acts is allowed.
func TestAReadRunReadsMeetingsAndNothingThatWrites(t *testing.T) {
	t.Parallel()

	reads := []string{ToolMeetingList, ToolMeetingGet, ToolMemory, ToolSupersetDashboards, ToolSupersetDashboard,
		ToolSupersetChartData, ToolSupersetFilterValues, ToolWhatsAppChats, ToolWhatsAppMessages, ToolWhatsAppMedia,
		ToolWhatsAppHistory, ToolZaloChats, ToolZaloMessages, ToolZaloMedia, ToolZaloHistory}

	var allowed []string

	for _, family := range ToolFamilies {
		for _, tool := range family.Tools {
			if ReadRunAllows(tool) && !slices.Contains(InboxTools, tool) {
				allowed = append(allowed, tool)
			}
		}
	}

	assert.ElementsMatch(t, reads, allowed)
	assert.True(t, ReadRunAllows(ToolAccountRead))

	for _, tool := range []string{ToolZaloSend, ToolWhatsAppSend, ToolSupersetAPI, ToolShowArtifact, ToolRoutines,
		ToolWebFetch, ToolNodeExec, ToolGenerateImage, ToolAudioSpeak} {
		assert.False(t, ReadRunAllows(tool), tool)
	}
}

// A browser take is followed and made with the video tools the phone's family names too (issue #1189, review of PR
// #1193): the full preset, which leaves the phone off, gives them with the browser, and a switch off of either
// family takes them away, as a deny of either of OpenClaw's groups does.
func TestTheBrowserFamilyGivesTheVideoToolsThatReadATake(t *testing.T) {
	t.Parallel()

	read := []string{devicetool.VideoStatus, devicetool.VideoList, devicetool.VideoMake, devicetool.VideoGet,
		devicetool.VideoTemplates}
	full := ToolSwitches{Preset: PresetFull}

	for _, tool := range append([]string{devicetool.BrowserVideoStart, devicetool.BrowserVideoSay}, read...) {
		assert.True(t, full.Allows(tool), tool)
	}

	assert.False(t, full.Allows(devicetool.VideoStart), "filming the phone stays the phone's")

	for _, off := range []string{"browser", "agent_browser_*", "phone", devicetool.Prefix + "*"} {
		switches := ToolSwitches{Preset: PresetFull, Off: []string{off}}
		for _, tool := range read {
			assert.False(t, switches.Allows(tool), "%s off: %s", off, tool)
		}
	}
}

// The browser's video tools do not turn the phone on: its family is on only with a tool of its own (issue #1189).
func TestTheBrowsersVideoToolsDoNotGiveThePhone(t *testing.T) {
	t.Parallel()

	full := ToolSwitches{Preset: PresetFull}
	assert.False(t, full.AllowsFamily("phone"))
	assert.True(t, full.AllowsAnyOf("phone"), "its route still lists video_status and video_list")
	assert.True(t, ToolSwitches{Preset: PresetFull, On: []string{"phone"}}.AllowsFamily("phone"))
	assert.True(t, full.AllowsFamily("browser"))
}
