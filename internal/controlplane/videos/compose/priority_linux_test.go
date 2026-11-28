package compose

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Check the OS scheduling priority from inside the process that stands in for ffmpeg.
func TestFFmpegStartsNiced(t *testing.T) {
	t.Parallel()

	pid := strconv.Itoa(os.Getpid())
	//nolint:gosec // ps reads the priority of the test's own process.
	parent, err := exec.CommandContext(t.Context(), "/bin/ps", "-o", "ni=", "-p", pid).Output()
	require.NoError(t, err)
	inherited, err := strconv.Atoi(strings.TrimSpace(string(parent)))
	require.NoError(t, err)

	tools := fakeFFmpeg(t, "exec /bin/ps -o ni= -p $$\n")
	out, err := (runner{tools: tools}).run(t.Context(), tools.FFmpeg)
	require.NoError(t, err)
	got, err := strconv.Atoi(strings.TrimSpace(string(out)))
	require.NoError(t, err)
	assert.Equal(t, min(inherited+10, 19), got)
}
