package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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

// The frame in the video (#908, PR 2, checks 3-6): the picture fills the hole and nothing else, the camera is
// black, a ring lands where the tap was, full is the video as before, and a turned phone is not framed.

// colorSegment encodes a segment of solid colors one after the other, each for its ms, at 30 fps, and returns it
// as take.json names it: its last frame lasts one frame, to the segment's end.
func colorSegment(t *testing.T, tools ffmpeg.Tools, dir, file string, w, h int, colors []string,
	eachMs []int64,
) devicetool.TakeSegment {
	t.Helper()

	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	inputs := make([]string, 0, len(colors))

	var total int64

	for k, c := range colors {
		args = append(args, "-f", "lavfi", "-i",
			fmt.Sprintf("color=c=%s:s=%dx%d:r=%d:d=%.3f", c, w, h, FPS, float64(eachMs[k])/1000))
		inputs = append(inputs, fmt.Sprintf("[%d:v]", k))
		total += eachMs[k]
	}

	args = append(args, "-filter_complex", fmt.Sprintf("%sconcat=n=%d:v=1[v]", strings.Join(inputs, ""), len(colors)),
		"-map", "[v]", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "10", "-pix_fmt", "yuv420p",
		filepath.Join(dir, file))

	out, err := exec.CommandContext(t.Context(), tools.FFmpeg, args...).CombinedOutput() //nolint:gosec // The test's.
	require.NoError(t, err, "%s", out)

	return devicetool.TakeSegment{
		File: file, LengthMs: total, LastFrameMs: total - 1000/FPS - 1, Width: w, Height: h,
	}
}

func writeTake(t *testing.T, dir string, take devicetool.Take) {
	t.Helper()

	raw, err := json.Marshal(take)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "take.json"), raw, 0o600))
}

// composeOne makes a one-scene video of the take in dir, its voice a 500 ms tone.
func composeOne(t *testing.T, tools ffmpeg.Tools, dir string, aspect Aspect, f Frame, bg Background,
	screen Size,
) (Result, string, error) {
	t.Helper()

	speech := &fakeSpeech{dir: dir, calls: map[string]int{}, lines: map[string]tone{"Một": {hz: 440, ms: 500}}}
	out := filepath.Join(dir, "v.mp4")
	res, err := Compose(t.Context(), tools, Input{
		Dir: dir, Voice: []string{speech.speak(t, "Một")}, Aspect: aspect, Screen: screen, Frame: f, Background: bg,
		Work: filepath.Join(dir, "work"), Out: out,
	})

	return res, out, err
}

// frameAt is the video's frame at a time, as RGB.
func frameAt(t *testing.T, tools ffmpeg.Tools, file string, atMs int64, size Size) []byte {
	t.Helper()

	//nolint:gosec // The test's ffmpeg.
	out, err := exec.CommandContext(t.Context(), tools.FFmpeg, "-v", "error", "-ss", ms(atMs), "-i", file,
		"-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-").Output()
	require.NoError(t, err)
	require.Len(t, out, 3*size.Width*size.Height)

	return out
}

func within(px []byte, want [3]int, tol int) bool {
	for k := range 3 {
		if d := int(px[k]) - want[k]; d > tol || d < -tol {
			return false
		}
	}

	return true
}

// magenta is the mask test's content color (#908, PR 2, check 3).
var magenta = [3]int{254, 0, 253}

