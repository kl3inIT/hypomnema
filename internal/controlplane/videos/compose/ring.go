// Ported from autodemo (https://github.com/mdayan8/autodemo) at commit 73e5408: the click ripple of src/render.js
// (line 141: it lasts 0.5 s from the click, its progress r from 0 to 1) and src/compositor.html (line 126: a ring
// of radius 12 + 34r, line width 4 and color rgba(70,130,255,0.9(1-r)), in the page's CSS pixels times the
// canvas scale). autodemo draws it on a canvas in Chrome; here each of its frames is a PNG drawn in Go, overlaid
// with ffmpeg as aidemo overlays its click rings (src/compose.ts, overlayPngBatches). Issue #818, row 7.
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
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// RingMs is how long the ring of a tap shows.
const RingMs = 500

// ringFrames is one PNG a frame of the ring's 0.5 s.
const ringFrames = RingMs * FPS / 1000

// dpWidth is the width of a phone's screen in Android's density-independent
// pixels, which stand for autodemo's CSS pixels: a ring is sized for a screen
// this wide (an inference; autodemo films a browser, whose pixels are CSS's).
const dpWidth = 360

// ringRadius and ringLine are autodemo's sizes, before the scale.
const (
	ringRadiusFrom = 12.0
	ringRadiusGrow = 34.0
	ringLine       = 4.0
)

// ringSize is the side of the ring's PNGs at scale s: the widest ring and its line.
func ringSize(s float64) int {
	return int(math.Ceil(2*(ringRadiusFrom+ringRadiusGrow+ringLine)*s)) | 1
}

// writeRing draws the ring's frames into dir as ring-00.png, ring-01.png, …
// at scale s, and returns the pattern ffmpeg reads them by.
func writeRing(dir string, s float64) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	size := ringSize(s)
	c := float64(size) / 2

	for k := range ringFrames {
		r := float64(k) / float64(ringFrames)
		radius := (ringRadiusFrom + ringRadiusGrow*r) * s
		half := ringLine * s / 2
		alpha := 0.9 * (1 - r)

		img := image.NewNRGBA(image.Rect(0, 0, size, size))

		for y := range size {
			for x := range size {
				d := math.Hypot(float64(x)+0.5-c, float64(y)+0.5-c)
				// Coverage of the stroke, smoothed over one pixel at its edges.
				cover := math.Max(0, math.Min(1, half+0.5-math.Abs(d-radius)))
				if cover == 0 {
					continue
				}

				img.SetNRGBA(x, y, color.NRGBA{R: 70, G: 130, B: 255, A: uint8(math.Round(255 * alpha * cover))})
			}
		}

		if err := writePNG(filepath.Join(dir, fmt.Sprintf("ring-%02d.png", k)), img); err != nil {
			return "", err
		}
	}

	return filepath.Join(dir, "ring-%02d.png"), nil
}

func writePNG(path string, img image.Image) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // The work folder.
	if err != nil {
		return err
	}

	err = png.Encode(f, img)

	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	return err
}
