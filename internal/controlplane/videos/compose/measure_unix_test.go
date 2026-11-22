//go:build unix

package compose

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
)

// fakeFFmpeg writes a shell script that stands for ffmpeg. A shell writes it, not this process: a file this process
// opened for writing can be inherited by another test's fork until that child execs, and running the script in that
// moment fails with ETXTBSY ("text file busy"), whatever the file is named; the shell's descriptor dies with it.
func fakeFFmpeg(t *testing.T, script string) ffmpeg.Tools {
	t.Helper()

	fake := filepath.Join(t.TempDir(), "ffmpeg")
	//nolint:gosec // The test's own shell, writing into its own folder.
	write := exec.CommandContext(t.Context(), "/bin/sh", "-c", `cat > "$1" && chmod 700 "$1"`, "sh", fake)
	write.Stdin = strings.NewReader("#!/bin/sh\n" + script)

	out, err := write.CombinedOutput()
	require.NoError(t, err, "%s", out)

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

// Once Measure returns at its deadline, nothing ffmpeg started is left running: the whole process group is killed,
// not ffmpeg alone (review of #1046: two `sleep 30` children were still sleeping after Measure returned).
func TestMeasureKillsWhatFFmpegStarted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	pids := filepath.Join(dir, "pids")
	tools := fakeFFmpeg(t, measureLines+"sleep 30 </dev/null 2>/dev/null &\necho $! >> '"+pids+"'\n"+
		"sleep 30 </dev/null &\necho $! >> '"+pids+"'\nwait\n")

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()

	_, err := Measure(ctx, tools, "track.mp3", 0)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	raw, err := os.ReadFile(pids) //nolint:gosec // The test's own file.
	require.NoError(t, err)

	children := strings.Fields(string(raw))
	require.Len(t, children, 2)

	for _, field := range children {
		pid, err := strconv.Atoi(field)
		require.NoError(t, err)

		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
		assert.Eventually(t, func() bool { return !running(pid) }, 2*time.Second, 20*time.Millisecond,
			"child %d is gone", pid)
	}
}

// ffmpeg that says progress=end and exits while something it started still holds its stderr alone: Measure kills
// that too, whether its deadline comes while it waits for stderr (150 ms) or later (2 s, after the 500 ms it gives a
// killed or ended ffmpeg's pipes), and whether ffmpeg exits 0 or fails (review rounds 3 and 4 of #1046).
func TestMeasureKillsWhatHoldsStderrAfterFFmpegEnds(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		deadline time.Duration
		exit     int
	}{
		"deadline first":               {150 * time.Millisecond, 0},
		"deadline later":               {2 * time.Second, 0},
		"ffmpeg fails, deadline later": {5 * time.Second, 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			pids := filepath.Join(t.TempDir(), "pids")
			tools := fakeFFmpeg(t, measureLines+"printf 'progress=end\\n'\n"+
				"sleep 30 </dev/null >/dev/null &\necho $! > '"+pids+"'\nexit "+strconv.Itoa(tc.exit)+"\n")

			ctx, cancel := context.WithTimeout(t.Context(), tc.deadline)
			defer cancel()

			start := time.Now()
			_, err := Measure(ctx, tools, "track.mp3", 0)
			took := time.Since(start)

			require.Error(t, err, "a pipe still held when ffmpeg is gone is an error")
			assert.Less(t, took, time.Second, "Measure returns within its pipes' 500 ms")

			raw, err := os.ReadFile(pids) //nolint:gosec // The test's own file.
			require.NoError(t, err)

			pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			require.NoError(t, err)

			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			assert.Eventually(t, func() bool { return !running(pid) }, 500*time.Millisecond, 10*time.Millisecond,
				"what held stderr is gone once Measure returns")
		})
	}
}

// running says whether a process is alive and not a zombie waiting to be reaped: by /proc where there is one.
func running(pid int) bool {
	if _, err := os.Stat("/proc/self"); err != nil {
		return syscall.Kill(pid, 0) == nil
	}

	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}

	// The state follows the command's name in parentheses: "pid (comm) S ...".
	_, after, _ := strings.Cut(string(stat), ") ")

	return !strings.HasPrefix(after, "Z")
}
