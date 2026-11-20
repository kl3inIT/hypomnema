// Ported from aidemo (https://github.com/tandryukha/aidemo) at commit b67235a, src/compose.ts: the scene loop of
// compose (lines 209-560: keep, extract, retime, hold, trim, the events' remap through the kept spans), the encode
// arguments every pass uses, extractAndConcat (line 1801), concatSegments (line 1382), the "contain" filter of
// resizeOutput (line 1581), overlayPngBatches (line 1881) and the audio of muxAudio (line 2081); and from
// src/voice.ts, assembleNarration (line 480). Issue #818, rows 3, 4, 7 and 8.
//
// What differs, and why: a take is one or more segments on one timeline, so a kept span is cut from every segment
// it overlaps, and each part is scaled to the output with "contain" before the parts are joined (row 8), where
// aidemo resizes the finished video; the still tail after a segment's last frame counts as frozen (row 3); each
// scene's voice is padded to the length of that scene's video as made, not by a fixed gap, so that no rounding to
// frames moves a later scene off its voice; captions, when given, are an ASS file burned with libass (row 6),
// where aidemo overlays PNGs from Chrome.
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

// Package compose makes a take into a video (issue #818): from the take's
// segments and a voice for each scene, it cuts dead time, fits each scene to
// its voice, draws the taps, scales to the output and writes one MP4.
package compose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// Aspect is the shape of the video made.
type Aspect string

// The aspects (issue #818, row 8).
const (
	Portrait  Aspect = "9:16"
	Landscape Aspect = "16:9"
)

// Size is a width and a height in pixels.
type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Size is the video's size for an aspect.
func (a Aspect) Size() (Size, error) {
	switch a {
	case Portrait:
		return Size{720, 1280}, nil
	case Landscape:
		return Size{1280, 720}, nil
	default:
		return Size{}, fmt.Errorf("aspect %q is neither %s nor %s", a, Portrait, Landscape)
	}
}

// Input is what a video is made from.
type Input struct {
	// Dir is the take's folder: take.json and the segments it names.
	Dir string
	// Voice is an audio file for each of the take's Scenes, in order.
	Voice []string
	// Captions is an ASS file timed on the video made, burned in at the end; empty for none.
	Captions string
	Aspect   Aspect
	// Screen is the size of the phone's screen, in the pixels a step's tap is in; zero: not known, and the taps get
	// no ring (a take filmed before the node recorded its screen).
	Screen Size
	// Work is a folder for what is made on the way, which the caller removes.
	Work string
	// Out is where the finished MP4 is renamed to; nothing is written there before it is whole.
	Out string
	// Progress, when set, is told how many scenes are cut and fitted to their voice, after each.
	Progress func(done int)
	// Frame is drawn around the picture, on Background (issue #908, rows 10-13); the zero Frame, like full, draws
	// none, and the picture fills the video as before.
	Frame      Frame
	Background Background
}

// SceneResult is how a scene was made, in the take's time (Keeps) and in the video's (OutStartMs, OutLengthMs,
// Ring).
type SceneResult struct {
	Scene
	Keeps       []Span  `json:"keeps"`
	VoiceMs     int64   `json:"voice_ms"`
	TargetMs    int64   `json:"target_ms"`
	SourceMs    int64   `json:"source_ms"`
	Factor      float64 `json:"factor"`
	TrimMs      int64   `json:"trim_ms"`
	HoldMs      int64   `json:"hold_ms"`
	OutStartMs  int64   `json:"out_start_ms"`
	OutLengthMs int64   `json:"out_length_ms"`
	// Ring is the tap's ring, when the scene has a tap and the take its screen's size.
	Ring *Ring `json:"ring,omitempty"`
}

// Ring is a tap's ring on the video, in its pixels: from AtMs for RingMs, centered at X, Y, Size pixels across
// (issue #908, row 9: the captions keep clear of it).
type Ring struct {
	AtMs int64 `json:"at_ms"`
	X    int   `json:"x"`
	Y    int   `json:"y"`
	Size int   `json:"size"`
}

// Result is what Compose made.
type Result struct {
	Size     Size          `json:"size"`
	LengthMs int64         `json:"length_ms"`
	Scenes   []SceneResult `json:"scenes"`
	Warnings []string      `json:"warnings,omitempty"`
}

