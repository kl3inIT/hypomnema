// The test tracks' bed, a brown-noise drone lowpassed with a slow amplitude wobble synthesized by ffmpeg at test time
// so that no audio file is committed (issue #910, row 19), is ported from agent-demo-video
// (https://github.com/OrionArchitekton/agent-demo-video) at commit f63b61a, src/sound.ts: ambientBedArgs (lines
// 20-29). Modified: its arguments are built in Go, and the tracks are encoded in the formats the library takes.
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

package clientapi

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
)

// pinnedTools is the pinned ffmpeg in HYPOMNEMA_FFMPEG_DIR; the test skips without it, unless HYPOMNEMA_NEED_FFMPEG
// says it is needed, as the videos' tests do.
func pinnedTools(t *testing.T) ffmpeg.Tools {
	t.Helper()

	if dir := os.Getenv("HYPOMNEMA_FFMPEG_DIR"); dir != "" {
		if tools, ok := ffmpeg.Installed(dir); ok {
			return tools
		}
	}

	if os.Getenv("HYPOMNEMA_NEED_FFMPEG") != "" {
		t.Fatal("the pinned ffmpeg is not installed: set HYPOMNEMA_FFMPEG_DIR")
	}

	t.Skip("the pinned ffmpeg is not installed: set HYPOMNEMA_FFMPEG_DIR")

	return ffmpeg.Tools{}
}

// bedSource is agent-demo-video's ambient bed of sec seconds, as an ffmpeg input and its filter.
func bedSource(sec float64) []string {
	return []string{
		"-f", "lavfi", "-i", fmt.Sprintf("anoisesrc=color=brown:sample_rate=44100:duration=%g:seed=7", sec),
		"-af", "lowpass=f=300,highpass=f=45,tremolo=f=0.12:d=0.22,volume=6dB",
	}
}

// track makes a track with ffmpeg's args, named name, and answers its bytes.
func track(t *testing.T, tools ffmpeg.Tools, name string, args ...string) []byte {
	t.Helper()

	raw, err := os.ReadFile(trackFile(t, tools, name, args...)) //nolint:gosec // The test's own file.
	require.NoError(t, err)

	return raw
}

// trackFile makes a file with ffmpeg's args, named name, and answers its path.
func trackFile(t *testing.T, tools ffmpeg.Tools, name string, args ...string) string {
	t.Helper()

	out := filepath.Join(t.TempDir(), name)
	//nolint:gosec // The pinned ffmpeg, in a test.
	cmd := exec.CommandContext(t.Context(), tools.FFmpeg, append(append([]string{"-hide_banner", "-loglevel", "error",
		"-y"}, args...), out)...)

	said, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", said)

	return out
}

