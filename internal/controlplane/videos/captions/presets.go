// Ported from demotape (https://github.com/gabosarmiento/demotape) at commit b268f1b,
// Sources/DemoTape/CaptionStyle.swift: CaptionPosition (line 5), the fields of CaptionStyle (lines 9-37),
// displayWord (line 42), maxWordsPerLine (line 63), the catalog (lines 69-137) and byID (line 88). The "bar"
// preset is from aidemo (https://github.com/tandryukha/aidemo) at commit b67235a, src/caption-render.ts (lines
// 61-76), under the same license, Copyright (c) 2026 Andrii Taran. Issue #908, rows 6, 7 and 9.
//
// What differs, and why: each macOS font is mapped to a bundled OFL font with every Vietnamese letter (row 7:
// Avenir Next Heavy to Be Vietnam Pro ExtraBold, Avenir Next Bold and Helvetica Neue Bold to Be Vietnam Pro Bold,
// Helvetica Neue Medium to Be Vietnam Pro Medium, SF Mono Semibold to JetBrains Mono SemiBold; the mapping is an
// inference); scaleActive is left out, as demotape's burner never reads it; "bar" takes Clean's size, font and
// color (an inference) with aidemo's band color, full width and no rounding; "legacy" is the captions made
// before the presets, kept for the revisions made with them (Default's ASS, unchanged); and a position "top" joins
// demotape's three (7% from the top, an inference).
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

import "strings"

// Position is where captions sit on the video (issue #908, row 9).
type Position string

// The positions: the preset's own, or one chosen.
const (
	PositionPreset     Position = "preset"
	PositionBottom     Position = "bottom"
	PositionLowerThird Position = "lower_third"
	PositionCenter     Position = "center"
	PositionTop        Position = "top"
)

// Positions are the positions, in the order they are offered.
var Positions = []Position{PositionPreset, PositionBottom, PositionLowerThird, PositionCenter, PositionTop}

// The fonts of the presets, by their full names, as ASS asks for them with Bold=0.
const (
	fontExtraBold = "Be Vietnam Pro ExtraBold"
	fontBold      = "Be Vietnam Pro Bold"
	fontMedium    = "Be Vietnam Pro Medium"
	fontMono      = "JetBrains Mono SemiBold"
)

// Preset is a caption look. Animated presets use each word's times (pop-in, karaoke highlight); the others show a
// phrase.
type Preset struct {
	ID       string
	Name     string
	Animated bool

	// Font is the font's full name.
	Font      string
	Uppercase bool
	// Base is a word's color, Active the spoken word's (animated), Future the words not spoken yet ("" is Base).
	Base, Active, Future string
	Outline              bool
	OutlineColor         string
	// Box is the rounded box behind the words, #RRGGBBAA; "" is none.
	Box string

	// RevealFuture: false, the words not spoken yet are hidden (they pop in); true, they are shown.
	RevealFuture        bool
	BaseMaxWordsPerLine int
	Position            Position

	// WordsAtATime is how many words are on screen at once, swapped as they are spoken; 0 is as much of the phrase
	// as fits.
	WordsAtATime int
	// FontScale multiplies the type's size.
	FontScale float64

	// Bar: the box is a band the video's width, flush with its edge (aidemo's "bar").
	Bar bool
}

// WordByWord says whether the preset shows a word or two at a time.
func (p Preset) WordByWord() bool { return p.WordsAtATime > 0 }

// HasBox says whether the words sit on a box or a band.
func (p Preset) HasBox() bool { return p.Box != "" }

// MaxWordsPerLine is the words a line holds, at most two on a square or tall video.
func (p Preset) MaxWordsPerLine(aspect float64) int {
	if aspect <= 1.05 {
		return max(1, min(p.BaseMaxWordsPerLine, 2))
	}

	return p.BaseMaxWordsPerLine
}

// DisplayWord is a word as shown: when only a word or two is on screen, a trailing comma, semicolon or colon is
// dropped ("PRODUCTION," alone reads as a mistake); sentence enders stay, as they carry tone.
func DisplayWord(text string, wordByWord bool) string {
	if !wordByWord {
		return text
	}

	if strings.HasSuffix(text, ",") || strings.HasSuffix(text, ";") || strings.HasSuffix(text, ":") {
		return text[:len(text)-1]
	}

	return text
}

// The preset ids that are not demotape's.
const (
	// Legacy is the captions made before the presets (#818), shown as "Classic".
	Legacy = "legacy"
	// DefaultPreset is the preset a look without one gets, demotape's fallback by id.
	DefaultPreset = "clean"
)

