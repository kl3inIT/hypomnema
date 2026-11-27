// Adapted from agent-demo-video (https://github.com/OrionArchitekton/agent-demo-video) at commit de61695,
// src/cards.ts: hookLayout (lines 102-121), which starts a card's words at their size and shrinks them about 5% at a
// time (at least 1 px) to a floor until every line, wrapped at its spaces by width (wrapByWidth, lines 89-99), fits
// the card's width. Issue #1230, row 1, and the review of #1242. What differs, and why: a line's width is the font's
// own advances at the size (Be Vietnam Pro, as libass draws it), where agent-demo-video estimates 0.68 em for a
// capital or digit and 0.55 em for the rest (estimateTextWidth, lines 83-87); and a word still wider than the card
// at the floor, as a long URL is, is broken where it reaches the card's width, as CSS's overflow-wrap: anywhere
// breaks a word that cannot fit, where agent-demo-video lets it overflow ("a documented limit").
//
// The notice is in third_party/agent-demo-video/NOTICE (MIT License, Copyright (c) 2026 agent-demo-video authors).

package compose

import (
	"fmt"
	"math"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
)

// The card's font files, as the ffmpeg install writes them.
var cardFontFiles = map[string]string{
	cardTitleFont: "BeVietnamPro-Bold.ttf",
	cardTextFont:  "BeVietnamPro-Medium.ttf",
}

var (
	cardFontsOnce sync.Once
	cardFonts     map[string]*sfnt.Font
	errCardFonts  error
)

// cardFont is one of the card's fonts, parsed once.
func cardFont(name string) (*sfnt.Font, error) {
	cardFontsOnce.Do(func() {
		cardFonts = map[string]*sfnt.Font{}

		for full, file := range cardFontFiles {
			raw, err := ffmpeg.FontFile(file)
			if err != nil {
				errCardFonts = err

				return
			}

			f, err := sfnt.Parse(raw)
			if err != nil {
				errCardFonts = fmt.Errorf("the font %s: %w", file, err)

				return
			}

			cardFonts[full] = f
		}
	})

	if errCardFonts != nil {
		return nil, errCardFonts
	}

	return cardFonts[name], nil
}

// textWidth is a line's width in pixels at an em of em pixels: its glyphs' advances, unhinted and without kerning,
// as libass lays a line out (its Kerning is off).
func textWidth(f *sfnt.Font, text string, em float64) (float64, error) {
	var (
		buf   sfnt.Buffer
		width fixed.Int26_6
	)

	ppem := fixed.Int26_6(math.Round(em * 64))

	for _, r := range text {
		idx, err := f.GlyphIndex(&buf, r)
		if err != nil {
			return 0, err
		}

		adv, err := f.GlyphAdvance(&buf, idx, ppem, font.HintingNone)
		if err != nil {
			return 0, err
		}

		width += adv
	}

	return float64(width) / 64, nil
}

// fitMargin is the share of the width a line is given less than the card's, for the pixel libass's anti-aliasing
// adds on each side and its hinting's rounding (captions/fonts.go).
const fitMargin = 0.97

// fitText is a card's line at its size or smaller, never under floor, and its lines: wrapped at its spaces where
// they reach maxW, and a word still wider than maxW at the floor broken where it reaches it.
func fitText(f *sfnt.Font, text string, em, floor, maxW float64) (float64, []string, error) {
	maxW *= fitMargin

	for {
		lines, widest, err := wrapByWidth(f, text, em, maxW)
		if err != nil {
			return 0, nil, err
		}

		if widest <= maxW || em <= floor {
			if widest <= maxW {
				return em, lines, nil
			}

			lines, err = breakWide(f, lines, em, maxW)

			return em, lines, err
		}

		em = math.Max(floor, math.Min(em-1, em*0.95))
	}
}

// wrapByWidth wraps a line at its spaces, greedily, where the next word would take it past maxW; it says its widest
// line.
func wrapByWidth(f *sfnt.Font, text string, em, maxW float64) ([]string, float64, error) {
	var (
		lines  []string
		cur    string
		widest float64
	)

	flush := func() error {
		w, err := textWidth(f, cur, em)
		widest = math.Max(widest, w)
		lines = append(lines, cur)

		return err
	}

	for _, word := range strings.Fields(text) {
		if cur == "" {
			cur = word

			continue
		}

		w, err := textWidth(f, cur+" "+word, em)
		if err != nil {
			return nil, 0, err
		}

		if w <= maxW {
			cur += " " + word

			continue
		}

		if err := flush(); err != nil {
			return nil, 0, err
		}

		cur = word
	}

	if cur != "" {
		if err := flush(); err != nil {
			return nil, 0, err
		}
	}

	return lines, widest, nil
}

// breakWide breaks each line wider than maxW where it reaches it, a character at a time.
func breakWide(f *sfnt.Font, lines []string, em, maxW float64) ([]string, error) {
	out := make([]string, 0, len(lines))

	for _, line := range lines {
		var cur []rune

		for _, r := range line {
			w, err := textWidth(f, string(append(cur, r)), em)
			if err != nil {
				return nil, err
			}

			if w > maxW && len(cur) > 0 {
				out = append(out, strings.TrimSpace(string(cur)))
				cur = cur[:0]
			}

			cur = append(cur, r)
		}

		if s := strings.TrimSpace(string(cur)); s != "" {
			out = append(out, s)
		}
	}

	return out, nil
}
