package takes

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

const (
	takeID = "tk_0123456789abcdef"
	node   = "nod_phone"
)

func sum(b []byte) string {
	s := sha256.Sum256(b)

	return hex.EncodeToString(s[:])
}

func openStore(t *testing.T) *Store {
	t.Helper()

	s, err := Open(filepath.Join(t.TempDir(), "takes"))
	require.NoError(t, err)

	s.FreeBytes = func(string) (uint64, error) { return 1 << 40, nil }

	require.NoError(t, s.Create(devicetool.Take{
		ID: takeID, Title: "Sign in", Agent: "d-1", AgentName: "dispatch 1", Node: node,
		StartedAt: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC), State: devicetool.TakeRecording,
	}))

	return s
}

// nodeTake is take.json as the node sends it, naming the segments given.
func nodeTake(t *testing.T, segments map[string][]byte, names ...string) []byte {
	t.Helper()

	take := devicetool.Take{
		ID: takeID, Title: "Sign in", Agent: "d-1", State: devicetool.TakeUploading, EndedBy: devicetool.EndedStop,
		DurationMs: 3000, Steps: []devicetool.TakeStep{{AtMs: 500, Text: "Tap Sign in"}},
		Pauses: []devicetool.TakePause{},
	}

	var at int64

	for _, name := range names {
		take.Segments = append(take.Segments, devicetool.TakeSegment{
			File: name, AtMs: at, LengthMs: 1500, Width: 720, Height: 1280, Bytes: int64(len(segments[name])),
			SHA256: sum(segments[name]),
		})
		at += 1500
	}

	raw, err := json.Marshal(take)
	require.NoError(t, err)

	return raw
}

func send(s *Store, from, name string, data []byte) error {
	return s.Receive(from, takeID, name, int64(len(data)), sum(data), bytes.NewReader(data))
}

// Scenario: the node uploads its segments then take.json, and the take is
// available, with the node's timeline and steps and the control plane's own
// title, agent and node. Files are private to the control plane's user.
func TestATakeIsAvailableOnceEveryFileArrived(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	segs := map[string][]byte{"seg-001.mp4": []byte("first segment"), "seg-002.mp4": []byte("second segment")}

	require.NoError(t, send(s, node, "seg-001.mp4", segs["seg-001.mp4"]))
	require.NoError(t, send(s, node, "seg-002.mp4", segs["seg-002.mp4"]))
	require.NoError(t, send(s, node, "take.json", nodeTake(t, segs, "seg-001.mp4", "seg-002.mp4")))

	take, err := s.Get(takeID)
	require.NoError(t, err)
	assert.Equal(t, devicetool.TakeAvailable, take.State)
	assert.Equal(t, "dispatch 1", take.AgentName)
	assert.Equal(t, node, take.Node)
	assert.Equal(t, int64(3000), take.DurationMs)
	require.Len(t, take.Segments, 2)
	assert.Equal(t, "Tap Sign in", take.Steps[0].Text)

	path, err := s.File(takeID, "seg-002.mp4")
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	dir, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dir.Mode().Perm())
}

// Scenario: the node's answer was lost and it sends a file again: the same
// sum is received again; another sum under that name is a conflict. A body
// cut short, or not the sum said, is refused and leaves nothing.
func TestAnUploadIsRetriedSafely(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seg := []byte("segment bytes")

	require.NoError(t, send(s, node, "seg-001.mp4", seg))
	require.NoError(t, send(s, node, "seg-001.mp4", seg), "a retry with the same sum is received again")
	require.ErrorIs(t, send(s, node, "seg-001.mp4", []byte("other bytes")), ErrConflict)

	err := s.Receive(node, takeID, "seg-002.mp4", 100, sum(seg), bytes.NewReader(seg))
	require.ErrorIs(t, err, ErrBadFile, "shorter than said")

	err = s.Receive(node, takeID, "seg-002.mp4", int64(len(seg)), sum([]byte("x")), bytes.NewReader(seg))
	require.ErrorIs(t, err, ErrBadFile, "not the sum said")

	entries, err := os.ReadDir(filepath.Join(s.dir, takeID))
	require.NoError(t, err)

	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}

	assert.ElementsMatch(t, []string{"take.json", "seg-001.mp4"}, names, "no part file is left")

	segs := map[string][]byte{"seg-001.mp4": seg}
	json := nodeTake(t, segs, "seg-001.mp4")
	require.NoError(t, send(s, node, "take.json", json))
	require.NoError(t, send(s, node, "take.json", json), "take.json sent again is taken again")
}

