// Ported from aidemo (https://github.com/tandryukha/aidemo) at commit b67235a: the card of src/types.ts:770-792
// (a title, a subtitle, 2600 ms on screen, a 350 ms fade, an accent of #6c8cff), its layout in src/cards.ts:41-79
// (a centered box at most 78% of the width: the accent's rule, W/22 wide, above the title at W/17, weight 750,
// #f4f6fb; the subtitle at W/40, weight 450, rgba(226,231,244,.72)), and cardSegment in src/compose.ts:1618-1671
// (the card looped at the video's rate for its length, faded in and out over at most a third of it each, encoded as
// the scenes are); and from src/compose.ts:1035-1060, the cards put before and after the content, which is encoded
// again as they are joined. And from agent-demo-video (https://github.com/OrionArchitekton/agent-demo-video) at
// commit de61695: the end card's 3.0 s and the 0.5 s a card lasts at least (src/types.ts:286-287), and its URL in
// the accent under the title (src/cards.ts:166-178, at 0.84 of its title's size). Issue #1230, row 1.
//
// What differs, and why: aidemo has Chrome rasterize the card's HTML; here the card stands on the look's background,
// painted in Go as the frame's is (background.go), and its words are an ASS file libass draws, as the captions are
// (Be Vietnam Pro Bold and Medium for aidemo's 750 and 450). The rule is an ASS drawing on the block's first line.
// The longest a card lasts, 10 s, is an inference: no reference bounds it.
//
// MIT License
//
// Copyright (c) 2026 Andrii Taran
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
//
// agent-demo-video: Copyright (c) 2026 agent-demo-video authors, under the same MIT License; the notice is in
// third_party/agent-demo-video/NOTICE.

package compose

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
)

