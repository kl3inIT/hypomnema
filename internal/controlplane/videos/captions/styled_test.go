package captions

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
)

// cue is one cue of words said one after another, each lasting each seconds, from 0.
func cue(each float64, ws ...string) Cue {
	c := Cue{Index: 1}
	for i, w := range ws {
		c.Words = append(c.Words, Word{Word: w, Start: float64(i) * each, End: float64(i+1) * each})
	}

	c.Text = strings.Join(ws, " ")
	c.EndMs = int64(float64(len(ws)) * each * 1000)

	return c
}

func placed(t *testing.T, preset string, pos Position, w, h int, cues []Cue, rings []compose.Ring) []placedFrame {
	t.Helper()

	r, err := newRenderer(preset, w, h)
	require.NoError(t, err)

	out, err := r.place(cues, pos, rings)
	require.NoError(t, err)

	return out
}

func shownWords(pf placedFrame) []string {
	var out []string

	for _, line := range pf.block.lines {
		for _, w := range line {
			out = append(out, w.text)
		}
	}

	return out
}

// Classic is the captions as they were made before the presets, byte for byte (issue #908, PR 1, check 5).
func TestLegacyIsTheCaptionsOfBefore(t *testing.T) {
	t.Parallel()

	cues, err := Build([]Scene{{StartMs: 1000, EndMs: 3000, Words: []Word{{"Xin", 0, 0.5}, {"chào", 0.6, 1.2}}}},
		Default)
	require.NoError(t, err)

	for _, size := range [][2]int{{720, 1280}, {1280, 720}} {
		got, err := Render(cues, Look{Preset: Legacy, Position: PositionCenter}, size[0], size[1],
			[]compose.Ring{{AtMs: 1000, X: 360, Y: 1200, Size: 101}})
		require.NoError(t, err)
		assert.Equal(t, ASS(cues, size[0], size[1], Default), got)
	}
}

func TestRenderRefusesWhatIsNotThere(t *testing.T) {
	t.Parallel()

	_, err := Render(nil, Look{Preset: "sparkle"}, 720, 1280, nil)
	require.ErrorContains(t, err, `"sparkle"`)

	_, err = Render(nil, Look{Preset: "clean", Position: "left"}, 720, 1280, nil)
	require.ErrorContains(t, err, `"left"`)

	got, err := Render([]Cue{cue(0.3, "Xin", "chào")}, Look{}, 720, 1280, nil)
	require.NoError(t, err)
	assert.Contains(t, got, "Style: Text,Be Vietnam Pro Bold,", "no preset is Clean")
}

// onScreen is how long each word is shown, as the frames show it.
func onScreen(frames []placedFrame) map[string]float64 {
	out := map[string]float64{}

	for _, pf := range frames {
		for _, w := range shownWords(pf) {
			out[w] += pf.to - pf.from
		}
	}

	return out
}

// The floors (issue #908, PR 1, check 2): a word stays at least 0.42 s in One Word and 0.14 s in Clean, and a
// cue too short for them still shows every word, in order, within the cue.
func TestFloorsAndShortCues(t *testing.T) {
	t.Parallel()

	long := cue(0.02, "w0", "w1", "w2", "w3", "w4", "w5", "w6", "w7")
	long.EndMs = 20000

	for preset, floor := range map[string]float64{"one-word": WordByWordMinSeconds, "clean": HighlightMinSeconds} {
		shown := onScreen(placed(t, preset, PositionPreset, 720, 1280, []Cue{long}, nil))
		require.Len(t, shown, 8, preset)

		for w, sec := range shown {
			assert.GreaterOrEqual(t, sec, floor-1e-6, "%s: %s", preset, w)
		}
	}

	// One Word: each word for exactly its stretch; five words in 1.0 s, all five, in order, inside the cue.
	short := cue(0.01, "một", "hai", "ba", "bốn", "năm")
	short.EndMs = 1000

	var seq []string

	for _, pf := range placed(t, "one-word", PositionPreset, 720, 1280, []Cue{short}, nil) {
		assert.GreaterOrEqual(t, pf.from, 0.0)
		assert.LessOrEqual(t, pf.to, 1.0+1e-9)
		seq = append(seq, shownWords(pf)...)
	}

	assert.Equal(t, []string{"MỘT", "HAI", "BA", "BỐN", "NĂM"}, seq)
}

func TestClausePunctuationIsDroppedOnlyWordByWord(t *testing.T) {
	t.Parallel()

	c := cue(0.5, "một,", "hai;", "ba:", "xong.")

	for preset, want := range map[string][]string{
		"one-word":  {"MỘT", "HAI", "BA", "XONG."},
		"word-pair": {"MỘT", "HAI", "BA", "XONG."},
		"clean":     {"một,", "hai;", "ba:", "xong."},
		"pop":       {"MỘT,", "HAI;", "BA:", "XONG."},
	} {
		seen := map[string]bool{}

		for _, pf := range placed(t, preset, PositionPreset, 1280, 720, []Cue{c}, nil) {
			for _, w := range shownWords(pf) {
				seen[w] = true
			}
		}

		for _, w := range want {
			assert.True(t, seen[w], "%s shows %q", preset, w)
		}

		assert.Len(t, seen, len(want), preset)
	}
}

