package compose

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// The compose fixture (issue #818, Tests): a take of two segments of two
// sizes across a pause, a still tail after the first's last frame, two steps
// at the same time, a scene shorter and one longer than its voice. Every
// frame carries its own time on the take as a code, so each frame of the
// video says which moment of the take it shows; each scene's voice is a tone
// of its own pitch, so the video's sound says where each voice starts. From
// them the test measures each scene's cut points, its retime and hold, the
// tap's ring and how far the picture is from the voice, as aidemo's
// test/lead-in.mjs measures scenes by the color of their frames.

// tools are the pinned ffmpeg, which CI installs with
// `go run ./internal/controlplane/videos/ffmpeg/installffmpeg <dir>` and names
// in HYPOMNEMA_FFMPEG_DIR. Without it the test skips, unless
// HYPOMNEMA_NEED_FFMPEG says it is needed.
func tools(t *testing.T) ffmpeg.Tools {
	t.Helper()

	dir := os.Getenv("HYPOMNEMA_FFMPEG_DIR")
	if dir != "" {
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

const (
	codeBits    = 12
	frameMs     = 1000.0 / FPS
	toleranceMs = 70 // two frames, and freezedetect's own frame
)

// pictureAt is the fixture's frame at a time of the take, of a segment of
// w×h: a grey ground, the frame's number in Gray code as a row of black and
// white blocks at the top (one block changes a frame, too little for
// freezedetect to call it motion), and, while the take is "active", a white
// bar that moves every frame.
func pictureAt(tMs float64, w, h int, active bool) []byte {
	img := make([]byte, w*h)
	for i := range img {
		img[i] = 64
	}

	n := int(math.Round(tMs / frameMs))
	gray := n ^ (n >> 1)

	block := float64(w) / 40

	for k := range codeBits {
		v := byte(16)
		if gray>>(codeBits-1-k)&1 == 1 {
			v = 235
		}

		fill(img, w, int(block*float64(2+2*k)), int(block*2), int(block*float64(3+2*k)), int(block*3), v)
	}

	if active {
		bar := h / 8
		y := h/2 + (n*13)%(h/2-bar)
		fill(img, w, 0, y, w, y+bar, 235)
	}

	return img
}

func fill(img []byte, w, x0, y0, x1, y1 int, v byte) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img[y*w+x] = v
		}
	}
}

// writeSegment encodes the frames of [fromMs, fromMs+lengthMs) of the take
// as one segment, as a node writes it: its last frame, at lastMs from its
// start, lasts until the segment ends (the still tail).
func writeSegment(t *testing.T, tools ffmpeg.Tools, path string, w, h int, fromMs, lengthMs, lastMs int64,
	active func(float64) bool,
) {
	t.Helper()

	frames := int(math.Round(float64(lastMs)/frameMs)) + 1
	tail := float64(lengthMs-lastMs) / frameMs

	encoded := path
	if math.Abs(tail-1) > 0.01 {
		encoded = path + ".cfr.mp4"
	}

	//nolint:gosec // The test's ffmpeg.
	cmd := exec.CommandContext(t.Context(), tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "rawvideo", "-pix_fmt", "gray", "-s", fmt.Sprintf("%dx%d", w, h), "-framerate", strconv.Itoa(FPS),
		"-i", "-", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "10", "-pix_fmt", "yuv420p", encoded)

	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	for i := range frames {
		tMs := float64(fromMs) + float64(i)*frameMs
		_, err := in.Write(pictureAt(tMs, w, h, active(tMs)))
		require.NoError(t, err)
	}

	require.NoError(t, in.Close())
	require.NoError(t, cmd.Wait())

	if encoded == path {
		return
	}

	// The last frame made long, as the node's muxer makes it (ultrafast has no B-frames: packets are in order).
	//nolint:gosec // The test's ffmpeg.
	out, err := exec.CommandContext(t.Context(), tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-i", encoded, "-c", "copy", "-bsf:v",
		fmt.Sprintf("setts=duration='if(eq(N,%d),DURATION*%.6f,DURATION)'", frames-1, tail), path).CombinedOutput()
	require.NoError(t, err, "%s", out)

	length, err := runner{tools: tools}.durationMs(t.Context(), path)
	require.NoError(t, err)
	require.InDelta(t, lengthMs, length, 2, "the segment lasts until its last frame ends")
}

