package takes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// A node's upload of a take (issue #817, row 11): each finished segment,
// then take.json, one file a request with its length and SHA-256 said
// first. A file is streamed to a temporary file and renamed only when its
// length and sum match, then answered; the same file sent again with the
// same sum is answered as received, another sum is refused. The take must be
// the node's own, as a files answer must match a request of that node
// (nodeapi/files.go:88-103), and a receipt that a retry gets again is
// docs/conventions.md's idempotent receipt.

const (
	// minFreeBytes is the free disk the control plane keeps beside a file it receives, autodemo's 400 MB
	// (src/live.js:97-99).
	minFreeBytes = 400_000_000
	// MaxSegmentBytes bounds a segment's file: 20 minutes at scrcpy's 8 Mbit/s are about 1.2 GB.
	MaxSegmentBytes = 8 << 30
	// maxTakeFileBytes bounds an uploaded take.json.
	maxTakeFileBytes = 4 << 20
	// uploaded is the node's take.json as it sent it, kept so that a retry of it is answered again.
	uploaded = "node-take.json"
)

// Errors of an upload.
var (
	// ErrNotYours reports a take another node films.
	ErrNotYours = errors.New("the take is not this node's")
	// ErrConflict reports a file that differs from the one received under its name, or a take.json whose
	// segments were not all received.
	ErrConflict = errors.New("the file conflicts with what was received")
	// ErrBadFile reports a file that is not one of a take, or not what its length and sum said.
	ErrBadFile = errors.New("the file is not one of the take's, or not as said")
	// ErrDiskLow reports a control plane with too little free disk to take the file.
	ErrDiskLow = errors.New("the control plane has too little free disk for the file")
)

// segmentNames are the names of a take's segments, as the node names them.
var segmentNames = regexp.MustCompile(`^seg-[0-9]{3}\.mp4$`)

// IsSegment says whether a name is a segment's.
func IsSegment(name string) bool { return segmentNames.MatchString(name) }

// Receive takes one file of a take from its node: a segment, or its
// take.json, which makes the take available once every segment it names was
// received whole.
func (s *Store) Receive(nodeID, id, name string, size int64, sum string, body io.Reader) error {
	dir := s.folder(id)

	switch {
	case dir == "":
		return fmt.Errorf("%w: %s", phone.ErrNoTake, id)
	case name != takeFile && !IsSegment(name):
		return fmt.Errorf("%w: %s", ErrBadFile, name)
	case size <= 0 || size > MaxSegmentBytes || name == takeFile && size > maxTakeFileBytes:
		return fmt.Errorf("%w: %d bytes", ErrBadFile, size)
	case len(sum) != sha256.Size*2:
		return fmt.Errorf("%w: no SHA-256", ErrBadFile)
	}

	s.mu.Lock()
	t, err := s.read(id)
	s.mu.Unlock()

	if err != nil {
		return err
	}

	if t.Node != nodeID {
		return fmt.Errorf("%w: %s", ErrNotYours, id)
	}

	target := filepath.Join(dir, name)
	if name == takeFile {
		target = filepath.Join(dir, uploaded)
	}

	if have, err := sumOf(target); err == nil {
		if have != sum {
			return fmt.Errorf("%w: %s was received with another sum", ErrConflict, name)
		}

		if name == takeFile {
			return s.complete(id)
		}

		return nil
	}

	if free, err := s.freeBytes(dir); err == nil && free < uint64(minFreeBytes)+uint64(size) {
		return fmt.Errorf("%w: %d MB free", ErrDiskLow, free/1_000_000)
	}

	if err := s.receive(dir, target, size, sum, body); err != nil {
		return err
	}

	if name == takeFile {
		return s.complete(id)
	}

	return nil
}

// deleted says whether a take's folder holds its tombstone.
func (s *Store) deleted(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, tombstone))

	return err == nil
}

// freeBytes is the free disk, by the test's function when it has one.
func (s *Store) freeBytes(dir string) (uint64, error) {
	if s.FreeBytes != nil {
		return s.FreeBytes(dir)
	}

	return platform.FreeBytes(dir)
}

