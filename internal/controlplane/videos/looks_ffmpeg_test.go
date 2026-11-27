package videos

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/captions"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// The look with the pinned ffmpeg (issue #908, PR 3's checks 1, 5 and 7; #937).

// span is a stretch of one color in a test segment.
type span struct {
	color string
	ms    int64
}

// writeSegment writes a segment of the spans, w × h at 30 fps, into the take's folder.
func writeSegment(t *testing.T, tools ffmpeg.Tools, dir, name string, w, h int, spans ...span) devicetool.TakeSegment {
	t.Helper()

	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	graph, total := "", int64(0)

	for k, s := range spans {
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("color=c=%s:s=%dx%d:r=30:d=%.3f", s.color, w, h,
			float64(s.ms)/1000))
		graph += fmt.Sprintf("[%d:v]", k)
		total += s.ms
	}

	args = append(args, "-filter_complex", fmt.Sprintf("%sconcat=n=%d:v=1[v]", graph, len(spans)), "-map", "[v]",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-g", "30", filepath.Join(dir, name))
	run(t.Context(), t, tools.FFmpeg, args...)

	return devicetool.TakeSegment{File: name, LengthMs: total, Width: w, Height: h}
}

// realRig is a rig that makes and samples with the pinned ffmpeg, and speaks lines as tones.
func realRig(t *testing.T, tools ffmpeg.Tools) *rig {
	t.Helper()

	r := newRig(t)
	r.svc.cfg.Maker = composeMaker{}
	r.svc.cfg.Tools = func(context.Context) (ffmpeg.Tools, error) { return tools, nil }
	r.svc.cfg.Speech = toneSpeech{fakeSpeech: r.speech, t: t, tools: tools}

	return r
}

// setTake gives the rig's take these segments and steps, and writes its take.json.
func (r *rig) setTake(t *testing.T, segs []devicetool.TakeSegment, steps ...devicetool.TakeStep) {
	t.Helper()

	at := int64(0)
	for k := range segs {
		segs[k].AtMs = at
		at += segs[k].LengthMs
	}

	r.takes.take.Segments, r.takes.take.DurationMs, r.takes.take.Steps = segs, at, steps
	writeTakeJSON(t, r.takes)
}

// rgb decodes a picture or a video's frame at atMs as RGB pixels.
func rgb(ctx context.Context, t *testing.T, tools ffmpeg.Tools, file string, atMs int64, w, h int) []byte {
	t.Helper()

	args := []string{"-hide_banner", "-loglevel", "error"}
	if atMs >= 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", float64(atMs)/1000))
	}

	px := run(ctx, t, tools.FFmpeg, append(args, "-i", file, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt",
		"rgb24", "-")...)
	require.Len(t, px, 3*w*h)

	return px
}

func (r *rig) sample(t *testing.T, tools ffmpeg.Tools, req PreviewRequest) []byte {
	t.Helper()

	jpeg, err := r.svc.Preview(t.Context(), req)
	require.NoError(t, err)

	file := filepath.Join(t.TempDir(), "sample.jpg")
	require.NoError(t, os.WriteFile(file, jpeg, 0o600))

	size, err := compose.Aspect(cmpOr(req.Aspect, "9:16")).Size()
	require.NoError(t, err)

	return rgb(t.Context(), t, tools, file, -1, size.Width, size.Height)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}

	return b
}

func pixelAt(px []byte, w, x, y int) [3]int {
	i := 3 * (y*w + x)

	return [3]int{int(px[i]), int(px[i+1]), int(px[i+2])}
}

func within(p, want [3]int, tol int) bool {
	for k := range 3 {
		if d := p[k] - want[k]; d > tol || d < -tol {
			return false
		}
	}

	return true
}

// The sample's picture is the take's frame at its first step: green when the step is where green starts, and the
// frame one before the take's end when the step is at its end, with no error.
func TestTheLookSampleShowsTheFirstStepsFrame(t *testing.T) {
	t.Parallel()

	tools := realTools(t)
	green := [3]int{0, 128, 0}

	for _, stepMs := range []int64{2000, 4000} {
		t.Run(fmt.Sprint(stepMs), func(t *testing.T) {
			t.Parallel()

			r := realRig(t, tools)
			r.setDefault(t, DefaultLook())
			r.setTake(t, []devicetool.TakeSegment{writeSegment(t, tools, r.takes.dir, "seg-001.mp4", 576, 1280,
				span{"red", 2000}, span{"green", 2000})}, devicetool.TakeStep{AtMs: stepMs, Text: "Một hai"})

			off := false
			px := r.sample(t, tools, PreviewRequest{Take: takeID, Captions: &off})

			// The worked numbers: the Android hole of a 576×1280 picture at 9:16 is 102, 68, 514×1142.
			assert.True(t, within(pixelAt(px, 720, 359, 639), green, 8), "the hole is green, not %v", pixelAt(px, 720, 359, 639))
			assert.True(t, within(pixelAt(px, 720, 90, 640), [3]int{11, 11, 13}, 16), "in an android frame")
		})
	}
}

