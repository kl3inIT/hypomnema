package videos

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// fixture writes a track with ffmpeg's args into a file named name, and answers its path.
func fixture(t *testing.T, tools ffmpeg.Tools, name string, args ...string) string {
	t.Helper()

	out := filepath.Join(t.TempDir(), name)
	run(t.Context(), t, tools.FFmpeg, append(append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...),
		out)...)

	return out
}

// upload uploads a file to the library as the route does, its size said.
func (r *rig) upload(t *testing.T, file string) (Track, error) {
	t.Helper()

	raw, err := os.ReadFile(file) //nolint:gosec // The test's own file.
	require.NoError(t, err)

	return r.svc.UploadMusic(t.Context(), filepath.Base(file), int64(len(raw)), bytes.NewReader(raw))
}

// bandRMS is the RMS level, in dBFS, of a file's sound above 2 kHz over dur seconds from start: the music's band of
// a video whose voice is a 440 Hz tone.
func bandRMS(t *testing.T, tools ffmpeg.Tools, file string, start, dur float64) float64 {
	t.Helper()

	out := run(t.Context(), t, tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-ss", strconv.FormatFloat(start,
		'f', 3, 64), "-t", strconv.FormatFloat(dur, 'f', 3, 64), "-i", file, "-map", "0:a:0", "-af",
		"highpass=f=2000,highpass=f=2000,highpass=f=2000,astats=metadata=1:reset=0,"+
			"ametadata=mode=print:key=lavfi.astats.Overall.RMS_level:file=-", "-f", "null", "-")

	level := 0.0
	found := false

	for line := range strings.Lines(string(out)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "lavfi.astats.Overall.RMS_level="); ok {
			f, err := strconv.ParseFloat(v, 64)
			require.NoError(t, err)

			level, found = f, true
		}
	}

	require.True(t, found, "astats measured nothing in %s", file)

	return level
}

// bandWindows are the RMS levels, in dBFS, of a file's sound above 2 kHz in each 100 ms from its start: the
// music's band of a video whose voice is a 440 Hz tone.
func bandWindows(t *testing.T, tools ffmpeg.Tools, file string) []float64 {
	t.Helper()

	out := run(t.Context(), t, tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-i", file, "-map", "0:a:0", "-af",
		"highpass=f=2000,highpass=f=2000,highpass=f=2000,asetnsamples=n=4410,astats=metadata=1:reset=1,"+
			"ametadata=mode=print:key=lavfi.astats.Overall.RMS_level:file=-", "-f", "null", "-")

	var levels []float64

	for line := range strings.Lines(string(out)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "lavfi.astats.Overall.RMS_level="); ok {
			f, err := strconv.ParseFloat(v, 64)
			require.NoError(t, err)

			levels = append(levels, f)
		}
	}

	return levels
}

// levelSpeech speaks each line as a 440 Hz tone of three seconds at its level, in dBFS of peak.
type levelSpeech struct {
	*fakeSpeech

	t      *testing.T
	tools  ffmpeg.Tools
	levels map[string]float64
}

func (s levelSpeech) Speak(ctx context.Context, source AudioSource, req speech.SpeakRequest) (speech.Speech, error) {
	if _, err := s.fakeSpeech.Speak(ctx, source, req); err != nil {
		return speech.Speech{}, err
	}

	file := fixture(s.t, s.tools, "clip.mp3", "-f", "lavfi", "-i",
		fmt.Sprintf("aevalsrc=%g*sin(2*PI*440*t):s=44100:d=3", math.Pow(10, s.levels[req.Input]/20)),
		"-c:a", "libmp3lame", "-b:a", "192k")

	raw, err := os.ReadFile(file) //nolint:gosec // The test's own file.
	if err != nil {
		return speech.Speech{}, err
	}

	return speech.Speech{Audio: raw, Provider: req.Provider}, nil
}

// controlMaker is package compose, which renders, beside each revision with music, its control: the same input with
// the duck left out (PR1.2's control), into control.
type controlMaker struct {
	composeMaker

	control string
}

