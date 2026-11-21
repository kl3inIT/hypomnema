// Ported from Kinocut (https://github.com/KyaniteLabs/kinocut) at commit d46975a: kinocut/engine_audio_bed_filters.py,
// _compute_loop_plays and _build_loop_filtergraph (lines 68-95: a short bed split into plays, each trimmed to the
// bed's length and crossfaded into the next, the whole trimmed to the target), and kinocut/engine_audio_bed.py,
// _prepare_looped_bed (lines 179-205: the bed read with -stream_loop -1 and written as a WAV file, cut at the target
// with -t); its constants DEFAULT_AUDIO_BED_LOOP_CROSSFADE (1.5 s) and DEFAULT_AUDIO_BED_LOOP_MAX_PLAYS (100) in
// kinocut/defaults.py:128-136. Issue #910, row 10. Modified: the plays are ⌈(T − 1.5)/(L − 1.5)⌉, where Kinocut adds
// a crossfade's margin, (T + 1.5)/(L − 1.5), and at least 2 (n plays with 1.5 s crossfades already last
// n·L − (n − 1)·1.5 ≥ T); the bed is made stereo 44.1 kHz float first and written as pcm_f32le, where Kinocut writes
// pcm_s16le, so a seam's overshoot is kept for the gain and the limiter instead of clipped; the crossfade's curves
// are equal-power (qsin), as Cap's are (layout only; crates/project/src/configuration.rs:2188-2189); a bed as long
// as the video or longer is only cut.
//
// Copyright 2026 Simon Gonzalez de Cruz
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
// The notice is in third_party/kinocut/NOTICE.

package compose

import (
	"context"
	"fmt"
	"strings"
)

const (
	// loopCrossfadeMs is the crossfade between two plays of a looped bed.
	loopCrossfadeMs = 1500
	// MaxLoopPlays is the most plays a bed is looped to: a track too short for the video in as many is refused.
	MaxLoopPlays = 100
	// bedFormat is the bed's format: stereo 44.1 kHz float, set before it is looped or cut (issue #910, row 10).
	bedFormat = "aresample=44100,aformat=sample_fmts=flt:channel_layouts=stereo"
	// BedBytesPerSec is the size of a second of the bed's file: 44 100 frames of two 4-byte samples.
	BedBytesPerSec = 44100 * 2 * 4
)

// loopPlays is how many plays of a track lengthMs long, crossfaded, cover targetMs: the least n with
// n·L − (n − 1)·1.5 s ≥ T, that is ⌈(T − 1.5)/(L − 1.5)⌉; more than MaxLoopPlays is an error.
func loopPlays(lengthMs, targetMs int64) (int, error) {
	if lengthMs <= loopCrossfadeMs {
		return 0, fmt.Errorf("the music is %d ms long, not longer than its loop's %d ms crossfade", lengthMs,
			loopCrossfadeMs)
	}

	over, each := targetMs-loopCrossfadeMs, lengthMs-loopCrossfadeMs
	n := (over + each - 1) / each

	if n > MaxLoopPlays {
		return 0, fmt.Errorf("the music is %s s long and the video %s s: covering it takes %d plays, more than %d",
			ms(lengthMs), ms(targetMs), n, MaxLoopPlays)
	}

	return int(max(n, 1)), nil
}

// loopGraph is Kinocut's loop: the track, made stereo 44.1 kHz float, split into plays, each trimmed to the
// track's length, crossfaded one into the next, the whole trimmed to the target, as [out].
func loopGraph(lengthMs, targetMs int64, plays int) string {
	var labels strings.Builder
	for i := range plays {
		fmt.Fprintf(&labels, "[s%d]", i)
	}

	parts := []string{fmt.Sprintf("[0:a:0]%s,asplit=%d%s", bedFormat, plays, labels.String())}
	for i := range plays {
		parts = append(parts, fmt.Sprintf("[s%d]atrim=0:%s,asetpts=PTS-STARTPTS[p%d]", i, ms(lengthMs), i))
	}

	previous := "p0"

	for i := 1; i < plays; i++ {
		out := fmt.Sprintf("x%d", i)
		if i == plays-1 {
			out = "looped"
		}

		parts = append(parts, fmt.Sprintf("[%s][p%d]acrossfade=d=%s:c1=qsin:c2=qsin[%s]", previous, i,
			ms(loopCrossfadeMs), out))
		previous = out
	}

	parts = append(parts, fmt.Sprintf("[%s]atrim=0:%s,asetpts=PTS-STARTPTS[out]", previous, ms(targetMs)))

	return strings.Join(parts, ";")
}

// prepareBed writes the bed for a video targetMs long into out, a pcm_f32le stereo 44.1 kHz WAV file: the track
// cut at the target when it is as long or longer, else looped (Kinocut's _prepare_looped_bed).
func (r runner) prepareBed(ctx context.Context, m Music, targetMs int64, out string) error {
	if m.LengthMs >= targetMs {
		return r.ffmpeg(ctx, "-i", m.File, "-map", "0:a:0", "-af", bedFormat, "-t", ms(targetMs),
			"-c:a", "pcm_f32le", out)
	}

	plays, err := loopPlays(m.LengthMs, targetMs)
	if err != nil {
		return err
	}

	return r.ffmpeg(ctx, "-stream_loop", "-1", "-i", m.File, "-filter_complex", loopGraph(m.LengthMs, targetMs, plays),
		"-map", "[out]", "-t", ms(targetMs), "-c:a", "pcm_f32le", out)
}
