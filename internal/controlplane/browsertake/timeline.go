// Ported from agent-demo-video (https://github.com/OrionArchitekton/agent-demo-video) at commit f63b61a, issue
// #1189, row 2: src/screencast.ts, MIN_FRAME_SEC, frameDurations and framesConcatContent (each frame lasts until
// the next one's CDP timestamp, the last until stop; a list for ffmpeg's concat demuxer with the last file listed
// once more, so that its duration binds), and src/ffmpeg.ts, concatListEntry, the X264 policy's crfFrames with its
// preset (lines 14-18) and framesEncodeArgs (lines 99-111: scale and pad to the size, constant frame rate, yuv420p,
// libx264, no audio).
//
// What differs, and why: in Go, with times in milliseconds; the output is cut to the timeline's length (-t), as the
// pinned ffmpeg (9.0) honours the last entry's duration and gives the repeated last file one more, which the
// reference's older ffmpeg did not; the frames, full-range JPEGs, are scaled to the TV range that yuv420p means to a
// player and to the Make pipeline (format=yuv420p alone kept them full range, yuvj420p); the first frame is shown from the take's start, as
// autodemo's render takes the first frame for an output time before any frame came (src/render.js:193-230), so that
// the steps, stamped from the start, are on the video's own timeline; and the encode goes straight to the segment's
// MP4 with no zoom hook (Hypomnema's ring and zoom are the Make pipeline's).
//
// MIT License
//
// Copyright (c) 2026 agent-demo-video authors
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

package browsertake

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	// minFrameMs floors a frame's time on screen; it guards timestamps that do not grow (MIN_FRAME_SEC).
	minFrameMs = 1
	// FPS is the segment's constant frame rate (agent-demo-video's and autodemo's 30).
	FPS = 30
	// x264Preset and x264CRF are agent-demo-video's X264 policy for a screencast's frames.
	x264Preset = "veryfast"
	x264CRF    = "18"
)

// frameDurations is how long each frame is on screen, in milliseconds, from
// when each was drawn on the take's timeline (atMs, in order) to stopMs: frame
// i lasts until frame i+1, the last until stop, and the first from the start.
// A gap that is not positive is minFrameMs.
func frameDurations(atMs []int64, stopMs int64) []int64 {
	out := make([]int64, len(atMs))

	for i := range atMs {
		from := atMs[i]
		if i == 0 {
			from = 0
		}

		next := stopMs
		if i+1 < len(atMs) {
			next = atMs[i+1]
		}

		out[i] = max(minFrameMs, next-from)
	}

	return out
}

// concatListEntry is a file as the concat demuxer's list names it.
func concatListEntry(path string) (string, error) {
	if strings.ContainsAny(path, "\r\n") {
		return "", errors.New("ffmpeg concat list paths cannot contain newlines")
	}

	return "file '" + strings.ReplaceAll(path, "'", `'\''`) + "'", nil
}

// framesConcatContent is the concat demuxer's list of the frames with their
// durations. The demuxer ignores the duration after the last entry, so the
// last file is listed once more to make its duration bind.
func framesConcatContent(files []string, durationsMs []int64) (string, error) {
	if len(files) == 0 {
		return "", errors.New("no frames captured")
	}

	if len(files) != len(durationsMs) {
		return "", fmt.Errorf("files/durations length mismatch (%d vs %d)", len(files), len(durationsMs))
	}

	lines := make([]string, 0, 2*len(files)+1)

	for i, file := range files {
		entry, err := concatListEntry(file)
		if err != nil {
			return "", err
		}

		lines = append(lines, entry, "duration "+strconv.FormatFloat(float64(durationsMs[i])/1000, 'f', 6, 64))
	}

	last, _ := concatListEntry(files[len(files)-1])

	return strings.Join(append(lines, last), "\n"), nil
}

// framesEncodeArgs are ffmpeg's arguments that encode the frames of a list to
// an H.264 file of width by height at FPS, lengthMs long.
func framesEncodeArgs(list, out string, width, height int, lengthMs int64) []string {
	chain := strings.Join([]string{
		fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:out_range=tv", width, height),
		fmt.Sprintf("pad=%d:%d:(ow-iw)/2:(oh-ih)/2", width, height),
		fmt.Sprintf("fps=%d", FPS),
		"format=yuv420p",
	}, ",")

	return []string{
		"-y", "-hide_banner", "-loglevel", "error", "-f", "concat", "-safe", "0", "-i", list, "-vf", chain,
		"-c:v", "libx264", "-preset", x264Preset, "-crf", x264CRF, "-an",
		"-t", strconv.FormatFloat(float64(lengthMs)/1000, 'f', 3, 64), out,
	}
}