// encodeArgs are the video encoder's arguments of every pass that encodes.
var encodeArgs = []string{
	"-r", strconv.Itoa(FPS), "-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-pix_fmt", "yuv420p",
}

// overlayBatch is the most rings drawn in one pass: each is one input and one
// overlay, and ffmpeg fails to configure a graph somewhere past ~90 of them.
const overlayBatch = 32

// segment is a take's segment with its file's path.
type segment struct {
	devicetool.TakeSegment
	path string
}

func (s segment) endMs() int64 { return s.AtMs + s.LengthMs }

// Compose makes the video of the take in in.Dir. The MP4 is written in
// in.Work and renamed to in.Out only once it is whole.
func Compose(ctx context.Context, tools ffmpeg.Tools, in Input) (Result, error) {
	// ffmpeg runs in the work folder: every path it is given is absolute.
	in, err := absolute(in)
	if err != nil {
		return Result{}, err
	}

	take, err := readTake(in.Dir)
	if err != nil {
		return Result{}, err
	}

	scenes, err := Scenes(take)
	if err != nil {
		return Result{}, err
	}

	if len(in.Voice) != len(scenes) {
		return Result{}, fmt.Errorf("the take has %d scenes and %d voice files", len(scenes), len(in.Voice))
	}

	size, err := in.Aspect.Size()
	if err != nil {
		return Result{}, err
	}

	segs, err := segments(take, in.Dir)
	if err != nil {
		return Result{}, err
	}

	if err := os.MkdirAll(in.Work, 0o700); err != nil {
		return Result{}, err
	}

	c := &composer{run: runner{tools: tools, dir: in.Work}, in: in, size: size, segs: segs, lengthMs: TakeLengthMs(take)}
	if c.frame, err = frameOf(in.Frame, in.Background, segs, size); err != nil {
		return Result{}, err
	}

	for _, sc := range scenes {
		c.noScreen = c.noScreen || sc.Tap != nil && (in.Screen.Width <= 0 || in.Screen.Height <= 0)
	}

	return c.compose(ctx, scenes)
}

type composer struct {
	run      runner
	in       Input
	size     Size
	segs     []segment
	lengthMs int64
	result   Result
	// noScreen: the screen's size is not known, and the taps were left out.
	noScreen bool
	// frame is where the frame puts the picture; nil for none.
	frame *Layout
}

// frameOf lays out a frame for a take's segments, all of which must fill its hole; nil for none.
func frameOf(f Frame, bg Background, segs []segment, size Size) (*Layout, error) {
	if !f.Framed() {
		return nil, nil //nolint:nilnil // No frame is no layout, and no error.
	}

	if err := bg.Check(); err != nil {
		return nil, err
	}

	l, err := FrameLayout(f, Size{segs[0].Width, segs[0].Height}, size)
	if err != nil {
		return nil, err
	}

	for _, seg := range segs {
		if !l.Fits(seg.Width, seg.Height) {
			return nil, fmt.Errorf("%w: segment %s is %dx%d, and the first %dx%d", ErrTurnedTake, seg.File,
				seg.Width, seg.Height, segs[0].Width, segs[0].Height)
		}
	}

	return &l, nil
}

func (c *composer) compose(ctx context.Context, scenes []Scene) (Result, error) {
	c.result = Result{Size: c.size}

	if c.noScreen {
		c.warn("the take does not say its screen's size, so its taps get no ring")
	}

	freezes, failed := c.freezes(ctx)

	var (
		sceneFiles []string
		rings      []Ring
		cursor     int64
	)

	for i, sc := range scenes {
		res, file, err := c.scene(ctx, i, sc, freezes, failed)
		if err != nil {
			return Result{}, fmt.Errorf("scene %d: %w", i+1, err)
		}

		res.OutStartMs = cursor

		if res.Ring != nil && c.noScreen {
			res.Ring = nil
		}

		if res.Ring != nil {
			x, y := c.place(sc)
			res.Ring = &Ring{AtMs: cursor + res.Ring.AtMs, X: x, Y: y, Size: ringSize(c.ringScale())}
			rings = append(rings, *res.Ring)
		}

		cursor += res.OutLengthMs
		sceneFiles = append(sceneFiles, file)
		c.result.Scenes = append(c.result.Scenes, res)

		if c.in.Progress != nil {
			c.in.Progress(i + 1)
		}
	}

	video, err := c.concat(ctx, sceneFiles, "content.mp4", "scenes")
	if err != nil {
		return Result{}, err
	}

	video, err = c.drawRings(ctx, video, rings)
	if err != nil {
		return Result{}, err
	}

	if video, err = c.framePicture(ctx, video); err != nil {
		return Result{}, err
	}

	final, err := c.mux(ctx, video)
	if err != nil {
		return Result{}, err
	}

	if c.result.LengthMs, err = c.run.durationMs(ctx, final); err != nil {
		return Result{}, err
	}

	if err := os.MkdirAll(filepath.Dir(c.in.Out), 0o700); err != nil {
		return Result{}, err
	}

	if err := os.Rename(final, c.in.Out); err != nil {
		return Result{}, fmt.Errorf("putting the video in place: %w", err)
	}

	return c.result, nil
}

