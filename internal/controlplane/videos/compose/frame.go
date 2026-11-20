// Ported from aidemo (https://github.com/tandryukha/aidemo) at commit b67235a, src/frame.ts: frameLayout (lines
// 52-85: the padding, the bezel's room, the status strip and the canvas grown to the output's aspect) and the
// geometry of renderFramePng (lines 100-159: the window, the hole, the bezel, its radius and the camera cut-outs),
// with the defaults of src/types.ts (FrameSchema, lines 538-562: padding 48, radius 40 for a device and 14 else,
// shadow on, 0-400 and 0-80). Issue #908, row 10.
//
// What differs, and why: aidemo works in CSS pixels and lets Chrome scale them; here the layout is in a phone's
// density-independent pixels (360 wide, as a tap's ring, ring.go) and the hole is anchored on even output pixels,
// each of its values rounded as agent-demo-video's scaledSize (src/framing.ts:34, :48), so that the picture is
// padded where the PNG's hole is, to the pixel (row 10). Every other length is derived from the scale and rounded
// once. Only the device chromes and "none" are ported (no browser or mac bar); "full" is the video as before, with
// no frame.
//
// From agent-demo-video (https://github.com/OrionArchitekton/agent-demo-video) at commit f63b61a, src/framing.ts:
// MAX_WINDOW_PAD_PX and fitPaddingPx (lines 53-66), the bars a contain fit would leave, measured here against the
// hole before it is rounded (#936: the rounding is not an aspect mismatch); and the fail-closed refusal of
// assertFramedContentAspect (lines 68-96) for a segment of another aspect (row 11).
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
// agent-demo-video: MIT License, Copyright (c) 2026 agent-demo-video authors; the same permission notice.

package compose

import (
	"errors"
	"fmt"
	"math"
)

// Chrome is what is drawn around the phone's picture (issue #908, row 10).
type Chrome string

// The chromes. Full is no frame: the picture fills the video with "contain", as before the look.
const (
	ChromeFull    Chrome = "full"
	ChromeNone    Chrome = "none"
	ChromeAndroid Chrome = "android"
	ChromeIPhone  Chrome = "iphone"
)

// Chromes are the chromes, in the order a catalog lists them.
var Chromes = []Chrome{ChromeFull, ChromeNone, ChromeAndroid, ChromeIPhone}

// ErrTurnedTake refuses a frame on a take whose segments are not all of the first one's shape (the phone turned):
// a frame around one shape would show the other with bars (issue #908, row 11).
var ErrTurnedTake = errors.New("the phone turned during the take, so its picture cannot be framed")

// Frame is a chrome and its sizes, in a phone's density-independent pixels (aidemo's Frame, src/types.ts:538-562).
type Frame struct {
	Chrome Chrome `json:"chrome"`
	// Padding is the room around the device or window, 0-400; a device takes at least its bezel's 22.
	Padding int `json:"padding"`
	// Radius is the hole's corner radius, 0-80.
	Radius int  `json:"radius"`
	Shadow bool `json:"shadow"`
	// SafeTop puts a status strip of bezel above the picture, where the camera's cut-out sits (devices only).
	SafeTop bool `json:"safe_top"`
}

// The frame's sizes, from aidemo's src/frame.ts and src/types.ts.
const (
	defaultPadding      = 48
	defaultDeviceRadius = 40
	defaultRadius       = 14
	maxPadding          = 400
	maxRadius           = 80
	bezelDp             = 14
	bezelRoomDp         = 8
	safeTopDp           = 46
	bezelBorderDp       = 2
	dotDp               = 14
	islandWDp           = 110
	islandHDp           = 32
	dotTopDp            = 14
	islandTopDp         = 12
	minCutTopDp         = 2
	// maxBarPx is the most a contain fit into the hole may leave, from rounding alone (MAX_WINDOW_PAD_PX).
	maxBarPx = 2
)

// DefaultFrame is a chrome with aidemo's defaults: padding 48, radius 40 for a device and 14 otherwise, a shadow.
func DefaultFrame(chrome Chrome) Frame {
	f := Frame{Chrome: chrome, Padding: defaultPadding, Radius: defaultRadius, Shadow: true}
	if f.device() {
		f.Radius = defaultDeviceRadius
	}

	return f
}

// Framed says whether the frame draws anything: every chrome but full (and none given).
func (f Frame) Framed() bool { return f.Chrome != "" && f.Chrome != ChromeFull }

func (f Frame) device() bool { return f.Chrome == ChromeAndroid || f.Chrome == ChromeIPhone }

// Check refuses an unknown chrome and sizes out of range.
func (f Frame) Check() error {
	switch f.Chrome {
	case "", ChromeFull, ChromeNone, ChromeAndroid, ChromeIPhone:
	default:
		return fmt.Errorf("chrome %q is none of full, none, android and iphone", f.Chrome)
	}

	if f.Padding < 0 || f.Padding > maxPadding {
		return fmt.Errorf("padding %d is not within 0-%d", f.Padding, maxPadding)
	}

	if f.Radius < 0 || f.Radius > maxRadius {
		return fmt.Errorf("radius %d is not within 0-%d", f.Radius, maxRadius)
	}

	return nil
}

// Rect is a rectangle in the video's pixels.
type Rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Cutout is the camera's cut-out of a device: a black rounded rectangle centered on the hole, in the video's
// pixels. Left may fall between pixels (an island of an odd width on an even hole).
type Cutout struct {
	Left   float64 `json:"left"`
	Top    int     `json:"top"`
	W      int     `json:"w"`
	H      int     `json:"h"`
	Radius float64 `json:"radius"`
}

