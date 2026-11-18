// Ported from aidemo (https://github.com/tandryukha/aidemo) at commit b67235a, src/compose.ts: IDLE_CAP_MS, FPS,
// MAX_STRETCH and MIN_FACTOR (lines 76-81), the retime, hold and overrun trim of a scene (lines 300-411), mergeIdle
// (line 1675), sceneAutoIdle (line 1693), keepIntervals (line 1732) and offsetInKeeps (line 1772). GAP_MS is
// src/voice.ts line 37. Issue #818, rows 3 and 4.
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
	"cmp"
	"math"
	"slices"
)

// IdleCapMs is what is kept of each idle span: idle "thinking" spans are
// trimmed down to at most this, then time-stretched.
const IdleCapMs = 400

// FPS is the frame rate of every file made.
const FPS = 30

// Bounds on how much a scene is retimed before the last frame is held
// instead: real motion is not slowed beyond MaxStretch, nor sped up beyond
// 1/MinFactor.
const (
	MaxStretch = 1.6
	MinFactor  = 0.5
)

// GapMs is the silence after each scene's voice: a scene's target length is
// its voice and this gap.
const GapMs = 300

// The freezedetect settings of aidemo's autoIdle (AutoIdleSchema's defaults):
// motionless for at least FreezeMinMs, within FreezeNoise.
const (
	FreezeMinMs = 1500
	FreezeNoise = 0.003
)

const (
	// edgeMs of a scene's start and end are never idle: the frame right after
	// a tap and the one the scene ends on are the beats a viewer reads.
	edgeMs = 300
	// tapProtectMs after a step's tap are never idle (issue #818, row 4): the
	// screen answering the tap is what the scene shows.
	tapProtectMs = 600
)

// Span is [StartMs, EndMs) on the take's timeline.
type Span struct {
	StartMs int64 `json:"start_ms"`
	EndMs   int64 `json:"end_ms"`
}

// mergeIdle is the union of overlapping or touching idle spans, sorted.
func mergeIdle(spans []Span) []Span {
	sorted := slices.Clone(spans)
	slices.SortFunc(sorted, func(a, b Span) int { return cmp.Compare(a.StartMs, b.StartMs) })

	var out []Span

	for _, s := range sorted {
		if n := len(out); n > 0 && s.StartMs <= out[n-1].EndMs {
			out[n-1].EndMs = max(out[n-1].EndMs, s.EndMs)

			continue
		}

		out = append(out, s)
	}

	return out
}

// sceneAutoIdle turns the motionless spans of the take into idle spans of one
// scene. Spans are clipped to the scene, and its first and last 300 ms are
// left alone, as are the 600 ms after the step's tap (aidemo's protected
// windows are its attention, focus and still events; a phone take has taps).
func sceneAutoIdle(freezes []Span, sc Scene) []Span {
	lo := sc.StartMs + edgeMs
	hi := sc.EndMs - edgeMs

	var protected []Span
	if sc.Tap != nil {
		protected = append(protected, Span{sc.TapAtMs, sc.TapAtMs + tapProtectMs})
	}

	var out []Span

	for _, f := range freezes {
		a := max(f.StartMs, lo)
		b := min(f.EndMs, hi)

		if b-a <= 200 {
			continue
		}

		if slices.ContainsFunc(protected, func(p Span) bool { return p.StartMs < b && p.EndMs > a }) {
			continue
		}

		out = append(out, Span{a, b})
	}

	return out
}

// keepIntervals are the spans of a scene to keep: what is not idle in full,
// each idle span capped to IdleCapMs.
func keepIntervals(start, end int64, idle []Span) []Span {
	idles := slices.Clone(idle)
	slices.SortFunc(idles, func(a, b Span) int { return cmp.Compare(a.StartMs, b.StartMs) })

	var keeps []Span

	cursor := start

	for _, sp := range idles {
		s := max(sp.StartMs, cursor)
		e := min(sp.EndMs, end)

		if s > cursor {
			keeps = append(keeps, Span{cursor, s}) // what is not idle before the idle span
		}

		if e > s {
			keeps = append(keeps, Span{s, min(e, s+IdleCapMs)}) // the capped idle span
		}

		cursor = max(cursor, sp.EndMs)
	}

	if cursor < end {
		keeps = append(keeps, Span{cursor, end})
	}

	// Empty and tiny spans are dropped.
	return slices.DeleteFunc(keeps, func(k Span) bool { return k.EndMs-k.StartMs <= 20 })
}

// offsetInKeeps is where a timeline time lands in the kept spans joined; a
// time inside a cut lands on the cut's start.
func offsetInKeeps(keeps []Span, t int64) int64 {
	var off int64

	for _, k := range keeps {
		if t < k.StartMs {
			return off
		}

		if t <= k.EndMs {
			return off + (t - k.StartMs)
		}

		off += k.EndMs - k.StartMs
	}

	return off
}

// retime is how a scene's kept footage of srcMs fills its target: played at
// Factor for StretchedMs, its tail trimmed by TrimMs when even 2x speed
// overruns the target (so that no later scene drifts from its voice), and
// its last frame held for HoldMs when even x1.6 slow motion is short.
type retime struct {
	Factor      float64
	StretchedMs float64
	TrimMs      float64
	HoldMs      float64
}

func retimeScene(srcMs, targetMs int64) retime {
	ratio := float64(targetMs) / float64(max(srcMs, 1))
	factor := math.Min(MaxStretch, math.Max(MinFactor, ratio))
	stretched := factor * float64(srcMs)

	var trim float64
	if stretched > float64(targetMs)+40 {
		trim = stretched - float64(targetMs)
		stretched = float64(targetMs)
	}

	return retime{Factor: factor, StretchedMs: stretched, TrimMs: trim, HoldMs: math.Max(0, float64(targetMs)-stretched)}
}

// holding says whether the hold is long enough to be made.
func (r retime) holding() bool { return r.HoldMs > 40 }