// fakeSpeech stands for the speech service of part 2 (issue #818, row 5): it
// "speaks" a line as a tone of the line's pitch and length, a WAV file, and
// counts what it was asked.
type fakeSpeech struct {
	dir   string
	lines map[string]tone

	mu    sync.Mutex
	calls map[string]int
}

type tone struct {
	hz float64
	ms int
}

const sampleRate = 48000

func (f *fakeSpeech) speak(t *testing.T, text string) string {
	t.Helper()

	f.mu.Lock()
	f.calls[text]++
	n := f.calls[text]
	f.mu.Unlock()

	tn, ok := f.lines[text]
	require.True(t, ok, "the fake speech knows no line %q", text)

	samples := make([]int16, tn.ms*sampleRate/1000)
	for i := range samples {
		samples[i] = int16(12000 * math.Sin(2*math.Pi*tn.hz*float64(i)/sampleRate))
	}

	path := filepath.Join(f.dir, fmt.Sprintf("voice-%.0f-%d.wav", tn.hz, n))
	writeWAV(t, path, samples)

	return path
}

func writeWAV(t *testing.T, path string, samples []int16) {
	t.Helper()

	data := uint32(len(samples) * 2) //nolint:gosec // A tone of seconds.
	header := []any{
		[4]byte{'R', 'I', 'F', 'F'}, 36 + data, [4]byte{'W', 'A', 'V', 'E'},
		[4]byte{'f', 'm', 't', ' '}, uint32(16), uint16(1), uint16(1), uint32(sampleRate), uint32(sampleRate * 2),
		uint16(2), uint16(16), [4]byte{'d', 'a', 't', 'a'}, data,
	}

	f, err := os.Create(path) //nolint:gosec // The test's own folder.
	require.NoError(t, err)

	for _, v := range header {
		require.NoError(t, binary.Write(f, binary.LittleEndian, v))
	}

	require.NoError(t, binary.Write(f, binary.LittleEndian, samples))
	require.NoError(t, f.Close())
}

// fixture writes the take into dir and returns its scenes' lines' voices.
func fixture(t *testing.T, tools ffmpeg.Tools, dir string) (*fakeSpeech, devicetool.Take) {
	t.Helper()

	// Active spans of the take: a lead-in still to 500, motion to 4000, still to the end of the first segment
	// (its last frame at 6000, a still tail to 7000), then in the second segment motion 7000-8000, still
	// 8000-9000 (too short to cut), motion 9000-10000 and still to the end.
	active := func(tMs float64) bool {
		return (tMs >= 500 && tMs < 4000) || (tMs >= 7000 && tMs < 8000) || (tMs >= 9000 && tMs < 10000)
	}

	writeSegment(t, tools, filepath.Join(dir, "0.mp4"), 360, 640, 0, 7000, 6000, active)
	writeSegment(t, tools, filepath.Join(dir, "1.mp4"), 270, 480, 7000, 6000, 5967, active)

	take := devicetool.Take{
		ID: "tk_fixture", Title: "Fixture", State: devicetool.TakeAvailable, DurationMs: 13000,
		Segments: []devicetool.TakeSegment{
			{File: "0.mp4", AtMs: 0, LengthMs: 7000, LastFrameMs: 6000, Width: 360, Height: 640},
			{File: "1.mp4", AtMs: 7000, LengthMs: 6000, LastFrameMs: 5967, Width: 270, Height: 480},
		},
		Pauses: []devicetool.TakePause{{AtMs: 7000}},
		Steps: []devicetool.TakeStep{
			{AtMs: 500, Text: "Mở ứng dụng bản đồ", Tap: &devicetool.TakeTap{X: 540, Y: 1500}},
			{AtMs: 3000, Text: "Chọn điểm đến."},
			{AtMs: 3000, Text: "Rồi tìm đường"},
			{AtMs: 9000, Text: "Xong", Tap: &devicetool.TakeTap{X: 270, Y: 960}},
		},
	}

	raw, err := json.Marshal(take)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "take.json"), raw, 0o600))

	speech := &fakeSpeech{dir: dir, calls: map[string]int{}, lines: map[string]tone{
		"Mở ứng dụng bản đồ":           {hz: 440, ms: 5000}, // longer than its footage: x1.6 and a hold
		"Chọn điểm đến. Rồi tìm đường": {hz: 880, ms: 3000}, // about its footage: retimed
		"Xong": {hz: 1320, ms: 400}, // shorter: 2x and its tail trimmed
	}}

	return speech, take
}

