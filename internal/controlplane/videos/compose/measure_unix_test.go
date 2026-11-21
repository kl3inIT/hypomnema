//go:build unix

package compose

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
)

// fakeFFmpeg writes a shell script that stands for ffmpeg.
func fakeFFmpeg(t *testing.T, script string) ffmpeg.Tools {
	t.Helper()

	fake := filepath.Join(t.TempDir(), "ffmpeg")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\n"+script), 0o700)) //nolint:gosec // A script.

	return ffmpeg.Tools{FFmpeg: fake}
}

// measureLines is what a real ffmpeg prints before its last progress line.
const measureLines = "printf 'lavfi.r128.I=-20.000\\nlavfi.r128.true_peak=0.5\\nout_time_us=4000000\\n'\n"

// PR1.9: Measure refuses a decode whose output ends before progress=end, as one cut short would, and output past
// its 32 MB cap. The ffmpeg here is a script that prints what a real one would, then stops.
func TestMeasureRefusesACutOrOverlongOutput(t *testing.T) {
	t.Parallel()

	for name, script := range map[string]string{
		"cut before the end": measureLines + "printf 'progress=continue\\n'\n",
		"no progress line":   measureLines,
		"over the cap": "yes lavfi.r128.true_peak=0.5 | head -c 40000000\nprintf 'lavfi.r128.I=-20\\n" +
			"out_time_us=1\\nprogress=end\\n'\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := Measure(t.Context(), fakeFFmpeg(t, script), "track.mp3", 0)
			require.Error(t, err)
		})
	}

	tools := fakeFFmpeg(t, measureLines+"printf 'lavfi.r128.true_peak=0.25\\nprogress=end\\n'\n")

	m, err := Measure(t.Context(), tools, "track.mp3", 0)
	require.NoError(t, err)
	require.Equal(t, Measurement{LUFS: -20, TruePeak: 0.5, LengthMs: 4000}, m, "the whole output is read")
}

// Measure returns at its deadline even when ffmpeg never ends, or when something it started still holds its
// stdout, or its stdout and stderr, after it is killed (review of #1046: a sleeper holding stdout kept a 100 ms
// deadline for 8 s).
func TestMeasureReturnsAtItsDeadline(t *testing.T) {
	t.Parallel()

	for name, script := range map[string]string{
		"never ends":                   measureLines + "sleep 8\n",
		"a child holds stdout":         measureLines + "sleep 8 </dev/null 2>/dev/null &\nwait\n",
		"a child holds both its pipes": measureLines + "sleep 8 </dev/null &\nwait\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()

			start := time.Now()
			_, err := Measure(ctx, fakeFFmpeg(t, script), "track.mp3", 0)

			require.ErrorIs(t, err, context.DeadlineExceeded)
			assert.Less(t, time.Since(start), 2*time.Second, "Measure returns soon after its deadline")
		})
	}
}