func (m controlMaker) Compose(ctx context.Context, tools ffmpeg.Tools, in compose.Input) (compose.Result, error) {
	res, err := compose.Compose(ctx, tools, in)
	if err != nil || in.Music == nil {
		return res, err
	}

	control, music := in, *in.Music
	music.NoDuck = true
	control.Music, control.Out, control.Work, control.Progress = &music, m.control, in.Work+"-control", nil

	if _, err := compose.Compose(ctx, tools, control); err != nil {
		return compose.Result{}, fmt.Errorf("the control: %w", err)
	}

	return res, nil
}

// PR2.11, with PR1.2's control: a track uploaded to the library and laid under a revision of three scenes whose
// clips are three seconds at -20, -30 and -40 dBFS, made end to end with the pinned ffmpeg, and its control rendered
// by compose from the same input with the duck left out. In every 100 ms from 0.2 s to the fade, the two 300 ms gaps
// between the clips included, the track's band is 12 dB under the control's, within 1 dB; the control's band is the
// track's own level moved to 3 LU under the voice's -16 LUFS, within 1 dB (PR1.4's BedGainDb, at gain 0).
func TestATrackFromTheLibraryIsDuckedUnderTheVoice(t *testing.T) {
	t.Parallel()

	tools := realTools(t)
	r := realRig(t, tools)
	control := filepath.Join(t.TempDir(), "control.mp4")
	r.svc.cfg.Maker = controlMaker{control: control}

	steps := []devicetool.TakeStep{{AtMs: 500, Text: "Một"}, {AtMs: 4000, Text: "Hai"}, {AtMs: 8000, Text: "Ba"}}
	r.svc.cfg.Speech = levelSpeech{fakeSpeech: r.speech, t: t, tools: tools,
		levels: map[string]float64{"Một": -20, "Hai": -30, "Ba": -40}}
	r.setTake(t, []devicetool.TakeSegment{writeSegment(t, tools, r.takes.dir, "seg-001.mp4", 360, 800,
		span{color: "0x404040", ms: 12_000})}, steps...)

	// A 4 kHz tone, the same in both channels: far above the voice's 440 Hz.
	file := fixture(t, tools, "tone.flac", "-f", "lavfi", "-i", "sine=f=4000:d=20:sample_rate=44100", "-af",
		"pan=stereo|c0=c0|c1=c0", "-c:a", "flac")

	track, err := r.upload(t, file)
	require.NoError(t, err)

	own, err := compose.Measure(t.Context(), tools, file, 0)
	require.NoError(t, err)

	off := false
	v, _, err := r.svc.Make(t.Context(), MakeRequest{Take: takeID, By: Owner, ByName: Owner, Captions: &off,
		Look: musicIn(track.ID)})
	require.NoError(t, err)
	r.runAll(t)

	made := r.revision(t, v.ID, 1)
	require.Equal(t, Review, made.State, made.FailedStage+": "+made.FailedCause)

	mp4, err := r.svc.File(t.Context(), v.ID, 1)
	require.NoError(t, err)

	ducked, plain := bandWindows(t, tools, mp4), bandWindows(t, tools, control)

	// Each scene is its clip and the 300 ms gap; the fade starts 1.8 s before the end.
	scene := float64(made.LengthMs) / 3 / 1000
	require.InDelta(t, 3+float64(compose.GapMs)/1000, scene, 0.05)

	last := int(math.Min(3*scene-float64(compose.GapMs)/1000, float64(made.LengthMs)/1000-1.8) * 10)
	gaps := 0

	for k := 2; k < last; k++ {
		at := float64(k) / 10
		if inGap := math.Mod(at, scene) >= 3 && at < 3*scene; inGap {
			gaps++
		}

		require.Less(t, k, min(len(ducked), len(plain)))
		assert.InDelta(t, -12, ducked[k]-plain[k], 1, "the duck at %.1f s", at)
	}

	assert.GreaterOrEqual(t, gaps, 4, "the windows of both gaps are measured")

	bedGain := compose.VoiceLUFS - 3 - own.LUFS
	want := bandRMS(t, tools, file, 2, 5) + bedGain
	assert.InDelta(t, want, mean(plain[2:last]), 1, "the control is the track 3 LU under the voice")
}

func mean(xs []float64) float64 {
	sum := 0.0
	for _, x := range xs {
		sum += x
	}

	return sum / float64(len(xs))
}
