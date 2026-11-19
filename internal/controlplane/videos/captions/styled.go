// Ported from demotape (https://github.com/gabosarmiento/demotape) at commit b268f1b,
// Sources/DemoTape/CaptionBurner.swift: captionOverlay (lines 185-231: the window of words on screen, the words
// popped in, the active word, the position by aspect and its offsets), window (line 235), wordsForCue's floors
// (lines 252-270), widthFitScale (line 349), minCaptionFontSize (line 358), drawBlock (lines 360-423: the ideal
// size, padding, line height and box radius, the shrink to the width) and wordColor (line 444). Issue #908, rows 8
// and 9.
//
// What differs, and why: demotape draws each frame's block with Core Text; here each stretch of a cue in which
// what is shown stays the same is one block of ASS events, burned by libass: the box a \p1 rounded rectangle on
// layer 0, each line of words an \an8\pos event on layer 1, as agent-demo-video writes one event a state of its
// growing words (src/captions.ts, toWordAss, lines 93-125). The size is an em, given to libass as its Fontsize
// through the font's OS/2 metrics (fonts.go), and a line's top is libass's, its ascender; the width cap counts the
// outline, and the box's padding only for a preset with a box (demotape counts the padding always, an
// inference), and the glyphs' ink besides their advances; at the 20 px floor a block still too wide is squeezed
// with \fscx, where demotape keeps its width and is clipped (an inference). A cue at the bottom flips to the top,
// whole, while a tap's ring reaches its tallest block (aidemo's flip on a collision, src/compose.ts, lines 1060-1068
// and 2001-2006, tested against the block, not a fixed band).
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
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
)

// WordByWordMinSeconds is the shortest a word stays on screen in a one- or two-word preset: long enough to read
// one word, short enough to keep pace.
const WordByWordMinSeconds = 0.42

// MinFontSize is the smallest em a block shrinks to, so a very long cue degrades into small text rather than into
// nothing legible.
const MinFontSize = 20.0

// SafeFraction is the share of the video's width a block may take.
const SafeFraction = 0.94

// The block's proportions, of its em: padding, line height, box radius, outline.
const (
	padXEm       = 0.55
	padYEm       = 0.34
	lineSpacing  = 1.18
	radiusOfH    = 0.28
	radiusEm     = 0.6
	outlineEm    = 0.04
	maxLines     = 2
	inkEdgePx    = 2.0
	bottomOffset = 0.07
	lowerOffset  = 0.16
	topOffset    = 0.07
)

// Look is how captions look: a preset and where they sit.
type Look struct {
	Preset   string
	Position Position
}

// Render is the captions of a video of width × height as an ASS file, in a look. rings are the taps' rings on
// the video, which a cue at the bottom keeps clear of. Legacy is the captions as ASS makes them.
func Render(cues []Cue, look Look, width, height int, rings []compose.Ring) (string, error) {
	look, err := resolveLook(look)
	if err != nil {
		return "", err
	}

	if look.Preset == Legacy {
		return ASS(cues, width, height, Default), nil
	}

	r, err := newRenderer(look.Preset, width, height)
	if err != nil {
		return "", err
	}

	placed, err := r.place(cues, look.Position, rings)
	if err != nil {
		return "", err
	}

	var events strings.Builder
	for _, pf := range placed {
		events.WriteString(r.events(pf))
	}

	return r.header(width, height) + events.String(), nil
}

// resolveLook fills a look's defaults and checks its ids.
func resolveLook(look Look) (Look, error) {
	if look.Preset == "" {
		look.Preset = DefaultPreset
	}

	if look.Position == "" {
		look.Position = PositionPreset
	}

	if _, ok := PresetByID(look.Preset); !ok {
		return look, fmt.Errorf("no caption preset is named %q", look.Preset)
	}

	if !validPosition(look.Position) {
		return look, fmt.Errorf("no caption position is named %q", look.Position)
	}

	return look, nil
}

func newRenderer(preset string, width, height int) (*renderer, error) {
	p, _ := PresetByID(preset)

	f, err := faceOf(p.Font)
	if err != nil {
		return nil, err
	}

	return &renderer{p: p, f: f, w: float64(width), h: float64(height)}, nil
}

// placedFrame is a frame and where its block starts on the video.
type placedFrame struct {
	frame

	top float64
}

// place lays out every cue's frames and places them: at the position, or, for a cue at the bottom that a ring
// reaches, at the top.
func (r *renderer) place(cues []Cue, chosen Position, rings []compose.Ring) ([]placedFrame, error) {
	pos := r.position(chosen)

	var out []placedFrame

	for _, c := range cues {
		frames, err := r.frames(c)
		if err != nil {
			return nil, err
		}

		at := pos
		if at == PositionBottom && r.ringReaches(c, frames, rings) {
			at = PositionTop
		}

		for _, fr := range frames {
			out = append(out, placedFrame{frame: fr, top: r.top(at, fr.block.h)})
		}
	}

	return out, nil
}

