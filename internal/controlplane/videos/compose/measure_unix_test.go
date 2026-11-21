//go:build unix

package compose

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
)

// PR1.9: Measure refuses a decode whose output ends before progress=end, as one cut short would, and output past
// its 32 MB cap. The ffmpeg here is a script that prints what a real one would, then stops.
func TestMeasureRefusesACutOrOverlongOutput(t *testing.T) {
	t.Parallel()

	for name, script := range map[string]string{
		"cut before the end": "printf 'lavfi.r128.I=-20.000\\nlavfi.r128.true_peak=0.5\\nout_time_us=4000000\\n" +
			"progress=continue\\n'\n",
		"never ended": "printf 'lavfi.r128.I=-20.000\\nlavfi.r128.true_peak=0.5\\nout_time_us=4000000\\n'\n",
		"over the cap": "yes lavfi.r128.true_peak=0.5 | head -c 40000000\nprintf 'lavfi.r128.I=-20\\n" +
			"out_time_us=1\\nprogress=end\\n'\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := filepath.Join(t.TempDir(), "ffmpeg")
			require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\n"+script), 0o700)) //nolint:gosec // A script.

			_, err := Measure(t.Context(), ffmpeg.Tools{FFmpeg: fake}, "track.mp3", 0)
			require.Error(t, err)
		})
	}

	fake := filepath.Join(t.TempDir(), "ffmpeg")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\nprintf 'lavfi.r128.I=-20.000\\n"+ //nolint:gosec // A script.
		"lavfi.r128.true_peak=0.5\\nlavfi.r128.true_peak=0.25\\nout_time_us=4000000\\nprogress=end\\n'\n"), 0o700))

	m, err := Measure(t.Context(), ffmpeg.Tools{FFmpeg: fake}, "track.mp3", 0)
	require.NoError(t, err)
	require.Equal(t, Measurement{LUFS: -20, TruePeak: 0.5, LengthMs: 4000}, m, "the whole output is read")
}