// The mask (check 3), only for the chromes that have a hole: magenta content, no taps, both aspects, the strip on
// and off; and the padding of 44, whose hole rounds to 518×1154 (#936, P1).
func TestTheFrameShowsThePictureInItsHoleOnly(t *testing.T) {
	t.Parallel()

	tools := tools(t)

	type mask struct {
		name   string
		frame  Frame
		aspect Aspect
	}

	padded := DefaultFrame(ChromeAndroid)
	padded.Padding = 44

	cases := []mask{{"android padding 44", padded, Portrait}}

	for _, aspect := range []Aspect{Portrait, Landscape} {
		for _, f := range []Frame{
			DefaultFrame(ChromeNone), DefaultFrame(ChromeAndroid), withSafeTop(DefaultFrame(ChromeAndroid)),
			DefaultFrame(ChromeIPhone), withSafeTop(DefaultFrame(ChromeIPhone)),
		} {
			cases = append(cases, mask{fmt.Sprintf("%s safe %v %s", f.Chrome, f.SafeTop, aspect), f, aspect})
		}
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			seg := colorSegment(t, tools, dir, "0.mp4", 576, 1280, []string{"0xfe00fd"}, []int64{1000})
			writeTake(t, dir, devicetool.Take{
				ID: "tk_mask", DurationMs: seg.LengthMs, Segments: []devicetool.TakeSegment{seg},
				Steps: []devicetool.TakeStep{{AtMs: 0, Text: "Một"}},
			})

			res, out, err := composeOne(t, tools, dir, tc.aspect, tc.frame, Background{Preset: "sunset"}, Size{})
			require.NoError(t, err)

			l, err := FrameLayout(tc.frame, Size{576, 1280}, res.Size)
			require.NoError(t, err)

			assertTheHoleIsThePicture(t, frameAt(t, tools, out, 300, res.Size), l)
			assert.Equal(t, "1:1", sar(t, tools, out))
		})
	}
}

// assertTheHoleIsThePicture checks a frame of magenta content in a layout: at least 99.9 % of the hole is magenta
// (but its rounded corners, the cut-out and a band at its edge), nothing outside it is, the cut-out is black.
//
// What x264 loses on so saturated a color is allowed for, as the coordinator chose for #936 (the frame pass's own
// output, before encoding, is exact from the hole's first pixel):
//   - the band is one sample of the video's 4:2:0 color, two pixels: the pixel pair at the hole's edge shares its
//     color with the bezel (measured with the pinned ffmpeg: rows 128-129 of a hole from 128 show (241, 5, 240),
//     row 130 (251, 0, 252));
//   - magenta is within ±10, not ±8: chroma ringing leaves one column inside the edge at (251, 0, 244);
//   - the camera is measured three pixels inside its edge, where it is ≤ 16 in every channel.
//
// A bar, black or of the pad's color, would still fail by some 250.
func assertTheHoleIsThePicture(t *testing.T, px []byte, l Layout) {
	t.Helper()

	w := l.Size.Width
	at := func(x, y int) []byte { return px[3*(y*w+x) : 3*(y*w+x)+3] }
	h, r := l.Hole, l.HoleRadius

	const (
		band      = 2
		tolerance = 10
		camInside = 3
	)

	var cut Rect
	if c := l.Cutout; c != nil {
		cut = Rect{int(c.Left), c.Top, c.W + 1, c.H}
	}

	in := func(rc Rect, x, y, margin int) bool {
		return x >= rc.X-margin && x < rc.X+rc.W+margin && y >= rc.Y-margin && y < rc.Y+rc.H+margin
	}

	var inside, shown, outside int

	for y := range l.Size.Height {
		for x := range w {
			if !in(h, x, y, 1) {
				if within(at(x, y), magenta, tolerance) {
					outside++
				}

				continue
			}

			corner := (x < h.X+r || x >= h.X+h.W-r) && (y < h.Y+r || y >= h.Y+h.H-r)
			if !in(h, x, y, -band) || corner || (l.Cutout != nil && in(cut, x, y, band)) {
				continue
			}

			inside++

			if within(at(x, y), magenta, tolerance) {
				shown++
			}
		}
	}

	require.Positive(t, inside)
	assert.GreaterOrEqual(t, float64(shown)/float64(inside), 0.999, "the hole shows the picture")
	assert.Zero(t, outside, "no picture outside the hole")

	if c := l.Cutout; c != nil {
		// The cut-out's inside, camInside pixels in from its edge: a stadium, or a circle, of its radius.
		cx, cy := c.Left+float64(c.W)/2, float64(c.Top)+float64(c.H)/2
		inner := 0

		for y := c.Top; y < c.Top+c.H; y++ {
			for x := int(c.Left); x <= int(c.Left)+c.W; x++ {
				dx := math.Max(0, math.Abs(float64(x)+0.5-cx)-(float64(c.W)/2-c.Radius))
				// The whole pixel, its center and half a pixel round it, is that far in.
				if math.Hypot(dx, float64(y)+0.5-cy)+0.5 > c.Radius-camInside {
					continue
				}

				inner++
				p := at(x, y)
				require.True(t, p[0] <= 16 && p[1] <= 16 && p[2] <= 16, "the camera at (%d, %d) is %v", x, y, p)
			}
		}

		require.Positive(t, inner)
	}
}

