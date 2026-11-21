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

package videos

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
)

// bedSource is agent-demo-video's ambient bed of sec seconds, as an ffmpeg input and its filter.
func bedSource(sec float64) []string {
	return []string{
		"-f", "lavfi", "-i", fmt.Sprintf("anoisesrc=color=brown:sample_rate=44100:duration=%g:seed=7", sec),
		"-af", "lowpass=f=300,highpass=f=45,tremolo=f=0.12:d=0.22,volume=6dB",
	}
}

// fixture writes a track with ffmpeg's args into a file named name, and answers its path.
func fixture(t *testing.T, tools ffmpeg.Tools, name string, args ...string) string {
	t.Helper()

	out := filepath.Join(t.TempDir(), name)
	run(t.Context(), t, tools.FFmpeg, append(append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...),
		out)...)

	return out
}

// upload uploads a file to the library as the route does, its size said.
func (r *rig) upload(t *testing.T, file string) (Track, error) {
	t.Helper()

	raw, err := os.ReadFile(file) //nolint:gosec // The test's own file.
	require.NoError(t, err)

	return r.svc.UploadMusic(t.Context(), filepath.Base(file), int64(len(raw)), bytes.NewReader(raw))
}

// PR2.2: the formats the library takes are kept with their type and their decoded length, a VBR MP3 without its
// Xing header and a truncated MP3 included; ALAC, two audio streams, a corrupt MP3, a track under 10 s or over
// 20 minutes and silence are refused, and leave nothing.
func TestTheLibraryTakesTheFormatsItPlays(t *testing.T) {
	t.Parallel()

	tools := realTools(t)
	r := newRig(t)
	r.useTools(tools)

	full := fixture(t, tools, "full.mp3", append(bedSource(30), "-c:a", "libmp3lame", "-b:a", "128k")...)
	raw, err := os.ReadFile(full) //nolint:gosec // The test's own file.
	require.NoError(t, err)

	truncated := filepath.Join(t.TempDir(), "truncated.mp3")
	require.NoError(t, os.WriteFile(truncated, raw[:len(raw)/2], 0o600)) //nolint:gosec // The test's own folder.

	// Bytes overwritten a third of the way in: ffmpeg decodes past them without -xerror, and stops with it.
	bad := append([]byte{}, raw...)
	for i := len(bad) / 3; i < len(bad)/3+4000; i++ {
		bad[i] = byte(i * 37) //nolint:gosec // Its low byte, a pattern.
	}

	corrupt := filepath.Join(t.TempDir(), "corrupt.mp3")
	require.NoError(t, os.WriteFile(corrupt, bad, 0o600)) //nolint:gosec // The test's own folder.

	for _, c := range []struct {
		name, file, mediaType string
		lengthMs, within      int64
	}{
		{"mp3 with ID3", fixture(t, tools, "id3.mp3", append(bedSource(12), "-c:a", "libmp3lame")...),
			"audio/mpeg", 12_000, 60},
		{"mp3 without ID3", fixture(t, tools, "bare.mp3", append(bedSource(12), "-c:a", "libmp3lame",
			"-id3v2_version", "0", "-write_id3v1", "0")...), "audio/mpeg", 12_000, 60},
		{"VBR mp3 without Xing", fixture(t, tools, "vbr.mp3", append(bedSource(60), "-c:a", "libmp3lame", "-q:a", "0",
			"-write_xing", "0")...), "audio/mpeg", 60_000, 100},
		{"m4a", fixture(t, tools, "aac.m4a", append(bedSource(12), "-c:a", "aac")...), "audio/mp4", 12_000, 60},
		{"wav of exactly 10 s", fixture(t, tools, "ten.wav", append(bedSource(10), "-c:a", "pcm_s16le")...),
			"audio/wav", 10_000, 0},
		{"flac", fixture(t, tools, "a.flac", append(bedSource(12), "-c:a", "flac")...), "audio/flac", 12_000, 60},
		{"ogg", fixture(t, tools, "a.ogg", append(bedSource(12), "-c:a", "libvorbis")...), "audio/ogg", 12_000, 60},
		{"opus", fixture(t, tools, "a.opus", append(bedSource(12), "-c:a", "libopus")...), "audio/ogg", 12_000, 60},
		{"truncated mp3", truncated, "audio/mpeg", 15_000, 500},
	} {
		track, err := r.upload(t, c.file)
		require.NoError(t, err, c.name)
		assert.Equal(t, c.mediaType, track.MediaType, c.name)
		assert.InDelta(t, c.lengthMs, track.LengthMs, float64(c.within), c.name)

		path, kind, err := r.svc.MusicAudio(t.Context(), track.ID)
		require.NoError(t, err, c.name)
		assert.Equal(t, c.mediaType, kind, c.name)

		kept, err := os.ReadFile(path) //nolint:gosec // The library's file.
		require.NoError(t, err)
		sent, err := os.ReadFile(c.file)
		require.NoError(t, err)
		assert.Equal(t, sent, kept, "%s: kept as sent", c.name)
	}

	lib, err := r.svc.Music(t.Context())
	require.NoError(t, err)
	require.Len(t, lib.Tracks, 9)

	for name, file := range map[string]string{
		"alac": fixture(t, tools, "alac.m4a", append(bedSource(12), "-c:a", "alac")...),
		"two audio streams": fixture(t, tools, "two.m4a", "-f", "lavfi", "-i", "sine=d=12", "-f", "lavfi", "-i",
			"sine=f=880:d=12", "-map", "0", "-map", "1", "-c:a", "aac"),
		"corrupt mp3": corrupt,
		"9.9 s":       fixture(t, tools, "short.wav", append(bedSource(9.9), "-c:a", "pcm_s16le")...),
		"over 20 minutes": fixture(t, tools, "long.mp3", "-f", "lavfi", "-i", "sine=f=440:d=1205:sample_rate=8000",
			"-ac", "1", "-c:a", "libmp3lame", "-b:a", "8k"),
		"silence": fixture(t, tools, "silence.wav", "-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo", "-t", "12",
			"-c:a", "pcm_s16le"),
	} {
		_, err := r.upload(t, file)
		require.ErrorIs(t, err, ErrInvalid, name)
	}

	after, err := r.svc.Music(t.Context())
	require.NoError(t, err)
	assert.Len(t, after.Tracks, 9, "a refused track is not kept")
	assert.Len(t, r.musicFiles(t), 9, "nor is its file")
}

