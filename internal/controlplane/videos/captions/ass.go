// Ported from demotape (https://github.com/gabosarmiento/demotape) at commit b268f1b,
// Sources/DemoTape/CaptionBurner.swift: normalize (line 279) and highlightMinSeconds (line 270), and the word
// highlight of makeLine and wordColor (lines 425-449) with the colors of its "highlight-yellow" style
// (Sources/DemoTape/CaptionStyle.swift, line 101). Issue #818, row 6.
//
// What differs, and why: demotape draws each frame's caption with Core Text; here each stretch of a cue in which
// the spoken word stays the same is one ASS event, burned by libass, the spoken word in the active color; words
// not spoken yet and spoken already are both the base color, as in that style (its futureHex is nil).
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
	"math"
	"slices"
	"strings"
)

// HighlightMinSeconds is the shortest a word is highlighted: a few frames, so that a word a transcript gives no
// length is still seen.
const HighlightMinSeconds = 0.14

// FontName is the font the captions are set in, Be Vietnam Pro SemiBold, which the ffmpeg install writes to its
// fonts folder.
const FontName = "Be Vietnam Pro SemiBold"

// The colors, as ASS writes them (&HBBGGRR): white words, the spoken one #FFD400.
const (
	baseColour   = "&H00FFFFFF"
	activeColour = "&H0000D4FF"
)

// Normalize makes a cue's words safe to highlight: every word at least
// minWordDuration long, in order, with no overlaps, and the whole run kept
// inside [cueStart, cueEnd]. Real timings keep their onset where they can; a
// word is only pushed later when the one before it needed the floor. If the
// floor would run past the cue, the run is scaled to fit.
func Normalize(words []Word, minWordDuration, cueStart, cueEnd float64) []Word {
	if len(words) == 0 {
		return nil
	}

	start0 := math.Min(words[0].Start, cueEnd)
	cursor := start0
	out := make([]Word, 0, len(words))

	for _, w := range words {
		s := math.Max(w.Start, cursor)
		e := s + math.Max(minWordDuration, w.End-w.Start)
		out = append(out, Word{Word: w.Word, Start: s, End: e})
		cursor = e
	}

	total, available := cursor-start0, cueEnd-start0
	if total > available && total > 0 {
		k := available / total
		for i := range out {
			out[i].Start = start0 + (out[i].Start-start0)*k
			out[i].End = start0 + (out[i].End-start0)*k
		}
	}

	return out
}

// ASS is the captions as an ASS file for a video of width × height: each
// cue at the bottom, wrapped as Wrap does, with one event for each stretch in
// which the word spoken is the same.
func ASS(cues []Cue, width, height int, cfg Config) string {
	size := FontSize(width, height, cfg.MaxCharsPerLine)

	var b strings.Builder

	fmt.Fprintf(&b, "[Script Info]\nScriptType: v4.00+\nWrapStyle: 2\nScaledBorderAndShadow: yes\n"+
		"PlayResX: %d\nPlayResY: %d\n\n", width, height)
	b.WriteString("[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, " +
		"BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, " +
		"Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	fmt.Fprintf(&b, "Style: Default,%s,%d,%s,%s,&H00101010,&H80000000,0,0,0,0,100,100,0,0,1,%d,0,2,%d,%d,%d,1\n\n",
		FontName, size, baseColour, baseColour, max(2, size/14), width/20, width/20, height/14)
	b.WriteString("[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")

	for _, c := range cues {
		start, end := float64(c.StartMs)/1000, float64(c.EndMs)/1000
		words := Normalize(c.Words, HighlightMinSeconds, start, end)
		lines := Wrap(c.Text, cfg.MaxCharsPerLine)

		for _, span := range stretches(words, start, end) {
			fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n", //nolint:misspell // ASS's own word.
				assTime(span.from), assTime(span.to), highlighted(lines, span.active))
		}
	}

	return b.String()
}

// FontSize is the captions' size: a line of chars characters fills about nine tenths of the width, and is never
// taller than a fourteenth of the height.
func FontSize(width, height, chars int) int {
	return max(12, min(int(math.Round(float64(width)*0.88/(float64(chars)*0.55))), height/14))
}

type stretch struct {
	from, to float64
	// active is the word spoken in it; -1 for none.
	active int
}

// stretches cut a cue at each word's start and end.
func stretches(words []Word, start, end float64) []stretch {
	cuts := []float64{start, end}
	for _, w := range words {
		cuts = append(cuts, w.Start, w.End)
	}

	slices.Sort(cuts)
	cuts = slices.Compact(cuts)

	var out []stretch

	for i := 0; i+1 < len(cuts); i++ {
		from, to := cuts[i], cuts[i+1]
		if from < start || to > end || to-from < 0.0005 {
			continue
		}

		active := -1

		for k, w := range words {
			if w.Start <= from && from < w.End {
				active = k
			}
		}

		if n := len(out); n > 0 && out[n-1].active == active && math.Abs(out[n-1].to-from) < 1e-9 {
			out[n-1].to = to

			continue
		}

		out = append(out, stretch{from: from, to: to, active: active})
	}

	return out
}

// highlighted is a cue's lines with its active word in the active color: the
// cue's words are its text's, in order, as cueText joins them.
func highlighted(lines []string, active int) string {
	k := 0
	out := make([]string, 0, len(lines))

	for _, line := range lines {
		fields := strings.Fields(line)
		for i, f := range fields {
			f = escape(f)
			if k == active {
				f = "{\\c" + activeColour + "&}" + f + "{\\c" + baseColour + "&}"
			}

			fields[i] = f
			k++
		}

		out = append(out, strings.Join(fields, " "))
	}

	return strings.Join(out, `\N`)
}

// escape keeps a word's text from being read as an ASS override or a line break.
func escape(s string) string {
	return strings.NewReplacer(`\`, `/`, "{", "(", "}", ")").Replace(s)
}

// assTime is a time as ASS writes it: h:mm:ss.cc.
func assTime(sec float64) string {
	cs := assCentiseconds(sec)

	return fmt.Sprintf("%d:%02d:%02d.%02d", cs/360000, cs/6000%60, cs/100%60, cs%100)
}

func assCentiseconds(sec float64) int64 { return int64(math.Round(sec * 100)) }
