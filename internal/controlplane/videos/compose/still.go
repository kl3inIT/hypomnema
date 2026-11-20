package compose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
)

// StillInput is what a still of a take is made from: the take's frame at its first step, in a look (issue #908,
// row 14, the look sample).
type StillInput struct {
	// Dir is the take's folder: take.json and the segments it names.
	Dir    string
	Aspect Aspect
	// Frame and Background are the look's, as Compose draws them.
	Frame      Frame
	Background Background
	// Captions is an ASS file on the video's size, empty for none; the still shows it as it is at CaptionsAtMs.
	Captions     string
	CaptionsAtMs int64
	// Work is a folder for what is made on the way, which the caller removes.
	Work string
	// Out is where the JPEG is written.
	Out string
}

// Still writes a JPEG of the take's picture at its first step, drawn as Compose draws the video: the same scale,
// frame, PNG and captions. It is a sample of the look, not a frame the video is sure to show: the video may cut
// that moment (keep.go).
//
// The moment is the first step's, or one frame before the take's end when the step is at its end, as Compose
// shows a scene with no footage (compose.go). Its frame is the one that covers it, the last one to start at or
// before it, which may have started long before: a still screen sends no frames, and a one-frame segment's frame
// covers the whole of it (#936).
func Still(ctx context.Context, tools ffmpeg.Tools, in StillInput) error {
	paths := []*string{&in.Dir, &in.Work, &in.Out}
	if in.Captions != "" {
		paths = append(paths, &in.Captions)
	}

	for _, p := range paths {
		abs, err := filepath.Abs(*p)
		if err != nil {
			return err
		}

		*p = abs
	}

	take, err := readTake(in.Dir)
	if err != nil {
		return err
	}

	scenes, err := Scenes(take)
	if err != nil {
		return err
	}

	size, err := in.Aspect.Size()
	if err != nil {
		return err
	}

	segs, err := segments(take, in.Dir)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(in.Work, 0o700); err != nil {
		return err
	}

	c := &composer{
		run: runner{tools: tools, dir: in.Work}, size: size, segs: segs, lengthMs: TakeLengthMs(take),
		in: Input{Work: in.Work, Background: in.Background, Frame: in.Frame},
	}
	if c.frame, err = frameOf(in.Frame, in.Background, segs, size); err != nil {
		return err
	}

	at := max(0, min(scenes[0].StepAtMs, c.lengthMs-1000/FPS))

	picture, err := c.coveringFrame(ctx, c.segAt(at), at)
	if err != nil {
		return err
	}

	args := []string{"-i", picture}
	graph := "[0:v]" + c.contain()

	if c.frame != nil {
		plate, err := c.writePlate()
		if err != nil {
			return err
		}

		args = append(args, "-i", plate)
		graph += "[picture];[picture]" + c.frameGraph(1)
	}

	if in.Captions != "" {
		if err := stageCaptions(in.Captions, in.Work, tools.FontsDir); err != nil {
			return err
		}

		// The picture is put at the moment plus half a millisecond, on a timebase fine enough to hold it: libass is
		// given the frame's time in whole milliseconds, truncated, so a cue starting at the moment shows, and one
		// ending there does not. On the PNG's own 1/25 timebase the moment fell to the 40 ms before it (#937).
		graph += fmt.Sprintf(",settb=1/2000,setpts=%d,subtitles=filename=captions.ass:fontsdir=fonts",
			2*in.CaptionsAtMs+1)
	}

	args = append(args, "-filter_complex", graph+"[vout]", "-map", "[vout]", "-frames:v", "1", "-q:v", "3",
		"-f", "image2", in.Out)

	return c.run.ffmpeg(ctx, args...)
}

// coveringFrame writes the frame of seg that covers the take's moment at, as a PNG, and returns its path: the last
// frame to start at or before it. ffmpeg's -ss alone would give the first frame after it, and nothing in a still
// tail; so the seek goes to the key frame before it, keeps what it decodes from there (-noaccurate_seek) on the
// segment's own times (-copyts -start_at_zero), and trim stops at the moment, each frame overwriting the last.
func (c *composer) coveringFrame(ctx context.Context, seg segment, at int64) (string, error) {
	local := min(max(0, at-seg.AtMs), seg.LengthMs)
	out := filepath.Join(c.in.Work, "picture.png")

	if err := c.run.ffmpeg(ctx, "-noaccurate_seek", "-ss", ms(local), "-copyts", "-start_at_zero", "-i", seg.path,
		"-an", "-vf", fmt.Sprintf("trim=end=%.4f", (float64(local)+0.5)/1000), "-fps_mode", "passthrough",
		"-update", "1", out); err != nil {
		return "", err
	}

	if _, err := os.Stat(out); err == nil || !errors.Is(err, os.ErrNotExist) {
		return out, err
	}

	// Before the segment's first frame: that frame.
	return out, c.run.ffmpeg(ctx, "-i", seg.path, "-an", "-frames:v", "1", "-update", "1", out)
}