// The width cap, as laid out (issue #908, row 8): a 60-character token at 720 px shrinks to the 20 px floor
// and is squeezed into 0.94 W, with or without a box.
func TestTheWidthCapSqueezesAtTheFloor(t *testing.T) {
	t.Parallel()

	token := strings.Repeat("W", 60)

	for _, preset := range []string{"one-word", "word-pair", "clean", "bar"} {
		frames := placed(t, preset, PositionPreset, 720, 1280, []Cue{cue(1, token)}, nil)
		require.NotEmpty(t, frames, preset)

		for _, pf := range frames {
			assert.InDelta(t, MinFontSize, pf.block.em, 1e-9, preset)
			assert.Less(t, pf.block.fx, 1.0, preset)
			assert.LessOrEqual(t, pf.block.w+inkEdgePx, 0.94*720, preset)
		}
	}

	// A short line keeps its ideal size and is not squeezed.
	for _, pf := range placed(t, "one-word", PositionPreset, 720, 1280, []Cue{cue(1, "ổn")}, nil) {
		assert.InDelta(t, 138.88, pf.block.em, 1e-9)
		assert.InDelta(t, 1.0, pf.block.fx, 0)
	}
}

// Where a cue sits (issue #908, row 9).
func TestPositions(t *testing.T) {
	t.Parallel()

	c := []Cue{cue(0.4, "Ổn", "định")}

	at := func(preset string, pos Position, w, h int) float64 {
		frames := placed(t, preset, pos, w, h, c, nil)
		require.NotEmpty(t, frames)

		return frames[0].top + frames[0].block.h/2
	}

	// Pop sits at the center of a tall video, and at the bottom of a wide one; a chosen center holds there.
	assert.InDelta(t, 640, at("pop", PositionPreset, 720, 1280), 1)
	assert.Greater(t, at("pop", PositionPreset, 1280, 720), 720*0.8)
	assert.InDelta(t, 360, at("pop", PositionCenter, 1280, 720), 1)

	// Bottom 7 %, lower third 16 %, top 7 %.
	frames := placed(t, "clean", PositionBottom, 720, 1280, c, nil)
	assert.InDelta(t, 1280-90, frames[0].top+frames[0].block.h, 0.5)
	frames = placed(t, "clean", PositionLowerThird, 720, 1280, c, nil)
	assert.InDelta(t, 1280-205, frames[0].top+frames[0].block.h, 0.5)
	frames = placed(t, "clean", PositionTop, 720, 1280, c, nil)
	assert.InDelta(t, 90, frames[0].top, 0)

	// A bar is flush with the bottom, whatever is chosen.
	frames = placed(t, "bar", PositionCenter, 720, 1280, c, nil)
	assert.InDelta(t, 1280, frames[0].top+frames[0].block.h, 0)
}

// A ring that shows while a cue at the bottom does, and reaches its tallest block, moves the whole cue to the top.
func TestARingFlipsTheWholeCue(t *testing.T) {
	t.Parallel()

	// Two windows of a tall video: the cue's blocks are of one and of two lines.
	c := []Cue{cue(0.5, "Ổn", "định", "hệ", "thống", "đăng")}
	stay := placed(t, "clean", PositionPreset, 720, 1280, c, nil)
	require.Len(t, stay, 2)

	tallest := max(stay[0].block.h, stay[1].block.h)
	blockTop := 1280 - 90 - tallest

	for name, tc := range map[string]struct {
		ring    compose.Ring
		flipped bool
	}{
		"in the block while it shows":   {compose.Ring{AtMs: 2400, X: 100, Y: int(blockTop) + 10, Size: 40}, true},
		"its edge touches the block":    {compose.Ring{AtMs: 0, X: 600, Y: int(blockTop) - 20, Size: 40}, true},
		"above the block":               {compose.Ring{AtMs: 0, X: 360, Y: int(blockTop) - 21, Size: 40}, false},
		"after the cue":                 {compose.Ring{AtMs: 2500, X: 360, Y: 1200, Size: 40}, false},
		"ends as the cue starts (-0.5)": {compose.Ring{AtMs: -compose.RingMs, X: 360, Y: 1200, Size: 40}, false},
	} {
		frames := placed(t, "clean", PositionPreset, 720, 1280, c, []compose.Ring{tc.ring})
		for _, pf := range frames {
			if tc.flipped {
				assert.InDelta(t, 90, pf.top, 0, name)
			} else {
				assert.InDelta(t, 1280-90-pf.block.h, pf.top, 0, name)
			}
		}
	}

	// A chosen center does not flip; a bar flips flush with the top.
	ring := []compose.Ring{{AtMs: 0, X: 360, Y: 1260, Size: 40}}
	for _, pf := range placed(t, "clean", PositionCenter, 720, 1280, c, ring) {
		assert.InDelta(t, 640, pf.top+pf.block.h/2, 1)
	}

	for _, pf := range placed(t, "bar", PositionPreset, 720, 1280, c, ring) {
		assert.InDelta(t, 0, pf.top, 0)
	}
}