func TestComposeFixture(t *testing.T) {
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

	out := filepath.Join(dir, "videos", "v1.mp4")
	res, err := Compose(t.Context(), tools, Input{
		Dir: dir, Voice: voices, Aspect: Portrait, Screen: Size{1080, 1920},
		Work: filepath.Join(dir, "work"), Out: out,
	})
	require.NoError(t, err)
	require.Empty(t, res.Warnings)
	require.Len(t, res.Scenes, 3)

	// What the plan says, from aidemo's arithmetic on this take.
	s0, s1, s2 := res.Scenes[0], res.Scenes[1], res.Scenes[2]
	assertSpans(t, []Span{{0, 400}, {500, 3000}}, s0.Keeps, "the lead-in is cut to 400 ms")
	assertSpans(t, []Span{{3000, 4000}, {4000, 4400}, {7000, 9000}}, s1.Keeps,
		"the still span and the tail after the last frame are cut to 400 ms, across the pause")
	assertSpans(t, []Span{{9000, 10000}, {10000, 10400}, {12700, 13000}}, s2.Keeps,
		"the still end is cut, its last 300 ms kept")
	assert.InDelta(t, MaxStretch, s0.Factor, 1e-9)
	assert.InDelta(t, 660, s0.HoldMs, 40)
	assert.InDelta(t, MinFactor, s2.Factor, 1e-9)
	assert.InDelta(t, 150, s2.TrimMs, 40)

	for i, sc := range res.Scenes {
		assert.InDelta(t, sc.TargetMs, sc.OutLengthMs, frameMs+1, "scene %d lasts its voice and the gap", i+1)
	}

	assert.InDelta(t, 5300+3300+700, res.LengthMs, 3*frameMs)

	// What the video shows: each frame's moment of the take.
	shown := decodeCodes(t, tools, out, 720, 0, 720)
	require.InDelta(t, float64(res.LengthMs)/frameMs, len(shown), 3)

	sceneOf := func(ms float64) int {
		for i, sc := range res.Scenes {
			if ms < float64(sc.OutStartMs+sc.OutLengthMs) {
				return i
			}
		}

		return len(res.Scenes) - 1
	}

	for f, src := range shown {
		outMs := float64(f) * frameMs
		sc := res.Scenes[sceneOf(outMs)]

		// A frame shows its own scene's footage (a frame of slack at its edges).
		if outMs > float64(sc.OutStartMs)+frameMs && outMs < float64(sc.OutStartMs+sc.OutLengthMs)-frameMs {
			assert.True(t, src >= float64(sc.StartMs)-toleranceMs && src < float64(sc.EndMs)+toleranceMs,
				"frame at %.0f ms of scene %d shows %.0f ms of the take", outMs, sc.Index+1, src)
		}

		// Nothing cut is shown.
		for _, cut := range []Span{{400, 500}, {4400, 7000}, {10400, 13000}} {
			assert.False(t, src > float64(cut.StartMs)+toleranceMs && src < float64(cut.EndMs)-toleranceMs,
				"frame at %.0f ms shows %.0f ms of the take, which is cut", outMs, src)
		}
	}

	// Each scene starts on its first kept frame: the picture's cut points.
	for i, first := range []float64{0, 3000, 9000} {
		f := int(math.Ceil(float64(res.Scenes[i].OutStartMs) / frameMs))
		assert.InDelta(t, first, shown[f], toleranceMs, "scene %d starts on the take's %.0f ms", i+1, first)
	}

	// The retime: x1.6 in the first scene, 2x in the last.
	assert.InDelta(t, 1000/MaxStretch, rate(shown, 1000, 3000), 25)
	assert.InDelta(t, 1000/MinFactor, rate(shown, float64(s2.OutStartMs)+50, float64(s2.OutStartMs)+450), 100)

	// The hold: the first scene's last 660 ms show one frame, near the end of its footage.
	holdFrom := int(math.Ceil(float64(s0.OutLengthMs-s0.HoldMs)/frameMs)) + 1
	holdTo := int(float64(s0.OutLengthMs)/frameMs) - 1

	for f := holdFrom; f < holdTo; f++ {
		assert.InDelta(t, 3000, shown[f], 2*toleranceMs, "the held frame at %.0f ms", float64(f)*frameMs)
	}

	// The taps' rings: 0.5 s from the tap's step through the cut and the retime, where the screen's point is.
	require.NotNil(t, s0.RingAtMs)
	require.NotNil(t, s2.RingAtMs)
	assert.InDelta(t, 400*MaxStretch, *s0.RingAtMs, 2, "after the cut lead-in, slowed")
	assert.Equal(t, s2.OutStartMs, *s2.RingAtMs)
	assert.Nil(t, s1.RingAtMs)

	for _, r := range []struct {
		at   int64
		x, y int
	}{{*s0.RingAtMs, 360, 1000}, {*s2.RingAtMs, 180, 640}} {
		blue := blueFrames(t, tools, out, r.x, r.y)
		first, last := -1, -1

		for f, b := range blue {
			if b {
				if first < 0 {
					first = f
				}

				last = f
			}
		}

		require.GreaterOrEqual(t, first, 0, "a ring at (%d, %d)", r.x, r.y)
		assert.InDelta(t, float64(r.at), float64(first)*frameMs, frameMs+1, "the ring starts at its tap")
		assert.InDelta(t, RingMs, float64(last-first+1)*frameMs, 3*frameMs, "the ring lasts 0.5 s")
	}

	// The sound: each scene's voice starts with its picture.
	onsets := toneOnsets(t, tools, out, []float64{440, 880, 1320})
	for i, sc := range res.Scenes {
		assert.InDelta(t, float64(sc.OutStartMs), onsets[i], 50, "scene %d's voice is with its picture", i+1)
	}
}

