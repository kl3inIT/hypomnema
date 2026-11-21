package devicetool

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every tool offered has an object schema that refuses what it does not
// take, so a model is shown each tool's own arguments only.
func TestEveryToolHasAnObjectSchema(t *testing.T) {
	t.Parallel()

	for _, tool := range Tools() {
		var schema map[string]any
		require.NoError(t, json.Unmarshal(tool.InputSchema, &schema), tool.Name)
		assert.Equal(t, "object", schema["type"], tool.Name)
		assert.Equal(t, false, schema["additionalProperties"], tool.Name)
		assert.NotEmpty(t, tool.Description, tool.Name)
	}
}

// A call is read into what its tool runs: whether a screen has boxes; a box,
// coordinates or a selector for a tap; and a call no tool takes is refused with what is wrong.
func TestACallIsReadOrRefused(t *testing.T) {
	t.Parallel()

	read := func(tool, arguments string) (Call, error) { return Read(tool, json.RawMessage(arguments)) }

	c, err := read(Screen, `{}`)
	require.NoError(t, err)
	assert.False(t, c.Boxes, "a plain screen unless the boxes are asked for")

	c, err = read(Screen, `{"boxes":true}`)
	require.NoError(t, err)
	assert.True(t, c.Boxes)

	c, err = read(Tap, `{"target":"#12"}`)
	require.NoError(t, err)
	assert.Equal(t, &Point{Box: 12}, c.Target)

	c, err = read(Tap, `{"target":"360, 640"}`)
	require.NoError(t, err)
	assert.Equal(t, &Point{X: 360, Y: 640}, c.Target)

	c, err = read(Tap, `{"target":"text=Đăng nhập,clickable=true"}`)
	require.NoError(t, err)
	assert.Nil(t, c.Target)
	assert.Equal(t, "text=Đăng nhập,clickable=true", c.Selector)

	c, err = read(Swipe, `{"from":"#1","to":"360,100"}`)
	require.NoError(t, err)
	assert.Equal(t, Point{Box: 1}, c.From)
	assert.Equal(t, 300, c.DurationMs, "the default duration")

	c, err = read(Type, `{"text":"Phở"}`)
	require.NoError(t, err)
	assert.Equal(t, "focused=true", c.Into, "the focused field by default")

	c, err = read(Logcat, ``)
	require.NoError(t, err)
	assert.Equal(t, DefaultLines, c.Lines)

	refused := []struct{ tool, arguments string }{
		{Tap, `{"target":"the blue button"}`},
		{Tap, `{"target":"#0"}`},
		{Tap, `{"target":"--wait=1"}`},
		{Swipe, `{"from":"#1"}`},
		{Swipe, `{"from":"#1","to":"#2","duration_ms":10}`},
		{Type, `{"text":""}`},
		{Type, `{"text":"x","into":"--submit"}`},
		{Key, `{"key":"power"}`},
		{Launch, `{"package":"com.example; reboot"}`},
		{Logcat, `{"lines":5000}`},
		{Logcat, `{"level":"X"}`},
		{Screen, `[1]`},
		{Screen, `{"target":"#1"}`},
		// The agent's own tools server takes save_to out (issue #1059); the control plane never takes it.
		{Screen, `{"save_to":"a.png"}`},
		{Tap, `{"target":"#1","force":true}`},
	}

	for _, tt := range refused {
		_, err := read(tt.tool, tt.arguments)
		require.ErrorIs(t, err, ErrBadArguments, "%s %s", tt.tool, tt.arguments)
	}

	_, err = read("device_reboot", `{}`)
	require.ErrorIs(t, err, ErrNoSuchTool)
}

