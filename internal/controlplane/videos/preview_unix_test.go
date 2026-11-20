//go:build unix

package videos

import (
	"context"
	"errors"
	"os"
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

// slowFFmpeg is an ffmpeg that writes its pid to pidFile and sleeps.
func slowFFmpeg(t *testing.T, r *rig) (pidFile string) {
	t.Helper()

	dir := t.TempDir()
	pidFile = filepath.Join(dir, "pid")
	script := filepath.Join(dir, "ffmpeg")
	sh := "#!/bin/sh\necho $$ > " + pidFile + "\nexec sleep 30\n"
	require.NoError(t, os.WriteFile(script, []byte(sh), 0o700)) //nolint:gosec // The test's own script.

	r.svc.cfg.Tools = func(context.Context) (ffmpeg.Tools, error) { return ffmpeg.Tools{FFmpeg: script}, nil }
	writeTakeJSON(t, r.takes)

	return pidFile
}

// gone says whether the ffmpeg whose pid is in pidFile has ended.
func gone(t *testing.T, pidFile string) bool {
	t.Helper()

	raw, err := os.ReadFile(pidFile) //nolint:gosec // The test's own file.
	require.NoError(t, err)

	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)

	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

// The look sample's limits (issue #908, row 14): one at a time, a wait of PreviewWait before it is refused as
// busy; PreviewTimeout before it is timed out, its ffmpeg killed; no ffmpeg is unavailable; a client that goes away
// leaves no ffmpeg running.
func TestTheLookSampleKeepsItsLimits(t *testing.T) {
	t.Parallel()

	off := false

	t.Run("busy", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		r.svc.cfg.PreviewWait = 50 * time.Millisecond
		r.svc.preview <- struct{}{}

		_, err := r.svc.Preview(t.Context(), PreviewRequest{Take: takeID})
		require.ErrorIs(t, err, ErrBusy)
	})

	t.Run("no ffmpeg", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		r.svc.cfg.Tools = func(context.Context) (ffmpeg.Tools, error) { return ffmpeg.Tools{}, errors.New("off") }

		_, err := r.svc.Preview(t.Context(), PreviewRequest{Take: takeID})
		require.ErrorIs(t, err, ErrUnavailable)
	})

	t.Run("too long", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		pidFile := slowFFmpeg(t, r)
		r.svc.cfg.PreviewTimeout = 300 * time.Millisecond

		_, err := r.svc.Preview(t.Context(), PreviewRequest{Take: takeID, Captions: &off})
		require.ErrorIs(t, err, ErrTimedOut)
		assert.True(t, gone(t, pidFile), "its ffmpeg is killed")

		// The next one is made: the slot was given back.
		r.svc.cfg.PreviewWait = 50 * time.Millisecond
		_, err = r.svc.Preview(t.Context(), PreviewRequest{Take: takeID, Captions: &off})
		require.ErrorIs(t, err, ErrTimedOut, "not busy")
	})

	t.Run("client gone", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		pidFile := slowFFmpeg(t, r)

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)

		go func() {
			_, err := r.svc.Preview(ctx, PreviewRequest{Take: takeID, Captions: &off})
			done <- err
		}()

		require.Eventually(t, func() bool { _, err := os.Stat(pidFile); return err == nil }, 5*time.Second,
			10*time.Millisecond)
		cancel()

		require.ErrorIs(t, <-done, context.Canceled)
		assert.True(t, gone(t, pidFile), "no ffmpeg is left running")
	})
}
