package agenttool

import (
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
	// (issue #818); the phone's switch names them too.
	offered = append(offered, devicetool.VideoMake, devicetool.VideoGet)

	phone := ToolFamilies[len(ToolFamilies)-1]
	require.Equal(t, "phone", phone.Name)
	assert.ElementsMatch(t, offered, phone.Tools)
	assert.False(t, devicetool.Routed(devicetool.VideoMake), "a render is not the phone's")
	assert.False(t, devicetool.Routed(devicetool.VideoGet))
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
