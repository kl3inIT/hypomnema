package device

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The web client's take player is tested on segments this writer made
// (web/tests/fixtures/take): two segments, the first 3 s with a key frame of
// another shade each second, the second 2 s of frames and an 8 s still tail.
// HYPOMNEMA_TAKE_FIXTURES names the folder to write them to; unset, nothing
// is written.
func TestWriteTheWebPlayersSegments(t *testing.T) {
	t.Parallel()

	dir := os.Getenv("HYPOMNEMA_TAKE_FIXTURES")
	if dir == "" {
		t.Skip("HYPOMNEMA_TAKE_FIXTURES is not set")
	}

	write := func(name string, shades []byte, length time.Duration) {
		video := &testVideo{width: 64, height: 112}
		w, err := createSegment(filepath.Join(dir, name), name, video.config())
		require.NoError(t, err)

		for i, shade := range shades {
			at := time.Duration(i) * time.Second
			key := at.Microseconds()
			delta := (at + 500*time.Millisecond).Microseconds()
			require.NoError(t, w.write(packet{Key: true, PTS: uint64(key), Data: video.key(shade)})) //nolint:gosec // >= 0
			require.NoError(t, w.write(packet{PTS: uint64(delta), Data: video.delta()}))             //nolint:gosec // >= 0
		}

		info, kept, err := w.close(length)
		require.NoError(t, err)
		require.True(t, kept)
		t.Logf("%s: %s, %d bytes, %s", name, info.Length, info.Bytes, info.SHA256)
	}

	write("seg-001.mp4", []byte{40, 110, 180}, 3*time.Second)
	write("seg-002.mp4", []byte{220, 90}, 10*time.Second)
}
