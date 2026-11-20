package compose

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// The look sample's still (#908, row 14; #936): the frame that covers the first step's moment, drawn as the video.

// stillOf writes the still of the take in dir and returns its pixels, as RGB.
func stillOf(t *testing.T, tools ffmpeg.Tools, dir string, in StillInput) ([]byte, Size) {
	t.Helper()

	in.Dir = dir
	in.Work = filepath.Join(dir, "still-work")
	in.Out = filepath.Join(dir, "still.jpg")
	require.NoError(t, Still(t.Context(), tools, in))

	size, err := in.Aspect.Size()
	require.NoError(t, err)

	//nolint:gosec // The test's ffmpeg.
	out, err := exec.CommandContext(t.Context(), tools.FFmpeg, "-v", "error", "-i", in.Out, "-f", "rawvideo",
		"-pix_fmt", "rgb24", "-").Output()
	require.NoError(t, err)
	require.Len(t, out, 3*size.Width*size.Height)

	return out, size
}

func pixel(px []byte, size Size, x, y int) [3]int {
	i := 3 * (y*size.Width + x)

	return [3]int{int(px[i]), int(px[i+1]), int(px[i+2])}
}

// ffmpeg's red and green sources: 0xff0000 and 0x008000.
var (
	red   = [3]int{255, 0, 0}
	green = [3]int{0, 128, 0}
)

func near(p, want [3]int, tol int) bool {
	for k := range 3 {
		if d := p[k] - want[k]; d > tol || d < -tol {
			return false
		}
	}

	return true
}

// redGreenTake writes a take of red for 2 s, its last red frame at 1966.667 ms lasting to 2000, then green for 2 s,
// with one step at stepMs.
func redGreenTake(t *testing.T, tools ffmpeg.Tools, dir string, stepMs int64) {
	t.Helper()

	seg := colorSegment(t, tools, dir, "0.mp4", 576, 1280, []string{"red", "green"}, []int64{2000, 2000})
	writeTake(t, dir, devicetool.Take{
		ID: "tk_still", DurationMs: seg.LengthMs, Segments: []devicetool.TakeSegment{seg},
		Steps: []devicetool.TakeStep{{AtMs: stepMs, Text: "Một"}},
	})
}

// The frame that covers the moment is the one that started before it and is still shown: at 1990 ms, the red
// frame of 1966.667 ms, where -ss 1.990 alone gives the green one of 2000 ms.
func TestAStillShowsTheFrameThatCoversItsMoment(t *testing.T) {
	t.Parallel()

	tools := tools(t)

	for _, tc := range []struct {
		stepMs int64
		want   [3]int
		name   string
	}{{1990, red, "red"}, {2000, green, "green"}, {0, red, "red"}, {4000, green, "green"}} {
		t.Run(fmt.Sprintf("%d ms", tc.stepMs), func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			redGreenTake(t, tools, dir, tc.stepMs)

			// A step at the take's end (4000 ms) shows the frame one frame before it, as a scene with no footage.
			px, size := stillOf(t, tools, dir, StillInput{Aspect: Portrait})
			p := pixel(px, size, 360, 640)
			assert.True(t, near(p, tc.want, 24), "the still at %d ms is %v, not %s", tc.stepMs, p, tc.name)
			assert.True(t, near(pixel(px, size, 20, 640), [3]int{}, 16), "full: black bars beside the picture")
		})
	}
}