// Presets are the presets offered, in order: demotape's eleven, aidemo's bar, and Classic.
var Presets = []Preset{
	// Word by word: big, low in the frame, never more than a word or two.
	{
		ID: "one-word", Name: "One Word", Animated: true, Font: fontExtraBold, Uppercase: true,
		Base: "#FFFFFF", Active: "#FFFFFF", Outline: true, OutlineColor: "#0B0B0B",
		RevealFuture: false, BaseMaxWordsPerLine: 1, Position: PositionBottom, WordsAtATime: 1, FontScale: 1.75,
	},
	{
		ID: "word-pair", Name: "Word Pair", Animated: true, Font: fontExtraBold, Uppercase: true,
		Base: "#FFFFFF", Active: "#FFE83D", Outline: true, OutlineColor: "#141000",
		RevealFuture: true, BaseMaxWordsPerLine: 2, Position: PositionBottom, WordsAtATime: 2, FontScale: 1.35,
	},
	// Animated.
	{
		ID: "pop", Name: "Pop", Animated: true, Font: fontExtraBold, Uppercase: true,
		Base: "#F0641E", Active: "#FFC400", Outline: true, OutlineColor: "#1A0E00",
		RevealFuture: false, BaseMaxWordsPerLine: 3, Position: PositionCenter, FontScale: 1,
	},
	{
		ID: "karaoke", Name: "Karaoke", Animated: true, Font: fontBold, Uppercase: false,
		Base: "#141414", Active: "#0A0A0A", Future: "#9A9A9A", Outline: false, OutlineColor: "#000000",
		Box: "#F4F4F2E6", RevealFuture: true, BaseMaxWordsPerLine: 4, Position: PositionLowerThird, FontScale: 1,
	},
	{
		ID: "highlight-yellow", Name: "Highlight", Animated: true, Font: fontExtraBold, Uppercase: true,
		Base: "#FFFFFF", Active: "#FFD400", Outline: true, OutlineColor: "#101010",
		RevealFuture: true, BaseMaxWordsPerLine: 3, Position: PositionCenter, FontScale: 1,
	},
	{
		ID: "highlight-green", Name: "Pop Green", Animated: true, Font: fontExtraBold, Uppercase: true,
		Base: "#FFFFFF", Active: "#33E06A", Outline: true, OutlineColor: "#0A1E10",
		RevealFuture: false, BaseMaxWordsPerLine: 2, Position: PositionCenter, FontScale: 1,
	},
	// Static.
	{
		ID: "clean", Name: "Clean", Animated: false, Font: fontBold, Uppercase: false,
		Base: "#FFFFFF", Active: "#FFFFFF", Outline: false, OutlineColor: "#000000",
		Box: "#0000008C", RevealFuture: true, BaseMaxWordsPerLine: 6, Position: PositionBottom, FontScale: 1,
	},
	{
		ID: "bold", Name: "Bold", Animated: false, Font: fontExtraBold, Uppercase: true,
		Base: "#FFFFFF", Active: "#FFFFFF", Outline: true, OutlineColor: "#101010",
		RevealFuture: true, BaseMaxWordsPerLine: 4, Position: PositionLowerThird, FontScale: 1,
	},
	{
		ID: "minimal", Name: "Minimal", Animated: false, Font: fontMedium, Uppercase: false,
		Base: "#FFFFFF", Active: "#FFFFFF", Outline: true, OutlineColor: "#0000009C",
		RevealFuture: true, BaseMaxWordsPerLine: 7, Position: PositionBottom, FontScale: 1,
	},
	{
		ID: "boxed", Name: "Boxed", Animated: false, Font: fontBold, Uppercase: true,
		Base: "#FFFFFF", Active: "#FFFFFF", Outline: false, OutlineColor: "#000000",
		Box: "#E0641EF2", RevealFuture: true, BaseMaxWordsPerLine: 4, Position: PositionBottom, FontScale: 1,
	},
	// For product demos: monospaced, on a dark slab, quietly at the bottom.
	{
		ID: "mono", Name: "Mono", Animated: false, Font: fontMono, Uppercase: false,
		Base: "#F2F4F7", Active: "#F2F4F7", Outline: false, OutlineColor: "#000000",
		Box: "#0D1117D9", RevealFuture: true, BaseMaxWordsPerLine: 8, Position: PositionBottom, FontScale: 1,
	},
	// aidemo's bar: a band the video's width, flush with its bottom edge, rgba(12,14,22,.66).
	{
		ID: "bar", Name: "Bar", Animated: false, Font: fontBold, Uppercase: false,
		Base: "#FFFFFF", Active: "#FFFFFF", Outline: false, OutlineColor: "#000000",
		Box: "#0C0E16A8", RevealFuture: true, BaseMaxWordsPerLine: 6, Position: PositionBottom, FontScale: 1,
		Bar: true,
	},
	{ID: Legacy, Name: "Classic"},
}

// PresetByID is the preset of an id, and whether there is one.
func PresetByID(id string) (Preset, bool) {
	for _, p := range Presets {
		if p.ID == id {
			return p, true
		}
	}

	return Preset{}, false
}
