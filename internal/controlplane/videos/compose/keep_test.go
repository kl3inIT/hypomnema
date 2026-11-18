package compose

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// aidemo has no unit tests of these (its test/ folder, at b67235a): the cases
// are what its comments in src/compose.ts say each does.

func TestKeepIntervals(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		idle []Span
		want []Span
	}{
		{"nothing idle is kept whole", nil, []Span{{0, 8000}}},
		{
			"an idle span is capped to 400 ms from its start",
			[]Span{{2000, 5000}},
			[]Span{{0, 2000}, {2000, 2400}, {5000, 8000}},
		},
		{
			"an idle span at the start keeps its first 400 ms",
			[]Span{{0, 1000}},
			[]Span{{0, 400}, {1000, 8000}},
		},
		{
			"an idle span past the end is clipped to it",
			[]Span{{7000, 9000}},
			[]Span{{0, 7000}, {7000, 7400}},
		},
		{
			"an idle span shorter than the cap is kept whole",
			[]Span{{3000, 3300}},
			[]Span{{0, 3000}, {3000, 3300}, {3300, 8000}},
		},
		{
			"unsorted idle spans are sorted",
			[]Span{{6000, 7000}, {1000, 2000}},
			[]Span{{0, 1000}, {1000, 1400}, {2000, 6000}, {6000, 6400}, {7000, 8000}},
		},
		{
			"spans of 20 ms or less are dropped",
			[]Span{{10, 7990}},
			[]Span{{10, 410}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, keepIntervals(0, 8000, tc.idle))
		})
	}
}

func TestMergeIdleJoinsOverlappingAndTouchingSpans(t *testing.T) {
	t.Parallel()

	got := mergeIdle([]Span{{5000, 6000}, {1000, 2000}, {2000, 3000}, {1500, 2500}, {5500, 5800}})
	assert.Equal(t, []Span{{1000, 3000}, {5000, 6000}}, got)
}

func TestSceneAutoIdle(t *testing.T) {
	t.Parallel()

	sc := Scene{StartMs: 1000, EndMs: 9000}

	// The scene's first and last 300 ms are left alone; what is left of 200 ms or less is dropped.
	got := sceneAutoIdle([]Span{{0, 3000}, {5000, 5150}, {7000, 10000}}, sc)
	assert.Equal(t, []Span{{1300, 3000}, {7000, 8700}}, got)

	// The 600 ms after a tap are never cut: a freeze over them stays.
	sc.Tap, sc.TapAtMs = &devicetool.TakeTap{X: 1, Y: 1}, 1000
	assert.Equal(t, []Span{{7000, 8700}}, sceneAutoIdle([]Span{{1200, 3000}, {7000, 10000}}, sc))
	assert.Equal(t, []Span{{1700, 3000}}, sceneAutoIdle([]Span{{1700, 3000}}, sc))
}

func TestOffsetInKeeps(t *testing.T) {
	t.Parallel()

	keeps := []Span{{0, 2000}, {2000, 2400}, {5000, 8000}}

	assert.Equal(t, int64(0), offsetInKeeps(keeps, 0))
	assert.Equal(t, int64(1500), offsetInKeeps(keeps, 1500))
	assert.Equal(t, int64(2400), offsetInKeeps(keeps, 3000), "a time inside a cut lands on the cut")
	assert.Equal(t, int64(2400), offsetInKeeps(keeps, 5000))
	assert.Equal(t, int64(3400), offsetInKeeps(keeps, 6000))
	assert.Equal(t, int64(5400), offsetInKeeps(keeps, 9000))
}

func TestRetimeScene(t *testing.T) {
	t.Parallel()

	// Within 0.5x-1.6x the footage is retimed to the target exactly.
	r := retimeScene(3000, 3600)
	assert.InDelta(t, 1.2, r.Factor, 1e-9)
	assert.InDelta(t, 3600, r.StretchedMs, 1e-6)
	assert.False(t, r.holding())
	assert.Zero(t, r.TrimMs)

	// Too short even at x1.6: the last frame is held for the rest.
	r = retimeScene(2000, 5000)
	assert.InDelta(t, MaxStretch, r.Factor, 1e-9)
	assert.InDelta(t, 3200, r.StretchedMs, 1e-6)
	assert.InDelta(t, 1800, r.HoldMs, 1e-6)
	assert.True(t, r.holding())

	// Too long even at 2x: the tail is trimmed, so that later scenes keep to their voice.
	r = retimeScene(4000, 1000)
	assert.InDelta(t, MinFactor, r.Factor, 1e-9)
	assert.InDelta(t, 1000, r.TrimMs, 1e-6)
	assert.InDelta(t, 1000, r.StretchedMs, 1e-6)
	assert.False(t, r.holding())

	// Within 40 ms over is neither trimmed nor held.
	r = retimeScene(2060, 1000)
	assert.Zero(t, r.TrimMs)
	assert.False(t, r.holding())
}

func TestParseFreezes(t *testing.T) {
	t.Parallel()

	out := "frame:45   pts:45     pts_time:1.5\n" +
		"lavfi.freezedetect.freeze_start=1.5\n" +
		"lavfi.freezedetect.freeze_duration=2.25\n" +
		"lavfi.freezedetect.freeze_end=3.75\n" +
		"lavfi.freezedetect.freeze_start=6.0333\n"

	assert.Equal(t, []Span{{1500, 3750}, {6033, openFreeze}}, parseFreezes(out))
	assert.Empty(t, parseFreezes(""))
}

func TestScenes(t *testing.T) {
	t.Parallel()

	tap := &devicetool.TakeTap{X: 10, Y: 20}
	take := devicetool.Take{
		DurationMs: 9000,
		Segments:   []devicetool.TakeSegment{{AtMs: 0, LengthMs: 9500}},
		Steps: []devicetool.TakeStep{
			{AtMs: 500, Text: "Mở ứng dụng", Tap: tap},
			{AtMs: 3000, Text: "Chọn bản đồ."},
			{AtMs: 3000, Text: "Rồi tìm đường", Tap: tap},
			{AtMs: 7000, Text: "Xong"},
		},
	}

	scenes, err := Scenes(take)
	require.NoError(t, err)
	require.Len(t, scenes, 3)

	assert.Equal(t, Scene{Index: 0, Text: "Mở ứng dụng", StepAtMs: 500, LeadInMs: 500, StartMs: 0, EndMs: 3000,
		Tap: tap, TapAtMs: 500}, scenes[0])
	assert.Equal(t, Scene{Index: 1, Text: "Chọn bản đồ. Rồi tìm đường", StepAtMs: 3000, StartMs: 3000, EndMs: 7000,
		Tap: tap, TapAtMs: 3000}, scenes[1], "steps at the same time are one scene, with the first tap")
	assert.Equal(t, Scene{Index: 2, Text: "Xong", StepAtMs: 7000, StartMs: 7000, EndMs: 9500}, scenes[2],
		"the last scene ends where the last segment does")

	_, err = Scenes(devicetool.Take{DurationMs: 1000})
	require.ErrorIs(t, err, ErrNoSteps)
}