// Card is a title card or an end card: words on the look's background, for its length.
type Card struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	// CTAURL is where the viewer is sent, drawn in the accent under the title.
	CTAURL     string `json:"cta_url,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// Cards are a video's title card and end card, each absent for none, drawn in an accent.
type Cards struct {
	Accent string `json:"accent"`
	Title  *Card  `json:"title,omitempty"`
	End    *Card  `json:"end,omitempty"`
}

// The cards' numbers (row 1).
const (
	// DefaultTitleMs and DefaultEndMs are aidemo's card and agent-demo-video's end card.
	DefaultTitleMs = 2600
	DefaultEndMs   = 3000
	// CardFadeMs is aidemo's fade.
	CardFadeMs = 350
	// MinCardMs is agent-demo-video's shortest card; MaxCardMs is an inference.
	MinCardMs = 500
	MaxCardMs = 10000
	// DefaultAccent is aidemo's.
	DefaultAccent = "#6c8cff"
	// The longest words a card holds, in characters (an inference: a title that fills the card at its size, a line
	// under it, a URL).
	MaxCardTitle    = 80
	MaxCardSubtitle = 120
	MaxCardURL      = 200
)

// The card's fonts, as the ffmpeg install writes them (captions/presets.go names the same).
const (
	cardTitleFont = "Be Vietnam Pro Bold"
	cardTextFont  = "Be Vietnam Pro Medium"
	// cardEm is Be Vietnam Pro's (usWinAscent + usWinDescent) / unitsPerEm, (1261 + 265) / 1000: libass sizes a
	// font by those, so an em of e pixels is an ASS Fontsize of e × cardEm (captions/fonts.go).
	cardEm = 1.526
)

// Check refuses an accent that is not #rrggbb and a card out of its bounds.
func (c Cards) Check() error {
	if !hexColor.MatchString(c.Accent) {
		return fmt.Errorf("the cards' accent %q is not #rrggbb", c.Accent)
	}

	for name, card := range map[string]*Card{"title": c.Title, "end": c.End} {
		if card == nil {
			continue
		}

		if err := card.check(); err != nil {
			return fmt.Errorf("the %s card: %w", name, err)
		}
	}

	return nil
}

func (c Card) check() error {
	if strings.TrimSpace(c.Title) == "" {
		return fmt.Errorf("a card needs a title")
	}

	if c.DurationMs < MinCardMs || c.DurationMs > MaxCardMs {
		return fmt.Errorf("a card lasts %d to %d ms, not %d", MinCardMs, MaxCardMs, c.DurationMs)
	}

	for _, f := range []struct {
		name, text string
		most       int
	}{{"title", c.Title, MaxCardTitle}, {"subtitle", c.Subtitle, MaxCardSubtitle}, {"URL", c.CTAURL, MaxCardURL}} {
		if n := utf8.RuneCountInString(f.text); n > f.most {
			return fmt.Errorf("a card's %s is at most %d characters, not %d", f.name, f.most, n)
		}

		if strings.ContainsFunc(f.text, unicode.IsControl) {
			return fmt.Errorf("a card's %s is one line", f.name)
		}
	}

	return nil
}

// frames is how many of the video's frames a card of ms lasts: the frames that start within it.
func cardFrames(ms int64) int64 { return (ms*FPS + 999) / 1000 }

// cardMs is how long a card is in the video: its whole frames.
func cardMs(c *Card) int64 {
	if c == nil {
		return 0
	}

	return int64(math.Round(float64(cardFrames(c.DurationMs)) * 1000 / FPS))
}

// TitleMs and EndMs are how long the cards are in the video, 0 for none.
func (c Cards) TitleMs() int64 { return cardMs(c.Title) }

func (c Cards) EndMs() int64 { return cardMs(c.End) }

// cardASS is a card's words as an ASS file for a video of size: one block in the middle, its lines the accent's
// rule, the title, the subtitle and the URL, each fitted within 78% of the width (aidemo's box) by fitText: smaller
// to the subtitle's size, then broken over lines (cardfit.go). The lines are broken here, and libass told not to
// wrap them again (WrapStyle 2).
func cardASS(card Card, accent string, size Size) (string, error) {
	w := float64(size.Width)
	title, sub := w/17, w/40
	url := 0.84 * title
	ruleW, ruleH := math.Round(w/22), math.Max(4, math.Round(w/256))
	margin := int(math.Round(w * 0.11))

	var b strings.Builder

	fmt.Fprintf(&b, "[Script Info]\nScriptType: v4.00+\nWrapStyle: 2\nScaledBorderAndShadow: yes\n"+
		"PlayResX: %d\nPlayResY: %d\n\n", size.Width, size.Height)
	b.WriteString("[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, " +
		"BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, " +
		"Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	fmt.Fprintf(&b, "Style: Card,%s,%.0f,&H00FBF6F4,&H00FBF6F4,&H00000000,&H73000000,0,0,0,0,100,100,0,0,1,0,0,5,"+
		"%d,%d,0,1\n\n", cardTitleFont, title*cardEm, margin, margin)
	b.WriteString("[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")

	ac := assColor(accent, 0)
	box := 0.78 * w
	lines := []string{
		// The rule is a box drawn in the accent, a line of its own at the size of the gap under it.
		fmt.Sprintf(`{\fs%.0f\c%s\p1}m 0 0 l %.0f 0 %.0f %.0f 0 %.0f{\p0}`, sub*1.2*cardEm, ac, ruleW, ruleW, ruleH, ruleH),
	}

	// Each block after the rule starts from the style again ({\r}): an override lasts to the event's end, over the
	// block's own line breaks too.
	for _, part := range []struct {
		text, font, override string
		em                   float64
	}{
		{card.Title, cardTitleFont, "", title},
		{card.Subtitle, cardTextFont, fmt.Sprintf(`\fn%s\c%s`, cardTextFont, assColor("#e2e7f4", 0.72)), sub},
		{card.CTAURL, cardTitleFont, `\c` + ac, url},
	} {
		if part.text == "" {
			continue
		}

		f, err := cardFont(part.font)
		if err != nil {
			return "", err
		}

		em, broken, err := fitText(f, part.text, part.em, math.Min(part.em, sub), box)
		if err != nil {
			return "", err
		}

		for k := range broken {
			broken[k] = cardText(broken[k])
		}

		lines = append(lines, fmt.Sprintf(`{\r\fs%.0f%s}%s`, em*cardEm, part.override, strings.Join(broken, `\N`)))
	}

	fmt.Fprintf(&b, "Dialogue: 0,0:00:00.00,9:59:59.99,Card,,0,0,0,,%s\n", //nolint:misspell // ASS's own word.
		strings.Join(lines, `\N`))

	return b.String(), nil
}

// assColor is a #rrggbb color as an ASS override, &HBBGGRR& with its alpha in the subtitle line's \alpha, so the
// opacity goes on the color's tag.
func assColor(hex string, opacity float64) string {
	c := hexRGBA(hex)
	out := fmt.Sprintf("&H%02X%02X%02X&", byte8(c.b), byte8(c.g), byte8(c.r))

	if opacity > 0 && opacity < 1 {
		out += fmt.Sprintf(`\alpha&H%02X&`, byte8(1-opacity))
	}

	return out
}

// cardText keeps a card's words from being read as an ASS override or a line break, as the captions do.
func cardText(s string) string {
	return strings.NewReplacer(`\`, `/`, "{", "(", "}", ")").Replace(s)
}

// writeCardFiles writes a card's background, the look's painted on the video's whole size, and its words' ASS
// file into work, as name.png and name.ass.
func writeCardFiles(work, name string, card Card, accent string, bg Background, size Size) (string, string, error) {
	c := newCanvas(size.Width, size.Height)
	paintBackground(c, bg)

	png := filepath.Join(work, name+".png")
	if err := writePNG(png, c.image()); err != nil {
		return "", "", err
	}

	ass := filepath.Join(work, name+".ass")

	text, err := cardASS(card, accent, size)
	if err != nil {
		return "", "", err
	}

	return png, ass, os.WriteFile(ass, []byte(text), 0o600)
}

// cardChain is the filter of a card's input: its PNG made the video's pixels, limited-range yuv420p as Still makes
// them for libass (#937), its words drawn, faded in and out over at most a third of its length each, as label.
func cardChain(input int, name string, ms int64, label string) string {
	sec := float64(ms) / 1000
	fade := math.Min(CardFadeMs, float64(ms)/3) / 1000

	return fmt.Sprintf("[%d:v]scale=out_range=tv,format=yuv420p,subtitles=filename=%s.ass:fontsdir=fonts,setsar=1,"+
		"fade=t=in:st=0:d=%.3f,fade=t=out:st=%.3f:d=%.3f[%s]", input, name, fade, sec-fade, fade, label)
}

// addCards puts the cards before and after the video, joined and encoded once, and moves the scenes and their
// rings after the title card: the captions are timed on them, and the voice is laid from them (row 1). The video
// as it is without cards.
func (c *composer) addCards(ctx context.Context, video string) (string, error) {
	cards := c.in.Cards
	if cards == nil || cards.Title == nil && cards.End == nil {
		return video, nil
	}

	if err := stageFonts(c.in.Work, c.run.tools.FontsDir); err != nil {
		return "", err
	}

	var (
		args   []string
		chains []string
		joined []string
	)

	input := 0
	card := func(name string, cd *Card) error {
		png, _, err := writeCardFiles(c.in.Work, name, *cd, cards.Accent, c.in.Background, c.size)
		if err != nil {
			return err
		}

		ms := cardMs(cd)
		args = append(args, "-loop", "1", "-framerate", fmt.Sprint(FPS), "-t", fmt.Sprintf("%.3f", float64(ms)/1000),
			"-i", png)
		chains = append(chains, cardChain(input, name, ms, name))
		joined = append(joined, "["+name+"]")
		input++

		return nil
	}

	if cards.Title != nil {
		if err := card("card-title", cards.Title); err != nil {
			return "", err
		}
	}

	args = append(args, "-i", video)
	chains = append(chains, fmt.Sprintf("[%d:v]setsar=1[content]", input))
	joined = append(joined, "[content]")
	input++

	if cards.End != nil {
		if err := card("card-end", cards.End); err != nil {
			return "", err
		}
	}

	chains = append(chains, fmt.Sprintf("%sconcat=n=%d:v=1:a=0[vout]", strings.Join(joined, ""), len(joined)))

	out := filepath.Join(c.in.Work, "carded.mp4")
	args = append(args, "-filter_complex", strings.Join(chains, ";"), "-map", "[vout]", "-an")

	if err := c.run.ffmpeg(ctx, append(append(args, encodeArgs...), out)...); err != nil {
		return "", fmt.Errorf("adding the cards: %w", err)
	}

	shift := cards.TitleMs()
	c.result.TitleMs, c.result.EndMs = shift, cards.EndMs()

	for k := range c.result.Scenes {
		c.result.Scenes[k].OutStartMs += shift
		if r := c.result.Scenes[k].Ring; r != nil {
			r.AtMs += shift
		}
	}

	return out, nil
}

// CardStillInput is what a still of a card is made from (the look sample of a card).
type CardStillInput struct {
	Card       Card
	Accent     string
	Background Background
	Aspect     Aspect
	// Work is a folder for what is made on the way, which the caller removes.
	Work string
	// Out is where the JPEG is written.
	Out string
}

// CardStill writes a JPEG of a card as the video shows it between its fades.
func CardStill(ctx context.Context, tools ffmpeg.Tools, in CardStillInput) error {
	size, err := in.Aspect.Size()
	if err != nil {
		return err
	}

	if err := (Cards{Accent: in.Accent, Title: &in.Card}).Check(); err != nil {
		return err
	}

	if err := in.Background.Check(); err != nil {
		return err
	}

	for _, p := range []*string{&in.Work, &in.Out} {
		if *p, err = filepath.Abs(*p); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(in.Work, 0o700); err != nil {
		return err
	}

	if err := stageFonts(in.Work, tools.FontsDir); err != nil {
		return err
	}

	png, _, err := writeCardFiles(in.Work, "card", in.Card, in.Accent, in.Background, size)
	if err != nil {
		return err
	}

	run := runner{tools: tools, dir: in.Work}

	return run.ffmpeg(ctx, "-i", png, "-vf", "scale=out_range=tv,format=yuv420p,"+
		"subtitles=filename=card.ass:fontsdir=fonts", "-frames:v", "1", "-q:v", "3", "-f", "image2", in.Out)
}
