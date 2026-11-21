// The ambient bed of TestTheBed is ported from agent-demo-video (https://github.com/OrionArchitekton/agent-demo-video)
// at commit f63b61a, src/sound.ts, ambientBedArgs (lines 21-29): brown noise at 44.1 kHz through
// lowpass=f=300,highpass=f=45,tremolo=f=0.12:d=0.22,volume=6dB. Modified: 7.3 s long, written as a 16-bit WAV, as
// a track with dynamics to loop. The notice is in third_party/agent-demo-video/NOTICE.
//
// MIT License
//
// Copyright (c) 2026 agent-demo-video authors
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
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// The music's tests (issue #910, PR 1's checks). Their audio is made by lavfi, as agent-demo-video synthesizes its
// bed (src/sound.ts:1-30); no audio file is committed. The voice's clips are tones of 440 Hz and the music's are of
// other pitches or noise, so each is read on its own in the mix by its pitch.

const (
	mixRate  = 44100
	voiceHz  = 440
	musicHz  = 2500
	windowMs = 100
)

// The numbers of design v5 (#910), written out here rather than read from the code, so that a change of one in the
// code fails the tests: the voice at -16 LUFS under a -1.5 dBTP ceiling (row 8), the bed 3 LU under it plus the
// track's gain, at most +10 dB (row 7), its fade over 1.8 s (row 11), its loop's 1.5 s crossfades and its float
// stereo 44.1 kHz file of 352 800 B/s with 400 MB of disk kept beside it (row 10), and the output's warnings over
// -1 dBTP or 2 LU off -16 (row 12).
const (
	v5VoiceLUFS      = -16.0
	v5VoiceCeiling   = -1.5
	v5BedUnderLU     = 3.0
	v5MaxGainDB      = 10.0
	v5FadeMs         = 1800
	v5CrossfadeMs    = 1500
	v5BedBytesPerSec = 352_800
	v5FreeFloor      = 400_000_000
	v5PeakDBTP       = -1.0
	v5LoudnessLU     = 2.0
)

// clip is a voice clip: a tone of voiceHz, its peak at dbfs, ms long.
type clip struct {
	dbfs float64
	ms   int
}

// lavfi writes what an ffmpeg lavfi source makes into path, with the output's args.
func lavfi(t *testing.T, tools ffmpeg.Tools, path, source string, args ...string) string {
	t.Helper()

	full := append([]string{"-hide_banner", "-v", "error", "-y", "-f", "lavfi", "-i", source}, args...)
	//nolint:gosec // The test's ffmpeg.
	out, err := exec.CommandContext(t.Context(), tools.FFmpeg, append(full, path)...).CombinedOutput()
	require.NoError(t, err, "%s", out)

	return path
}

// sineTrack is a mono 44.1 kHz WAV of a sine of hz at peak amp, sec long.
func sineTrack(t *testing.T, tools ffmpeg.Tools, path string, hz, amp, sec float64) string {
	t.Helper()

	return lavfi(t, tools, path, fmt.Sprintf("aevalsrc=%g*sin(2*PI*%g*t):s=%d:d=%g", amp, hz, mixRate, sec),
		"-c:a", "pcm_s16le")
}

// musicTake writes a take of one 3 s segment with a step for each clip, 500 ms apart, and the clips' voices.
func musicTake(t *testing.T, tools ffmpeg.Tools, dir string, clips []clip) []string {
	t.Helper()

	writeSegment(t, tools, filepath.Join(dir, "0.mp4"), 360, 640, 0, 3000, 2967,
		func(tMs float64) bool { return true })

	take := devicetool.Take{
		ID: "tk_music", DurationMs: 3000,
		Segments: []devicetool.TakeSegment{{File: "0.mp4", LengthMs: 3000, LastFrameMs: 2967, Width: 360, Height: 640}},
	}

	voices := make([]string, len(clips))

	for i, c := range clips {
		take.Steps = append(take.Steps, devicetool.TakeStep{AtMs: int64(i) * 500, Text: fmt.Sprintf("Bước %d", i+1)})

		voices[i] = lavfi(t, tools, filepath.Join(dir, fmt.Sprintf("voice-%d.wav", i)),
			fmt.Sprintf("aevalsrc=%g*sin(2*PI*%d*t):s=%d:d=%s", math.Pow(10, c.dbfs/20), voiceHz, sampleRate,
				ms(int64(c.ms))), "-c:a", "pcm_s16le")
	}

	raw, err := json.Marshal(take)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "take.json"), raw, 0o600))

	return voices
}

