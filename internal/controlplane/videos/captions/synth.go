// Ported from demotape (https://github.com/gabosarmiento/demotape) at commit b268f1b,
// Sources/DemoTape/CaptionBurner.swift: synthesizeWords (lines 319-337). Issue #908, row 14.
//
// What differs, and why: a word's length is counted in letters of its NFC form, as Swift counts a String's
// Characters; it times the words of the look sample's line, which has no voice yet, where demotape times a cue whose
// transcript has no word times.
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
	"math"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// CharactersPerSecond is the speaking rate words are timed at when no voice says them (demotape's default).
const CharactersPerSecond = 14.0

// SynthesizeWords times the words of text from start to end as if spoken: each gets the longer of its speaking time
// at charactersPerSecond and minWordDuration, laid end to end from start, and scaled to fit when they run past end.
func SynthesizeWords(text string, start, end, charactersPerSecond, minWordDuration float64) []Word {
	tokens := strings.FieldsFunc(text, func(r rune) bool { return r == ' ' || r == '\n' })
	if len(tokens) == 0 {
		return nil
	}

	cue := math.Max(0.01, end-start)
	durations := make([]float64, len(tokens))
	total := 0.0

	for k, tok := range tokens {
		durations[k] = math.Max(minWordDuration, float64(max(1, utf8.RuneCountInString(norm.NFC.String(tok))))/
			charactersPerSecond)
		total += durations[k]
	}

	if total > cue {
		scale := cue / total
		for k := range durations {
			durations[k] *= scale
		}
	}

	out := make([]Word, 0, len(tokens))
	t := start

	for k, tok := range tokens {
		out = append(out, Word{Word: tok, Start: t, End: math.Min(end, t+durations[k])})
		t += durations[k]
	}

	return out
}