// assertSpans compares kept spans within freezedetect's frame.
func assertSpans(t *testing.T, want, got []Span, msg string) {
	t.Helper()

	if !assert.Len(t, got, len(want), msg+": %v", got) {
		return
	}

	for i := range want {
		assert.InDelta(t, want[i].StartMs, got[i].StartMs, frameMs+1, msg+": %v", got)
		assert.InDelta(t, want[i].EndMs, got[i].EndMs, frameMs+1, msg+": %v", got)
	}
}

// rate is how many ms of the take a second of the video shows over [fromMs, toMs) of it.
func rate(shown []float64, fromMs, toMs float64) float64 {
	a, b := int(fromMs/frameMs), int(toMs/frameMs)

	return (shown[b] - shown[a]) / ((toMs - fromMs) / 1000)
}

// decodeCodes reads each frame's code, the moment of the take it shows in
// ms, from a video frameW wide whose picture is picW wide from picX.
func decodeCodes(t *testing.T, tools ffmpeg.Tools, video string, frameW int, picX, picW float64) []float64 {
	t.Helper()

	const h = 80

	var shown []float64

	readFrames(t, tools, video, []string{"-vf", fmt.Sprintf("crop=%d:%d:0:0", frameW, h), "-pix_fmt", "gray"},
		frameW*h, func(frame []byte) {
			block := picW / 40
			gray := 0

			for k := range codeBits {
				x, y := int(picX+block*(2.5+2*float64(k))), int(block*2.5)
				bit := 0

				if mean(frame, frameW, x, y) > 128 {
					bit = 1
				}

				gray = gray<<1 | bit
			}

			n := 0
			for g := gray; g != 0; g >>= 1 {
				n ^= g
			}

			shown = append(shown, float64(n)*frameMs)
		})

	return shown
}