func validPosition(p Position) bool {
	for _, q := range Positions {
		if p == q {
			return true
		}
	}

	return false
}

type renderer struct {
	p    Preset
	f    *face
	w, h float64
}

// position is where the cues sit: a bar at the bottom; a chosen position in both aspects; else the preset's own on
// a square or tall video, and the bottom on a wide one.
func (r *renderer) position(chosen Position) Position {
	switch {
	case r.p.Bar:
		return PositionBottom
	case chosen != PositionPreset:
		return chosen
	case r.w/r.h <= 1.05:
		return r.p.Position
	default:
		return PositionBottom
	}
}

// word is a word as shown, and its color.
type word struct {
	text  string
	color string
}

// block is what is on screen in a stretch of a cue, laid out.
type block struct {
	lines [][]word
	// em is the type's size in pixels, fx the horizontal squeeze (1 is none).
	em, fx float64
	// textW is the widest line's span at em, before fx.
	textW               float64
	padX, padY, outline float64
	lineH               float64
	// w and h are the block's size: the box's, or the text's and its outline's.
	w, h float64
}

// frame is a stretch of a cue and its block.
type frame struct {
	from, to float64
	key      string
	block    block
}

// frames are the stretches of a cue, each with what it shows: cut at each word's start and end, and joined where
// what is shown stays the same.
func (r *renderer) frames(c Cue) ([]frame, error) {
	start, end := float64(c.StartMs)/1000, float64(c.EndMs)/1000

	floor := HighlightMinSeconds
	if r.p.WordByWord() {
		floor = WordByWordMinSeconds
	}

	words := Normalize(c.Words, floor, start, end)
	if len(words) == 0 {
		return nil, nil
	}

	maxWords := r.p.MaxWordsPerLine(r.w / r.h)

	size := max(1, maxWords*maxLines)
	if r.p.WordByWord() {
		size = r.p.WordsAtATime
	}

	var out []frame

	for _, s := range stretches(words, start, end) {
		t := s.from

		idx, win := Window(t, words, size)

		visible := win
		if r.p.Animated && !r.p.RevealFuture {
			visible = nil

			for _, w := range win {
				if w.Start <= t {
					visible = append(visible, w)
				}
			}
		}

		if len(visible) == 0 {
			continue
		}

		active := -1

		for k, w := range win {
			if w.Start <= t && t < w.End {
				active = k
			}
		}

		key := strconv.Itoa(idx)
		if r.p.Animated {
			key = fmt.Sprintf("%d|%d|%d", idx, len(visible), active)
		}

		if n := len(out); n > 0 && out[n-1].key == key && math.Abs(out[n-1].to-s.from) < 1e-9 {
			out[n-1].to = s.to

			continue
		}

		lines := r.lines(visible, maxWords, t)

		b, err := r.layout(lines)
		if err != nil {
			return nil, err
		}

		out = append(out, frame{from: s.from, to: s.to, key: key, block: b})
	}

	return out, nil
}

// Window splits a cue's words into windows of size and returns the one on screen at t: the first whose last word
// has not finished, else the last.
func Window(t float64, words []Word, size int) (int, []Word) {
	n := len(words)
	if n <= size {
		return 0, words
	}

	c := 0
	for lo := 0; lo < n; lo += size {
		hi := min(lo+size, n)
		if t < words[hi-1].End {
			return c, words[lo:hi]
		}

		c++
	}

	return c - 1, words[(c-1)*size:]
}

// lines chunks the words shown into lines of maxWords, each word as shown and in its color at t.
func (r *renderer) lines(visible []Word, maxWords int, t float64) [][]word {
	var out [][]word

	for i := 0; i < len(visible); i += maxWords {
		var line []word

		for _, w := range visible[i:min(i+maxWords, len(visible))] {
			text := DisplayWord(strings.TrimSpace(w.Word), r.p.WordByWord())
			if r.p.Uppercase {
				text = strings.ToUpper(text)
			}

			line = append(line, word{text: escape(norm.NFC.String(text)), color: r.color(w, t)})
		}

		out = append(out, line)
	}

	return out
}

// color is a word's color at t: base for a phrase preset; active while spoken, base once spoken, future before.
func (r *renderer) color(w Word, t float64) string {
	switch {
	case !r.p.Animated:
		return r.p.Base
	case w.Start <= t && t < w.End:
		return r.p.Active
	case w.End <= t:
		return r.p.Base
	case r.p.Future != "":
		return r.p.Future
	default:
		return r.p.Base
	}
}

