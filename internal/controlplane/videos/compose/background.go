// Ported from autodemo (https://github.com/mdayan8/autodemo) at commit 73e5408, src/compositor.html: the background
// templates (TEMPLATES, lines 21-25), the scale unit U (line 72) and background() (lines 53-77: the diagonal
// gradient of two colors, then aurora's and the macOS wallpapers' color fields, grid's lines and glow, or the soft
// glow of the rest), with blob() (line 28). Issue #908, row 13. autodemo paints them on a canvas in Chrome; here
// they are painted in Go into the frame's PNG (plate.go), with the canvas's rules: a gradient is read at each
// pixel's center and its stops are interpolated without premultiplying (the HTML canvas spec), and a 1 px line
// covers the pixels it overlaps. Not ported: the menu bar and the Dock of the macOS templates, and an image.
//
// MIT License
//
// Copyright (c) 2026 Md Ayan
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

package compose

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
)

// Background is what a frame stands on: one of autodemo's templates, or a gradient of two colors.
type Background struct {
	Preset string   `json:"preset,omitempty"`
	Colors []string `json:"colors,omitempty"`
}

// Backgrounds are autodemo's templates, in the order its documentation lists them (src/mcp.js:293).
var Backgrounds = []string{
	"macos", "macos-light", "midnight", "sunset", "ocean", "forest", "peach", "aurora", "grid", "mono", "light",
}

// DefaultBackground is autodemo's: midnight (src/compositor.html:28).
const DefaultBackground = "midnight"

// templates are each template's two colors (TEMPLATES).
var templates = map[string][2]string{
	"midnight": {"#1b2140", "#3a2257"}, "sunset": {"#ff7e5f", "#7b2ff7"}, "ocean": {"#0f4c75", "#00b4d8"},
	"forest": {"#134e3a", "#6fbf73"}, "peach": {"#ffd1b3", "#ff8fab"}, "mono": {"#111111", "#3a3a3a"},
	"light": {"#eef1f6", "#cfd8e8"}, "aurora": {"#0b1026", "#141a3a"}, "grid": {"#0e1117", "#1a1f2b"},
	"macos": {"#1d2b64", "#6a3093"}, "macos-light": {"#a8c0ff", "#f6d5f7"},
}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Check refuses an unknown template, and colors that are not two #rrggbb.
func (b Background) Check() error {
	switch {
	case b.Preset != "" && len(b.Colors) > 0:
		return fmt.Errorf("a background is a preset or two colors, not both")
	case len(b.Colors) > 0:
		if len(b.Colors) != 2 {
			return fmt.Errorf("a background has two colors, not %d", len(b.Colors))
		}

		for _, c := range b.Colors {
			if !hexColor.MatchString(c) {
				return fmt.Errorf("color %q is not #rrggbb", c)
			}
		}
	case b.Preset != "":
		if _, ok := templates[b.Preset]; !ok {
			return fmt.Errorf("background %q is not one of %v", b.Preset, Backgrounds)
		}
	}

	return nil
}

// rgba is a color with straight (not premultiplied) alpha, each channel 0-1.
type rgba struct{ r, g, b, a float64 }

func hexRGBA(s string) rgba {
	v, _ := strconv.ParseUint(s[1:], 16, 32)

	return rgba{float64(v>>16&0xff) / 255, float64(v>>8&0xff) / 255, float64(v&0xff) / 255, 1}
}

func lerp(a, b rgba, t float64) rgba {
	return rgba{a.r + (b.r-a.r)*t, a.g + (b.g-a.g)*t, a.b + (b.b-a.b)*t, a.a + (b.a-a.a)*t}
}

// paintBackground is autodemo's background(W, H) on c, which it fills.
func paintBackground(c *canvas, bg Background) {
	style, colors := DefaultBackground, templates[DefaultBackground]

	switch {
	case len(bg.Colors) == 2:
		// Two colors replace the template's; the style stays the default's, as autodemo's setup leaves it.
		colors = [2]string{bg.Colors[0], bg.Colors[1]}
	case bg.Preset != "":
		style, colors = bg.Preset, templates[bg.Preset]
	}

	w, h := float64(c.w), float64(c.h)
	u := math.Min(w, h) / 1080
	from, to := hexRGBA(colors[0]), hexRGBA(colors[1])
	norm := w*w + h*h

	c.each(func(x, y float64) (rgba, float64) {
		t := math.Max(0, math.Min(1, (x*w+y*h)/norm))

		return lerp(from, to, t), 1
	})

	switch style {
	case "aurora":
		blob(c, w*.2, h*.2, w*.5, rgba{56. / 255, 189. / 255, 248. / 255, .45})
		blob(c, w*.8, h*.3, w*.45, rgba{168. / 255, 85. / 255, 247. / 255, .45})
		blob(c, w*.5, h*1, w*.5, rgba{34. / 255, 197. / 255, 94. / 255, .35})
	case "grid":
		line := rgba{1, 1, 1, .06}
		for x := 0.0; x < w; x += 48 * u {
			c.each(func(px, _ float64) (rgba, float64) { return line, overlap(px-.5, x-.5, x+.5) })
		}

		for y := 0.0; y < h; y += 48 * u {
			c.each(func(_, py float64) (rgba, float64) { return line, overlap(py-.5, y-.5, y+.5) })
		}

		blob(c, w*.5, h*.45, w*.45, rgba{99. / 255, 102. / 255, 241. / 255, .25})
	case "macos":
		blob(c, w*.15, h*.85, w*.55, rgba{236. / 255, 72. / 255, 153. / 255, .45})
		blob(c, w*.85, h*.2, w*.55, rgba{59. / 255, 130. / 255, 246. / 255, .5})
		blob(c, w*.6, h*.9, w*.4, rgba{249. / 255, 115. / 255, 22. / 255, .35})
	case "macos-light":
		blob(c, w*.15, h*.85, w*.55, rgba{1, 183. / 255, 197. / 255, .7})
		blob(c, w*.85, h*.2, w*.55, rgba{147. / 255, 197. / 255, 253. / 255, .7})
		blob(c, w*.6, h*.9, w*.4, rgba{253. / 255, 224. / 255, 171. / 255, .6})
	default:
		glow := rgba{1, 1, 1, .10}
		radial(c, w*0.8, h*0.1, w*0.7, glow, rgba{1, 1, 1, 0})
	}
}

// blob is a radial gradient from a color at its center to transparent black at r.
func blob(c *canvas, cx, cy, r float64, col rgba) { radial(c, cx, cy, r, col, rgba{}) }

func radial(c *canvas, cx, cy, r float64, from, to rgba) {
	c.each(func(x, y float64) (rgba, float64) {
		t := math.Min(1, math.Hypot(x-cx, y-cy)/r)

		return lerp(from, to, t), 1
	})
}

// overlap is how much of the pixel from p to p+1 lies within [a, b].
func overlap(p, a, b float64) float64 {
	return math.Max(0, math.Min(p+1, b)-math.Max(p, a))
}