func (c *composer) warn(format string, args ...any) {
	c.result.Warnings = append(c.result.Warnings, fmt.Sprintf(format, args...))
}

// freezes are the motionless spans of every segment, on the take's timeline:
// what freezedetect finds, and the still tail after the segment's last frame.
// A segment freezedetect fails on is marked failed, and the scenes over it
// are kept whole.
func (c *composer) freezes(ctx context.Context) ([]Span, map[int]bool) {
	var spans []Span

	failed := map[int]bool{}

	for k, seg := range c.segs {
		found, err := c.run.freezeSpans(ctx, seg.path, FreezeMinMs, FreezeNoise)
		if err != nil {
			failed[k] = true

			c.warn("freezedetect failed on %s, so its scenes are kept whole: %s", seg.File, firstLine(err))

			continue
		}

		for _, f := range found {
			spans = append(spans, Span{seg.AtMs + f.StartMs, seg.AtMs + min(f.EndMs, seg.LengthMs)})
		}

		if seg.LastFrameMs > 0 && seg.LastFrameMs < seg.LengthMs {
			spans = append(spans, Span{seg.AtMs + seg.LastFrameMs, seg.endMs()})
		}
	}

	return mergeIdle(spans), failed
}

// scene makes one scene's video and says how.
func (c *composer) scene(
	ctx context.Context, i int, sc Scene, freezes []Span, failed map[int]bool,
) (SceneResult, string, error) {
	res := SceneResult{Scene: sc}

	voiceMs, err := c.run.durationMs(ctx, c.in.Voice[i])
	if err != nil {
		return res, "", fmt.Errorf("reading its voice: %w", err)
	}

	res.VoiceMs = voiceMs
	res.TargetMs = voiceMs + GapMs

	var idle []Span
	if sc.LeadInMs > 0 {
		idle = append(idle, Span{sc.StartMs, sc.StartMs + sc.LeadInMs})
	}

	if c.overFailed(sc, failed) {
		c.warn("scene %d is kept whole: freezedetect failed on its footage", i+1)
	} else {
		idle = append(idle, sceneAutoIdle(freezes, sc)...)
	}

	res.Keeps = keepIntervals(sc.StartMs, sc.EndMs, mergeIdle(idle))
	if len(res.Keeps) == 0 {
		// A scene with no footage (a step said at the take's very end) shows the frame where it starts.
		at := max(0, min(sc.StartMs, c.lengthMs-1000/FPS))
		res.Keeps = []Span{{at, at + 1000/FPS}}
	}

	raw := filepath.Join(c.in.Work, fmt.Sprintf("scene-%d-raw.mp4", i))
	if err := c.extractAndConcat(ctx, res.Keeps, raw, i); err != nil {
		return res, "", err
	}

	if res.SourceMs, err = c.run.durationMs(ctx, raw); err != nil {
		return res, "", err
	}

	rt := retimeScene(res.SourceMs, res.TargetMs)
	res.Factor = rt.Factor
	res.TrimMs = int64(math.Round(rt.TrimMs))

	// fps resamples the retimed frames by their times: left to the encoder's -r, which goes by the frames' durations
	// that setpts leaves as they were, the first frames of a sped-up scene played at 1x (ffmpeg 9.0).
	vf := fmt.Sprintf("setpts=%.6f*PTS,fps=%d", rt.Factor, FPS)
	if rt.TrimMs > 0 {
		vf += fmt.Sprintf(",trim=duration=%.3f,setpts=PTS-STARTPTS", float64(res.TargetMs)/1000)
	}

	if rt.holding() {
		res.HoldMs = int64(math.Round(rt.HoldMs))
		vf += fmt.Sprintf(",tpad=stop_mode=clone:stop_duration=%.3f", rt.HoldMs/1000)
	}

	file := filepath.Join(c.in.Work, fmt.Sprintf("scene-%d.mp4", i))

	// The scene is cut at its target: retimed frames are rounded to the output's, which may leave one or two more.
	args := append([]string{"-i", raw, "-vf", vf, "-an", "-t", ms(res.TargetMs)}, encodeArgs...)
	if err := c.run.ffmpeg(ctx, append(args, file)...); err != nil {
		return res, "", err
	}

	if res.OutLengthMs, err = c.run.durationMs(ctx, file); err != nil {
		return res, "", err
	}

	if sc.Tap != nil {
		// The tap's time through the kept spans and the retime, as aidemo places a click (toContent).
		at := int64(math.Round(math.Min(float64(offsetInKeeps(res.Keeps, sc.TapAtMs))*rt.Factor, rt.StretchedMs)))
		res.Ring = &Ring{AtMs: at}
	}

	return res, file, nil
}

