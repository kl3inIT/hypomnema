package takes

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// A browser take (issue #1189, row 3) is filmed on the control plane itself:
// its frames are written in a folder of the take's own while it is filmed, and
// its one segment is encoded from them beside it and put in place here, where
// a phone's arrives by upload (upload.go). The frames go once it is in place,
// and with the take when the owner deletes it.

// framesDir is the folder of a browser take's frames, in its take's folder.
const framesDir = "frames"

// Frames is the folder a browser take's frames are written in while it is
// filmed, made the first time it is asked for.
func (s *Store) Frames(id string) (string, error) {
	dir := s.folder(id)
	if dir == "" {
		return "", fmt.Errorf("%w: %s", phone.ErrNoTake, id)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.deleted(dir) {
		return "", fmt.Errorf("%w: %s", ErrDeleted, id)
	}

	frames := filepath.Join(dir, framesDir)
	if err := os.MkdirAll(frames, 0o700); err != nil {
		return "", fmt.Errorf("making the take's frames folder: %w", err)
	}

	return frames, nil
}

// Finish puts a browser take's segment, a file made in its frames folder, in
// place as seg's file, and makes the take available with it; the frames go.
// A take deleted meanwhile keeps nothing.
func (s *Store) Finish(id, file string, seg devicetool.TakeSegment) error {
	dir := s.folder(id)
	if dir == "" || !IsSegment(seg.File) {
		return fmt.Errorf("%w: %s %s", ErrBadFile, id, seg.File)
	}

	have, err := sumOf(file)
	if err != nil || have != seg.SHA256 {
		return fmt.Errorf("%w: %s is not the segment said", ErrBadFile, filepath.Base(file))
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	t, err := s.read(id)
	if err != nil {
		return err
	}

	if err := os.Rename(file, filepath.Join(dir, seg.File)); err != nil {
		return fmt.Errorf("putting the segment in place: %w", err)
	}

	t.Segments, t.State = []devicetool.TakeSegment{seg}, devicetool.TakeAvailable
	if err := write(dir, t); err != nil {
		return err
	}

	return os.RemoveAll(filepath.Join(dir, framesDir))
}

// Fail marks a browser take failed, for why, and drops its frames.
func (s *Store) Fail(id, endedBy, why string) (devicetool.Take, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, err := s.read(id)
	if err != nil {
		return devicetool.Take{}, err
	}

	if t.State == devicetool.TakeAvailable {
		return t, nil
	}

	t.State, t.Reason = devicetool.TakeFailed, why
	if t.EndedBy == "" {
		t.EndedBy = endedBy
	}

	dir := s.folder(id)
	if err := write(dir, t); err != nil {
		return devicetool.Take{}, err
	}

	return t, os.RemoveAll(filepath.Join(dir, framesDir))
}
