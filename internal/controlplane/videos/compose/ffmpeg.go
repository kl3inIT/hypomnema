// Ported from aidemo (https://github.com/tandryukha/aidemo) at commit b67235a, src/ffmpeg.ts: runFfmpeg (line 4),
// probeDurationMs (line 80) and probeFreezeSpans (line 331). Issue #818, row 4. Here ffmpeg and ffprobe are called
// by the absolute paths the control plane installed them at, never through PATH, and a probe that fails says so
// instead of answering no spans: the caller keeps the scene whole and warns (issue #818, row 4).
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

package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
)

// runner runs the installed programs, in dir when it is set.
type runner struct {
	tools ffmpeg.Tools
	dir   string
}

// ffmpeg runs ffmpeg with args; on failure the error has the tail of what it wrote.
func (r runner) ffmpeg(ctx context.Context, args ...string) error {
	_, err := r.run(ctx, r.tools.FFmpeg, append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)...)

	return err
}

// ffmpegLog runs ffmpeg with args at the info level, and answers what it wrote to stderr: a filter's summary.
func (r runner) ffmpegLog(ctx context.Context, args ...string) (string, error) {
	var stderr bytes.Buffer

	//nolint:gosec // The installed program, by its absolute path.
	cmd := exec.CommandContext(ctx, r.tools.FFmpeg, append([]string{"-hide_banner", "-nostats", "-loglevel", "info", "-y"},
		args...)...)
	cmd.Dir = r.dir
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")

		return "", fmt.Errorf("%s: %w\n%s", r.tools.FFmpeg, err, strings.Join(lines[max(0, len(lines)-25):], "\n"))
	}

	return stderr.String(), nil
}

func (r runner) run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // The installed program, by its absolute path.
	cmd.Dir = r.dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")

		return nil, fmt.Errorf("%s: %w\n%s", bin, err, strings.Join(lines[max(0, len(lines)-25):], "\n"))
	}

	return stdout.Bytes(), nil
}

// durationMs is a media file's length, in ms.
func (r runner) durationMs(ctx context.Context, file string) (int64, error) {
	out, err := r.run(ctx, r.tools.FFprobe, "-v", "quiet", "-print_format", "json", "-show_format", file)
	if err != nil {
		return 0, err
	}

	var probe struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}

	if err := json.Unmarshal(out, &probe); err != nil {
		return 0, fmt.Errorf("reading what ffprobe says of %s: %w", file, err)
	}

	sec, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil {
		return 0, fmt.Errorf("%s has no duration: %w", file, err)
	}

	return int64(math.Round(sec * 1000)), nil
}

var (
	freezeStart = regexp.MustCompile(`freeze_start=\s*([\d.]+)`)
	freezeEnd   = regexp.MustCompile(`freeze_end=\s*([\d.]+)`)
)

// openFreeze ends a span still frozen at the end of the file.
const openFreeze = math.MaxInt64

// freezeSpans are the spans of file, in ms from its start, where nothing on
// screen moves for at least minMs (ffmpeg's freezedetect).
func (r runner) freezeSpans(ctx context.Context, file string, minMs int64, noise float64) ([]Span, error) {
	d := math.Max(0.1, float64(minMs)/1000)

	out, err := r.run(ctx, r.tools.FFmpeg, "-hide_banner", "-nostats", "-i", file,
		"-vf", fmt.Sprintf("freezedetect=n=%g:d=%.3f,metadata=print:file=-", noise, d), "-an", "-f", "null", "-")
	if err != nil {
		return nil, err
	}

	return parseFreezes(string(out)), nil
}

// parseFreezes reads what freezedetect prints, per span:
//
//	lavfi.freezedetect.freeze_start=12.34
//	lavfi.freezedetect.freeze_duration=2.5
//	lavfi.freezedetect.freeze_end=14.84
//
// A span still frozen at the end of the file has a start with no end.
func parseFreezes(out string) []Span {
	var (
		spans []Span
		start = -1.0
	)

	for line := range strings.Lines(out) {
		if m := freezeStart.FindStringSubmatch(line); m != nil {
			start, _ = strconv.ParseFloat(m[1], 64)

			continue
		}

		if m := freezeEnd.FindStringSubmatch(line); m != nil && start >= 0 {
			end, _ := strconv.ParseFloat(m[1], 64)
			spans = append(spans, Span{int64(math.Round(start * 1000)), int64(math.Round(end * 1000))})
			start = -1
		}
	}

	if start >= 0 {
		spans = append(spans, Span{int64(math.Round(start * 1000)), openFreeze})
	}

	out2 := spans[:0]

	for _, s := range spans {
		if s.EndMs > s.StartMs {
			out2 = append(out2, s)
		}
	}

	return out2
}