// On a take whose phone turned, the default's android frame falls back: the sample is full, the picture with
// black bars beside it.
func TestTheLookSampleOfATurnedTakeIsFull(t *testing.T) {
	t.Parallel()

	tools := realTools(t)
	r := realRig(t, tools)
	r.setDefault(t, DefaultLook())
	r.setTake(t, []devicetool.TakeSegment{
		writeSegment(t, tools, r.takes.dir, "seg-001.mp4", 576, 1280, span{"red", 2000}),
		writeSegment(t, tools, r.takes.dir, "seg-002.mp4", 1280, 576, span{"blue", 2000}),
	}, devicetool.TakeStep{AtMs: 500, Text: "Một hai"})

	off := false
	px := r.sample(t, tools, PreviewRequest{Take: takeID, Captions: &off})
	bar := pixelAt(px, 720, 20, 640)
	assert.True(t, within(bar, [3]int{}, 16), "black bars, not a frame: %v", bar)
	assert.True(t, within(pixelAt(px, 720, 360, 640), [3]int{255, 0, 0}, 24), "the picture at the step")
}

// The sample shows the line's captions as its second word starts: in Highlight, the spoken word is yellow, so the
// yellow is the second word's, on the right; a moment early it would be the first's (#937, the review of #936).
func TestTheLookSampleShowsTheSecondWordAsItStarts(t *testing.T) {
	t.Parallel()

	tools := realTools(t)
	r := realRig(t, tools)
	r.setTake(t, []devicetool.TakeSegment{writeSegment(t, tools, r.takes.dir, "seg-001.mp4", 576, 1280,
		span{"0x404040", 2000})}, devicetool.TakeStep{AtMs: 500, Text: "Mộtmột Haihai"})

	px := r.sample(t, tools, PreviewRequest{Take: takeID, Look: &LookInput{
		Captions: &CaptionsInput{Preset: ptr("highlight-yellow")}, Frame: &FrameInput{Chrome: ptr("full")},
	}})

	n, sum := 0, 0

	for y := range 1280 {
		for x := range 720 {
			if p := pixelAt(px, 720, x, y); p[0] > 200 && p[1] > 170 && p[2] < 90 {
				n++
				sum += x
			}
		}
	}

	require.Greater(t, n, 200, "a spoken word is yellow")
	assert.Greater(t, sum/n, 360, "the second word is the spoken one")
}

// diffBox is the box around the pixels that differ by more than 40 between a and b: x0, y0, x1, y1.
func diffBox(a, b []byte, w, h int) [4]int {
	box := [4]int{w, h, -1, -1}

	for y := range h {
		for x := range w {
			p, q := pixelAt(a, w, x, y), pixelAt(b, w, x, y)
			if !within(p, q, 40) {
				box = [4]int{min(box[0], x), min(box[1], y), max(box[2], x), max(box[3], y)}
			}
		}
	}

	return box
}

// meanDiff is the mean difference, in 0-255, of a and b inside box (in) or outside it.
func meanDiff(a, b []byte, w, h int, box [4]int, in bool) float64 {
	total, n := 0.0, 0

	for y := range h {
		for x := range w {
			inside := x >= box[0] && x <= box[2] && y >= box[1] && y <= box[3]
			if inside != in {
				continue
			}

			p, q := pixelAt(a, w, x, y), pixelAt(b, w, x, y)
			for k := range 3 {
				total += math.Abs(float64(p[k] - q[k]))
			}

			n += 3
		}
	}

	return total / float64(n)
}

