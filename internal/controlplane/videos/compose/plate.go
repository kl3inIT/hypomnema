// Ported from aidemo (https://github.com/tandryukha/aidemo) at commit b67235a, src/frame.ts: what renderFramePng
// draws (lines 107-190): the background, the device's bezel (#0b0b0d, a 2 px border of #2b2d33, the two shadows
// "0 24px 60px rgba(0,0,0,.55), 0 2px 8px rgba(0,0,0,.35)") or, with no device, the window's shadow, the window
// (#0b0b0d, or #0b0e18), the transparent rounded hole the picture shows through, and the camera's cut-out (#000)
// over it. Issue #908, row 12.
//
// What differs, and why: aidemo has Chrome rasterize its HTML; here the PNG is drawn in Go with the standard
// library, so it is made without a browser and tested without ffmpeg. Each shape is a signed distance, read at the
// pixel's center and covered over one pixel; a shadow is its shape's coverage blurred by three box passes, the
// approximation of a Gaussian of half the blur radius that the Filter Effects spec gives for feGaussianBlur.
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

package compose

import (
	"image"
	"math"
)

// The frame's colors (aidemo src/frame.ts:151-152, :177).
var (
	bezelColor  = hexRGBA("#0b0b0d")
	borderColor = hexRGBA("#2b2d33")
	windowColor = hexRGBA("#0b0e18")
	cameraColor = hexRGBA("#000000")
	black       = rgba{a: 1}
)

// Plate is the frame's PNG for a layout: the background, the shadow, the bezel and the window, transparent in the
// hole, the camera's cut-out on top. The picture is padded under it at the hole (row 12).
func Plate(l Layout, bg Background) *image.NRGBA {
	return plate(l, func(c *canvas) { paintBackground(c, bg) })
}

// plate draws the frame over what ground paints.
func plate(l Layout, ground func(*canvas)) *image.NRGBA {
	c := newCanvas(l.Size.Width, l.Size.Height)
	ground(c)

	device := l.Cutout != nil

	// The shadow is cast by the bezel, or by the window when there is no device (frame.ts:151, :177).
	caster := roundRect(l.Window, float64(l.HoleRadius))
	if device {
		caster = roundRect(l.Bezel, float64(l.BezelRadius))
	}

	if l.Shadow {
		for k := range l.ShadowY {
			c.shadow(caster, l.ShadowY[k], l.ShadowBlur[k]/2, shadowAlpha[k])
		}
	}

	if device {
		inner := Rect{l.Bezel.X + l.Border, l.Bezel.Y + l.Border, l.Bezel.W - 2*l.Border, l.Bezel.H - 2*l.Border}
		c.fill(caster, borderColor)
		c.fill(roundRect(inner, math.Max(0, float64(l.BezelRadius-l.Border))), bezelColor)
	}

	win := windowColor
	if device {
		win = bezelColor
	}

	c.fill(roundRect(l.Window, float64(l.HoleRadius)), win)
	c.cut(roundRect(l.Hole, float64(l.HoleRadius)))

	if device {
		cam := l.Cutout
		c.fill(shape{
			cx: cam.Left + float64(cam.W)/2, cy: float64(cam.Top) + float64(cam.H)/2,
			hw: float64(cam.W) / 2, hh: float64(cam.H) / 2, r: cam.Radius,
		}, cameraColor)
	}

	return c.image()
}

// shape is a rounded rectangle: its center, half its width and height, its corner radius.
type shape struct{ cx, cy, hw, hh, r float64 }

func roundRect(r Rect, radius float64) shape {
	return shape{
		cx: float64(r.X) + float64(r.W)/2, cy: float64(r.Y) + float64(r.H)/2,
		hw: float64(r.W) / 2, hh: float64(r.H) / 2, r: math.Min(radius, math.Min(float64(r.W), float64(r.H))/2),
	}
}

// cover is how much of the pixel centered at (x, y) the shape covers: its signed distance, over one pixel.
func (s shape) cover(x, y float64) float64 {
	qx := math.Abs(x-s.cx) - s.hw + s.r
	qy := math.Abs(y-s.cy) - s.hh + s.r
	d := math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - s.r

	return math.Max(0, math.Min(1, 0.5-d))
}

// canvas is a picture of premultiplied colors, each channel 0-1.
type canvas struct {
	w, h int
	px   []float64
}

