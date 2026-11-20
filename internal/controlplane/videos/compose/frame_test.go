package compose

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The worked numbers of issue #908 (segment 576×1280, as scrcpy films the node's phone), exactly.
func TestFrameLayoutGivesTheWorkedNumbers(t *testing.T) {
	t.Parallel()

	segment := Size{576, 1280}

	cases := []struct {
		name   string
		frame  Frame
		out    Size
		scale  float64
		hole   Rect
		strip  int
		window Rect
		bezel  Rect
		radii  [2]int
		cutout Cutout
	}{
		{
			name: "android 9:16", frame: DefaultFrame(ChromeAndroid), out: Size{720, 1280}, scale: 1.428571,
			hole: Rect{102, 68, 514, 1142}, window: Rect{102, 68, 514, 1142}, bezel: Rect{82, 48, 554, 1182},
			radii: [2]int{57, 77}, cutout: Cutout{Left: 349, Top: 88, W: 20, H: 20, Radius: 10},
		},
		{
			name: "android 16:9", frame: DefaultFrame(ChromeAndroid), out: Size{1280, 720}, scale: 0.803571,
			hole: Rect{496, 38, 290, 642}, window: Rect{496, 38, 290, 642}, bezel: Rect{485, 27, 312, 664},
			radii: [2]int{32, 43}, cutout: Cutout{Left: 635.5, Top: 49, W: 11, H: 11, Radius: 5.5},
		},
		{
			name: "android 9:16 safe top", frame: withSafeTop(DefaultFrame(ChromeAndroid)), out: Size{720, 1280},
			scale: 1.358811, hole: Rect{116, 128, 490, 1088}, strip: 63, window: Rect{116, 65, 490, 1151},
			bezel: Rect{97, 46, 528, 1189}, radii: [2]int{54, 73},
			cutout: Cutout{Left: 351.5, Top: 87, W: 19, H: 19, Radius: 9.5},
		},
		{
			name: "iphone 9:16 safe top", frame: withSafeTop(DefaultFrame(ChromeIPhone)), out: Size{720, 1280},
			scale: 1.358811, hole: Rect{116, 128, 490, 1088}, strip: 63, window: Rect{116, 65, 490, 1151},
			bezel: Rect{97, 46, 528, 1189}, radii: [2]int{54, 73},
			cutout: Cutout{Left: 286.5, Top: 75, W: 149, H: 43, Radius: 21.5},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			l, err := FrameLayout(tc.frame, segment, tc.out)
			require.NoError(t, err)
			assert.InDelta(t, tc.scale, l.Scale, 5e-7)
			assert.Equal(t, tc.hole, l.Hole)
			assert.Equal(t, tc.strip, l.Strip)
			assert.Equal(t, tc.window, l.Window)
			assert.Equal(t, tc.bezel, l.Bezel)
			assert.Equal(t, [2]int{l.HoleRadius, l.BezelRadius}, tc.radii)
			require.NotNil(t, l.Cutout)
			assert.Equal(t, tc.cutout, *l.Cutout)
			assert.InDelta(t, float64(tc.hole.X)+float64(tc.hole.W)/2, tc.cutout.Left+float64(tc.cutout.W)/2, 1e-9,
				"the cut-out is centered on the hole")
		})
	}
}

func withSafeTop(f Frame) Frame {
	f.SafeTop = true

	return f
}

// A radius sent is used as sent: 40 is only a device's default (#908, row 10).
func TestFrameLayoutUsesTheRadiusSent(t *testing.T) {
	t.Parallel()

	f := DefaultFrame(ChromeAndroid)
	f.Radius = 20

	l, err := FrameLayout(f, Size{576, 1280}, Size{720, 1280})
	require.NoError(t, err)
	assert.Equal(t, 29, l.HoleRadius, "round(20 × 1.428571)")
	assert.Equal(t, 49, l.BezelRadius, "round(34 × 1.428571)")

	none, err := FrameLayout(DefaultFrame(ChromeNone), Size{576, 1280}, Size{720, 1280})
	require.NoError(t, err)
	assert.Equal(t, 14, DefaultFrame(ChromeNone).Radius)
	assert.Equal(t, Rect{}, none.Bezel, "no bezel without a device")
	assert.Nil(t, none.Cutout, "no camera without a device")
	assert.Equal(t, none.Hole, none.Window)
}

// The hole's rounding is not an aspect mismatch (#936, P1): at padding 44, the hole is 518×1154, and a contain
// fit of 576×1280 into it would leave 3 px; the segment still fits, and is stretched by under 0.4 %.
func TestTheHolesRoundingIsNotATurnedPhone(t *testing.T) {
	t.Parallel()

	f := DefaultFrame(ChromeAndroid)
	f.Padding = 44

	l, err := FrameLayout(f, Size{576, 1280}, Size{720, 1280})
	require.NoError(t, err)
	assert.Equal(t, Size{518, 1154}, Size{l.Hole.W, l.Hole.H})
	assert.True(t, l.Fits(576, 1280))
	assert.True(t, l.Fits(1080, 2400), "another size of the same shape")
	assert.False(t, l.Fits(1280, 576), "the phone turned")
	assert.False(t, l.Fits(576, 1200), "a real mismatch of a few pixels")

	stretch := float64(l.Hole.H) / float64(l.Hole.W) / (1280.0 / 576.0)
	assert.InDelta(t, 1, stretch, 0.003808)
}

func TestFrameChecksItsSizes(t *testing.T) {
	t.Parallel()

	for _, f := range []Frame{
		{Chrome: "mac"}, {Chrome: ChromeNone, Padding: -1}, {Chrome: ChromeNone, Padding: 401},
		{Chrome: ChromeAndroid, Radius: 81}, {Chrome: ChromeAndroid, Radius: -1},
	} {
		require.Error(t, f.Check(), "%+v", f)
	}

	for _, f := range []Frame{{}, {Chrome: ChromeFull}, {Chrome: ChromeIPhone, Padding: 400, Radius: 80}} {
		require.NoError(t, f.Check(), "%+v", f)
	}
}
