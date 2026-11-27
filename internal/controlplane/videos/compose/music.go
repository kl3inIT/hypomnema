// Ported from aidemo (https://github.com/tandryukha/aidemo) at commit b67235a, src/compose.ts: muxAudio's music bed
// (lines 2081-2200: the bed under the narration with its gain, faded out over its last 1800 ms, mixed by amix with
// normalize=0 so neither input is divided by their count, AAC 192k) and the -16 LUFS master of LoudnessSchema in
// src/types.ts:803-812. And from Kinocut (https://github.com/KyaniteLabs/kinocut) at commit d46975a: the voice's gain
// to a loudness target held under a true-peak ceiling (kinocut/engine_audio_normalize.py:159-166, the peak after a
// linear gain), the measured output's I and TP (kinocut/engine_audio_normalize_output.py:17-28), the mix of voice and
// bed with amix duration=first:normalize=0 (kinocut/engine_audio_bed_filters.py:22-46) and the stereo format of each
// input (kinocut/engine_audio_mix.py:44-46). Issue #910, rows 7, 8, 11, 12 and 14. Modified, and why: the voice is
// set to -16 LUFS by a measured linear gain, the bed 3 LU under it (HyperFrames' "music under it about -31 LUFS",
// skills/media-use/audio/references/tts.md:21) and ducked by the scenes' clips (duck.go), and the bed is made
// beforehand (bed.go). The master is aidemo's final loudnorm to -16 LUFS integrated, -1.5 dBTP and an LRA of 11, its
// 192 kHz put back to 44.1 kHz (src/compose.ts:2176-2187, docs/AUTHORING.md:400-406; issue #1230, row 2), where #910
// left the level where the voice's peak let its gain put it (-21.2 LUFS on the SecondBite trial). It is aimed by a
// first pass that measures the mix, as Kinocut's audio normalization runs loudnorm twice (Kinocut at commit c890db7,
// kinocut/engine_audio_normalize.py:142-187, 254-299): aidemo's single pass turned a mix of three clips 10 dB apart
// from -16.5 LUFS into -19.7 LUFS, which the second pass makes -16.0 (TestATrackUploadedAndMadeIsDuckedUnderTheVoice).
// #910's alimiter at -3 dBFS at four times the rate stays after it, where aidemo has none: AAC goes over loudnorm's
// -1.5 dBTP, to -0.8 dBTP on TestTheMixIsLimited's clicks without it, and the limiter costs a tenth of a LU on speech
// (TestTheMasterIsAtItsLoudness). The result is measured, a level still off its target being a warning.
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
// Kinocut: Copyright 2026 Simon Gonzalez de Cruz
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
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Music is a track laid under the voice (issue #910).
type Music struct {
	// File is the track; its first audio stream is used.
	File string
	// LengthMs is the track's decoded length, Measure's LengthMs.
	LengthMs int64
	// GainDB moves the bed from its level 3 LU under the voice, from MinMusicGainDB to MaxMusicGainDB.
	GainDB float64

	// NoDuck leaves out the duck: the control a test of a whole revision renders beside it (issue #910, PR 2's
	// check 11); the runner never sets it.
	NoDuck bool
	// NoMaster leaves out the master's loudnorm and limiter (issue #1230, row 2), whose gain follows each mix's own
	// measure: the twin and the control a test of a whole revision renders beside it compare their mixes without
	// it; the runner never sets it.
	NoMaster bool
}

// MusicResult is the levels a mix was made at and measured at: the voice's and the bed's loudness before their
// gains, the gains, and the made video's loudness and true peak.
type MusicResult struct {
	VoiceLUFS   float64 `json:"voice_lufs"`
	VoiceGainDB float64 `json:"voice_gain_db"`
	BedLUFS     float64 `json:"bed_lufs"`
	BedGainDB   float64 `json:"bed_gain_db"`
	OutLUFS     float64 `json:"out_lufs"`
	OutTPdB     float64 `json:"out_tp_db"`
}

// The levels of the mix (issue #910, rows 7, 8 and 12).
const (
	// VoiceLUFS is the voice's loudness after its gain, aidemo's master target.
	VoiceLUFS = -16.0
	// voiceCeilingDBTP is the voice's true peak after its gain at most, Kinocut's bed true peak.
	voiceCeilingDBTP = -1.5
	// bedUnderLU is how far under the voice the bed is set before its own gain and the duck.
	bedUnderLU = 3.0
	// maxBedGainDB caps the gain a quiet bed is given.
	maxBedGainDB = 24.0
	// The range of Music.GainDB.
	MinMusicGainDB = -20.0
	MaxMusicGainDB = 10.0
	// fadeOutMs is the bed's fade at the video's end, aidemo's default.
	fadeOutMs = 1800
	// The master's loudnorm, aidemo's LoudnessSchema (src/types.ts:806-813): -16 LUFS integrated, -1.5 dBTP, LRA 11.
	masterLUFS = VoiceLUFS
	masterTPdB = -1.5
	masterLRA  = 11
	// limiterLimit is alimiter's ceiling after the loudnorm, -3 dBFS, run at limiterRate (#910, row 12).
	limiterLimit = 0.707946
	limiterRate  = 176400
	// A made video whose true peak is over warnPeakDBTP, or whose loudness is more than warnLoudnessLU off
	// VoiceLUFS, is made with a warning.
	warnPeakDBTP   = -1.0
	warnLoudnessLU = 2.0
	// minFreeBytes is the free disk kept beside the bed, as a revision keeps (videos.minFreeBytes).
	minFreeBytes = 400_000_000
)

// Check says whether a track can be laid under a video: a positive length and a gain in range.
func (m Music) Check() error {
	if m.File == "" || m.LengthMs <= 0 {
		return fmt.Errorf("the music needs a file and its length; it has %q and %d ms", m.File, m.LengthMs)
	}

	if m.GainDB < MinMusicGainDB || m.GainDB > MaxMusicGainDB || math.IsNaN(m.GainDB) {
		return fmt.Errorf("the music's gain is %g dB, outside %g to %g", m.GainDB, MinMusicGainDB, MaxMusicGainDB)
	}

	return nil
}

// voiceGraph is the voice's chain, from input first on: each scene's clip resampled and padded with silence to
// the length of its scene's video, the clips joined, as [aout] (aidemo's assembleNarration).
func (c *composer) voiceGraph(first int) []string {
	filters := make([]string, 0, len(c.result.Scenes)+1)
	labels := make([]string, 0, len(c.result.Scenes))

	for k, sc := range c.result.Scenes {
		sec := float64(sc.OutLengthMs) / 1000
		filters = append(filters, fmt.Sprintf(
			"[%d:a]aresample=44100,aformat=sample_rates=44100:channel_layouts=mono,"+
				"apad=whole_dur=%.3f,atrim=end=%.3f[a%d]", k+first, sec, sec, k))
		labels = append(labels, fmt.Sprintf("[a%d]", k))
	}

	join := fmt.Sprintf("%sconcat=n=%d:v=0:a=1", strings.Join(labels, ""), len(labels))
	if title, end := c.result.TitleMs, c.result.EndMs; title > 0 || end > 0 {
		// The voice starts after the title card and is silent under the end card (issue #1230, row 1).
		if title > 0 {
			join += fmt.Sprintf(",adelay=delays=%d:all=1", title)
		}

		join += fmt.Sprintf(",apad=whole_dur=%s,atrim=end=%s", ms(c.totalMs()), ms(c.totalMs()))
	}

	return append(filters, join+"[aout]")
}

// mixMusic makes the video with the music under the voice: the voice written and measured, the bed made and
// measured, both set to their levels, the bed ducked under the clips and faded out, mixed, normalized and encoded,
// and the result measured. A bed silent over the video's length leaves the video without music, with a warning;
// done is false then, and the caller makes it as without music.
func (c *composer) mixMusic(ctx context.Context, video string, totalMs int64) (string, bool, error) {
	m := *c.in.Music
	voiceFile := filepath.Join(c.in.Work, "voice.wav")

	args := make([]string, 0, 2*len(c.in.Voice)+8)
	for _, v := range c.in.Voice {
		args = append(args, "-i", v)
	}

	args = append(args, "-filter_complex", strings.Join(c.voiceGraph(0), ";"), "-map", "[aout]",
		"-c:a", "pcm_f32le", voiceFile)
	if err := c.run.ffmpeg(ctx, args...); err != nil {
		return "", false, fmt.Errorf("writing the voice: %w", err)
	}

	voice, err := Measure(ctx, c.run.tools, voiceFile, 0)
	if err != nil {
		return "", false, fmt.Errorf("measuring the voice: %w", err)
	}

	need := uint64(minFreeBytes) + uint64(totalMs)*BedBytesPerSec/1000 //nolint:gosec // A video's length is positive.
	if free, err := c.freeBytes(c.in.Work); err == nil && free < need {
		return "", false, fmt.Errorf("the control plane has %d MB of disk free; the music's bed needs %d MB",
			free/1_000_000, need/1_000_000)
	}

	bedFile := filepath.Join(c.in.Work, "music-bed.wav")
	if err := c.run.prepareBed(ctx, m, totalMs, bedFile); err != nil {
		return "", false, fmt.Errorf("making the music's bed: %w", err)
	}

	bed, err := Measure(ctx, c.run.tools, bedFile, 0)
	if err != nil {
		return "", false, fmt.Errorf("measuring the music's bed: %w", err)
	}

	if bed.LUFS <= SilentLUFS {
		c.warn("the music is silent over the video's %s s, so the video has no music", ms(totalMs))

		return "", false, nil
	}

	levels := MusicResult{VoiceLUFS: voice.LUFS, BedLUFS: bed.LUFS}
	levels.VoiceGainDB = math.Min(VoiceLUFS-voice.LUFS, voiceCeilingDBTP-voice.TPdB())
	levels.BedGainDB = levels.VoiceLUFS + levels.VoiceGainDB - bedUnderLU - levels.BedLUFS + m.GainDB

	if levels.BedGainDB > maxBedGainDB {
		c.warn("the music is so quiet that it needs %+.1f dB to be %g LU under the voice; it is given %+g dB",
			levels.BedGainDB, bedUnderLU, maxBedGainDB)
		levels.BedGainDB = maxBedGainDB
	}

	out, err := c.muxMusic(ctx, video, voiceFile, bedFile, totalMs, levels)
	if err != nil {
		return "", false, err
	}

	c.result.Music = &levels
	c.checkMix(ctx, out)

	return out, true, nil
}

// muxMusic is the mux with the music: the video, captions burned when there are any, and the voice and the bed at
// their gains, the bed ducked and faded, mixed and normalized by the master, encoded as AAC. The graph is read from
// a file (-/filter_complex), as a long video's duck is long.
func (c *composer) muxMusic(ctx context.Context, video, voice, bed string, totalMs int64, levels MusicResult,
) (string, error) {
	fade := min(fadeOutMs, totalMs)
	bedChain := fmt.Sprintf("[2:a]volume=%.3fdB", levels.BedGainDB)

	if !c.in.Music.NoDuck {
		bedChain += fmt.Sprintf(",asetnsamples=n=256,volume='%s':eval=frame", duckGain(duckSpans(c.result.Scenes)))
	}

	bedChain += fmt.Sprintf(",afade=t=out:st=%s:d=%s[bed]", ms(totalMs-fade), ms(fade))

	mix := []string{
		fmt.Sprintf("[1:a]volume=%.3fdB,aformat=channel_layouts=stereo[voice]", levels.VoiceGainDB),
		bedChain,
		"[voice][bed]amix=inputs=2:normalize=0:duration=first[mixed]",
	}

	master := "[mixed]anull[aout]"
	if !c.in.Music.NoMaster {
		measured, err := c.measureMix(ctx, video, voice, bed, mix)
		if err != nil {
			return "", err
		}

		master = fmt.Sprintf("[mixed]%s,aresample=%d,alimiter=limit=%g:level=false:latency=true,aresample=44100[aout]",
			loudnorm(measured), limiterRate, limiterLimit)
	}

	filters := append(mix, master) //nolint:gocritic // mix is not used after.

	videoMap, videoCodec := "0:v", []string{"-c:v", "copy"}

	if c.in.Captions != "" {
		if err := c.stageCaptions(); err != nil {
			return "", err
		}

		filters = append(filters, "[0:v]subtitles=filename=captions.ass:fontsdir=fonts[vout]")
		videoMap, videoCodec = "[vout]", encodeArgs
	}

	graph := filepath.Join(c.in.Work, "mix.graph")
	if err := os.WriteFile(graph, []byte(strings.Join(filters, ";")), 0o600); err != nil {
		return "", err
	}

	out := filepath.Join(c.in.Work, "video.mp4")
	args := []string{"-i", video, "-i", voice, "-i", bed, "-/filter_complex", graph, "-map", videoMap, "-map", "[aout]"}
	args = append(args, videoCodec...)
	args = append(args, "-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", "-t", ms(totalMs), out)

	return out, c.run.ffmpeg(ctx, args...)
}

// loudnorm is the master's loudnorm: with the mix's first-pass measure, its second pass, which applies one gain when
// that keeps the true peak under its ceiling and else its dynamic mode, both aimed by the measure (Kinocut's
// _build_loudnorm_render_filter, engine_audio_normalize.py:142-187); without one, aidemo's single pass.
func loudnorm(measured string) string {
	out := fmt.Sprintf("loudnorm=I=%g:TP=%g:LRA=%d", masterLUFS, masterTPdB, masterLRA)
	if measured != "" {
		out += ":" + measured + ":linear=true"
	}

	return out
}

// loudnormJSON is the summary loudnorm writes, as Kinocut finds it (_loudnorm_summaries, engine_audio_normalize.py:
// 49-58): the JSON block after the filter's name.
var loudnormJSON = regexp.MustCompile(`(?s)\[Parsed_loudnorm_[^\]]*\]\s*(\{.*?\})`)

// measureMix is the master's first pass (Kinocut's two-pass loudnorm, engine_audio_normalize.py:254-299): the mix,
// as the mux makes it, through loudnorm with print_format=json, read into its measured_I, measured_TP, measured_LRA,
// measured_thresh and offset (_measurement, lines 61-81). A mix it cannot measure, as one whose loudness is gated
// away, gives no measure, and the master is then one pass.
func (c *composer) measureMix(ctx context.Context, video, voice, bed string, mix []string) (string, error) {
	graph := filepath.Join(c.in.Work, "measure.graph")
	analysis := append(append([]string{}, mix...), "[mixed]"+loudnorm("")+":print_format=json[aout]")

	if err := os.WriteFile(graph, []byte(strings.Join(analysis, ";")), 0o600); err != nil {
		return "", err
	}

	log, err := c.run.ffmpegLog(ctx, "-i", video, "-i", voice, "-i", bed, "-/filter_complex", graph,
		"-map", "[aout]", "-f", "null", "-")
	if err != nil {
		return "", fmt.Errorf("measuring the mix: %w", err)
	}

	blocks := loudnormJSON.FindAllStringSubmatch(log, -1)
	if len(blocks) == 0 {
		return "", fmt.Errorf("measuring the mix: loudnorm wrote no summary")
	}

	var summary map[string]string
	if err := json.Unmarshal([]byte(blocks[len(blocks)-1][1]), &summary); err != nil {
		return "", fmt.Errorf("measuring the mix: %w", err)
	}

	parts := make([]string, 0, 5)

	for _, k := range []struct{ to, from string }{
		{"measured_I", "input_i"}, {"measured_TP", "input_tp"}, {"measured_LRA", "input_lra"},
		{"measured_thresh", "input_thresh"}, {"offset", "target_offset"},
	} {
		v, perr := strconv.ParseFloat(summary[k.from], 64)
		if perr != nil || math.IsInf(v, 0) || math.IsNaN(v) {
			// A measure that is not a number, as -inf for a gated mix, is no measure (Kinocut's _measurement).
			return "", nil //nolint:nilerr // No measure is one pass, not a failure.
		}

		parts = append(parts, fmt.Sprintf("%s=%g", k.to, v))
	}

	return strings.Join(parts, ":"), nil
}

// checkMix measures the made video's sound into the result, and warns when its true peak or its loudness is off;
// a measurement that fails is a warning.
func (c *composer) checkMix(ctx context.Context, out string) {
	got, err := Measure(ctx, c.run.tools, out, 0)
	if err != nil {
		c.warn("the mix could not be measured: %s", firstLine(err))

		return
	}

	c.result.Music.OutLUFS, c.result.Music.OutTPdB = got.LUFS, got.TPdB()

	if got.TPdB() > warnPeakDBTP {
		c.warn("the mix's true peak is %.1f dBTP, over %g dBTP", got.TPdB(), warnPeakDBTP)
	}

	if math.Abs(got.LUFS-VoiceLUFS) > warnLoudnessLU {
		c.warn("the mix is %.1f LUFS, more than %g LU off %g LUFS", got.LUFS, warnLoudnessLU, VoiceLUFS)
	}
}
