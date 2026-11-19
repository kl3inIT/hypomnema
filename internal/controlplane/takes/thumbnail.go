package takes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// thumbFile is a take's first frame, kept in its folder (issue #818, row 12).
const thumbFile = "thumbnail.jpg"

// FirstFrame writes a picture of a video's first frame to out (the videos'
// ffmpeg, videos.Service.FirstFrame).
type FirstFrame func(ctx context.Context, video, out string) error

// Thumbnail is the path of an available take's first frame, for its card in
// the Studio: made from its first segment by first the first time it is
// asked for, and kept in the take's folder, so it goes when the take is
// deleted. A take still filmed, or failed, has none.
func (s *Store) Thumbnail(ctx context.Context, id string, first FirstFrame) (string, error) {
	t, err := s.Get(id)
	if err != nil {
		return "", err
	}

	if t.State != devicetool.TakeAvailable || len(t.Segments) == 0 || !IsSegment(t.Segments[0].File) {
		return "", fmt.Errorf("%w: take %s has no first frame while it is %s", phone.ErrNoTake, id, t.State)
	}

	dir := s.folder(id)
	path := filepath.Join(dir, thumbFile)

	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return path, nil
	}

	// Made beside it and renamed into place, as an upload is (upload.go), so a request that reads it meanwhile
	// never reads half a picture; two made at once each rename a whole one.
	tmp, err := os.CreateTemp(dir, ".thumbnail-*.jpg")
	if err != nil {
		return "", err
	}

	_ = tmp.Close()

	defer func() { _ = os.Remove(tmp.Name()) }()

	if err := first(ctx, filepath.Join(dir, t.Segments[0].File), tmp.Name()); err != nil {
		return "", fmt.Errorf("taking the first frame of take %s: %w", id, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// A delete while it was made has emptied the folder: nothing is put back in it.
	if s.deleted(dir) {
		return "", fmt.Errorf("%w: %s", ErrDeleted, id)
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}

	return path, nil
}
