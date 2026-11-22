package clientapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// The music of a video end to end (issue #910, PR 2's check 11, with PR1.2's control): a track uploaded through the
// route, a make through the API, the runner making it with the pinned ffmpeg, and the MP4 read back through the API.

const e2eTake = "tk_00000000000000e2"

// e2eTakes holds one available take of three steps over a 12 s grey segment.
type e2eTakes struct {
	dir  string
	take devicetool.Take
}

func (f *e2eTakes) Get(id string) (devicetool.Take, error) {
	if id != f.take.ID {
		return devicetool.Take{}, fmt.Errorf("no take %s", id)
	}

	return f.take, nil
}

func (f *e2eTakes) File(id, name string) (string, error) {
	if id != f.take.ID {
		return "", fmt.Errorf("no take %s", id)
	}

	return filepath.Join(f.dir, name), nil
}

// levelSpeech speaks each line as a 440 Hz tone of three seconds at its level, in dBFS of peak.
type levelSpeech struct {
	t      *testing.T
	tools  ffmpeg.Tools
	levels map[string]float64
}

func (s levelSpeech) Speak(ctx context.Context, _ videos.AudioSource, req speech.SpeakRequest) (speech.Speech, error) {
	file := filepath.Join(s.t.TempDir(), "clip.mp3")
	ffmpegRun(ctx, s.t, s.tools, "-f", "lavfi", "-i",
		fmt.Sprintf("aevalsrc=%g*sin(2*PI*440*t):s=44100:d=3", math.Pow(10, s.levels[req.Input]/20)),
		"-c:a", "libmp3lame", "-b:a", "192k", file)

	raw, err := os.ReadFile(file) //nolint:gosec // The test's own file.
	if err != nil {
		return speech.Speech{}, err
	}

	return speech.Speech{Audio: raw, Provider: req.Provider}, nil
}

func (levelSpeech) Transcribe(context.Context, videos.AudioSource, speech.TranscribeRequest,
) (speech.Transcript, error) {
	return speech.Transcript{}, fmt.Errorf("the video has no captions")
}

// controlMaker is package compose, which renders, beside each revision with music, its control: the same input with
// the duck left out (PR1.2's control), into control.
type controlMaker struct {
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

func (controlMaker) Burn(ctx context.Context, tools ffmpeg.Tools, video, captions, work string) (string, error) {
	return compose.Burn(ctx, tools, video, captions, work)
}

func (controlMaker) Thumbnail(ctx context.Context, tools ffmpeg.Tools, video, out string) error {
	return compose.Thumbnail(ctx, tools, video, out)
}

func ffmpegRun(ctx context.Context, t *testing.T, tools ffmpeg.Tools, args ...string) []byte {
	t.Helper()

	//nolint:gosec // The pinned ffmpeg, in a test.
	cmd := exec.CommandContext(ctx, tools.FFmpeg, append([]string{"-hide_banner", "-loglevel", "error", "-y"},
		args...)...)

	var stderr strings.Builder

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())

	return out
}

// The bands of a video whose voice is a 440 Hz tone and whose music is a 4 kHz one: above 2 kHz the music, under
// 1 kHz the voice, each through three filters.
const (
	musicBand = "highpass=f=2000,highpass=f=2000,highpass=f=2000"
	voiceBand = "lowpass=f=1000,lowpass=f=1000,lowpass=f=1000"
)

// bandWindows are the RMS levels, in dBFS, of a file's sound in a band in each 100 ms from its start, or over the
// whole file with whole.
func bandWindows(t *testing.T, tools ffmpeg.Tools, file, band string, whole bool) []float64 {
	t.Helper()

	frame := "asetnsamples=n=4410,astats=metadata=1:reset=1"
	if whole {
		frame = "astats=metadata=1:reset=0"
	}

	out := ffmpegRun(t.Context(), t, tools, "-i", file, "-map", "0:a:0", "-af",
		band+","+frame+
			",ametadata=mode=print:key=lavfi.astats.Overall.RMS_level:file=-", "-f", "null", "-")

	var levels []float64

	for line := range strings.Lines(string(out)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "lavfi.astats.Overall.RMS_level="); ok {
			f, err := strconv.ParseFloat(v, 64)
			require.NoError(t, err)

			levels = append(levels, f)
		}
	}

	require.NotEmpty(t, levels)

	return levels
}