// made is a video Compose made with music, and its work folder.
type made struct {
	Result
	out, work string
}

func composeWith(t *testing.T, tools ffmpeg.Tools, dir string, voices []string, m *Music, name string) made {
	t.Helper()

	work := filepath.Join(dir, "work-"+name)
	out := filepath.Join(dir, name+".mp4")
	res, err := Compose(t.Context(), tools, Input{
		Dir: dir, Voice: voices, Aspect: Portrait, Work: work, Out: out, Music: m,
	})
	require.NoError(t, err)

	return made{res, out, work}
}

// decode is a file's first audio stream as float samples at 44.1 kHz, of channels channels, interleaved.
func decode(t *testing.T, tools ffmpeg.Tools, file string, channels int) []float32 {
	t.Helper()

	//nolint:gosec // The test's ffmpeg.
	out, err := exec.CommandContext(t.Context(), tools.FFmpeg, "-v", "error", "-i", file, "-map", "0:a:0",
		"-ac", strconv.Itoa(channels), "-ar", strconv.Itoa(mixRate), "-f", "f32le", "-").Output()
	require.NoError(t, err)

	samples := make([]float32, len(out)/4)
	require.NoError(t, binary.Read(bytes.NewReader(out), binary.LittleEndian, samples))

	return samples
}

// bandDB is the level of the tone of hz in samples [fromMs, fromMs+windowMs), mono, as the peak of a sine in dBFS.
func bandDB(samples []float32, fromMs, hz float64) float64 {
	a := int(fromMs * mixRate / 1000)
	n := windowMs * mixRate / 1000
	coeff := 2 * math.Cos(2*math.Pi*hz/mixRate)

	var s1, s2 float64

	for _, x := range samples[a : a+n] {
		s0 := float64(x) + coeff*s1 - s2
		s2, s1 = s1, s0
	}

	power := s1*s1 + s2*s2 - coeff*s1*s2

	return 20 * math.Log10(2*math.Sqrt(math.Max(power, 1e-30))/float64(n))
}

// rmsDB is the RMS level of samples [fromMs, fromMs+windowMs), mono, in dBFS.
func rmsDB(samples []float32, fromMs float64) float64 {
	a := int(fromMs * mixRate / 1000)

	var sum float64
	for _, x := range samples[a : a+windowMs*mixRate/1000] {
		sum += float64(x) * float64(x)
	}

	return 10 * math.Log10(math.Max(sum/float64(windowMs*mixRate/1000), 1e-30))
}

func peakOf(samples []float32) float64 {
	var p float64
	for _, x := range samples {
		p = math.Max(p, math.Abs(float64(x)))
	}

	return p
}