// overFailed says whether a scene shows footage of a segment freezedetect failed on.
func (c *composer) overFailed(sc Scene, failed map[int]bool) bool {
	for k, seg := range c.segs {
		if failed[k] && seg.AtMs < sc.EndMs && seg.endMs() > sc.StartMs {
			return true
		}
	}

	return false
}

// extractAndConcat cuts each kept span from the segments it overlaps, scaled
// to the video's size, and joins them into one file. A part is padded with
// its last frame to its length, so that a part in a segment's still tail,
// whose one frame starts before it, is as long as it should be.
func (c *composer) extractAndConcat(ctx context.Context, keeps []Span, out string, scene int) error {
	var parts []string

	for _, k := range keeps {
		for _, seg := range c.segs {
			a, b := max(k.StartMs, seg.AtMs), min(k.EndMs, seg.endMs())
			if b <= a {
				continue
			}

			from := a - seg.AtMs
			if seg.LastFrameMs > 0 && seg.LastFrameMs < seg.LengthMs {
				// The tail is one frame, and ffmpeg drops a frame that starts before the seek: a part in the tail is
				// read from just before that frame starts (10 ms, for a last_frame_ms rounded up).
				from = min(from, max(0, seg.LastFrameMs-10))
			}

			length := float64(b-a) / 1000
			part := filepath.Join(c.in.Work, fmt.Sprintf("s%d-k%d.mp4", scene, len(parts)))
			args := []string{
				"-ss", ms(from), "-i", seg.path, "-t", fmt.Sprintf("%.3f", length), "-an",
				"-vf", c.contain() + fmt.Sprintf(",tpad=stop_mode=clone:stop_duration=%.3f", length),
			}

			if err := c.run.ffmpeg(ctx, append(append(args, encodeArgs...), part)...); err != nil {
				return err
			}

			parts = append(parts, part)
		}
	}

	if len(parts) == 0 {
		return errors.New("no segment of the take has its footage")
	}

	_, err := c.concatTo(ctx, parts, out, fmt.Sprintf("s%d-list", scene))

	return err
}

// contain scales a picture to fit the video and pads the rest in black (aidemo's resizeOutput, "contain"). In a
// frame, it scales the picture to exactly the hole's size, which its shape fills but for the hole's rounding
// (issue #908, row 11, from agent-demo-video's scaledSize and MAX_WINDOW_PAD_PX).
func (c *composer) contain() string {
	if c.frame != nil {
		return fmt.Sprintf("scale=%d:%d:flags=lanczos,setsar=1", c.frame.Hole.W, c.frame.Hole.H)
	}

	w, h := c.size.Width, c.size.Height

	return fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:flags=lanczos,"+
		"pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1", w, h, w, h)
}

// place is where a scene's tap is on the video: from the screen's pixels to
// its segment's, then through the same contain scale.
func (c *composer) place(sc Scene) (int, int) {
	seg := c.segAt(sc.TapAtMs)
	sx := float64(sc.Tap.X) * float64(seg.Width) / float64(c.in.Screen.Width)
	sy := float64(sc.Tap.Y) * float64(seg.Height) / float64(c.in.Screen.Height)
	kx, ky, ox, oy := c.fit(seg)

	return int(math.Round(ox + sx*kx)), int(math.Round(oy + sy*ky))
}