// The video tools are offered to an agent with their own arguments; the
// control plane's own ones, and the take it names a start with, are taken
// only as the node reads a call.
func TestAVideoCallIsReadOrRefused(t *testing.T) {
	t.Parallel()

	c, err := Read(VideoStart, json.RawMessage(`{"title":" Sign in ","request_id":"r1"}`))
	require.NoError(t, err)
	assert.Equal(t, Call{Tool: VideoStart, Title: "Sign in", RequestID: "r1"}, c)

	_, err = Read(VideoStart, json.RawMessage(`{"title":"Sign in","take":"tk_1"}`))
	require.ErrorIs(t, err, ErrBadArguments, "an agent does not name its take")

	c, err = ReadNode(VideoStart, json.RawMessage(`{"title":"Sign in","take":"tk_1"}`))
	require.NoError(t, err)
	assert.Equal(t, "tk_1", c.Take)

	_, err = Read(VideoStart, json.RawMessage(`{}`))
	require.ErrorIs(t, err, ErrBadArguments)

	c, err = Read(VideoSay, json.RawMessage(`{"take":"tk_1","text":"Tap Sign in","tap":"#3","chapter":"Sign in"}`))
	require.NoError(t, err)
	assert.Equal(t, Call{Tool: VideoSay, Take: "tk_1", Text: "Tap Sign in", Target: &Point{Box: 3},
		Chapter: "Sign in"}, c)

	_, err = Read(VideoSay, json.RawMessage(`{"text":"no take"}`))
	require.ErrorIs(t, err, ErrBadArguments)

	_, err = Read(VideoSay, json.RawMessage(`{"take":"../etc","text":"x"}`))
	require.ErrorIs(t, err, ErrBadArguments)

	_, err = Read(VideoSay, json.RawMessage(`{"take":"tk_1","text":"x","tap":"the blue one"}`))
	require.ErrorIs(t, err, ErrBadArguments)

	for _, internal := range []string{VideoPause, VideoResume, VideoEnd} {
		_, err = Read(internal, json.RawMessage(`{"take":"tk_1"}`))
		require.ErrorIs(t, err, ErrNoSuchTool, "%s is the control plane's own", internal)
	}

	c, err = ReadNode(VideoEnd, json.RawMessage(`{"take":"tk_1","ended_by":"work_end"}`))
	require.NoError(t, err)
	assert.Equal(t, EndedWorkEnd, c.EndedBy)

	_, err = ReadNode(VideoEnd, json.RawMessage(`{"take":"tk_1","ended_by":"bored"}`))
	require.ErrorIs(t, err, ErrBadArguments)

	assert.True(t, Routed(VideoStart))
	assert.True(t, Routed(Screen))
	assert.False(t, Routed("web_search"))
}

// device_location (issue #851) is read as Appium formats an emulator's
// `geo fix`: each value rounded up to 5 decimals, written without trailing
// zeros; a latitude outside -90..90 or a longitude outside -180..180, or one
// missing, is refused.
func TestALocationIsReadAsGeoFixTakesIt(t *testing.T) {
	t.Parallel()

	c, err := Read(Location, json.RawMessage(`{"latitude":21.028511,"longitude":105.854223}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"105.85423", "21.02852"}, GeoFix(c))
	assert.Equal(t, []string{"105,85423", "21,02852"}, GeoFixComma(c))

	c, err = Read(Location, json.RawMessage(`{"latitude":-90,"longitude":180,"altitude":12.5}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"180", "-90", "12.5"}, GeoFix(c))
	assert.Equal(t, []string{"180", "-90", "12,5"}, GeoFixComma(c))

	c, err = Read(Location, json.RawMessage(`{"latitude":-0.000001,"longitude":-151.209301}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"-151.2093", "0"}, GeoFix(c), "rounded up, as Math.ceil; -0 is written 0")

	for _, args := range []string{
		`{"latitude":90.1,"longitude":0}`,
		`{"latitude":-91,"longitude":0}`,
		`{"latitude":0,"longitude":180.5}`,
		`{"latitude":0,"longitude":-181}`,
		`{"latitude":21}`,
		`{"longitude":105}`,
		`{"latitude":"21","longitude":"105"}`,
		`{"latitude":21,"longitude":105,"speed":3}`,
	} {
		_, err := Read(Location, json.RawMessage(args))
		require.ErrorIs(t, err, ErrBadArguments, args)
	}
}

// video_list (issue #851) takes no take, and a state only of a take's states.
func TestAVideoListIsReadOrRefused(t *testing.T) {
	t.Parallel()

	c, err := Read(VideoList, nil)
	require.NoError(t, err)
	assert.Equal(t, Call{Tool: VideoList}, c)

	c, err = Read(VideoList, json.RawMessage(`{"state":"recording"}`))
	require.NoError(t, err)
	assert.Equal(t, TakeRecording, c.State)

	_, err = Read(VideoList, json.RawMessage(`{"state":"lost"}`))
	require.ErrorIs(t, err, ErrBadArguments)

	_, err = Read(VideoList, json.RawMessage(`{"take":"tk_1"}`))
	require.ErrorIs(t, err, ErrBadArguments)

	assert.True(t, Routed(VideoList))
	assert.True(t, Routed(Location))
}
