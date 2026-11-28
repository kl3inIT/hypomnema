package ffmpeg

import (
	"context"
	"os/exec"
	"runtime"
)

// Command starts work with the installed ffmpeg. On Linux, nice sets the priority before exec replaces it with
// ffmpeg, so even the encoder's first threads inherit it and cancellation still kills the same process.
func (t Tools) Command(ctx context.Context, args ...string) *exec.Cmd {
	if runtime.GOOS == "linux" {
		args = append([]string{"-n", "10", "--", t.FFmpeg}, args...)

		//nolint:gosec // The installed program and its arguments.
		return exec.CommandContext(ctx, "/usr/bin/nice", args...)
	}

	return exec.CommandContext(ctx, t.FFmpeg, args...) //nolint:gosec // The installed program, by its absolute path.
}