func mean(frame []byte, w, x, y int) float64 {
	var sum float64

	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			sum += float64(frame[(y+dy)*w+x+dx])
		}
	}

	return sum / 25
}

// blueFrames says of each frame whether the ring's blue is around (x, y).
func blueFrames(t *testing.T, tools ffmpeg.Tools, video string, x, y int) []bool {
	t.Helper()

	const side = 200

	var out []bool

	readFrames(t, tools, video, []string{
		"-vf", fmt.Sprintf("crop=%d:%d:%d:%d", side, side, x-side/2, y-side/2), "-pix_fmt", "rgb24",
	}, side*side*3, func(frame []byte) {
		blue := false

		for i := 0; i < len(frame); i += 3 {
			if int(frame[i+2])-int(frame[i]) > 50 {
				blue = true

				break
			}
		}

		out = append(out, blue)
	})

	return out
}

func readFrames(t *testing.T, tools ffmpeg.Tools, video string, args []string, size int, each func([]byte)) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	full := append(append([]string{"-v", "error", "-i", video}, args...), "-f", "rawvideo", "-")
	cmd := exec.CommandContext(ctx, tools.FFmpeg, full...) //nolint:gosec // The test's ffmpeg.

	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	r := bufio.NewReaderSize(stdout, 1<<20)
	frame := make([]byte, size)

	for {
		if _, err := io.ReadFull(r, frame); err != nil {
			break
		}

		each(frame)
	}

	require.NoError(t, cmd.Wait())
}

// toneOnsets is when each pitch is first heard, in ms of the video: the first
// 10 ms window where it is the loudest of the pitches and loud.
func toneOnsets(t *testing.T, tools ffmpeg.Tools, video string, pitches []float64) []float64 {
	t.Helper()

	//nolint:gosec // The test's ffmpeg.
	out, err := exec.CommandContext(t.Context(), tools.FFmpeg, "-v", "error", "-i", video,
		"-map", "0:a", "-ac", "1", "-ar", strconv.Itoa(sampleRate), "-f", "s16le", "-").Output()
	require.NoError(t, err)

	samples := make([]int16, len(out)/2)
	require.NoError(t, binary.Read(bytes.NewReader(out), binary.LittleEndian, samples))

	const window = sampleRate / 100

	onsets := make([]float64, len(pitches))
	for i := range onsets {
		onsets[i] = -1
	}

	for w := 0; (w+1)*window <= len(samples); w++ {
		chunk := samples[w*window : (w+1)*window]
		best, bestPower := -1, 0.0

		for i, hz := range pitches {
			if p := goertzel(chunk, hz); p > bestPower {
				best, bestPower = i, p
			}
		}

		if best >= 0 && bestPower > 1e12 && onsets[best] < 0 {
			onsets[best] = float64(w) * 10
		}
	}

	for i, o := range onsets {
		require.GreaterOrEqual(t, o, 0.0, "the tone of %.0f Hz is heard", pitches[i])
	}

	return onsets
}

func goertzel(samples []int16, hz float64) float64 {
	coeff := 2 * math.Cos(2*math.Pi*hz/sampleRate)

	var s1, s2 float64

	for _, x := range samples {
		s0 := float64(x) + coeff*s1 - s2
		s2, s1 = s1, s0
	}

	return s1*s1 + s2*s2 - coeff*s1*s2
}