// PR2.2 through the route, with the pinned ffmpeg: the formats the library takes answer 201 with their type and
// their decoded length (a VBR MP3 without its Xing header, an M4A with chapters, an MP3 with a cover and a
// truncated MP3 included); ALAC, QuickTime's MOV, two audio streams, a video, a corrupt MP3, under 10 s, over 20
// minutes and silence answer 400, as does a track sent as text/plain; nothing refused is kept.
func TestTheRouteTakesTheFormatsTheLibraryPlays(t *testing.T) {
	t.Parallel()

	tools := pinnedTools(t)
	w := newMusicWorld(t, func(cfg *videos.Config) {
		cfg.Tools = func(context.Context) (ffmpeg.Tools, error) { return tools, nil }
	})

	full := track(t, tools, "full.mp3", append(bedSource(30), "-c:a", "libmp3lame", "-b:a", "128k")...)

	// Bytes overwritten a third of the way in: ffmpeg decodes past them without -xerror, and stops with it.
	corrupt := append([]byte{}, full...)
	for i := len(corrupt) / 3; i < len(corrupt)/3+4000; i++ {
		corrupt[i] = byte(i * 37) //nolint:gosec // Its low byte, a pattern.
	}

	chapters := filepath.Join(t.TempDir(), "chapters.txt")
	require.NoError(t, os.WriteFile(chapters, []byte(";FFMETADATA1\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=6000\n"+
		"title=One\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=6000\nEND=12000\ntitle=Two\n"), 0o600))

	accepted := 0

	for _, c := range []struct {
		name, mediaType  string
		body             []byte
		lengthMs, within int64
	}{
		{"mp3 with ID3", "audio/mpeg", track(t, tools, "id3.mp3", append(bedSource(12), "-c:a", "libmp3lame")...),
			12_000, 60},
		{"mp3 without ID3", "audio/mpeg", track(t, tools, "bare.mp3", append(bedSource(12), "-c:a", "libmp3lame",
			"-id3v2_version", "0", "-write_id3v1", "0")...), 12_000, 60},
		{"mp3 with a cover", "audio/mpeg", track(t, tools, "cover.mp3", "-i",
			trackFile(t, tools, "plain.mp3", append(bedSource(12), "-c:a", "libmp3lame")...), "-i",
			trackFile(t, tools, "cover.jpg", "-f", "lavfi", "-i", "color=red:s=64x64", "-frames:v", "1"),
			"-map", "0", "-map", "1", "-c", "copy", "-id3v2_version", "3", "-disposition:v", "attached_pic"),
			12_000, 60},
		{"VBR mp3 without Xing", "audio/mpeg", track(t, tools, "vbr.mp3", append(bedSource(60), "-c:a", "libmp3lame",
			"-q:a", "0", "-write_xing", "0")...), 60_000, 100},
		{"m4a", "audio/mp4", track(t, tools, "aac.m4a", append(bedSource(12), "-c:a", "aac")...), 12_000, 60},
		{"m4a with chapters", "audio/mp4", track(t, tools, "chapters.m4a", append(bedSource(12)[:4], "-i", chapters,
			"-map", "0", "-map_metadata", "1", "-map_chapters", "1", "-c:a", "aac")...), 12_000, 60},
		{"wav of exactly 10 s", "audio/wav", track(t, tools, "ten.wav", append(bedSource(10), "-c:a", "pcm_s16le")...),
			10_000, 0},
		{"flac", "audio/flac", track(t, tools, "a.flac", append(bedSource(12), "-c:a", "flac")...), 12_000, 60},
		{"ogg", "audio/ogg", track(t, tools, "a.ogg", append(bedSource(12), "-c:a", "libvorbis")...), 12_000, 60},
		{"opus", "audio/ogg", track(t, tools, "a.opus", append(bedSource(12), "-c:a", "libopus")...), 12_000, 60},
		{"truncated mp3", "audio/mpeg", full[:len(full)/2], 15_000, 500},
	} {
		status, got := w.post(t, c.name, c.body, octetStream)
		require.Equal(t, 201, status, c.name)
		assert.Equal(t, c.mediaType, got.MediaType, c.name)
		assert.InDelta(t, c.lengthMs, got.LengthMs, float64(c.within), c.name)
		assert.Equal(t, int64(len(c.body)), got.Bytes, c.name)

		accepted++
	}

	for name, body := range map[string][]byte{
		"alac":            track(t, tools, "alac.m4a", append(bedSource(12), "-c:a", "alac")...),
		"QuickTime's MOV": track(t, tools, "aac.mov", append(bedSource(12), "-c:a", "aac", "-f", "mov")...),
		"two audio streams": track(t, tools, "two.m4a", "-f", "lavfi", "-i", "sine=d=12", "-f", "lavfi", "-i",
			"sine=f=880:d=12", "-map", "0", "-map", "1", "-c:a", "aac"),
		"a video": track(t, tools, "video.mp4", "-f", "lavfi", "-i", "sine=d=12", "-f", "lavfi", "-i",
			"color=red:s=64x64:d=12", "-c:a", "aac", "-c:v", "libx264", "-pix_fmt", "yuv420p"),
		"corrupt mp3": corrupt,
		"9.9 s":       track(t, tools, "short.wav", append(bedSource(9.9), "-c:a", "pcm_s16le")...),
		"over 20 minutes": track(t, tools, "long.mp3", "-f", "lavfi", "-i", "sine=f=440:d=1205:sample_rate=8000",
			"-ac", "1", "-c:a", "libmp3lame", "-b:a", "8k"),
		"silence": track(t, tools, "silence.wav", "-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo", "-t", "12",
			"-c:a", "pcm_s16le"),
	} {
		status, _ := w.post(t, name, body, octetStream)
		assert.Equal(t, 400, status, name)
	}

	status, _ := w.post(t, "as text", full, "text/plain")
	assert.Equal(t, 400, status, "a track sent as text/plain")

	lib, err := w.videos.Music(t.Context())
	require.NoError(t, err)
	assert.Len(t, lib.Tracks, accepted, "a refused track is not kept")

	entries, err := os.ReadDir(filepath.Join(w.dir, "music"))
	require.NoError(t, err)
	assert.Len(t, entries, accepted, "nor is its file")
	assert.True(t, bytes.Equal(full[:len(full)/2], mustRead(t, filepath.Join(w.dir, "music"), lib)),
		"a track is kept as sent")
}

// mustRead is the bytes of the library's truncated mp3, the one named so.
func mustRead(t *testing.T, dir string, lib videos.Library) []byte {
	t.Helper()

	for _, tr := range lib.Tracks {
		if tr.Name == "truncated mp3" {
			raw, err := os.ReadFile(filepath.Join(dir, tr.ID+".mp3")) //nolint:gosec // The library's file.
			require.NoError(t, err)

			return raw
		}
	}

	t.Fatal("no truncated mp3 in the library")

	return nil
}