// Layout is where a frame puts things on a video, in its pixels (issue #908, row 10). The hole is the anchor: even
// on every side, the picture scaled to exactly its size; the rest is derived from it and the scale.
type Layout struct {
	Size  Size    `json:"size"`
	Scale float64 `json:"scale"`
	// Content is the hole before it is rounded, which a segment's shape is measured against.
	ContentW float64 `json:"content_w"`
	ContentH float64 `json:"content_h"`

	Hole       Rect `json:"hole"`
	HoleRadius int  `json:"hole_radius"`
	// Window is the hole and the status strip above it.
	Window Rect `json:"window"`
	Strip  int  `json:"strip"`
	// Bezel, its radius and border are a device's; zero otherwise.
	Bezel       Rect    `json:"bezel"`
	BezelRadius int     `json:"bezel_radius"`
	Border      int     `json:"border"`
	Cutout      *Cutout `json:"cutout,omitempty"`
	// ShadowY and ShadowBlur are aidemo's two shadows' offsets and blurs (CSS's blur radius, twice the sigma).
	Shadow     bool       `json:"shadow"`
	ShadowY    [2]float64 `json:"shadow_y"`
	ShadowBlur [2]float64 `json:"shadow_blur"`
}

// The shadows of aidemo's frame, "0 24px 60px rgba(0,0,0,.55), 0 2px 8px rgba(0,0,0,.35)", in dp.
var (
	shadowYDp    = [2]float64{24, 2}
	shadowBlurDp = [2]float64{60, 8}
	shadowAlpha  = [2]float64{0.55, 0.35}
)

// FrameLayout lays a frame out on a video of size out around a picture of content's shape (aidemo's
// frameLayout and renderFramePng).
func FrameLayout(f Frame, content, out Size) (Layout, error) {
	if !f.Framed() {
		return Layout{}, errors.New("a full video has no frame")
	}

	if err := f.Check(); err != nil {
		return Layout{}, err
	}

	if content.Width <= 0 || content.Height <= 0 || out.Width <= 0 || out.Height <= 0 {
		return Layout{}, fmt.Errorf("a frame needs the picture's size and the video's, not %v and %v", content, out)
	}

	device := f.device()

	pad := float64(f.Padding)
	if device {
		pad = math.Max(pad, bezelDp+bezelRoomDp)
	}

	strip := 0.0
	if device && f.SafeTop {
		strip = safeTopDp
	}

	cw := float64(dpWidth)
	ch := cw * float64(content.Height) / float64(content.Width)

	// The canvas grows on one axis to the video's aspect, so the frame fills it with no bars.
	w, h := cw+2*pad, ch+2*pad+strip
	if aspect := float64(out.Width) / float64(out.Height); w/h < aspect {
		w = h * aspect
	} else {
		h = w / aspect
	}

	padX, padY := (w-cw)/2, (h-ch-strip)/2
	s := float64(out.Width) / w

	l := Layout{
		Size: out, Scale: s, ContentW: cw * s, ContentH: ch * s,
		Hole:       Rect{even(padX * s), even((padY + strip) * s), even(cw * s), even(ch * s)},
		HoleRadius: round(float64(f.Radius) * s),
		Shadow:     f.Shadow,
	}

	if strip > 0 {
		l.Strip = round(strip * s)
	}

	l.Window = Rect{l.Hole.X, l.Hole.Y - l.Strip, l.Hole.W, l.Hole.H + l.Strip}

	for k := range shadowYDp {
		l.ShadowY[k] = shadowYDp[k] * s
		l.ShadowBlur[k] = shadowBlurDp[k] * s
	}

	if !device {
		return l, nil
	}

	b := round(bezelDp * s)
	l.Bezel = Rect{l.Window.X - b, l.Window.Y - b, l.Window.W + 2*b, l.Window.H + 2*b}
	l.BezelRadius = round(float64(f.Radius+bezelDp) * s)
	// Chrome draws a border of whole pixels; its width is rounded here so the PNG and its golden agree (inference).
	l.Border = max(1, round(bezelBorderDp*s))
	l.Cutout = cutout(f.Chrome, l, strip, s)

	return l, nil
}

// cutout is a device's camera: a punch-hole dot or an island, centered on the hole, in the strip when there is
// one, else over the picture's top (aidemo src/frame.ts:155-158).
func cutout(chrome Chrome, l Layout, strip, s float64) *Cutout {
	wDp, hDp, topDp := float64(dotDp), float64(dotDp), float64(dotTopDp)
	if chrome == ChromeIPhone {
		wDp, hDp, topDp = islandWDp, islandHDp, islandTopDp
	}

	if strip > 0 {
		topDp = math.Max(minCutTopDp, (strip-hDp)/2)
	}

	c := &Cutout{W: round(wDp * s), H: round(hDp * s), Top: l.Window.Y + round(topDp*s)}
	c.Left = float64(l.Hole.X) + float64(l.Hole.W)/2 - float64(c.W)/2
	c.Radius = float64(c.H) / 2

	return c
}

// Fits says whether a picture of w×h fills the hole when scaled to exactly its size: a contain fit into the hole
// before it was rounded leaves at most maxBarPx (agent-demo-video's fitPaddingPx). The hole's own rounding to even
// pixels is not counted, so a stretch of a few tenths of a percent is, and a turned phone is not.
func (l Layout) Fits(w, h int) bool {
	if w <= 0 || h <= 0 {
		return false
	}

	f := math.Min(l.ContentW/float64(w), l.ContentH/float64(h))

	return math.Max(l.ContentW-float64(w)*f, l.ContentH-float64(h)*f) <= maxBarPx
}

// round is JavaScript's Math.round, which the references use: halves go up.
func round(v float64) int { return int(math.Floor(v + 0.5)) }

// even is agent-demo-video's even, Math.round(n / 2) * 2.
func even(v float64) int { return 2 * round(v/2) }