// PR1.2 and PR1.4: three 3 s clips at -20, -30 and -40 dBFS, mixed with a tone of music and again with the duck
// off (the control). Under the clips and in their 300 ms gaps the music is 12 dB under the control's; the voice is
// the control's under each; the gains are the design's arithmetic, and the control's music is the bed's at its gain.
func TestTheMusicIsDuckedUnderTheClips(t *testing.T) {
	t.Parallel()

	tools := tools(t)
	dir := t.TempDir()
	clips := []clip{{-20, 3000}, {-30, 3000}, {-40, 3000}}
	voices := musicTake(t, tools, dir, clips)
	track := sineTrack(t, tools, filepath.Join(dir, "music.wav"), musicHz, 0.25, 30)

	ducked := composeWith(t, tools, dir, voices, &Music{File: track, LengthMs: 30000}, "ducked")
	control := composeWith(t, tools, dir, voices, &Music{File: track, LengthMs: 30000, noDuck: true}, "control")
	quieter := composeWith(t, tools, dir, voices, &Music{File: track, LengthMs: 30000, GainDB: -6}, "quieter")
	require.NotNil(t, ducked.Music)
	require.NotNil(t, control.Music)
	require.NotNil(t, quieter.Music)
	assert.Empty(t, ducked.Warnings)

	// The voice's gain sets it to -16 LUFS unless that would put its true peak over -1.5 dBTP; the bed is set 3 LU
	// under the voice, moved by the track's gain: 0, then -6 dB.
	lv := ducked.Music
	voice, err := Measure(t.Context(), tools, filepath.Join(ducked.work, "voice.wav"), 0)
	require.NoError(t, err)
	assert.InDelta(t, voice.LUFS, lv.VoiceLUFS, 0.001)
	assert.InDelta(t, math.Min(v5VoiceLUFS-voice.LUFS, v5VoiceCeiling-voice.TPdB()), lv.VoiceGainDB, 0.01)
	assert.InDelta(t, v5VoiceLUFS, lv.VoiceLUFS+lv.VoiceGainDB, 0.01, "this voice's peak leaves room for -16 LUFS")

	for name, got := range map[string]made{"no gain": ducked, "-6 dB": quieter} {
		gain := map[string]float64{"no gain": 0, "-6 dB": -6}[name]
		assert.InDelta(t, lv.BedLUFS, got.Music.BedLUFS, 0.001, name)
		assert.InDelta(t, v5VoiceLUFS-v5BedUnderLU-got.Music.BedLUFS+gain, got.Music.BedGainDB, 0.01, name)
	}

	assert.InDelta(t, v5VoiceLUFS, lv.OutLUFS, v5LoudnessLU)
	assert.LessOrEqual(t, lv.OutTPdB, v5PeakDBTP)
	t.Logf("levels: %+v; at -6 dB: %+v", *lv, *quieter.Music)

	// Where the clips are, from the fixture: each scene lasts its clip and the 300 ms gap, 99 frames.
	var starts []float64

	totalMs := 0.0
	for _, cl := range clips {
		starts = append(starts, totalMs)
		totalMs += float64(cl.ms + GapMs)
	}

	require.InDelta(t, totalMs, ducked.LengthMs, frameMs+1)

	// The music is ducked from 0.2 s to the last clip's end or the fade, whichever is first: 0.2 s to 8.1 s, 79
	// windows, six of them in the two gaps.
	end := math.Min(totalMs-GapMs, totalMs-v5FadeMs)
	d, c := decode(t, tools, ducked.out, 1), decode(t, tools, control.out, 1)
	worst := 0.0
	checked, inGaps := 0, 0

	for at := 200.0; at+windowMs <= end; at += windowMs {
		diff := bandDB(d, at, musicHz) - bandDB(c, at, musicHz)
		worst = math.Max(worst, math.Abs(diff+12))
		assert.InDelta(t, -12, diff, 1, "the music at %.0f ms is ducked 12 dB", at)

		checked++

		for _, s := range starts[1:] {
			if at >= s-GapMs && at+windowMs <= s {
				inGaps++
			}
		}
	}

	assert.Equal(t, 79, checked, "windows checked")
	assert.Equal(t, 6, inGaps, "windows checked in the gaps")
	t.Logf("ducked - control: worst %.2f dB off -12 over %d windows", worst, checked)

	for i, s := range starts {
		at := s + 1500
		assert.InDelta(t, bandDB(c, at, voiceHz), bandDB(d, at, voiceHz), 0.5, "the voice of clip %d", i+1)
	}

	// The track's -6 dB is heard: its music is 6 dB under the ducked video's.
	q := decode(t, tools, quieter.out, 1)
	for _, at := range []float64{500, 3100, 5000, 7000} {
		assert.InDelta(t, bandDB(d, at, musicHz)-6, bandDB(q, at, musicHz), 0.5, "the music at -6 dB at %.0f ms", at)
	}

	// The control's music is the bed's tone moved by its gain, before the fade.
	bed := decode(t, tools, filepath.Join(control.work, "music-bed.wav"), 1)
	for _, at := range []float64{1000, 4000, 7000} {
		assert.InDelta(t, bandDB(bed, at, musicHz)+control.Music.BedGainDB, bandDB(c, at, musicHz), 0.5,
			"the control's music at %.0f ms", at)
	}
}

