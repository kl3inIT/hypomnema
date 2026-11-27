package captions

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
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

	// Every cue's frames laid out, as estimated, and placed; fit is left out, as these tests need no ffmpeg.
	laid, err := r.layoutCues(cues)
	require.NoError(t, err)

	return r.placeCues(laid, pos, rings)
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
		got, err := Render(t.Context(), ffmpeg.Tools{}, cues, Look{Preset: Legacy, Position: PositionPreset},
			size[0], size[1], []compose.Ring{{AtMs: 1000, X: 360, Y: 1200, Size: 101}})
		require.NoError(t, err)
		assert.Equal(t, positionedASS(cues, size[0], size[1], Default, PositionPreset), got)
	}
}

func TestRenderRefusesWhatIsNotThere(t *testing.T) {
	t.Parallel()

	_, err := Render(t.Context(), ffmpeg.Tools{}, nil, Look{Preset: "sparkle"}, 720, 1280, nil)
	require.ErrorContains(t, err, `"sparkle"`)

	_, err = Render(t.Context(), ffmpeg.Tools{}, nil, Look{Preset: "clean", Position: "left"}, 720, 1280, nil)
	require.ErrorContains(t, err, `"left"`)

	look, err := resolveLook(Look{})
	require.NoError(t, err)
	assert.Equal(t, Look{Preset: "clean", Position: PositionPreset}, look, "no preset is Clean")

	// A preset is fitted by libass's drawing: without ffmpeg, it is refused, never let through unmeasured.
	_, err = Render(t.Context(), ffmpeg.Tools{}, []Cue{cue(0.3, "Xin", "chào")}, Look{}, 720, 1280, nil)
	require.ErrorContains(t, err, "ffmpeg is needed")
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

// The width cap, as estimated before libass draws it (issue #908, row 8; fit.go then measures what libass drew): a
// 60-character token at 720 px shrinks to the 20 px floor and is squeezed into 0.94 W, with or without a box.
func TestTheWidthCapSqueezesAtTheFloor(t *testing.T) {
	t.Parallel()

	token := strings.Repeat("W", 60)

	for _, preset := range []string{"one-word", "word-pair", "clean", "bar"} {
		r, err := newRenderer(preset, 720, 1280)
		require.NoError(t, err)

		frames := placed(t, preset, PositionPreset, 720, 1280, []Cue{cue(1, token)}, nil)
		require.NotEmpty(t, frames, preset)

		for _, pf := range frames {
			assert.InDelta(t, MinFontSize, pf.block.em, 1e-9, preset)
			assert.Less(t, pf.block.fx, 1.0, preset)
			assert.LessOrEqual(t, r.drawn(pf.block), 0.94*720, preset)
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

// A character the preset's font has no glyph for is folded to one it has (NFKC) or left out, so libass never
// falls back to a font of the machine (the review of #943; the coordinator's choice, 2026-11-19).
func TestOnlyWhatTheFontDrawsIsShown(t *testing.T) {
	t.Parallel()

	for preset, tc := range map[string]struct{ words, want []string }{
		"one-word": {[]string{"ﬃﬃ", "Ổn✅", "😀😀", "định"}, []string{"FFIFFI", "ỔN", "ĐỊNH"}},
		// Unicode's full uppercase, as demotape's Swift uppercased() (CaptionBurner.swift, line 435): ß is SS.
		"word-pair": {[]string{"straße", "ﬀ"}, []string{"STRASSE", "FF"}},
		"clean":     {[]string{"ﬃ→", "Ổn✅", "😀😀", "định"}, []string{"ffi", "Ổn", "định"}},
		"mono":      {[]string{"ＡＢＣ", "Ổn✅", "😀😀", "định"}, []string{"ABC", "Ổn", "định"}},
	} {
		var seen []string

		for _, pf := range placed(t, preset, PositionPreset, 1280, 720, []Cue{cue(0.5, tc.words...)}, nil) {
			for _, w := range shownWords(pf) {
				if !slices.Contains(seen, w) {
					seen = append(seen, w)
				}
			}
		}

		assert.Equal(t, tc.want, seen, preset)
	}

	// A cue of nothing drawable draws nothing.
	assert.Empty(t, placed(t, "clean", PositionPreset, 720, 1280, []Cue{cue(0.5, "😀", "✅")}, nil))
}

// overlapping is the fourth review's speech: "xong." is still said when "Bây" starts, so the cue of the first
// sentence ends (1.10 s) after the second starts (1.00 s).
func overlapping(t *testing.T) []Cue {
	t.Helper()

	cues, err := Build([]Scene{{StartMs: 0, EndMs: 3000, Words: []Word{
		{"Đăng", 0.2, 0.5}, {"nhập", 0.5, 0.8}, {"xong.", 0.8, 1.1},
		{"Bây", 1.0, 1.3}, {"giờ", 1.3, 1.6}, {"kiểm", 1.6, 1.9}, {"tra.", 1.9, 2.2},
	}}}, Default)
	require.NoError(t, err)
	require.Len(t, cues, 2)
	require.Greater(t, cues[0].EndMs, cues[1].StartMs, "the cues overlap")

	return cues
}

// Only the first cue that covers an instant is shown, as demotape draws (CaptionBurner.swift, line 187): a cue
// starts to show once the cues before it end, so no two are ever drawn at once.
func TestOverlappingCuesAreShownOneAtATime(t *testing.T) {
	t.Parallel()

	cues := overlapping(t)

	for _, p := range Presets {
		if p.ID == Legacy {
			continue
		}

		frames := placed(t, p.ID, PositionPreset, 720, 1280, cues, nil)

		for i, a := range frames {
			for _, b := range frames[i+1:] {
				assert.False(t, a.from < b.to-1e-9 && b.from < a.to-1e-9,
					"%s: [%.2f, %.2f) %v and [%.2f, %.2f) %v at once", p.ID, a.from, a.to, shownWords(a),
					b.from, b.to, shownWords(b))
			}
		}

		var second []placedFrame

		for _, pf := range frames {
			for _, w := range shownWords(pf) {
				if strings.EqualFold(w, "Bây") {
					second = append(second, pf)
				}
			}
		}

		require.NotEmpty(t, second, p.ID)
		assert.InDelta(t, 1.1, second[0].from, 1e-9, "%s: the second cue shows once the first ends", p.ID)
	}
}

// ringBeforeShown is the fifth review's speech: the second sentence starts at 450 ms, while "xong." is said until
// 560 ms, so it shows from 560 ms; a tap's ring at the bottom shows from 0 to 500 ms.
func ringBeforeShown(t *testing.T) ([]Cue, []compose.Ring) {
	t.Helper()

	cues, err := Build([]Scene{{StartMs: 0, EndMs: 3000, Words: []Word{
		{"Đăng", 0, 0.2}, {"nhập", 0.2, 0.35}, {"xong.", 0.35, 0.56},
		{"Bây", 0.45, 0.7}, {"giờ", 0.7, 1.0}, {"kiểm", 1.0, 1.3}, {"tra.", 1.3, 1.6},
	}}}, Default)
	require.NoError(t, err)
	require.Len(t, cues, 2)
	require.Equal(t, int64(450), cues[1].StartMs)
	require.Equal(t, int64(560), cues[0].EndMs)

	return cues, []compose.Ring{{AtMs: 0, X: 360, Y: 1200, Size: 101}}
}

// A ring flips a cue only while the cue is drawn (row 9, aidemo src/compose.ts, lines 1994-1999): the first
// sentence, drawn with the ring, sits at the top, whole; the second, drawn only from 560 ms, after the ring ended
// at 500 ms, stays at the bottom, though its own start (450 ms) is within the ring's.
func TestARingFlipsOnlyTheCueItIsDrawnWith(t *testing.T) {
	t.Parallel()

	cues, rings := ringBeforeShown(t)

	for _, preset := range []string{"clean", "one-word", "bar"} {
		r, err := newRenderer(preset, 720, 1280)
		require.NoError(t, err)

		frames := placed(t, preset, PositionPreset, 720, 1280, cues, rings)
		require.NotEmpty(t, frames)

		for _, pf := range frames {
			if pf.from < 0.56-1e-9 {
				assert.InDelta(t, r.top(PositionTop, pf.block.h), pf.top, 0, "%s: the first sentence at the top", preset)
			} else {
				assert.InDelta(t, r.top(PositionBottom, pf.block.h), pf.top, 0,
					"%s: %v at %.2f s stays at the bottom", preset, shownWords(pf), pf.from)
			}
		}
	}
}

// pauseCue is the sixth review's speech, "Rồi, bây giờ kiểm tra.": a pause after "Rồi," from 450 ms to 1.25 s, in
// which One Word draws nothing.
func pauseCue(t *testing.T) []Cue {
	t.Helper()

	// Each word at least One Word's floor (0.42 s), so that none is moved back into the pause.
	cues, err := Build([]Scene{{StartMs: 0, EndMs: 4000, Words: []Word{
		{"Rồi,", 0, 0.45}, {"bây", 1.25, 1.7}, {"giờ", 1.7, 2.15}, {"kiểm", 2.15, 2.6}, {"tra.", 2.6, 3.05},
	}}}, Default)
	require.NoError(t, err)
	require.Len(t, cues, 1)

	return cues
}

// A ring flips a cue only while one of its frames is drawn, not in a pause between them where nothing is (row 9,
// aidemo src/compose.ts, lines 1994-1999): a ring from 640 ms, in the pause, leaves One Word at the bottom; one
// from 250 ms, while "RỒI" is drawn, moves the whole cue to the top.
func TestARingInAPauseFlipsNothing(t *testing.T) {
	t.Parallel()

	cues := pauseCue(t)
	r, err := newRenderer("one-word", 720, 1280)
	require.NoError(t, err)

	frames := placed(t, "one-word", PositionPreset, 720, 1280, cues, nil)
	for _, pf := range frames {
		assert.False(t, pf.from < 1.25-1e-9 && pf.to > 0.45+1e-9, "nothing is drawn in the pause: %v", shownWords(pf))
	}

	for atMs, top := range map[int64]bool{640: false, 250: true} {
		ring := []compose.Ring{{AtMs: atMs, X: 360, Y: 1200, Size: 101}}

		for _, pf := range placed(t, "one-word", PositionPreset, 720, 1280, cues, ring) {
			want := r.top(PositionBottom, pf.block.h)
			if top {
				want = r.top(PositionTop, pf.block.h)
			}

			assert.InDelta(t, want, pf.top, 0, "ring at %d ms: %v at %.2f s", atMs, shownWords(pf), pf.from)
		}
	}
}