// A take of one frame (a still screen, as node/device's TestAStillScreenCountsOnTheTimeline films it): its frame
// at 0 ms covers a step at 30 s.
func TestAStillOfAOneFrameTake(t *testing.T) {
	t.Parallel()

	tools := tools(t)
	dir := t.TempDir()

	one := filepath.Join(dir, "one.mp4")
	//nolint:gosec // The test's ffmpeg.
	out, err := exec.CommandContext(t.Context(), tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=red:s=576x1280:r=30", "-frames:v", "1", "-c:v", "libx264", "-pix_fmt", "yuv420p",
		one).CombinedOutput()
	require.NoError(t, err, "%s", out)

	//nolint:gosec // The test's ffmpeg.
	out, err = exec.CommandContext(t.Context(), tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-i", one, "-c", "copy", "-bsf:v", "setts=duration='DURATION*3600'", filepath.Join(dir, "0.mp4")).CombinedOutput()
	require.NoError(t, err, "%s", out)

	writeTake(t, dir, devicetool.Take{
		ID: "tk_one", DurationMs: 120_000,
		Segments: []devicetool.TakeSegment{{File: "0.mp4", LengthMs: 120_000, Width: 576, Height: 1280}},
		Steps:    []devicetool.TakeStep{{AtMs: 30_000, Text: "Một"}},
	})

	px, size := stillOf(t, tools, dir, StillInput{Aspect: Portrait})
	p := pixel(px, size, 360, 640)
	assert.True(t, near(p, red, 24), "the one frame, not %v", p)
}

// In a frame, the still is drawn as the video: the picture in the hole, the frame around it, the captions as
// they are at the moment given.
func TestAStillIsDrawnAsTheVideo(t *testing.T) {
	t.Parallel()

	tools := tools(t)
	dir := t.TempDir()
	redGreenTake(t, tools, dir, 3000)

	px, size := stillOf(t, tools, dir, StillInput{Aspect: Portrait, Frame: DefaultFrame(ChromeAndroid)})
	assert.True(t, near(pixel(px, size, 359, 639), green, 24), "the picture in the hole")
	assert.True(t, near(pixel(px, size, 90, 640), [3]int{11, 11, 13}, 16), "the bezel at x 90 (the hole from 102)")
	assert.True(t, near(pixel(px, size, 359, 98), [3]int{}, 16), "the camera, over the picture")

	white := func(atMs int64) int {
		px, size := stillOf(t, tools, dir, StillInput{
			Aspect: Landscape, Captions: filepath.Join("testdata", "captions.ass"), CaptionsAtMs: atMs,
		})

		n := 0

		for y := 580; y < size.Height; y++ {
			for x := range size.Width {
				if p := pixel(px, size, x, y); p[0] > 220 && p[1] > 220 && p[2] > 220 {
					n++
				}
			}
		}

		return n
	}

	assert.Greater(t, white(600), 500, "the cue of 0.2-1.0 s at 0.6 s")
	assert.Zero(t, white(1500), "no cue at 1.5 s")
}

// A still at the moment a cue starts shows it, and a still a millisecond before does not (#937, the review of
// #936): on the PNG's 1/25 timebase a cue starting at 230 ms was missing at 230 ms and shown at 240 ms.
func TestAStillAtACuesStartShowsIt(t *testing.T) {
	t.Parallel()

	tools := tools(t)
	dir := t.TempDir()
	redGreenTake(t, tools, dir, 3000)

	raw, err := os.ReadFile(filepath.Join("testdata", "captions.ass"))
	require.NoError(t, err)

	ass := filepath.Join(dir, "at-230.ass")
	//nolint:gosec // The test's own file.
	require.NoError(t, os.WriteFile(ass, []byte(strings.Replace(string(raw), "0:00:00.20,0:00:01.00",
		"0:00:00.23,0:00:00.47", 1)), 0o600))

	white := func(atMs int64) int {
		px, size := stillOf(t, tools, dir, StillInput{Aspect: Landscape, Captions: ass, CaptionsAtMs: atMs})

		n := 0

		for y := 580; y < size.Height; y++ {
			for x := range size.Width {
				if p := pixel(px, size, x, y); p[0] > 220 && p[1] > 220 && p[2] > 220 {
					n++
				}
			}
		}

		return n
	}

	assert.Zero(t, white(229), "not yet at 229 ms")
	assert.Greater(t, white(230), 500, "the cue at its start, 230 ms")
	assert.Greater(t, white(469), 500, "still at 469 ms")
	assert.Zero(t, white(470), "gone at its end, 470 ms")
}
