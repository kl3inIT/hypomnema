package videos

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
)

// realTools is the pinned ffmpeg in HYPOMNEMA_FFMPEG_DIR; the test skips without it, unless
// HYPOMNEMA_NEED_FFMPEG says it is needed (as compose's tests).
func realTools(t *testing.T) ffmpeg.Tools {
	t.Helper()

	if dir := os.Getenv("HYPOMNEMA_FFMPEG_DIR"); dir != "" {
		if tools, ok := ffmpeg.Installed(dir); ok {
			return tools
		}
	}

	why := "the pinned ffmpeg is not installed: run `go run ./internal/controlplane/videos/ffmpeg/installffmpeg <dir>` " +
		"and set HYPOMNEMA_FFMPEG_DIR=<dir>"
	if os.Getenv("HYPOMNEMA_NEED_FFMPEG") != "" {
		t.Fatal(why)
	}

	t.Skip(why)

	return ffmpeg.Tools{}
}

func run(ctx context.Context, t *testing.T, bin string, args ...string) []byte {
	t.Helper()

	var stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // The pinned ffmpeg, in a test.
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())

	return out
}

// toneSpeech speaks a line as a second of a tone, a real MP3, and hears it as its words.
type toneSpeech struct {
	*fakeSpeech

	t     *testing.T
	tools ffmpeg.Tools
}

func (s toneSpeech) Speak(ctx context.Context, req speech.SpeakRequest) (speech.Speech, error) {
	if _, err := s.fakeSpeech.Speak(ctx, req); err != nil {
		return speech.Speech{}, err
	}

	file := filepath.Join(s.t.TempDir(), "tone.mp3")
	run(ctx, s.t, s.tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i",
		"sine=frequency=440:duration=1", "-c:a", "libmp3lame", file)

	raw, err := os.ReadFile(file) //nolint:gosec // The test's own file.
	if err != nil {
		return speech.Speech{}, err
	}

	s.fakeSpeech.mu.Lock()
	s.fakeSpeech.lines = append(s.fakeSpeech.lines, req.Input)
	s.fakeSpeech.mu.Unlock()

	return speech.Speech{Audio: raw, Provider: req.Provider}, nil
}

func (s toneSpeech) Transcribe(ctx context.Context, req speech.TranscribeRequest) (speech.Transcript, error) {
	s.fakeSpeech.mu.Lock()
	line := ""
	if len(s.fakeSpeech.lines) > 0 {
		line, s.fakeSpeech.lines = s.fakeSpeech.lines[0], s.fakeSpeech.lines[1:]
	}
	s.fakeSpeech.mu.Unlock()

	return s.fakeSpeech.Transcribe(ctx, speech.TranscribeRequest{Audio: speech.Audio{
		Size: int64(len(line)), Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(line)), nil },
	}})
}

// A revision made with the pinned ffmpeg end to end: a take of two steps, voiced with tones, composed, captioned
// from the words of each clip with the spoken word highlighted, and put in place with its first frame.
func TestARevisionMadeWithFFmpeg(t *testing.T) {
	t.Parallel()

	tools := realTools(t)
	r := newRig(t)
	r.svc.cfg.Maker = composeMaker{}
	r.svc.cfg.Tools = func(context.Context) (ffmpeg.Tools, error) { return tools, nil }
	r.svc.cfg.Speech = toneSpeech{fakeSpeech: r.speech, t: t, tools: tools}

	// A 6 s segment of a grey screen, 360×800: no yellow of its own.
	run(t.Context(), t, tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i",
		"color=c=0x404040:size=360x800:rate=30:duration=6", "-c:v", "libx264", "-pix_fmt", "yuv420p",
		filepath.Join(r.takes.dir, "seg-001.mp4"))
	r.takes.take.Segments[0].Width, r.takes.take.Segments[0].Height = 360, 800
	writeTakeJSON(t, r.takes)

	v, _ := r.make(t, "")
	r.runAll(t)

	got := r.revision(t, v.ID, 1)
	require.Equal(t, Review, got.State, got.FailedStage+": "+got.FailedCause)
	assert.InDelta(t, 2*(1000+compose.GapMs), got.LengthMs, 150, "each scene as long as its voice and the gap")

	mp4, err := r.svc.File(t.Context(), v.ID, 1)
	require.NoError(t, err)

	streams := run(t.Context(), t, tools.FFprobe, "-v", "error", "-show_entries", "stream=codec_name,width,height",
		"-of", "csv=p=0", mp4)
	assert.Contains(t, string(streams), "h264,720,1280")
	assert.Contains(t, string(streams), "aac")

	// The captions: yellow (the spoken word) at the bottom at 0.1 s, in the first scene.
	frame := run(t.Context(), t, tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-ss", "0.1", "-i", mp4,
		"-frames:v", "1",
		"-vf", "crop=720:260:0:1000", "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
	yellow := 0

	for i := 0; i+2 < len(frame); i += 3 {
		if frame[i] > 200 && frame[i+1] > 170 && frame[i+2] < 80 {
			yellow++
		}
	}

	assert.Greater(t, yellow, 100, "the spoken word is highlighted")

	thumb, err := r.svc.Thumbnail(t.Context(), v.ID, 1)
	require.NoError(t, err)

	raw, err := os.ReadFile(thumb) //nolint:gosec // The test's own file.
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(raw, []byte{0xFF, 0xD8}), "a JPEG")
}

// writeTakeJSON writes the take's take.json into its folder, which compose reads.
func writeTakeJSON(t *testing.T, f *fakeTakes) {
	t.Helper()

	raw, err := json.Marshal(f.take)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, "take.json"), raw, 0o600))
}
