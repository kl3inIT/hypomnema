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
		{Tap, `{"target":"#1","force":true}`},
	}

	for _, tt := range refused {
		_, err := read(tt.tool, tt.arguments)
		require.ErrorIs(t, err, ErrBadArguments, "%s %s", tt.tool, tt.arguments)
	}

	_, err = read("device_reboot", `{}`)
	require.ErrorIs(t, err, ErrNoSuchTool)
}
