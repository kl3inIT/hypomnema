// Ported from demotape (https://github.com/gabosarmiento/demotape) at commit b268f1b,
// Tests/DemoTapeTests/CaptionStyleTests.swift (lines 11-129: the catalog, mobile word wrapping, hex parsing,
// word-by-word styles) and Tests/DemoTapeTests/CaptionBurnerTests.swift (lines 11-86: windows and width fitting).
// Issue #908, PR 1, check 1. The preview image's test is left out, as no preview image is drawn here.
//
// MIT License
//
// Copyright (c) 2026 DemoTape contributors
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package captions

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// demotapes are demotape's eleven styles.
func demotapes() []Preset {
	var out []Preset

	for _, p := range Presets {
		if p.ID != Legacy && !p.Bar {
			out = append(out, p)
		}
	}

	return out
}

func TestCatalogOffersBothAnimatedAndStaticStyles(t *testing.T) {
	t.Parallel()

	var animated, static int

	for _, p := range demotapes() {
		if p.Animated {
			animated++
		} else {
			static++
		}

		assert.NotEmpty(t, p.Name, p.ID)
		assert.Positive(t, p.FontScale, p.ID)
	}

	assert.Len(t, demotapes(), 11)
	assert.GreaterOrEqual(t, animated, 4)
	assert.GreaterOrEqual(t, static, 4)
}

func TestCatalogIDsStayUniqueAndResolvable(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}

	for _, p := range Presets {
		assert.False(t, seen[p.ID], p.ID)
		seen[p.ID] = true
		assert.Equal(t, p.ID, ByID(p.ID).ID)
	}

	assert.Equal(t, "karaoke", ByID("karaoke").ID)
	assert.Equal(t, "clean", ByID("does-not-exist").ID, "unknown falls back to Clean, never crashes")
	assert.Equal(t, DefaultPreset, ByID("nope").ID)
}

func TestMobileAspectTightensWordsPerLine(t *testing.T) {
	t.Parallel()

	for _, p := range demotapes() {
		assert.LessOrEqual(t, p.MaxWordsPerLine(1.0), 2, p.ID)
		assert.LessOrEqual(t, p.MaxWordsPerLine(0.5625), 2, p.ID, "9:16")
		assert.GreaterOrEqual(t, p.MaxWordsPerLine(1.0), 1, p.ID, "never below one")
	}

	clean := ByID("clean")
	assert.Equal(t, clean.BaseMaxWordsPerLine, clean.MaxWordsPerLine(16.0/9.0), "a wide video keeps its base")
}

func TestHexParses(t *testing.T) {
	t.Parallel()

	r, g, b, a := rgba("#FF8000")
	assert.Equal(t, [4]int{255, 128, 0, 255}, [4]int{r, g, b, a})

	_, _, _, a = rgba("#00000080")
	assert.Equal(t, 128, a)

	assert.Equal(t, "&H73000000", assColor("#0000008C"), "an ASS alpha is a transparency")
	assert.Equal(t, "&H3DE8FF&", colorOnly("#FFE83D"))
}

func TestWordByWordStylesDeclareTheirWordCount(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 1, ByID("one-word").WordsAtATime)
	assert.Equal(t, 2, ByID("word-pair").WordsAtATime)
	assert.True(t, ByID("one-word").WordByWord())
	assert.False(t, ByID("clean").WordByWord())
	assert.Equal(t, 0, ByID("clean").WordsAtATime)

	for _, id := range []string{"one-word", "word-pair"} {
		assert.Greater(t, ByID(id).FontScale, 1.2, "%s should be noticeably bigger", id)
		assert.Equal(t, PositionBottom, ByID(id).Position, "%s should sit low in the frame", id)
	}
}

func TestTrailingCommaIsDroppedOnlyWordByWord(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "production", DisplayWord("production,", true))
	assert.Equal(t, "production,", DisplayWord("production,", false))
	assert.Equal(t, "nhập", DisplayWord("nhập;", true))
	assert.Equal(t, "nhập", DisplayWord("nhập:", true))
	assert.Equal(t, "Refusé.", DisplayWord("Refusé.", true), "sentence enders carry tone")
	assert.Equal(t, "really?", DisplayWord("really?", true))
	assert.Equal(t, "stop!", DisplayWord("stop!", true))
}

func words(n int) []Word {
	out := make([]Word, n)
	for i := range out {
		out[i] = Word{Word: fmt.Sprintf("w%d", i), Start: float64(i), End: float64(i) + 1}
	}

	return out
}

func texts(ws []Word) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.Word)
	}

	return out
}

