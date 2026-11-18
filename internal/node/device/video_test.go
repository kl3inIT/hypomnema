package device

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// Scenario: the control plane passes an agent's video tools as it passes a
// phone tool. video_start films under the id the control plane gave; a line
// with a tap at x,y is stamped with the point; the app the agent opens with
// device_launch, with its version, is the take's app; video_stop answers at
// once with the take finalizing. Each answer carries the take for the
// control plane, and a line of text for the agent.
func TestTheVideoToolsFilmThroughTheNodesCall(t *testing.T) {
	t.Parallel()

	r := newTakeRig(t)
	r.sys.tool = func(c Command) (Output, bool) {
		if slices.Contains(c.Args, "dumpsys") {
			return Output{Stdout: "Packages:\n  versionCode=42 minSdk=24\n    versionName=3.1.0\n"}, true
		}

		return Output{}, false
	}

	answer := r.m.Call(t.Context(), "d-1", devicetool.VideoStart,
		json.RawMessage(`{"title":"Sign in","request_id":"r1","take":"tk_0123456789abcdef"}`))
	require.False(t, answer.IsError, answer.Content)
	require.NotNil(t, answer.Take)
	assert.Equal(t, "tk_0123456789abcdef", answer.Take.ID)
	assert.Contains(t, answer.Content[0].Text, "video_say")

	<-r.opened

	video := &testVideo{width: 32, height: 32}
	r.send(t, configPacket(video))
	r.send(t, keyPacket(video, 0))

	launched := r.m.Call(t.Context(), "d-1", devicetool.Launch, json.RawMessage(`{"package":"vn.shop.app"}`))
	require.False(t, launched.IsError, launched.Content)

	said := r.m.Call(t.Context(), "d-1", devicetool.VideoSay,
		json.RawMessage(`{"take":"tk_0123456789abcdef","text":"Tap Sign in","tap":"360,640","chapter":"Sign in"}`))
	require.False(t, said.IsError, said.Content)
	require.Len(t, said.Take.Steps, 1)
	assert.Equal(t, devicetool.TakeStep{Text: "Tap Sign in", Tap: &devicetool.TakeTap{X: 360, Y: 640},
		Chapter: "Sign in"}, said.Take.Steps[0])
	assert.Equal(t, "vn.shop.app", said.Take.AppPackage)
	assert.Equal(t, "3.1.0", said.Take.AppVersion)

	other := r.m.Call(t.Context(), "d-2", devicetool.VideoStop, json.RawMessage(`{"take":"tk_0123456789abcdef"}`))
	assert.True(t, other.IsError, "another agent's stop is refused")

	stopped := r.m.Call(t.Context(), "d-1", devicetool.VideoStop, json.RawMessage(`{"take":"tk_0123456789abcdef"}`))
	require.False(t, stopped.IsError, stopped.Content)
	assert.Equal(t, devicetool.TakeFinalizing, stopped.Take.State)
	assert.Contains(t, stopped.Content[0].Text, "video_status")

	take := r.finished(t, "tk_0123456789abcdef")
	assert.Equal(t, devicetool.TakeUploading, take.State)

	status := r.m.Call(t.Context(), "d-1", devicetool.VideoStatus, json.RawMessage(`{"take":"tk_0123456789abcdef"}`))
	require.False(t, status.IsError)
	assert.Contains(t, status.Content[0].Text, "uploading")
}