// PR1.3: the duck's release, on a tone through the expression of spans 1 s apart. From 0.45 s after a span's end
// to the next span, the tone is within 1 dB of no duck; inside a span it is 12 dB down.
func TestTheDuckReleasesBetweenSpans(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []Span{{500, 1500}, {2500, 3800}}, mergeSpeech([]Span{{2500, 3000}, {500, 1500}, {3300, 3800}}),
		"spans 300 ms apart merge, 1 s apart do not")

	tools := tools(t)
	dir := t.TempDir()
	spans := []Span{{500, 1500}, {2500, 3500}}

	graph := filepath.Join(dir, "duck.graph")
	require.NoError(t, os.WriteFile(graph,
		[]byte(fmt.Sprintf("[0:a]asetnsamples=n=256,volume='%s':eval=frame[out]", duckGain(spans))), 0o600))

	//nolint:gosec // The test's ffmpeg.
	raw, err := exec.CommandContext(t.Context(), tools.FFmpeg, "-v", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("aevalsrc=0.5*sin(2*PI*%d*t):s=%d:d=4.5", musicHz, mixRate),
		"-/filter_complex", graph, "-map", "[out]", "-f", "f32le", "-").Output()
	require.NoError(t, err)

	tone := make([]float32, len(raw)/4)
	require.NoError(t, binary.Read(bytes.NewReader(raw), binary.LittleEndian, tone))

	free := 20 * math.Log10(0.5)
	for at := 1950.0; at+windowMs <= 2500; at += windowMs / 2 {
		assert.InDelta(t, free, bandDB(tone, at, musicHz), 1, "released at %.0f ms", at)
	}

	for _, at := range []float64{700, 1300, 2700, 3300} {
		assert.InDelta(t, free-12.04, bandDB(tone, at, musicHz), 0.2, "ducked at %.0f ms", at)
	}

	assert.InDelta(t, free, bandDB(tone, 0, musicHz), 0.1, "the bed is up before the first span")
}

// PR1.5: a 40 s track whose first 20 s are silent, under a 20 s video: the bed used is silent, and the video is
// made with no music and a warning.
func TestABedSilentOverTheVideoGivesNoMusic(t *testing.T) {
	t.Parallel()

	tools := tools(t)
	dir := t.TempDir()
	voices := musicTake(t, tools, dir, []clip{{-20, 19700}})
	track := lavfi(t, tools, filepath.Join(dir, "music.wav"),
		fmt.Sprintf("aevalsrc=if(lt(t\\,20)\\,0\\,0.3*sin(2*PI*%d*t)):s=%d:d=40", musicHz, mixRate), "-c:a", "pcm_s16le")

	res := composeWith(t, tools, dir, voices, &Music{File: track, LengthMs: 40000}, "silent")
	assert.Nil(t, res.Music)
	assert.Equal(t, []string{"the music is silent over the video's 20.000 s, so the video has no music"},
		res.Warnings)
	assert.Less(t, bandDB(decode(t, tools, res.out, 1), 10000, musicHz), -90.0, "no music in the video")
}

// PR1.6: the loop's plays, the least that cover the video with 1.5 s crossfades, at most 100.
func TestLoopPlays(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		lengthMs, targetMs int64
		plays              int
	}{
		{10000, 850000, 100}, // Kinocut's margin would ask 101
		{7300, 20000, 4},
		{30000, 95000, 4},
		{10000, 18500, 2}, // exactly 2·10 − 1.5
		{10000, 18501, 3},
	} {
		got, err := loopPlays(tc.lengthMs, tc.targetMs)
		require.NoError(t, err)
		assert.Equal(t, tc.plays, got, "L=%d T=%d", tc.lengthMs, tc.targetMs)
		assert.GreaterOrEqual(t, int64(got)*tc.lengthMs-int64(got-1)*v5CrossfadeMs, tc.targetMs)
	}

	_, err := loopPlays(8000, 850000)
	require.ErrorContains(t, err, "131 plays, more than 100")

	_, err = loopPlays(1500, 20000)
	require.Error(t, err)
}