// receive streams a body to a temporary file of the take's folder and
// renames it to target once its length and sum match. The rename happens
// under mu and only while the take is not deleted, so a delete that races an
// upload leaves nothing behind.
func (s *Store) receive(dir, target string, size int64, sum string, body io.Reader) error {
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".*.part")
	if err != nil {
		return fmt.Errorf("receiving %s: %w", filepath.Base(target), err)
	}

	defer func() { _ = os.Remove(tmp.Name()) }()

	hash := sha256.New()

	n, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(body, size+1))
	if err == nil {
		err = tmp.Sync()
	}

	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}

	switch {
	case err != nil:
		return fmt.Errorf("receiving %s: %w", filepath.Base(target), err)
	case n != size:
		return fmt.Errorf("%w: %s is %d bytes, not %d", ErrBadFile, filepath.Base(target), n, size)
	case hex.EncodeToString(hash.Sum(nil)) != sum:
		return fmt.Errorf("%w: %s does not have the SHA-256 said", ErrBadFile, filepath.Base(target))
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.deleted(dir) {
		return fmt.Errorf("%w: %s", ErrDeleted, filepath.Base(dir))
	}

	return os.Rename(tmp.Name(), target)
}

// complete reads the take.json its node sent and makes the take what it
// says: available when every segment it names was received with its sum,
// failed as the node says when it failed.
func (s *Store) complete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, err := s.read(id)
	if err != nil {
		return err
	}

	dir := s.folder(id)

	raw, err := os.ReadFile(filepath.Join(dir, uploaded)) //nolint:gosec // A take's folder, by a checked id.
	if err != nil {
		return err
	}

	var said devicetool.Take
	if err := json.Unmarshal(raw, &said); err != nil || said.ID != id {
		_ = os.Remove(filepath.Join(dir, uploaded))

		return fmt.Errorf("%w: take.json is not take %s's", ErrBadFile, id)
	}

	for _, seg := range said.Segments {
		if !IsSegment(seg.File) {
			_ = os.Remove(filepath.Join(dir, uploaded))

			return fmt.Errorf("%w: take.json names %q", ErrBadFile, seg.File)
		}

		have, err := sumOf(filepath.Join(dir, seg.File))
		if err != nil || have != seg.SHA256 {
			_ = os.Remove(filepath.Join(dir, uploaded))

			return fmt.Errorf("%w: segment %s was not received as take.json says", ErrConflict, seg.File)
		}
	}

	if t.State == devicetool.TakeAvailable {
		return nil
	}

	t = merge(t, said)
	t.Segments, t.Steps, t.Pauses = orEmpty(said.Segments), orEmpty(said.Steps), orEmpty(said.Pauses)

	switch {
	case said.State == devicetool.TakeFailed:
		t.State = devicetool.TakeFailed
	case len(said.Segments) == 0:
		t.State, t.Reason = devicetool.TakeFailed, "no frame from the phone"
	default:
		t.State = devicetool.TakeAvailable
	}

	return write(dir, t)
}

// sumOf is a file's SHA-256 in hex.
func sumOf(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // A take's folder, by a checked id and name.
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck // Read only.

	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Delete removes a take's files and leaves its tombstone: a late upload of
// it is refused, and its node told to drop its copy. A take deleted already
// is deleted.
func (s *Store) Delete(id string) error {
	dir := s.folder(id)
	if dir == "" {
		return fmt.Errorf("%w: %s", phone.ErrNoTake, id)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.deleted(dir) {
		return nil
	}

	if _, err := s.read(id); err != nil {
		return err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("deleting take %s: %w", id, err)
		}
	}

	return os.WriteFile(filepath.Join(dir, tombstone), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// Deleted says whether the owner deleted a take.
func (s *Store) Deleted(id string) bool {
	dir := s.folder(id)

	return dir != "" && s.deleted(dir)
}

// File is a file of an available take, for the owner: a segment, or its take.json.
func (s *Store) File(id, name string) (string, error) {
	t, err := s.Get(id)
	if err != nil {
		return "", err
	}

	if t.State != devicetool.TakeAvailable {
		return "", fmt.Errorf("%w: take %s is %s", phone.ErrNoTake, id, t.State)
	}

	if name != takeFile && !IsSegment(name) {
		return "", fmt.Errorf("%w: %s", fs.ErrNotExist, name)
	}

	path := filepath.Join(s.folder(id), name)
	if _, err := os.Stat(path); err != nil {
		return "", err
	}

	return path, nil
}