// fit is the scale of a segment's picture across and down, and where it starts on the video: contain's, or in a
// frame the hole's.
func (c *composer) fit(seg segment) (float64, float64, float64, float64) {
	if seg.Width <= 0 || seg.Height <= 0 {
		return 1, 1, 0, 0
	}

	if l := c.frame; l != nil {
		return float64(l.Hole.W) / float64(seg.Width), float64(l.Hole.H) / float64(seg.Height),
			float64(l.Hole.X), float64(l.Hole.Y)
	}

	s := math.Min(float64(c.size.Width)/float64(seg.Width), float64(c.size.Height)/float64(seg.Height))

	return s, s, (float64(c.size.Width) - float64(seg.Width)*s) / 2,
		(float64(c.size.Height) - float64(seg.Height)*s) / 2
}

// origin is where the picture the scenes are cut at starts on the video: the hole's corner in a frame.
func (c *composer) origin() (int, int) {
	if c.frame != nil {
		return c.frame.Hole.X, c.frame.Hole.Y
	}

	return 0, 0
}

// segAt is the segment a time of the take is in, or the last one before it.
func (c *composer) segAt(t int64) segment {
	at := c.segs[0]

	for _, seg := range c.segs {
		if seg.AtMs <= t {
			at = seg
		}
	}

	return at
}

func (c *composer) concat(ctx context.Context, parts []string, name, list string) (string, error) {
	return c.concatTo(ctx, parts, filepath.Join(c.in.Work, name), list)
}

// concatTo joins files of one format without encoding them again.
func (c *composer) concatTo(ctx context.Context, parts []string, out, list string) (string, error) {
	if len(parts) == 1 {
		return out, os.Rename(parts[0], out)
	}

	var lines strings.Builder
	for _, p := range parts {
		fmt.Fprintf(&lines, "file '%s'\n", strings.ReplaceAll(p, "'", `'\''`))
	}

	listPath := filepath.Join(c.in.Work, list+".txt")
	if err := os.WriteFile(listPath, []byte(lines.String()), 0o600); err != nil {
		return "", err
	}

	return out, c.run.ffmpeg(ctx, "-f", "concat", "-safe", "0", "-i", listPath, "-an", "-c", "copy", out)
}

// drawRings overlays the taps' rings, in passes of at most overlayBatch.
func (c *composer) drawRings(ctx context.Context, video string, rings []Ring) (string, error) {
	if len(rings) == 0 {
		return video, nil
	}

	scale := c.ringScale()

	pattern, err := writeRing(filepath.Join(c.in.Work, "ring"), scale)
	if err != nil {
		return "", err
	}

	half := ringSize(scale) / 2
	// The rings are drawn on the picture before it is framed, and placed on the video.
	ox, oy := c.origin()

	for b := 0; b*overlayBatch < len(rings); b++ {
		batch := rings[b*overlayBatch : min(len(rings), (b+1)*overlayBatch)]
		args := []string{"-i", video}
		chain := make([]string, 0, 2*len(batch))
		prev := "0:v"

		for k, r := range batch {
			args = append(args, "-framerate", strconv.Itoa(FPS), "-i", pattern)
			label := fmt.Sprintf("o%d", k)

			if k == len(batch)-1 {
				label = "vout"
			}

			chain = append(chain,
				fmt.Sprintf("[%d:v]setpts=PTS-STARTPTS+%.3f/TB[r%d]", k+1, float64(r.AtMs)/1000, k),
				fmt.Sprintf("[%s][r%d]overlay=x=%d:y=%d:eof_action=pass:repeatlast=0[%s]",
					prev, k, r.X-ox-half, r.Y-oy-half, label))
			prev = label
		}

		out := filepath.Join(c.in.Work, fmt.Sprintf("rings-%d.mp4", b))
		args = append(args, "-filter_complex", strings.Join(chain, ";"), "-map", "[vout]", "-an")

		if err := c.run.ffmpeg(ctx, append(append(args, encodeArgs...), out)...); err != nil {
			return "", err
		}

		video = out
	}

	return video, nil
}

