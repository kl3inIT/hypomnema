package clientcli

import (
	"archive/zip"
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

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/takes"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

type cliTakes struct{ *takes.Store }

func (c cliTakes) Delete(_ context.Context, id string) error { return c.Store.Delete(id) }

// Scenario: the owner lists a take an agent filmed, reads its steps,
// downloads it as a zip of its segment and take.json, and deletes it, with
// the CLI (issue #817).
func TestTheOwnerReadsAndDeletesTakesWithTheCLI(t *testing.T) {
	t.Parallel()

	store, err := takes.Open(filepath.Join(t.TempDir(), "takes"))
	require.NoError(t, err)

	const id = "tk_0123456789abcdef"

	require.NoError(t, store.Create(devicetool.Take{ID: id, Title: "Sign in to the shop", Agent: "d-1",
		AgentName: "dispatch 1", Node: "nod_1", StartedAt: time.Now(), State: devicetool.TakeRecording}))

	seg := []byte("the segment")
	segSum := sha256.Sum256(seg)
	require.NoError(t, store.Receive("nod_1", id, "seg-001.mp4", int64(len(seg)), hex.EncodeToString(segSum[:]),
		bytes.NewReader(seg)))

	raw, err := json.Marshal(devicetool.Take{ID: id, State: devicetool.TakeUploading, DurationMs: 65_000,
		Segments: []devicetool.TakeSegment{{File: "seg-001.mp4", LengthMs: 65_000, Width: 720, Height: 1280,
			Bytes: int64(len(seg)), SHA256: hex.EncodeToString(segSum[:])}},
		Steps: []devicetool.TakeStep{{AtMs: 61_000, Text: "Tap Sign in", Chapter: "Sign in"}}})
	require.NoError(t, err)

	rawSum := sha256.Sum256(raw)
	require.NoError(t, store.Receive("nod_1", id, "take.json", int64(len(raw)), hex.EncodeToString(rawSum[:]),
		bytes.NewReader(raw)))

	w := newWorldWith(t, nil, func(s *clientapi.Server) { s.WithTakes(cliTakes{store}) })
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "takes", "list")
	require.NoError(t, err)
	assert.Contains(t, out, id+"  available  1:05  1 steps")
	assert.Contains(t, out, "dispatch 1  Sign in to the shop")

	out, err = w.cmd(t, "takes", "get", id)
	require.NoError(t, err)
	assert.Contains(t, out, "segment seg-001.mp4  at 0:00  1:05  720x1280")
	assert.Contains(t, out, "1:01  [Sign in] Tap Sign in")

	zipFile := filepath.Join(t.TempDir(), "take.zip")
	out, err = w.cmd(t, "takes", "download", id, "--out", zipFile)
	require.NoError(t, err)
	assert.Contains(t, out, "saved take "+id)

	zr, err := zip.OpenReader(zipFile)
	require.NoError(t, err)

	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}

	_ = zr.Close()

	assert.Equal(t, []string{"seg-001.mp4", "take.json"}, names)

	_, err = w.cmd(t, "takes", "download", id, "--out", zipFile)
	require.ErrorContains(t, err, "already there", "a file is never replaced")

	out, err = w.cmd(t, "takes", "delete", id)
	require.NoError(t, err)
	assert.Contains(t, out, "deleted take "+id)

	out, err = w.cmd(t, "takes", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "no takes")
}
