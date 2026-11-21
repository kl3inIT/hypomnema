//go:build unix

package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PR1.1: without music, the mux's command is the one made before the music (issue #910, row 14), with and without
// captions: the argv as main ran it, read by a fake ffmpeg that writes down its arguments. (Its output, stream for
// stream, was compared with main's by hand; an md5 golden would differ across the builds of ffmpeg.)
func TestTheMuxWithoutMusicIsUnchanged(t *testing.T) {
	t.Parallel()

	voiceGraph := "[1:a]aresample=44100,aformat=sample_rates=44100:channel_layouts=mono,apad=whole_dur=1.300," +
		"atrim=end=1.300[a0];[2:a]aresample=44100,aformat=sample_rates=44100:channel_layouts=mono," +
		"apad=whole_dur=2.033,atrim=end=2.033[a1];[a0][a1]concat=n=2:v=0:a=1[aout]"

	for name, tc := range map[string]struct {
		captions bool
		tail     []string
	}{
		"without captions": {tail: []string{
			"-filter_complex", voiceGraph, "-map", "0:v", "-map", "[aout]", "-c:v", "copy",
		}},
		"with captions": {captions: true, tail: []string{
			"-filter_complex", voiceGraph + ";[0:v]subtitles=filename=captions.ass:fontsdir=fonts[vout]",
			"-map", "[vout]", "-map", "[aout]",
			"-r", "30", "-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-pix_fmt", "yuv420p",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			argv := filepath.Join(dir, "argv")
			tools := fakeFFmpeg(t, `for a in "$@"; do printf '%s\n' "$a"; done > '`+argv+"'\n")
			tools.FontsDir = filepath.Join(dir, "fonts")
			require.NoError(t, os.MkdirAll(tools.FontsDir, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(tools.FontsDir, "a.ttf"), nil, 0o600))

			work := filepath.Join(dir, "work")
			require.NoError(t, os.MkdirAll(work, 0o700))

			in := Input{Voice: []string{filepath.Join(dir, "v0.wav"), filepath.Join(dir, "v1.wav")}, Work: work}
			if tc.captions {
				captions, err := filepath.Abs(filepath.Join("testdata", "captions.ass"))
				require.NoError(t, err)

				in.Captions = captions
			}

			c := &composer{run: runner{tools: tools, dir: work}, in: in}
			c.result.Scenes = []SceneResult{{OutLengthMs: 1300}, {OutLengthMs: 2033}}

			out, err := c.mux(t.Context(), filepath.Join(dir, "content.mp4"))
			require.NoError(t, err)

			want := []string{
				"-hide_banner", "-loglevel", "error", "-y", "-i", filepath.Join(dir, "content.mp4"),
				"-i", in.Voice[0], "-i", in.Voice[1],
			}
			want = append(want, tc.tail...)
			want = append(want, "-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", "-t", "3.333", out)

			raw, err := os.ReadFile(argv) //nolint:gosec // The test's own file.
			require.NoError(t, err)
			assert.Equal(t, want, strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n"))
		})
	}
}