// ringScale is the scale a ring is drawn at: sized for a phone's screen as wide as the video shows it.
func (c *composer) ringScale() float64 {
	s, _, _, _ := c.fit(c.segs[0])

	return float64(c.segs[0].Width) * s / dpWidth
}

// framePicture pads the picture onto the video at the hole and overlays the frame's PNG (aidemo's frame pass,
// src/compose.ts:939-972); the video as it is without a frame.
func (c *composer) framePicture(ctx context.Context, video string) (string, error) {
	if c.frame == nil {
		return video, nil
	}

	plate, err := c.writePlate()
	if err != nil {
		return "", err
	}

	out := filepath.Join(c.in.Work, "framed.mp4")
	args := []string{"-i", video, "-i", plate, "-filter_complex", "[0:v]" + c.frameGraph(1) + "[vout]",
		"-map", "[vout]", "-an"}

	return out, c.run.ffmpeg(ctx, append(append(args, encodeArgs...), out)...)
}

// writePlate writes the frame's PNG into the work folder.
func (c *composer) writePlate() (string, error) {
	path := filepath.Join(c.in.Work, "frame.png")

	return path, writePNG(path, Plate(*c.frame, c.in.Background))
}

// frameGraph pads a picture of the hole's size onto the video at the hole, in aidemo's padColor, and overlays
// the frame's PNG, input number plate.
func (c *composer) frameGraph(plate int) string {
	l := c.frame

	return fmt.Sprintf("pad=%d:%d:%d:%d:color=0x0e1322[framed];[framed][%d:v]overlay=0:0:format=auto",
		l.Size.Width, l.Size.Height, l.Hole.X, l.Hole.Y, plate)
}

// mux burns the captions, when there are any, and adds the voice: each
// scene's clip resampled and padded with silence to the length of its scene's
// video, the clips joined, encoded as AAC (aidemo's assembleNarration and
// muxAudio).
func (c *composer) mux(ctx context.Context, video string) (string, error) {
	args := []string{"-i", video}
	filters := make([]string, 0, len(c.result.Scenes)+2)
	labels := make([]string, 0, len(c.result.Scenes))

	for k, sc := range c.result.Scenes {
		args = append(args, "-i", c.in.Voice[k])
		sec := float64(sc.OutLengthMs) / 1000
		filters = append(filters, fmt.Sprintf(
			"[%d:a]aresample=44100,aformat=sample_rates=44100:channel_layouts=mono,"+
				"apad=whole_dur=%.3f,atrim=end=%.3f[a%d]", k+1, sec, sec, k))
		labels = append(labels, fmt.Sprintf("[a%d]", k))
	}

	filters = append(filters, fmt.Sprintf("%sconcat=n=%d:v=0:a=1[aout]", strings.Join(labels, ""), len(labels)))

	videoMap, videoCodec := "0:v", []string{"-c:v", "copy"}

	if c.in.Captions != "" {
		if err := c.stageCaptions(); err != nil {
			return "", err
		}

		// Relative names, read in the work folder: a path in a filter's option needs no escaping then.
		filters = append(filters, "[0:v]subtitles=filename=captions.ass:fontsdir=fonts[vout]")
		videoMap, videoCodec = "[vout]", encodeArgs
	}

	var total int64
	for _, sc := range c.result.Scenes {
		total += sc.OutLengthMs
	}

	out := filepath.Join(c.in.Work, "video.mp4")
	args = append(args, "-filter_complex", strings.Join(filters, ";"), "-map", videoMap, "-map", "[aout]")
	args = append(args, videoCodec...)
	args = append(args, "-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart",
		"-t", fmt.Sprintf("%.3f", float64(total)/1000), out)

	return out, c.run.ffmpeg(ctx, args...)
}

// stageCaptions copies the captions and the fonts into the work folder.
func (c *composer) stageCaptions() error {
	return stageCaptions(c.in.Captions, c.in.Work, c.run.tools.FontsDir)
}