func newCanvas(w, h int) *canvas { return &canvas{w: w, h: h, px: make([]float64, 4*w*h)} }

// each paints, source over, the color fn gives at each pixel's center with the coverage it gives.
func (c *canvas) each(fn func(x, y float64) (rgba, float64)) {
	for y := range c.h {
		for x := range c.w {
			col, cov := fn(float64(x)+0.5, float64(y)+0.5)
			if a := col.a * cov; a > 0 {
				c.over(4*(y*c.w+x), col, a)
			}
		}
	}
}

func (c *canvas) over(i int, col rgba, a float64) {
	p := c.px[i : i+4 : i+4]
	p[0] = col.r*a + p[0]*(1-a)
	p[1] = col.g*a + p[1]*(1-a)
	p[2] = col.b*a + p[2]*(1-a)
	p[3] = a + p[3]*(1-a)
}

func (c *canvas) fill(s shape, col rgba) {
	c.each(func(x, y float64) (rgba, float64) { return col, s.cover(x, y) })
}

// cut makes the shape transparent: aidemo's mask, opaque everywhere but the hole.
func (c *canvas) cut(s shape) {
	for y := range c.h {
		for x := range c.w {
			if cov := s.cover(float64(x)+0.5, float64(y)+0.5); cov > 0 {
				i := 4 * (y*c.w + x)
				for k := range 4 {
					c.px[i+k] *= 1 - cov
				}
			}
		}
	}
}

// shadow paints a black shadow of the shape, moved down by dy and blurred with sigma, at alpha (CSS box-shadow).
func (c *canvas) shadow(s shape, dy, sigma, alpha float64) {
	mask := make([]float64, c.w*c.h)

	for y := range c.h {
		for x := range c.w {
			mask[y*c.w+x] = s.cover(float64(x)+0.5, float64(y)+0.5-dy)
		}
	}

	blur(mask, c.w, c.h, sigma)

	for i, m := range mask {
		if a := m * alpha; a > 0 {
			c.over(4*i, black, a)
		}
	}
}

// blur is three box blurs a direction approximating a Gaussian of sigma: boxes of d = ⌊sigma·3·√(2π)/4 + 0.5⌋,
// the three centered when d is odd; when it is even, two of d, off center to either side, and one of d+1 (the
// Filter Effects spec, feGaussianBlur).
func blur(a []float64, w, h int, sigma float64) {
	d := int(math.Floor(sigma*3*math.Sqrt(2*math.Pi)/4 + 0.5))
	if d < 2 {
		return
	}

	boxes := [3][2]int{{(d - 1) / 2, (d - 1) / 2}, {(d - 1) / 2, (d - 1) / 2}, {(d - 1) / 2, (d - 1) / 2}}
	if d%2 == 0 {
		boxes = [3][2]int{{d / 2, d/2 - 1}, {d/2 - 1, d / 2}, {d / 2, d / 2}}
	}

	tmp := make([]float64, max(w, h))

	for _, b := range boxes {
		for y := range h {
			box(a[y*w:], w, 1, b[0], b[1], tmp)
		}
	}

	for _, b := range boxes {
		for x := range w {
			box(a[x:], h, w, b[0], b[1], tmp)
		}
	}
}

// box averages each of n values, stride apart, over the lo before it and the hi after it; outside is zero.
func box(a []float64, n, stride, lo, hi int, tmp []float64) {
	size := float64(lo + hi + 1)
	sum := 0.0

	for k := 0; k < hi && k < n; k++ {
		sum += a[k*stride]
	}

	for i := range n {
		if j := i + hi; j < n {
			sum += a[j*stride]
		}

		tmp[i] = sum / size

		if j := i - lo; j >= 0 {
			sum -= a[j*stride]
		}
	}

	for i := range n {
		a[i*stride] = tmp[i]
	}
}

// image is the canvas as 8-bit straight alpha.
func (c *canvas) image() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, c.w, c.h))

	for i := 0; i < len(c.px); i += 4 {
		a := c.px[i+3]
		if a <= 0 {
			continue
		}

		img.Pix[i] = byte8(c.px[i] / a)
		img.Pix[i+1] = byte8(c.px[i+1] / a)
		img.Pix[i+2] = byte8(c.px[i+2] / a)
		img.Pix[i+3] = byte8(a)
	}

	return img
}

func byte8(v float64) uint8 { return uint8(math.Round(255 * math.Max(0, math.Min(1, v)))) }