// PR1.6: the bed, looped and cut, is stereo 44.1 kHz float of 352 800 bytes a second whatever its source; a seam
// of uncorrelated noise is as loud as the rest; music with dynamics lasts the video and never drops to silence; an
// in-phase seam's overshoot is kept, not clipped.
func TestTheBed(t *testing.T) {
	t.Parallel()

	tools := tools(t)
	dir := t.TempDir()
	r := runner{tools: tools, dir: dir}

	const targetMs = 25000

	pink := lavfi(t, tools, filepath.Join(dir, "pink.wav"),
		fmt.Sprintf("anoisesrc=color=pink:seed=7:amplitude=0.3:sample_rate=%d:duration=10", mixRate),
		"-c:a", "pcm_s16le")
	// agent-demo-video's ambient bed (src/sound.ts, ambientBedArgs): brown noise, filtered, with a slow wobble.
	ambient := lavfi(t, tools, filepath.Join(dir, "ambient.wav"), "anoisesrc=color=brown:sample_rate=44100:duration=7.3",
		"-af", "lowpass=f=300,highpass=f=45,tremolo=f=0.12:d=0.22,volume=6dB", "-c:a", "pcm_s16le")
	hires := lavfi(t, tools, filepath.Join(dir, "hires.flac"),
		"aevalsrc=0.3*sin(2*PI*1000*t)|0.3*sin(2*PI*1500*t):s=96000:d=12", "-c:a", "flac", "-sample_fmt", "s32")
	// Stereo, so its peak is 0.92 in each channel of the bed, as a mono track's would be 3 dB under once upmixed.
	inPhase := lavfi(t, tools, filepath.Join(dir, "in-phase.wav"),
		"aevalsrc=0.92*sin(2*PI*1000*t)|0.92*sin(2*PI*1000*t):s=44100:d=10", "-c:a", "pcm_f32le")

	bedOf := func(name, file string, lengthMs int64) string {
		t.Helper()

		out := filepath.Join(dir, name+"-bed.wav")
		require.NoError(t, r.prepareBed(t.Context(), Music{File: file, LengthMs: lengthMs}, targetMs, out))

		probe := probeStream(t, tools, out)
		assert.Equal(t, "pcm_f32le", probe.Codec, name)
		assert.Equal(t, 2, probe.Channels, name)
		assert.Equal(t, "44100", probe.Rate, name)

		info, err := os.Stat(out)
		require.NoError(t, err)
		assert.InDelta(t, v5BedBytesPerSec*targetMs/1000, info.Size(), v5BedBytesPerSec/FPS, "%s is %d bytes", name,
			info.Size())

		return out
	}

	// The seams of pink noise, at k·(10 − 1.5) s for 1.5 s, against the rest: power means within 1 dB.
	p := decode(t, tools, bedOf("pink", pink, 10000), 1)

	var seam, rest []float64

	for at := 0.0; at+windowMs <= targetMs; at += windowMs {
		k := math.Floor(at / 8500)
		if k >= 1 && at >= k*8500 && at+windowMs <= k*8500+v5CrossfadeMs {
			seam = append(seam, math.Pow(10, rmsDB(p, at)/10))
		} else if k >= 0 && math.Mod(at, 8500) > 2000 {
			rest = append(rest, math.Pow(10, rmsDB(p, at)/10))
		}
	}

	require.NotEmpty(t, seam)
	seamDB, restDB := 10*math.Log10(meanOf(seam)), 10*math.Log10(meanOf(rest))
	t.Logf("pink seed 7: seams %.2f dB, the rest %.2f dB", seamDB, restDB)
	assert.InDelta(t, restDB, seamDB, 1, "the seams are as loud as the rest")

	a := decode(t, tools, bedOf("ambient", ambient, 7300), 1)
	assert.InDelta(t, targetMs, float64(len(a))*1000/mixRate, 1000.0/FPS, "the bed lasts the video")

	for at := 0.0; at+windowMs <= targetMs-v5FadeMs; at += windowMs {
		require.Greater(t, rmsDB(a, at), -70.0, "the bed is heard at %.0f ms", at)
	}

	bedOf("hires", hires, 12000)

	peak := peakOf(decode(t, tools, bedOf("in-phase", inPhase, 10000), 2))
	t.Logf("in-phase seam at 0.92: the bed's peak is %+.2f dBFS", 20*math.Log10(peak))
	assert.Greater(t, peak, 1.0, "the seam's overshoot is kept in float")
}

func meanOf(xs []float64) float64 {
	var s float64
	for _, x := range xs {
		s += x
	}

	return s / float64(len(xs))
}

type streamProbe struct {
	Codec    string `json:"codec_name"`
	Channels int    `json:"channels"`
	Rate     string `json:"sample_rate"`
	Duration string `json:"duration"`
}

// probeStream is the first audio stream of file as ffprobe reads it.
func probeStream(t *testing.T, tools ffmpeg.Tools, file string) streamProbe {
	t.Helper()

	return probeOf(t, tools, file, "a:0")
}

// streamMs is the duration of a file's stream, as ffprobe reads it from the stream, in ms.
func streamMs(t *testing.T, tools ffmpeg.Tools, file, stream string) float64 {
	t.Helper()

	sec, err := strconv.ParseFloat(probeOf(t, tools, file, stream).Duration, 64)
	require.NoError(t, err)

	return sec * 1000
}