// The sample and the video draw alike (row 14): with the same captions at the same moment, a still and the
// video's frame match outside the captions (frame, background and picture, mean ≤ 4), and the captions' block
// sits at the same place (±2 px) and looks the same (mean ≤ 8), in each chrome and both aspects. The sample does
// not promise the video shows its moment; the take here is one still color, so it does.
func TestTheLookSampleDrawsAsTheVideo(t *testing.T) {
	t.Parallel()

	tools := realTools(t)

	safeTop := compose.DefaultFrame(compose.ChromeAndroid)
	safeTop.SafeTop = true

	frames := map[string]compose.Frame{
		"android": compose.DefaultFrame(compose.ChromeAndroid), "iphone": compose.DefaultFrame(compose.ChromeIPhone),
		"safe_top": safeTop, "none": compose.DefaultFrame(compose.ChromeNone), "full": {Chrome: compose.ChromeFull},
	}

	tone := filepath.Join(t.TempDir(), "tone.mp3")
	run(t.Context(), t, tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i",
		"sine=frequency=440:duration=1", "-c:a", "libmp3lame", tone)

	for _, aspect := range []compose.Aspect{compose.Portrait, compose.Landscape} {
		for name, frame := range frames {
			t.Run(fmt.Sprintf("%s %s", aspect, name), func(t *testing.T) {
				t.Parallel()

				ctx := t.Context()
				dir := t.TempDir()
				seg := writeSegment(t, tools, dir, "seg-001.mp4", 576, 1280, span{"0x8c9aa8", 3000})
				seg.AtMs = 0
				raw, err := json.Marshal(devicetool.Take{
					ID: takeID, DurationMs: seg.LengthMs, Segments: []devicetool.TakeSegment{seg},
					Steps: []devicetool.TakeStep{{AtMs: 500, Text: "Mở ứng dụng"}},
				})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "take.json"), raw, 0o600))

				size, err := aspect.Size()
				require.NoError(t, err)

				ass, atMs, err := sampleCaptions(ctx, tools, CaptionsLook{Preset: "clean", Position: "preset"},
					"Đăng nhập hệ thống ngay", aspect, filepath.Join(dir, "captions.ass"))
				require.NoError(t, err)

				bg := compose.Background{Preset: "sunset"}
				still := func(captions string) []byte {
					out := filepath.Join(t.TempDir(), "still.jpg")
					require.NoError(t, compose.Still(ctx, tools, compose.StillInput{
						Dir: dir, Aspect: aspect, Frame: frame, Background: bg, Captions: captions, CaptionsAtMs: atMs,
						Work: t.TempDir(), Out: out,
					}))

					return rgb(ctx, t, tools, out, -1, size.Width, size.Height)
				}

				composed := filepath.Join(t.TempDir(), "composed.mp4")
				_, err = compose.Compose(ctx, tools, compose.Input{
					Dir: dir, Voice: []string{tone}, Aspect: aspect, Frame: frame, Background: bg, Work: t.TempDir(),
					Out: composed,
				})
				require.NoError(t, err)

				burned, err := compose.Burn(ctx, tools, composed, ass, t.TempDir())
				require.NoError(t, err)

				s1, s0 := still(ass), still("")
				e1, e0 := rgb(ctx, t, tools, burned, atMs, size.Width, size.Height),
					rgb(ctx, t, tools, composed, atMs, size.Width, size.Height)

				sb, eb := diffBox(s1, s0, size.Width, size.Height), diffBox(e1, e0, size.Width, size.Height)
				require.GreaterOrEqual(t, sb[2], sb[0], "the sample has captions")

				for k := range 4 {
					assert.InDelta(t, sb[k], eb[k], 2, "the captions' block: sample %v, video %v", sb, eb)
				}

				assert.LessOrEqual(t, meanDiff(s1, e1, size.Width, size.Height, sb, false), 4.0,
					"frame, background and picture")
				// Per pixel, the box at least 97 % within ±8 (the box drawn in full range, black as 16, put 9 out
				// of every channel) and the text at least 92 %, every pixel within ±28 (measured on the pinned
				// build: box 98.0-99.4 %, text 94.1-98.9 %, the most 23).
				for part, share := range map[captionPart]float64{boxPart: 0.97, textPart: 0.92} {
					within8, n, worst := captionPixels(s1, e1, size.Width, size.Height, sb, part)
					require.Positive(t, n, "%s pixels", part)
					assert.GreaterOrEqual(t, float64(within8)/float64(n), share, "%s: pixels within ±8 (%d of %d)",
						part, within8, n)
					assert.LessOrEqual(t, worst, 28, "%s: the most a pixel differs", part)
				}
			})
		}
	}
}

// A revision queued before the looks (legacy captions, no frame, as the migration writes it) is made as before:
// its video stream is the one main's pipeline makes from the same clips (compose with no frame, the legacy ASS),
// also on a take whose phone turned.
func TestARevisionQueuedBeforeTheLooksIsMadeAsBefore(t *testing.T) {
	t.Parallel()

	tools := realTools(t)

	for name, shapes := range map[string][][2]int{"upright": {{360, 800}}, "turned": {{360, 800}, {800, 360}}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := realRig(t, tools)
			r.setDefault(t, DefaultLook())

			var segs []devicetool.TakeSegment
			for k, s := range shapes {
				segs = append(segs, writeSegment(t, tools, r.takes.dir, fmt.Sprintf("seg-%03d.mp4", k+1), s[0], s[1],
					span{"0x404040", 3000}))
			}

			r.setTake(t, segs, devicetool.TakeStep{AtMs: 500, Text: "Mở ứng dụng"},
				devicetool.TakeStep{AtMs: 2000, Text: "Chạm vào Đăng nhập"})

			v, _ := r.make(t, "")
			_, err := r.svc.db.ExecContext(t.Context(), `UPDATE video_revisions SET look = ? WHERE video_id = ?`,
				`{"captions":{"preset":"legacy"},"frame":{"chrome":"full"}}`, v.ID)
			require.NoError(t, err)

			r.runAll(t)

			got := r.revision(t, v.ID, 1)
			require.Equal(t, Review, got.State, got.FailedCause)

			mp4, err := r.svc.File(t.Context(), v.ID, 1)
			require.NoError(t, err)

			assert.Equal(t, streamMD5(t, tools, mainPipeline(t, tools, r, got)), streamMD5(t, tools, mp4))
		})
	}
}