// bandRMS is the RMS level, in dBFS, of a file's sound above 2 kHz over dur seconds from start: the music's band of
// a video whose voice is a 440 Hz tone.
func bandRMS(t *testing.T, tools ffmpeg.Tools, file string, start, dur float64) float64 {
	t.Helper()

	out := run(t.Context(), t, tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-ss", strconv.FormatFloat(start,
		'f', 3, 64), "-t", strconv.FormatFloat(dur, 'f', 3, 64), "-i", file, "-map", "0:a:0", "-af",
		"highpass=f=2000,highpass=f=2000,highpass=f=2000,astats=metadata=1:reset=0,"+
			"ametadata=mode=print:key=lavfi.astats.Overall.RMS_level:file=-", "-f", "null", "-")

	level := 0.0
	found := false

	for line := range strings.Lines(string(out)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "lavfi.astats.Overall.RMS_level="); ok {
			f, err := strconv.ParseFloat(v, 64)
			require.NoError(t, err)

			level, found = f, true
		}
	}

	require.True(t, found, "astats measured nothing in %s", file)

	return level
}

// PR2.11: a track uploaded to the library and laid under a revision's voice, end to end with the pinned ffmpeg. The
// track's band in the video, while the voice speaks and before the fade, is the track's own level moved to 3 LU under
// the voice's -16 LUFS and ducked by 12 dB (PR1.2), within 1 dB; a look change of its gain alone to -6 dB lowers it
// by 6 dB, within 0.5.
func TestATrackFromTheLibraryIsDuckedUnderTheVoice(t *testing.T) {
	t.Parallel()

	tools := realTools(t)
	r := realRig(t, tools)

	run(t.Context(), t, tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i",
		"color=c=0x404040:size=360x800:rate=30:duration=6", "-c:v", "libx264", "-pix_fmt", "yuv420p",
		filepath.Join(r.takes.dir, "seg-001.mp4"))
	r.takes.take.Segments[0].Width, r.takes.take.Segments[0].Height = 360, 800
	writeTakeJSON(t, r.takes)

	// A 4 kHz tone, the same in both channels: far above the voice's 440 Hz.
	file := fixture(t, tools, "tone.flac", "-f", "lavfi", "-i", "sine=f=4000:d=20:sample_rate=44100", "-af",
		"pan=stereo|c0=c0|c1=c0", "-c:a", "flac")

	track, err := r.upload(t, file)
	require.NoError(t, err)

	own, err := compose.Measure(t.Context(), tools, file, 0)
	require.NoError(t, err)

	v, _, err := r.makeWith(t, musicIn(track.ID))
	require.NoError(t, err)
	r.runAll(t)

	_, _, err = r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{Look: musicIn(track.ID, -6), RequestID: "gain"})
	require.NoError(t, err)
	r.runAll(t)

	levels := make([]float64, 0, 2)

	for rev := 1; rev <= 2; rev++ {
		made := r.revision(t, v.ID, rev)
		require.Equal(t, Review, made.State, made.FailedStage+": "+made.FailedCause)

		mp4, err := r.svc.File(t.Context(), v.ID, rev)
		require.NoError(t, err)

		// The voice speaks from 0 to 2.3 s; the fade starts 1.8 s before the end, at 0.8 s.
		start, end := 0.2, float64(made.LengthMs)/1000-1.8
		require.Greater(t, end-start, 0.4, "a window before the fade")
		levels = append(levels, bandRMS(t, tools, mp4, start, end-start))
	}

	bedGain := compose.VoiceLUFS - 3 - own.LUFS
	want := bandRMS(t, tools, file, 2, 5) + bedGain - 12

	assert.InDelta(t, want, levels[0], 1, "the track 3 LU under the voice, ducked by 12 dB")
	assert.InDelta(t, -6, levels[1]-levels[0], 0.5, "its gain moves it")
}
