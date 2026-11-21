// Ported from HyperFrames (https://github.com/heygen-com/hyperframes) at commit a744f5c,
// skills/media-use/scripts/lib/duck.mjs: speechSpans' merge of spans less than 0.6 s apart (mergeIntervals, lines
// 89-102) and duckKeyframes (lines 37-56: down to 0.25 of the bed over 0.15 s from a span's start, back to 1 over
// 0.4 s from its end). Issue #910, row 9. Modified: the spans are the scenes' clips on the video, where HyperFrames
// takes each word's; and the keyframes are written as one ffmpeg expression of t for the volume filter, where
// HyperFrames writes a volume lane for its player.
//
// Copyright 2026 HeyGen, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// The notice is in third_party/hyperframes/NOTICE.

package compose

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// The duck's shape, HyperFrames' duckKeyframes defaults (duck.mjs:39) and speechSpans' mergeGap (duck.mjs:4).
const (
	// duckTo is the bed's gain while the voice speaks: 0.25, about -12 dB.
	duckTo = 0.25
	// duckAttackMs is how long the bed takes to go down from a span's start, and duckReleaseMs to come back up
	// from its end.
	duckAttackMs  = 150
	duckReleaseMs = 400
	// duckMergeMs: spans less than this apart are one span, and the bed stays down between them.
	duckMergeMs = 600
)

// duckSpans are the spans the bed is ducked under, on the video's time: each scene's clip, from its scene's
// start for the clip's length, merged when less than duckMergeMs apart (issue #910, row 9).
func duckSpans(scenes []SceneResult) []Span {
	spans := make([]Span, 0, len(scenes))
	for _, sc := range scenes {
		if sc.VoiceMs > 0 {
			spans = append(spans, Span{sc.OutStartMs, sc.OutStartMs + sc.VoiceMs})
		}
	}

	return mergeSpeech(spans)
}

// mergeSpeech is HyperFrames' mergeIntervals: sorted, a span that overlaps the one before or starts less than
// duckMergeMs after it ends is joined to it.
func mergeSpeech(spans []Span) []Span {
	sorted := slices.Clone(spans)
	slices.SortFunc(sorted, func(a, b Span) int {
		return cmp.Or(cmp.Compare(a.StartMs, b.StartMs), cmp.Compare(a.EndMs, b.EndMs))
	})

	var merged []Span

	for _, s := range sorted {
		if n := len(merged); n > 0 && (s.StartMs <= merged[n-1].EndMs || s.StartMs-merged[n-1].EndMs < duckMergeMs) {
			merged[n-1].EndMs = max(merged[n-1].EndMs, s.EndMs)

			continue
		}

		merged = append(merged, s)
	}

	return merged
}

// duckGain is the bed's gain at time t, in seconds, as an expression of ffmpeg's volume filter (eval=frame):
//
//	1 - (1-duckTo) * clip(Σ [clip((t-a)/attack, 0, 1) - clip((t-b)/release, 0, 1)], 0, 1)
//
// over the spans [a, b]: the linear ramps between HyperFrames' keyframes, down from a, up from b.
func duckGain(spans []Span) string {
	if len(spans) == 0 {
		return "1"
	}

	terms := make([]string, 0, len(spans))
	for _, s := range spans {
		terms = append(terms, fmt.Sprintf("clip((t-%s)/%s,0,1)-clip((t-%s)/%s,0,1)",
			ms(s.StartMs), ms(duckAttackMs), ms(s.EndMs), ms(duckReleaseMs)))
	}

	return fmt.Sprintf("1-%g*clip(%s,0,1)", 1-duckTo, strings.Join(terms, "+"))
}