// mainPipeline makes a revision's video as main did before the looks: the clips the runner kept, composed with no
// frame, captioned with the legacy ASS.
func mainPipeline(t *testing.T, tools ffmpeg.Tools, r *rig, rev Revision) string {
	t.Helper()

	ctx := t.Context()

	var clips []string

	var scenes []captions.Scene

	for _, line := range rev.Lines {
		clips = append(clips, filepath.Join(r.dir, voiceDir, voiceHash(line, rev.Provider, rev.Model, rev.UsedVoice,
			Language)+"."+clipFormat))
	}

	work := t.TempDir()
	in := compose.Input{
		Dir: r.takes.dir, Voice: clips, Aspect: compose.Aspect(rev.Aspect), Work: filepath.Join(work, "compose"),
		Out:    filepath.Join(work, "composed.mp4"),
		Screen: compose.Size{Width: r.takes.take.Screen.Width, Height: r.takes.take.Screen.Height},
	}

	made, err := compose.Compose(ctx, tools, in)
	require.NoError(t, err)

	for k, clip := range clips {
		raw, err := os.ReadFile(clip + wordsSuffix) //nolint:gosec // The test's own file.
		require.NoError(t, err)

		var words []captions.Word
		require.NoError(t, json.Unmarshal(raw, &words))

		sc := made.Scenes[k]
		scenes = append(scenes, captions.Scene{StartMs: sc.OutStartMs, EndMs: sc.OutStartMs + sc.OutLengthMs,
			Words: words})
	}

	cues, err := captions.Build(scenes, captions.Default)
	require.NoError(t, err)

	classic := captions.Look{Preset: captions.Legacy, Position: captions.PositionPreset}
	text, err := captions.Render(ctx, tools, cues, classic, made.Size.Width, made.Size.Height, []compose.Ring{})
	require.NoError(t, err)

	ass := filepath.Join(work, "captions.ass")
	require.NoError(t, os.WriteFile(ass, []byte(text), 0o600))

	out, err := compose.Burn(ctx, tools, in.Out, ass, filepath.Join(work, "burn"))
	require.NoError(t, err)

	return out
}

func streamMD5(t *testing.T, tools ffmpeg.Tools, file string) string {
	t.Helper()

	return string(run(t.Context(), t, tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-i", file, "-map", "0:v",
		"-c", "copy", "-f", "md5", "-"))
}

// captionPart is a part of the captions' block: its text, white, or its box, dark.
type captionPart string

const (
	textPart captionPart = "text"
	boxPart  captionPart = "box"
)

// captionPixels compares the sample's and the video's captions pixel by pixel, inside box: the pixels of part (the
// text, white in the sample; the box, dark), each at least a pixel from an edge in either picture, as the encoder
// moves an edge by a 4:2:0 sample. It says how many are within ±8 in every channel, of how many, and the most any
// differs. x264 at crf 20 and 4:2:0 dims a thin white stroke by up to 20 where the sample's JPEG keeps it (measured,
// #937), so not every text pixel of the video can be within ±8 of the sample's.
func captionPixels(sample, video []byte, w, h int, box [4]int, part captionPart) (within8, n, worst int) {
	for y := box[1]; y <= box[3]; y++ {
		for x := box[0]; x <= box[2]; x++ {
			p, q := pixelAt(sample, w, x, y), pixelAt(video, w, x, y)

			switch {
			case part == textPart && min(p[0], p[1], p[2]) <= 200,
				part == boxPart && max(p[0], p[1], p[2]) >= 100,
				edgeNear(sample, w, h, x, y), edgeNear(video, w, h, x, y):
				continue
			}

			d := 0
			for k := range 3 {
				d = max(d, int(math.Abs(float64(p[k]-q[k]))))
			}

			n++
			worst = max(worst, d)

			if d <= 8 {
				within8++
			}
		}
	}

	return within8, n, worst
}

// edgeNear says whether a pixel next to x, y differs from it by more than 24 in a channel: an edge.
func edgeNear(img []byte, w, h, x, y int) bool {
	c := pixelAt(img, w, x, y)

	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			xx, yy := x+dx, y+dy
			if xx >= 0 && yy >= 0 && xx < w && yy < h && !within(pixelAt(img, w, xx, yy), c, 24) {
				return true
			}
		}
	}

	return false
}