func probeOf(t *testing.T, tools ffmpeg.Tools, file, stream string) streamProbe {
	t.Helper()

	//nolint:gosec // The test's ffprobe.
	out, err := exec.CommandContext(t.Context(), tools.FFprobe, "-v", "error", "-select_streams", stream,
		"-show_entries", "stream=codec_name,channels,sample_rate,duration", "-of", "json", file).Output()
	require.NoError(t, err)

	var probe struct {
		Streams []streamProbe `json:"streams"`
	}

	require.NoError(t, json.Unmarshal(out, &probe))
	require.Len(t, probe.Streams, 1)

	return probe.Streams[0]
}

// PR1.6, PR1.7 and PR1.8: two looped tracks at +10 dB: the in-phase sine of TestTheBed, whose seams go over full
// scale in the bed, and the stress of the peaks, a quiet sine with loud clicks. In both videos the true peak is at
// most -1 dBTP and no sample reaches full scale; without the limiter the stress's true peak is warned of. The
// sound is stereo 44.1 kHz as long as its picture, and Burn keeps it as it is.
func TestTheMixIsLimited(t *testing.T) {
	t.Parallel()

	tools := tools(t)
	dir := t.TempDir()
	voices := musicTake(t, tools, dir, []clip{{-20, 6000}, {-14, 6000}})
	inPhase := lavfi(t, tools, filepath.Join(dir, "in-phase.wav"),
		"aevalsrc=0.92*sin(2*PI*1000*t)|0.92*sin(2*PI*1000*t):s=44100:d=10", "-c:a", "pcm_f32le")
	track := lavfi(t, tools, filepath.Join(dir, "stress.wav"),
		fmt.Sprintf("aevalsrc=0.1*sin(2*PI*1000*t)+if(lt(mod(t\\,0.5)\\,0.002)\\,0.82\\,0):s=%d:d=10", mixRate),
		"-c:a", "pcm_s16le")

	var limited made

	for name, file := range map[string]string{"in-phase": inPhase, "stress": track} {
		got := composeWith(t, tools, dir, voices, &Music{File: file, LengthMs: 10000, GainDB: v5MaxGainDB}, name)
		require.NotNil(t, got.Music)
		t.Logf("%s: %+v, warnings %q", name, *got.Music, got.Warnings)
		assert.LessOrEqual(t, got.Music.OutTPdB, v5PeakDBTP, name)
		assert.NotContains(t, fmt.Sprint(got.Warnings), "true peak", name)
		assert.Less(t, peakOf(decode(t, tools, got.out, 2)), 1.0, "%s: no sample at full scale", name)

		if name == "stress" {
			limited = got
		}
	}

	probe := probeStream(t, tools, limited.out)
	assert.Equal(t, 2, probe.Channels)
	assert.Equal(t, "44100", probe.Rate)

	video, audio := streamMs(t, tools, limited.out, "v:0"), streamMs(t, tools, limited.out, "a:0")
	t.Logf("the picture lasts %.1f ms and the sound %.1f ms", video, audio)
	assert.InDelta(t, video, audio, 1000.0/FPS, "the sound lasts the picture")

	burned, err := Burn(t.Context(), tools, limited.out, filepath.Join("testdata", "captions.ass"),
		filepath.Join(dir, "burn"))
	require.NoError(t, err)
	assert.Equal(t, audioMD5(t, tools, limited.out), audioMD5(t, tools, burned), "Burn copies the sound")

	loud := composeWith(t, tools, dir, voices,
		&Music{File: track, LengthMs: 10000, GainDB: v5MaxGainDB, noLimiter: true}, "unlimited")
	require.NotNil(t, loud.Music)
	t.Logf("without the limiter: %+v", *loud.Music)
	assert.Greater(t, loud.Music.OutTPdB, v5PeakDBTP)
	assert.Contains(t, loud.Warnings, fmt.Sprintf("the mix's true peak is %.1f dBTP, over -1 dBTP", loud.Music.OutTPdB))
}

func audioMD5(t *testing.T, tools ffmpeg.Tools, file string) string {
	t.Helper()

	//nolint:gosec // The test's ffmpeg.
	out, err := exec.CommandContext(t.Context(), tools.FFmpeg, "-v", "error", "-i", file, "-map", "0:a",
		"-c", "copy", "-f", "md5", "-").Output()
	require.NoError(t, err)

	return string(out)
}