// PR2.11: a 4 kHz track uploaded by POST /api/v1/videos/music, a make through POST /videos with its music, made by
// the runner under a voice of three 3 s clips at -20, -30 and -40 dBFS, and the MP4 read through the API. In every
// 100 ms from 0.2 s to the fade, the two 300 ms gaps between the clips included, the track's band is 12 dB under the
// control's (PR1.2), within 1 dB, while the voice's band is the control's within 0.5 dB; the control's band is the track's own level moved to 3 LU under the voice's
// -16 LUFS (PR1.4's BedGainDb, at gain 0), within 1 dB.
func TestATrackUploadedAndMadeIsDuckedUnderTheVoice(t *testing.T) {
	t.Parallel()

	tools := pinnedTools(t)
	control := filepath.Join(t.TempDir(), "control.mp4")

	takes := &e2eTakes{dir: t.TempDir(), take: devicetool.Take{
		ID: e2eTake, Title: "Nhạc", Agent: "d-1", State: devicetool.TakeAvailable, DurationMs: 12_000,
		Screen:   &devicetool.TakeScreen{Width: 360, Height: 800},
		Segments: []devicetool.TakeSegment{{File: "seg-001.mp4", LengthMs: 12_000, Width: 360, Height: 800}},
		Steps:    []devicetool.TakeStep{{AtMs: 500, Text: "Một"}, {AtMs: 4000, Text: "Hai"}, {AtMs: 8000, Text: "Ba"}},
	}}
	ffmpegRun(t.Context(), t, tools, "-f", "lavfi", "-i", "color=c=0x404040:s=360x800:r=30:d=12", "-c:v",
		"libx264", "-pix_fmt", "yuv420p", "-g", "30", filepath.Join(takes.dir, "seg-001.mp4"))

	raw, err := json.Marshal(takes.take)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(takes.dir, "take.json"), raw, 0o600))

	w := newMusicWorld(t, func(cfg *videos.Config) {
		cfg.Tools = func(context.Context) (ffmpeg.Tools, error) { return tools, nil }
		cfg.Takes, cfg.Maker = takes, controlMaker{control: control}
		cfg.Speech = levelSpeech{t: t, tools: tools, levels: map[string]float64{"Một": -20, "Hai": -30, "Ba": -40}}
		cfg.Settings = func(context.Context) (speech.Settings, error) { return speech.Settings{}, nil }
	})
	// The look made before the looks: no frame to draw, so the test is the sound's.
	w.exec(`UPDATE video_settings SET look = '{"captions":{"preset":"legacy"},"frame":{"chrome":"full"}}'`)

	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)

		w.videos.Run(ctx)
	}()

	t.Cleanup(func() { cancel(); <-stopped })

	// A 4 kHz tone, the same in both channels: far above the voice's 440 Hz.
	file := filepath.Join(t.TempDir(), "tone.flac")
	ffmpegRun(t.Context(), t, tools, "-f", "lavfi", "-i", "sine=f=4000:d=20:sample_rate=44100", "-af",
		"pan=stereo|c0=c0|c1=c0", "-c:a", "flac", file)

	body, err := os.ReadFile(file) //nolint:gosec // The test's own file.
	require.NoError(t, err)

	status, track := w.post(t, "Tone", body, octetStream)
	require.Equal(t, http.StatusCreated, status)

	owner := w.client(w.owner)
	off := false
	made, err := owner.MakeVideoWithResponse(t.Context(), &openapi.MakeVideoParams{}, openapi.VideoMake{
		Take: e2eTake, Captions: &off,
		Look: &openapi.VideoLookInput{Music: &openapi.VideoMusicInput{Track: track.Id}},
	})
	require.NoError(t, err)
	require.NotNil(t, made.JSON201, string(made.Body))

	video := made.JSON201.Id
	rev := waitMade(t, owner, video)

	mp4 := filepath.Join(t.TempDir(), "video.mp4")
	resp, err := owner.ReadVideoFile(t.Context(), video, 1, &openapi.ReadVideoFileParams{})
	require.NoError(t, err)

	got, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, os.WriteFile(mp4, got, 0o600))

	ducked, plain := bandWindows(t, tools, mp4, musicBand, false), bandWindows(t, tools, control, musicBand, false)

	// Each scene is its clip and the 300 ms gap; the fade starts 1.8 s before the end.
	scene := float64(rev.LengthMs) / 3 / 1000
	require.InDelta(t, 3+float64(compose.GapMs)/1000, scene, 0.05)

	last := int(math.Min(3*scene-float64(compose.GapMs)/1000, float64(rev.LengthMs)/1000-1.8) * 10)
	gaps, sum := 0, 0.0

	for k := 2; k < last; k++ {
		at := float64(k) / 10
		if math.Mod(at, scene) >= 3 && at < 3*scene {
			gaps++
		}

		require.Less(t, k, min(len(ducked), len(plain)))
		assert.InDelta(t, -12, ducked[k]-plain[k], 1, "the duck at %.1f s", at)

		sum += plain[k]
	}

	assert.GreaterOrEqual(t, gaps, 4, "the windows of both gaps are measured")

	// The voice is the same with the duck and without it, within 0.5 dB, in the middle of each clip (PR1.2).
	voiced, voicePlain := bandWindows(t, tools, mp4, voiceBand, false), bandWindows(t, tools, control, voiceBand, false)
	for i := range 3 {
		k := int((float64(i)*scene + 1.5) * 10)
		require.Less(t, k, min(len(voiced), len(voicePlain)))
		assert.InDelta(t, voicePlain[k], voiced[k], 0.5, "the voice of clip %d", i+1)
	}

	own, err := compose.Measure(t.Context(), tools, file, 0)
	require.NoError(t, err)

	track1 := bandWindows(t, tools, file, musicBand, true)
	want := track1[len(track1)-1] + compose.VoiceLUFS - 3 - own.LUFS
	assert.InDelta(t, want, sum/float64(last-2), 1, "the control is the track 3 LU under the voice")
}

// waitMade waits for the video's first revision to be made, and answers it.
func waitMade(t *testing.T, owner *openapi.ClientWithResponses, video string) openapi.VideoRevision {
	t.Helper()

	deadline := time.Now().Add(3 * time.Minute)

	for time.Now().Before(deadline) {
		got, err := owner.GetVideoWithResponse(t.Context(), video)
		require.NoError(t, err)
		require.NotNil(t, got.JSON200, string(got.Body))

		rev := got.JSON200.Revisions[0]

		switch rev.State {
		case openapi.VideoRevisionStateReview:
			return rev
		case openapi.VideoRevisionStateFailed:
			t.Fatalf("the revision failed at %s: %s", val(rev.FailedStage), val(rev.FailedCause))
		default:
		}

		time.Sleep(200 * time.Millisecond)
	}

	t.Fatal("the revision was not made within 3 minutes")

	return openapi.VideoRevision{}
}