func sar(t *testing.T, tools ffmpeg.Tools, file string) string {
	t.Helper()

	//nolint:gosec // The test's ffprobe.
	out, err := exec.CommandContext(t.Context(), tools.FFprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=sample_aspect_ratio", "-of", "default=nw=1:nk=1", file).Output()
	require.NoError(t, err)

	return strings.TrimSpace(string(out))
}

// The ring (check 5): grey content and one tap; the ring is centered within 2 px of the tap's point through the
// hole, which compose also returns.
func TestARingInAFrameIsWhereTheTapWas(t *testing.T) {
	t.Parallel()

	tools := tools(t)
	dir := t.TempDir()
	seg := colorSegment(t, tools, dir, "0.mp4", 576, 1280, []string{"0x808080"}, []int64{1000})
	writeTake(t, dir, devicetool.Take{
		ID: "tk_ring", DurationMs: seg.LengthMs, Segments: []devicetool.TakeSegment{seg},
		Steps: []devicetool.TakeStep{{AtMs: 0, Text: "Một", Tap: &devicetool.TakeTap{X: 270, Y: 1800}}},
	})

	res, out, err := composeOne(t, tools, dir, Portrait, DefaultFrame(ChromeAndroid), Background{}, Size{1080, 2400})
	require.NoError(t, err)
	require.Len(t, res.Scenes, 1)

	// The screen's (270, 1800) is the segment's (144, 960), and the hole's 102 + 144 × 514/576, 68 + 960 × 1142/1280.
	wantX, wantY := 102+144.0*514/576, 68+960.0*1142/1280

	ring := res.Scenes[0].Ring
	require.NotNil(t, ring)
	assert.InDelta(t, wantX, ring.X, 1)
	assert.InDelta(t, wantY, ring.Y, 1)
	assert.Equal(t, ringSize(514.0/dpWidth), ring.Size, "sized for the phone as wide as the hole")

	// The ring's blue pixels, a few frames in, are centered on the point.
	px := frameAt(t, tools, out, ring.AtMs+100, res.Size)

	var sx, sy, n float64

	for y := range res.Size.Height {
		for x := range res.Size.Width {
			p := px[3*(y*res.Size.Width+x):]
			if int(p[2])-int(p[0]) > 50 {
				sx, sy, n = sx+float64(x), sy+float64(y), n+1
			}
		}
	}

	require.Positive(t, n, "the ring is drawn")
	assert.InDelta(t, wantX, sx/n, 2)
	assert.InDelta(t, wantY, sy/n, 2)
}

// Full (check 4) is the video as it was before the look: the same stream as no frame at all.
func TestAFullFrameIsTheVideoAsBefore(t *testing.T) {
	t.Parallel()

	tools := tools(t)
	dir := t.TempDir()
	speech, take := fixture(t, tools, dir)

	scenes, err := Scenes(take)
	require.NoError(t, err)

	voices := make([]string, len(scenes))
	for i, sc := range scenes {
		voices[i] = speech.speak(t, sc.Text)
	}

	md5 := make([]string, 0, 2)

	for k, f := range []Frame{{}, {Chrome: ChromeFull, Padding: 400}} {
		out := filepath.Join(dir, fmt.Sprintf("v%d.mp4", k))
		_, err := Compose(t.Context(), tools, Input{
			Dir: dir, Voice: voices, Aspect: Portrait, Screen: Size{1080, 1920}, Frame: f,
			Background: Background{Preset: "aurora"}, Work: filepath.Join(dir, fmt.Sprintf("work%d", k)), Out: out,
		})
		require.NoError(t, err)

		md5 = append(md5, streamMD5(t.Context(), t, tools, out))
	}

	assert.Equal(t, md5[0], md5[1])
}

func streamMD5(ctx context.Context, t *testing.T, tools ffmpeg.Tools, file string) string {
	t.Helper()

	//nolint:gosec // The test's ffmpeg.
	out, err := exec.CommandContext(ctx, tools.FFmpeg, "-v", "error", "-i", file, "-map", "0:v", "-c", "copy",
		"-f", "md5", "-").Output()
	require.NoError(t, err)

	return strings.TrimSpace(string(out))
}

// A turned phone (check 6): compose refuses to frame a take whose segments are not of one shape, and makes it
// with no frame.
func TestATurnedTakeIsNotFramed(t *testing.T) {
	t.Parallel()

	tools := tools(t)
	dir := t.TempDir()
	upright := colorSegment(t, tools, dir, "0.mp4", 576, 1280, []string{"0x808080"}, []int64{1000})
	turned := colorSegment(t, tools, dir, "1.mp4", 1280, 576, []string{"0x808080"}, []int64{1000})
	turned.AtMs = upright.LengthMs
	writeTake(t, dir, devicetool.Take{
		ID: "tk_turn", DurationMs: 2000, Segments: []devicetool.TakeSegment{upright, turned},
		Steps: []devicetool.TakeStep{{AtMs: 0, Text: "Một"}},
	})

	for _, chrome := range []Chrome{ChromeNone, ChromeAndroid, ChromeIPhone} {
		_, _, err := composeOne(t, tools, dir, Portrait, DefaultFrame(chrome), Background{}, Size{})
		require.ErrorIs(t, err, ErrTurnedTake, "chrome %s", chrome)

		err = Still(t.Context(), tools, StillInput{
			Dir: dir, Aspect: Portrait, Frame: DefaultFrame(chrome), Work: filepath.Join(dir, "still"),
			Out: filepath.Join(dir, "still.jpg"),
		})
		require.ErrorIs(t, err, ErrTurnedTake, "chrome %s", chrome)
	}

	_, _, err := composeOne(t, tools, dir, Portrait, DefaultFrame(ChromeFull), Background{}, Size{})
	require.NoError(t, err)
}

// The App Store's aspect (issue #1230, row 4) is a video of exactly 886 × 1920, square pixels, a full frame's picture
// filling it: Apple's size for the modern iPhone class.
func TestTheAppStoreAspectIsAppleSize(t *testing.T) {
	t.Parallel()

	tools := tools(t)
	dir := t.TempDir()
	seg := colorSegment(t, tools, dir, "0.mp4", 432, 936, []string{"0xff0000"}, []int64{1000})
	writeTake(t, dir, devicetool.Take{
		ID: "tk_store", DurationMs: seg.LengthMs, Segments: []devicetool.TakeSegment{seg},
		Steps: []devicetool.TakeStep{{AtMs: 0, Text: "Một"}},
	})

	res, out, err := composeOne(t, tools, dir, AppStore, DefaultFrame(ChromeFull), Background{}, Size{})
	require.NoError(t, err)
	assert.Equal(t, Size{886, 1920}, res.Size)
	assert.Equal(t, "1:1", sar(t, tools, out))

	px := frameAt(t, tools, out, 500, res.Size)
	for _, at := range [][2]int{{20, 20}, {443, 960}, {866, 1900}} {
		p := px[3*(at[1]*res.Size.Width+at[0]):]
		assert.True(t, within(p, [3]int{235, 16, 16}, 30), "the picture fills the video at %v: %v", at, p[:3])
	}
}