// WidthFitScale is how much to shrink a block so that it fits the frame's width: never more than 1.
func WidthFitScale(blockWidth, frameWidth, safeFraction float64) float64 {
	if blockWidth <= 0 || frameWidth <= 0 || safeFraction <= 0 {
		return 1
	}

	if limit := frameWidth * safeFraction; blockWidth > limit {
		return limit / blockWidth
	}

	return 1
}

// layout lays a block out at its ideal size, shrunk to fit the width when it overflows, and squeezed when it still
// overflows at the floor.
func (r *renderer) layout(lines [][]word) (block, error) {
	scale := 0.05
	if r.p.Animated {
		scale = 0.062
	}

	ideal := max(MinFontSize, r.h*scale*r.p.FontScale)

	b, err := r.measure(lines, ideal)
	if err != nil {
		return block{}, err
	}

	if s := WidthFitScale(r.drawn(b), r.w, SafeFraction); s < 1 {
		if b, err = r.measure(lines, max(MinFontSize, ideal*s)); err != nil {
			return block{}, err
		}
	}

	// At the floor, the text is squeezed to fit, to whole pixels, as the box is drawn in them.
	if limit := math.Floor(r.w * SafeFraction); r.drawn(b) > limit && b.textW > 0 {
		b.fx = (limit - r.drawn(b) + b.textW) / b.textW
		b.w = r.width(b)

		// The box is rounded up to a whole pixel, which may take one more.
		if r.drawn(b) > limit {
			b.fx = (limit - 1 - r.drawn(b) + b.textW*b.fx) / b.textW
			b.w = r.width(b)
		}
	}

	return b, nil
}

// measure lays a block out at an em.
func (r *renderer) measure(lines [][]word, em float64) (block, error) {
	b := block{lines: lines, em: em, fx: 1, padX: em * padXEm, padY: em * padYEm, lineH: r.f.assSize(em) * lineSpacing}
	if r.p.Outline {
		b.outline = em * outlineEm
	}

	for _, line := range lines {
		span, err := r.f.span(lineText(line), em)
		if err != nil {
			return block{}, err
		}

		b.textW = max(b.textW, math.Ceil(span))
	}

	b.w = r.width(b)
	b.h = math.Ceil(math.Ceil(float64(len(lines))*b.lineH) + 2*b.padY)

	return b, nil
}

// width is a block's width: a box's, the text and its padding, in whole pixels; else the text and its outline.
// A bar's band is the video's width, and its block is its text.
func (r *renderer) width(b block) float64 {
	if r.p.HasBox() && !r.p.Bar {
		return math.Ceil(b.textW*b.fx + 2*b.padX - 1e-9)
	}

	return b.textW*b.fx + 2*b.outline
}

// drawn is a block's width in the pixels libass draws: its edges, anti-aliased, touch a pixel more on each side
// (the box's too, as measured with the pinned ffmpeg). The cap is on these pixels, not only the advances (#935).
func (r *renderer) drawn(b block) float64 { return b.w + inkEdgePx }

func lineText(line []word) string {
	texts := make([]string, 0, len(line))
	for _, w := range line {
		texts = append(texts, w.text)
	}

	return strings.Join(texts, " ")
}

// top is where a block of height h starts, at a position.
func (r *renderer) top(at Position, h float64) float64 {
	switch {
	case r.p.Bar && at == PositionTop:
		return 0
	case r.p.Bar:
		return r.h - h
	}

	switch at {
	case PositionCenter:
		return math.Round((r.h - h) / 2)
	case PositionLowerThird:
		return r.h - math.Round(r.h*lowerOffset) - h
	case PositionTop:
		return math.Round(r.h * topOffset)
	default:
		return r.h - math.Round(r.h*bottomOffset) - h
	}
}

// ringReaches says whether a tap's ring shows while a cue does and reaches the top of its tallest block at the
// bottom: the cue then sits at the top, whole.
func (r *renderer) ringReaches(c Cue, frames []frame, rings []compose.Ring) bool {
	tallest := 0.0
	for _, fr := range frames {
		tallest = max(tallest, fr.block.h)
	}

	if tallest == 0 {
		return false
	}

	top := r.top(PositionBottom, tallest)

	for _, g := range rings {
		if g.AtMs < c.EndMs && g.AtMs+compose.RingMs > c.StartMs && float64(g.Y)+float64(g.Size)/2 >= top {
			return true
		}
	}

	return false
}

