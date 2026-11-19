package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/takes"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// A take a video is being made of is not deleted (issue #818, row 2): the owner is told to wait, and its files
// stay.
func TestATakeWithAVideoBeingMadeIsNotDeleted(t *testing.T) {
	t.Parallel()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	store, err := takes.Open(filepath.Join(t.TempDir(), "takes"))
	require.NoError(t, err)

	const id = "tk_00000000000000aa"

	require.NoError(t, store.Create(devicetool.Take{ID: id, Agent: "d-1", Node: "nod_1", StartedAt: time.Now(),
		State: devicetool.TakeRecording}))

	// Its node uploads its segment, then its take.json: it is available.
	seg := []byte("frames")
	segSum := sha256.Sum256(seg)
	require.NoError(t, store.Receive("nod_1", id, "seg-001.mp4", 6, hex.EncodeToString(segSum[:]), bytes.NewReader(seg)))

	raw, err := json.Marshal(devicetool.Take{ID: id, State: devicetool.TakeUploading, DurationMs: 1000,
		Segments: []devicetool.TakeSegment{{File: "seg-001.mp4", LengthMs: 1000, Bytes: 6,
			SHA256: hex.EncodeToString(segSum[:])}},
		Steps: []devicetool.TakeStep{{AtMs: 0, Text: "Mở"}}})
	require.NoError(t, err)

	sum := sha256.Sum256(raw)
	require.NoError(t, store.Receive("nod_1", id, "take.json", int64(len(raw)), hex.EncodeToString(sum[:]),
		bytes.NewReader(raw)))

	vs, err := videos.New(videos.Config{DB: db, Dir: filepath.Join(t.TempDir(), "videos"), Takes: store})
	require.NoError(t, err)

	_, _, err = vs.Make(t.Context(), videos.MakeRequest{Take: id, By: videos.Owner})
	require.NoError(t, err)

	err = ownerTakes{Store: store, videos: vs}.Delete(context.Background(), id)
	require.ErrorIs(t, err, orchestration.ErrConflict)

	got, err := store.Get(id)
	require.NoError(t, err)
	assert.Equal(t, devicetool.TakeAvailable, got.State, "the take and its files stay")
}