// stageCaptions copies an ASS file and the fonts into a work folder, as captions.ass and fonts/: ffmpeg reads them
// there by relative names, which need no escaping in a filter's option.
func stageCaptions(captions, work, fontsDir string) error {
	if err := copyFile(captions, filepath.Join(work, "captions.ass")); err != nil {
		return fmt.Errorf("reading the captions: %w", err)
	}

	fonts := filepath.Join(work, "fonts")
	if err := os.MkdirAll(fonts, 0o700); err != nil {
		return err
	}

	entries, err := os.ReadDir(fontsDir)
	if err != nil {
		return fmt.Errorf("reading the fonts: %w", err)
	}

	for _, e := range entries {
		if e.Type().IsRegular() {
			if err := copyFile(filepath.Join(fontsDir, e.Name()), filepath.Join(fonts, e.Name())); err != nil {
				return err
			}
		}
	}

	return nil
}

// Burn burns an ASS file's captions into a made video, its sound copied as it
// is (issue #818, row 6: the captions are timed on the video Compose made, so
// they are burned after it). The result is written in work, as burned.mp4,
// and its path returned.
func Burn(ctx context.Context, tools ffmpeg.Tools, video, captions, work string) (string, error) {
	paths := []*string{&video, &captions, &work}
	for _, p := range paths {
		abs, err := filepath.Abs(*p)
		if err != nil {
			return "", err
		}

		*p = abs
	}

	if err := os.MkdirAll(work, 0o700); err != nil {
		return "", err
	}

	if err := stageCaptions(captions, work, tools.FontsDir); err != nil {
		return "", err
	}

	run := runner{tools: tools, dir: work}
	out := filepath.Join(work, "burned.mp4")
	args := append([]string{"-i", video, "-vf", "subtitles=filename=captions.ass:fontsdir=fonts", "-map", "0:v",
		"-map", "0:a"}, encodeArgs...)
	args = append(args, "-c:a", "copy", "-movflags", "+faststart", out)

	return out, run.ffmpeg(ctx, args...)
}

// Thumbnail writes a video's first frame as a JPEG.
func Thumbnail(ctx context.Context, tools ffmpeg.Tools, video, out string) error {
	return runner{tools: tools}.ffmpeg(ctx, "-i", video, "-frames:v", "1", "-q:v", "3", "-f", "image2", out)
}

// LengthMs is a media file's length, in ms, as ffprobe reads it.
func LengthMs(ctx context.Context, tools ffmpeg.Tools, file string) (int64, error) {
	return runner{tools: tools}.durationMs(ctx, file)
}

func copyFile(from, to string) error {
	raw, err := os.ReadFile(from) //nolint:gosec // The caller's own file.
	if err != nil {
		return err
	}

	return os.WriteFile(to, raw, 0o600) //nolint:gosec // Into the work folder, by a name the caller chose.
}

func absolute(in Input) (Input, error) {
	var err error

	paths := []*string{&in.Dir, &in.Work, &in.Out}
	if in.Captions != "" {
		paths = append(paths, &in.Captions)
	}

	in.Voice = append([]string(nil), in.Voice...)
	for k := range in.Voice {
		paths = append(paths, &in.Voice[k])
	}

	for _, p := range paths {
		if *p, err = filepath.Abs(*p); err != nil {
			return in, err
		}
	}

	return in, nil
}

// readTake reads take.json in dir.
func readTake(dir string) (devicetool.Take, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "take.json")) //nolint:gosec // The take's own folder.
	if err != nil {
		return devicetool.Take{}, fmt.Errorf("reading the take: %w", err)
	}

	var take devicetool.Take
	if err := json.Unmarshal(raw, &take); err != nil {
		return devicetool.Take{}, fmt.Errorf("reading the take: %w", err)
	}

	return take, nil
}

// segments are the take's segments with their files, which must be in its folder.
func segments(take devicetool.Take, dir string) ([]segment, error) {
	if len(take.Segments) == 0 {
		return nil, errors.New("take has no segments")
	}

	out := make([]segment, 0, len(take.Segments))

	for _, seg := range take.Segments {
		if !filepath.IsLocal(seg.File) || strings.ContainsAny(seg.File, `/\`) {
			return nil, fmt.Errorf("segment %q is not a file of the take's folder", seg.File)
		}

		out = append(out, segment{TakeSegment: seg, path: filepath.Join(dir, seg.File)})
	}

	return out, nil
}

// ms is a time in ms as ffmpeg's seconds.
func ms(t int64) string { return fmt.Sprintf("%.3f", float64(t)/1000) }

func firstLine(err error) string {
	line, _, _ := strings.Cut(err.Error(), "\n")

	return line
}