// Scenario: take.json names a segment that did not arrive: it is a conflict
// and the take stays uploading.
func TestATakeJSONWithAMissingSegmentIsRefused(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	segs := map[string][]byte{"seg-001.mp4": []byte("one")}

	require.ErrorIs(t, send(s, node, "take.json", nodeTake(t, segs, "seg-001.mp4")), ErrConflict)

	take, err := s.Get(takeID)
	require.NoError(t, err)
	assert.Equal(t, devicetool.TakeRecording, take.State)
}

// Another node's token cannot upload the take, a name that is not a take's
// file is refused, and a take id that is not one names no folder.
func TestOnlyItsNodeUploadsATake(t *testing.T) {
	t.Parallel()

	s := openStore(t)

	require.ErrorIs(t, send(s, "nod_other", "seg-001.mp4", []byte("x")), ErrNotYours)
	require.ErrorIs(t, send(s, node, "../take.json", []byte("x")), ErrBadFile)
	require.ErrorIs(t, send(s, node, "seg-1.mp4", []byte("x")), ErrBadFile)

	err := s.Receive(node, "../../etc", "seg-001.mp4", 1, sum([]byte("x")), bytes.NewReader([]byte("x")))
	require.ErrorIs(t, err, phone.ErrNoTake)

	_, err = s.File(takeID, "../take.json")
	require.Error(t, err)
}

// The control plane keeps 400 MB free beside what it receives.
func TestAnUploadNeedsFreeDisk(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	s.FreeBytes = func(string) (uint64, error) { return 300 << 20, nil }

	require.ErrorIs(t, send(s, node, "seg-001.mp4", []byte("x")), ErrDiskLow)
}

// slowReader gives its bytes once released.
type slowReader struct {
	data    []byte
	release chan struct{}
	started chan struct{}
	once    sync.Once
}

func (r *slowReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release

	if len(r.data) == 0 {
		return 0, io.EOF
	}

	n := copy(p, r.data)
	r.data = r.data[n:]

	return n, nil
}

// Scenario: the owner deletes a take while a segment of it is uploaded. The
// upload is refused as deleted and leaves no file; the take is a tombstone,
// and a later upload is refused too.
func TestADeleteRacingAnUploadLeavesNothing(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seg := []byte("segment bytes")
	body := &slowReader{data: seg, release: make(chan struct{}), started: make(chan struct{})}

	done := make(chan error, 1)

	go func() { done <- s.Receive(node, takeID, "seg-001.mp4", int64(len(seg)), sum(seg), body) }()

	<-body.started
	require.NoError(t, s.Delete(takeID))
	close(body.release)

	require.ErrorIs(t, <-done, ErrDeleted)

	entries, err := os.ReadDir(filepath.Join(s.dir, takeID))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, tombstone, entries[0].Name())

	assert.True(t, s.Deleted(takeID))

	_, err = s.Get(takeID)
	require.ErrorIs(t, err, ErrDeleted)
	assert.Empty(t, s.List())

	require.ErrorIs(t, send(s, node, "seg-001.mp4", seg), ErrDeleted)
	require.NoError(t, s.Delete(takeID), "deleted twice is deleted")
}

// Scenario (issue #851): a take ended is never running again. The node's
// answer to a call made before the stop, kept after it, leaves the take
// finalizing; paused and resumed while running, it follows the node.
func TestAnEndedTakeIsNeverRunningAgain(t *testing.T) {
	t.Parallel()

	s := openStore(t)

	take, err := s.Update(devicetool.Take{ID: takeID, State: devicetool.TakePaused})
	require.NoError(t, err)
	assert.Equal(t, devicetool.TakePaused, take.State)

	_, err = s.Update(devicetool.Take{ID: takeID, State: devicetool.TakeFinalizing, EndedBy: devicetool.EndedStop})
	require.NoError(t, err)

	take, err = s.Update(devicetool.Take{
		ID: takeID, State: devicetool.TakeRecording, Steps: []devicetool.TakeStep{{AtMs: 1, Text: "late"}},
	})
	require.NoError(t, err)
	assert.Equal(t, devicetool.TakeFinalizing, take.State)
	assert.Equal(t, devicetool.EndedStop, take.EndedBy)

	take, err = s.Update(devicetool.Take{ID: takeID, State: devicetool.TakeUploading})
	require.NoError(t, err)
	assert.Equal(t, devicetool.TakeUploading, take.State)
}