// A scene that starts in a segment's still tail shows its last frame; a
// 16:9 video shows the phone's picture centered between black bars; captions
// are burned in with the fonts installed beside ffmpeg.
func TestComposeTailLandscapeAndCaptions(t *testing.T) {
	t.Parallel()

	tools := tools(t)
	dir := t.TempDir()

	writeSegment(t, tools, filepath.Join(dir, "0.mp4"), 360, 640, 0, 7000, 6000,
		func(tMs float64) bool { return tMs < 4000 })

	take := devicetool.Take{
		ID: "tk_tail", DurationMs: 7000,
		Segments: []devicetool.TakeSegment{{File: "0.mp4", LengthMs: 7000, LastFrameMs: 6000, Width: 360, Height: 640}},
		Steps:    []devicetool.TakeStep{{AtMs: 0, Text: "Một"}, {AtMs: 6500, Text: "Hai"}},
	}

	raw, err := json.Marshal(take)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "take.json"), raw, 0o600))

	speech := &fakeSpeech{dir: dir, calls: map[string]int{}, lines: map[string]tone{
		"Một": {hz: 440, ms: 1000}, "Hai": {hz: 880, ms: 1000},
	}}

	captions := filepath.Join("testdata", "captions.ass")

	out := filepath.Join(dir, "v.mp4")
	res, err := Compose(t.Context(), tools, Input{
		Dir: dir, Voice: []string{speech.speak(t, "Một"), speech.speak(t, "Hai")}, Captions: captions,
		Aspect: Landscape, Work: filepath.Join(dir, "work"), Out: out,
	})
	require.NoError(t, err)
	require.Len(t, res.Scenes, 2)
	assert.Equal(t, Size{1280, 720}, res.Size)

	tail := res.Scenes[1]
	assert.Equal(t, []Span{{6500, 7000}}, tail.Keeps)
	assert.InDelta(t, 1300, tail.OutLengthMs, frameMs+1)

	// The picture is 405 px wide (640 high scaled to 720), from x = 437.5.
	shown := decodeCodes(t, tools, out, 1280, 437.5, 405)
	from := int(math.Ceil(float64(tail.OutStartMs)/frameMs)) + 1

	require.Greater(t, len(shown), from)

	for f := from; f < len(shown); f++ {
		assert.InDelta(t, 6000, shown[f], 1, "the tail shows the segment's last frame")
	}

	// Black bars beside the picture; the caption's white letters at the bottom while its cue lasts.
	var bars, captioned []bool

	readFrames(t, tools, out, []string{"-vf", "crop=1280:140:0:580", "-pix_fmt", "gray"}, 1280*140,
		func(frame []byte) {
			bars = append(bars, frame[70*1280+100] < 30 && frame[70*1280+1180] < 30)

			white := 0
			for _, v := range frame {
				if v > 220 {
					white++
				}
			}

			captioned = append(captioned, white > 500)
		})

	require.NotEmpty(t, bars)
	assert.NotContains(t, bars, false)
	assert.True(t, captioned[int(600/frameMs)], "the cue is shown at 0.6 s")
	assert.False(t, captioned[int(1500/frameMs)], "the cue is gone at 1.5 s")
}

// A take filmed before the node recorded its screen's size is made without its taps' rings, with a warning
// (issue #818).
func TestComposeWithoutTheScreenDrawsNoRing(t *testing.T) {
	t.Parallel()

	tools := tools(t)
	dir := t.TempDir()

	writeSegment(t, tools, filepath.Join(dir, "0.mp4"), 360, 640, 0, 3000, 2000,
		func(tMs float64) bool { return true })

	take := devicetool.Take{
		ID: "tk_noscreen", DurationMs: 3000,
		Segments: []devicetool.TakeSegment{{File: "0.mp4", LengthMs: 3000, LastFrameMs: 2000, Width: 360, Height: 640}},
		Steps:    []devicetool.TakeStep{{AtMs: 0, Text: "Một", Tap: &devicetool.TakeTap{X: 10, Y: 10}}},
	}

	raw, err := json.Marshal(take)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "take.json"), raw, 0o600))

	speech := &fakeSpeech{dir: dir, calls: map[string]int{}, lines: map[string]tone{"Một": {hz: 440, ms: 500}}}

	res, err := Compose(t.Context(), tools, Input{
		Dir: dir, Voice: []string{speech.speak(t, "Một")}, Aspect: Portrait, Work: filepath.Join(dir, "work"),
		Out: filepath.Join(dir, "v.mp4"),
	})
	require.NoError(t, err)
	require.Len(t, res.Scenes, 1)
	assert.Nil(t, res.Scenes[0].RingAtMs)
	assert.Contains(t, res.Warnings, "the take does not say its screen's size, so its taps get no ring")
}