func TestWindow(t *testing.T) {
	t.Parallel()

	// A short cue that fits in one window is returned whole.
	idx, out := Window(0.5, words(4), 6)
	assert.Equal(t, 0, idx)
	assert.Len(t, out, 4)

	// A long cue is split; early time, the first window, never the whole paragraph.
	ws := words(13)
	idx, out = Window(0.5, ws, 6)
	assert.Equal(t, 0, idx)
	assert.Equal(t, []string{"w0", "w1", "w2", "w3", "w4", "w5"}, texts(out))

	idx, out = Window(7.5, ws, 6)
	assert.Equal(t, 1, idx)
	assert.Equal(t, "w6", out[0].Word)
	assert.Len(t, out, 6)

	idx, out = Window(12.5, ws, 6)
	assert.Equal(t, 2, idx)
	assert.Equal(t, []string{"w12"}, texts(out))

	// Past the end of the last word, the last window rather than none.
	idx, out = Window(999, ws, 6)
	assert.Equal(t, 2, idx)
	assert.Equal(t, []string{"w12"}, texts(out))

	// No window exceeds its size.
	for t10 := range 40 {
		_, out := Window(float64(t10)/2, words(20), 4)
		assert.LessOrEqual(t, len(out), 4)
		assert.NotEmpty(t, out)
	}

	// A single word window advances with time.
	for at, want := range map[float64]string{0.5: "w0", 2.5: "w2", 3.9: "w3"} {
		_, out := Window(at, words(4), 1)
		assert.Equal(t, []string{want}, texts(out))
	}
}

func TestWidthFitScale(t *testing.T) {
	t.Parallel()

	// A block that already fits is never touched, nor inflated.
	assert.InDelta(t, 1.0, WidthFitScale(600, 1080, SafeFraction), 0)
	assert.InDelta(t, 1.0, WidthFitScale(100, 1080, SafeFraction), 0)

	// A block wider than the frame is scaled to the safe width.
	scale := WidthFitScale(1400, 1080, SafeFraction)
	assert.Less(t, scale, 1.0)
	assert.InDelta(t, 1080*0.94, 1400*scale, 0.001)

	for w := 200.0; w <= 5000; w += 137 {
		assert.LessOrEqual(t, w*WidthFitScale(w, 1080, SafeFraction), 1080*0.94+0.001, "block %v", w)
	}

	// Degenerate sizes give no zero or NaN scale.
	assert.InDelta(t, 1.0, WidthFitScale(0, 1080, SafeFraction), 0)
	assert.InDelta(t, 1.0, WidthFitScale(500, 0, SafeFraction), 0)
	assert.InDelta(t, 1.0, WidthFitScale(500, 1080, 0), 0)
}

// The preset table, field by field: demotape's CaptionStyle.swift, lines 75-137, with each font mapped (#908, row
// 7), aidemo's bar (src/caption-render.ts, lines 61-76), and Classic.
func TestPresetTable(t *testing.T) {
	t.Parallel()

	want := []Preset{
		{"one-word", "One Word", true, "Be Vietnam Pro ExtraBold", true, "#FFFFFF", "#FFFFFF", "", true, "#0B0B0B", "",
			false, 1, PositionBottom, 1, 1.75, false},
		{"word-pair", "Word Pair", true, "Be Vietnam Pro ExtraBold", true, "#FFFFFF", "#FFE83D", "", true, "#141000", "",
			true, 2, PositionBottom, 2, 1.35, false},
		{"pop", "Pop", true, "Be Vietnam Pro ExtraBold", true, "#F0641E", "#FFC400", "", true, "#1A0E00", "",
			false, 3, PositionCenter, 0, 1, false},
		{"karaoke", "Karaoke", true, "Be Vietnam Pro Bold", false, "#141414", "#0A0A0A", "#9A9A9A", false, "#000000",
			"#F4F4F2E6", true, 4, PositionLowerThird, 0, 1, false},
		{"highlight-yellow", "Highlight", true, "Be Vietnam Pro ExtraBold", true, "#FFFFFF", "#FFD400", "", true,
			"#101010", "", true, 3, PositionCenter, 0, 1, false},
		{"highlight-green", "Pop Green", true, "Be Vietnam Pro ExtraBold", true, "#FFFFFF", "#33E06A", "", true,
			"#0A1E10", "", false, 2, PositionCenter, 0, 1, false},
		{"clean", "Clean", false, "Be Vietnam Pro Bold", false, "#FFFFFF", "#FFFFFF", "", false, "#000000",
			"#0000008C", true, 6, PositionBottom, 0, 1, false},
		{"bold", "Bold", false, "Be Vietnam Pro ExtraBold", true, "#FFFFFF", "#FFFFFF", "", true, "#101010", "",
			true, 4, PositionLowerThird, 0, 1, false},
		{"minimal", "Minimal", false, "Be Vietnam Pro Medium", false, "#FFFFFF", "#FFFFFF", "", true, "#0000009C", "",
			true, 7, PositionBottom, 0, 1, false},
		{"boxed", "Boxed", false, "Be Vietnam Pro Bold", true, "#FFFFFF", "#FFFFFF", "", false, "#000000",
			"#E0641EF2", true, 4, PositionBottom, 0, 1, false},
		{"mono", "Mono", false, "JetBrains Mono SemiBold", false, "#F2F4F7", "#F2F4F7", "", false, "#000000",
			"#0D1117D9", true, 8, PositionBottom, 0, 1, false},
		{"bar", "Bar", false, "Be Vietnam Pro Bold", false, "#FFFFFF", "#FFFFFF", "", false, "#000000",
			"#0C0E16A8", true, 6, PositionBottom, 0, 1, true},
		{ID: "legacy", Name: "Classic"},
	}

	require.Len(t, Presets, len(want))

	for i := range want {
		assert.Equal(t, want[i], Presets[i], want[i].ID)
	}
}