// PR1.8: a video shorter than the fade fades over its whole length; PR1.6: too little disk for the bed fails.
func TestAShortVideoFadesOverItsLengthAndTheBedNeedsDisk(t *testing.T) {
	t.Parallel()

	tools := tools(t)
	dir := t.TempDir()
	voices := musicTake(t, tools, dir, []clip{{-20, 700}})
	track := sineTrack(t, tools, filepath.Join(dir, "music.wav"), musicHz, 0.25, 12)

	// The video is the clip and its 300 ms gap, 1 s; the bed needs 400 MB and a second of 352 800 B free beside it.
	const lengthMs = 1000

	need := uint64(v5FreeFloor + v5BedBytesPerSec*lengthMs/1000)
	compose := func(name string, free uint64, m Music) (made, error) {
		work, out := filepath.Join(dir, "work-"+name), filepath.Join(dir, name+".mp4")
		res, err := Compose(t.Context(), tools, Input{
			Dir: dir, Voice: voices, Aspect: Portrait, Work: work, Out: out, Music: &m,
			FreeBytes: func(string) (uint64, error) { return free, nil },
		})

		return made{res, out, work}, err
	}

	short, err := compose("short", need, Music{File: track, LengthMs: 12000, noDuck: true})
	require.NoError(t, err, "exactly the disk the bed needs is enough")
	require.NotNil(t, short.Music)
	require.InDelta(t, lengthMs, short.LengthMs, frameMs+1)
	require.Less(t, short.LengthMs, int64(v5FadeMs))

	s := decode(t, tools, short.out, 1)
	first, last := bandDB(s, 0, musicHz), bandDB(s, float64(short.LengthMs)-200, musicHz)
	t.Logf("a %d ms video: the music is %.1f dB at its start and %.1f dB at its end", short.LengthMs, first, last)
	assert.Less(t, last, first-14, "faded over the video's length, not over 1.8 s")

	_, err = compose("disk", need-1, Music{File: track, LengthMs: 12000})
	require.ErrorContains(t, err, "the music's bed needs", "a byte less is not")
	assert.NoFileExists(t, filepath.Join(dir, "disk.mp4"))
}

var summaryI = regexp.MustCompile(`I:\s+(-?[\d.]+) LUFS`)

// PR1.9: Measure's units: a 0.5 tone is -6.02 dBTP at ebur128's loudness, a 10 s WAV is 10 000 ms, and silence
// has no peak and is silent.
func TestMeasure(t *testing.T) {
	t.Parallel()

	tools := tools(t)
	dir := t.TempDir()

	tone := sineTrack(t, tools, filepath.Join(dir, "tone.wav"), 1000, 0.5, 10)
	m, err := Measure(t.Context(), tools, tone, 0)
	require.NoError(t, err)
	assert.InDelta(t, -6.02, m.TPdB(), 0.1)
	assert.Equal(t, int64(10000), m.LengthMs)
	assert.False(t, m.Silent())

	//nolint:gosec // The test's ffmpeg.
	summary, err := exec.CommandContext(t.Context(), tools.FFmpeg, "-hide_banner", "-nostats", "-i", tone,
		"-af", "ebur128", "-f", "null", "-").CombinedOutput()
	require.NoError(t, err)

	got := summaryI.FindAllSubmatch(summary, -1)
	require.NotEmpty(t, got)
	want, err := strconv.ParseFloat(string(got[len(got)-1][1]), 64)
	require.NoError(t, err)
	assert.InDelta(t, want, m.LUFS, 0.05, "ebur128's summary says %.1f LUFS", want)

	limited, err := Measure(t.Context(), tools, tone, 4)
	require.NoError(t, err)
	assert.Equal(t, int64(4000), limited.LengthMs, "-t bounds the decode")

	silence := lavfi(t, tools, filepath.Join(dir, "silence.wav"), "anullsrc=r=44100:cl=mono", "-t", "10",
		"-c:a", "pcm_s16le")
	m, err = Measure(t.Context(), tools, silence, 0)
	require.NoError(t, err)
	assert.True(t, math.IsInf(m.TPdB(), -1))
	assert.InDelta(t, -70, m.LUFS, 0.001)
	assert.True(t, m.Silent())
}