// header is the ASS file's script info and styles: the text's, and the box's.
func (r *renderer) header(width, height int) string {
	var b strings.Builder

	fmt.Fprintf(&b, "[Script Info]\nScriptType: v4.00+\nWrapStyle: 2\nScaledBorderAndShadow: yes\n"+
		"PlayResX: %d\nPlayResY: %d\n\n", width, height)
	b.WriteString("[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, " +
		"BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, " +
		"Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")

	base := assColor(r.p.Base)
	fmt.Fprintf(&b, "Style: Text,%s,%s,%s,%s,%s,&H00000000,0,0,0,0,100,100,0,0,1,0,0,8,0,0,0,1\n",
		r.p.Font, num(r.f.assSize(MinFontSize)), base, base, assColor(r.p.OutlineColor))

	box := "&H00000000"
	if r.p.HasBox() {
		box = assColor(r.p.Box)
	}

	fmt.Fprintf(&b, "Style: Box,%s,%s,%s,%s,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,0,0,7,0,0,0,1\n\n",
		r.p.Font, num(r.f.assSize(MinFontSize)), box, box)
	b.WriteString("[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")

	return b.String()
}

// boxRect is a block's box on the video: x, y, w, h.
func (r *renderer) boxRect(pf placedFrame) (float64, float64, float64, float64) {
	if r.p.Bar {
		return 0, pf.top, r.w, pf.block.h
	}

	return math.Round((r.w - pf.block.w) / 2), pf.top, pf.block.w, pf.block.h
}

// events are a placed frame's ASS events: its box, then each line centered on the video.
func (r *renderer) events(pf placedFrame) string {
	b, top := pf.block, pf.top
	from, to := assTime(pf.from), assTime(pf.to)

	var out strings.Builder

	if r.p.HasBox() {
		x, y, w, h := r.boxRect(pf)

		radius := math.Min(h*radiusOfH, b.em*radiusEm)
		if r.p.Bar {
			radius = 0
		}

		fmt.Fprintf(&out, "Dialogue: 0,%s,%s,Box,,0,0,0,,{\\pos(%s,%s)\\p1}%s{\\p0}\n", //nolint:misspell // ASS.
			from, to, num(x), num(y), roundedRect(w, h, radius))
	}

	for row, line := range b.lines {
		y := top + b.padY + float64(row)*b.lineH

		var text strings.Builder

		fmt.Fprintf(&text, `{\an8\pos(%s,%s)\fs%s\fscx%s\bord%s}`,
			num(r.w/2), num(y), num(r.f.assSize(b.em)), num(100*b.fx), num(b.outline))

		for k, w := range line {
			if k > 0 {
				text.WriteString(" ")
			}

			fmt.Fprintf(&text, `{\1c%s\1a%s}%s`, colorOnly(w.color), alphaOnly(w.color), w.text)
		}

		fmt.Fprintf(&out, "Dialogue: 1,%s,%s,Text,,0,0,0,,%s\n", from, to, text.String()) //nolint:misspell // ASS.
	}

	return out.String()
}

// roundedRect is an ASS drawing of a rectangle w × h with corners of radius r, from (0, 0).
func roundedRect(w, h, r float64) string {
	if r <= 0 {
		return fmt.Sprintf("m 0 0 l %s 0 %s %s 0 %s", num(w), num(w), num(h), num(h))
	}

	// The control points of a quarter circle's cubic Bézier.
	c := r * (1 - 0.5523)

	return strings.Join([]string{
		"m", num(r), "0",
		"l", num(w - r), "0",
		"b", num(w - c), "0", num(w), num(c), num(w), num(r),
		"l", num(w), num(h - r),
		"b", num(w), num(h - c), num(w - c), num(h), num(w - r), num(h),
		"l", num(r), num(h),
		"b", num(c), num(h), "0", num(h - c), "0", num(h - r),
		"l", "0", num(r),
		"b", "0", num(c), num(c), "0", num(r), "0",
	}, " ")
}

// assColor is a color #RRGGBB or #RRGGBBAA as an ASS style writes it, &HAABBGGRR, its alpha a transparency.
func assColor(hex string) string {
	rr, gg, bb, aa := rgba(hex)

	return fmt.Sprintf("&H%02X%02X%02X%02X", 255-aa, bb, gg, rr)
}

func colorOnly(hex string) string {
	rr, gg, bb, _ := rgba(hex)

	return fmt.Sprintf("&H%02X%02X%02X&", bb, gg, rr)
}

func alphaOnly(hex string) string {
	_, _, _, aa := rgba(hex)

	return fmt.Sprintf("&H%02X&", 255-aa)
}

// rgba reads a preset's color: #RRGGBB, or #RRGGBBAA.
func rgba(hex string) (r, g, b, a int) {
	s := strings.TrimPrefix(hex, "#")

	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 255, 255, 255, 255
	}

	switch len(s) {
	case 6:
		return int(v >> 16 & 0xFF), int(v >> 8 & 0xFF), int(v & 0xFF), 255
	case 8:
		return int(v >> 24 & 0xFF), int(v >> 16 & 0xFF), int(v >> 8 & 0xFF), int(v & 0xFF)
	default:
		return 255, 255, 255, 255
	}
}

// num is a number as ASS reads it, to two decimals, with no trailing zeros.
func num(v float64) string {
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}
