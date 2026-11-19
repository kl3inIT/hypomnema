package takes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
)

// Scenario: the Studio shows a take's first frame (issue #818, row 12).
// None while the take is filmed; once it is available the frame is made
// from its first segment once and kept, private, in the take's folder; a
// failed make leaves nothing; the take's delete takes it away.
func TestATakesFirstFrameIsMadeOnceAndGoesWithIt(t *testing.T) {
	t.Parallel()

	s := openStore(t)

	var made atomic.Int32

	frame := func(_ context.Context, video, out string) error {
		made.Add(1)
		assert.Equal(t, "seg-001.mp4", filepath.Base(video))

		return os.WriteFile(out, []byte("jpeg"), 0o600)
	}

	_, err := s.Thumbnail(t.Context(), takeID, frame)
	require.ErrorIs(t, err, phone.ErrNoTake, "none while it is filmed")
	assert.Zero(t, made.Load())

	segs := map[string][]byte{"seg-001.mp4": []byte("first segment"), "seg-002.mp4": []byte("second segment")}
	require.NoError(t, send(s, node, "seg-001.mp4", segs["seg-001.mp4"]))
	require.NoError(t, send(s, node, "seg-002.mp4", segs["seg-002.mp4"]))
	require.NoError(t, send(s, node, "take.json", nodeTake(t, segs, "seg-001.mp4", "seg-002.mp4")))

	_, err = s.Thumbnail(t.Context(), takeID, func(context.Context, string, string) error {
		return errors.New("ffmpeg failed")
	})
	require.Error(t, err)

	entries, err := os.ReadDir(filepath.Join(s.dir, takeID))
	require.NoError(t, err)

	for _, e := range entries {
		assert.NotContains(t, e.Name(), "thumbnail", "a failed make leaves nothing")
	}

	path, err := s.Thumbnail(t.Context(), takeID, frame)
	require.NoError(t, err)

	again, err := s.Thumbnail(t.Context(), takeID, frame)
	require.NoError(t, err)
	assert.Equal(t, path, again)
	assert.Equal(t, int32(1), made.Load(), "made once, then kept")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	require.NoError(t, s.Delete(takeID))
	assert.NoFileExists(t, path)

	_, err = s.Thumbnail(t.Context(), takeID, frame)
	require.ErrorIs(t, err, ErrDeleted)
}
