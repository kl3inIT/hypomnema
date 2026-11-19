package clientapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
)

// The videos made of takes (issue #818, rows 10 and 11), the owner's only, as
// takes are: listed and read in any state, made (the take's first make
// creates its video, a later one adds a revision), approved, asked for
// changes, deleted, and a made revision's MP4 played or downloaded with
// ranges and its first frame shown. Writes that a client may repeat take an
// Idempotency-Key. An agent reads only its own videos' metadata, through its
// tools (agentapi).

// Videos keeps the videos (videos.Service).
type Videos interface {
	List(ctx context.Context) ([]videos.Video, error)
	Get(ctx context.Context, id string) (videos.Video, error)
	Make(ctx context.Context, req videos.MakeRequest) (videos.Video, videos.Revision, error)
	Approve(ctx context.Context, id string, rev int) (videos.Video, error)
	RequestChanges(ctx context.Context, id string, rev int, req videos.ChangeRequest) (videos.Video, error)
	Delete(ctx context.Context, id string) error
	File(ctx context.Context, id string, rev int) (string, error)
	Thumbnail(ctx context.Context, id string, rev int) (string, error)
}

// WithVideos sets the videos the owner reads. Without them the routes answer
// that there are none.
func (s *Server) WithVideos(v Videos) *Server {
	s.videos = v

	return s
}

var errNoVideos = fmt.Errorf("%w: this control plane makes no video", orchestration.ErrUnavailable)

// videoError is a videos error as the API's.
func videoError(err error) error {
	switch {
	case errors.Is(err, videos.ErrNotFound):
		return fmt.Errorf("%w: %w", orchestration.ErrNotFound, err)
	case errors.Is(err, videos.ErrConflict):
		return fmt.Errorf("%w: %w", orchestration.ErrConflict, err)
	case errors.Is(err, videos.ErrInvalid):
		return fmt.Errorf("%w: %w", orchestration.ErrInvalid, err)
	case errors.Is(err, videos.ErrIdempotency):
		return fmt.Errorf("%w: %w", orchestration.ErrIdempotencyConflict, err)
	}

	return err
}

func videoOf(v videos.Video) openapi.Video {
	out := openapi.Video{
		Id: v.ID, Take: v.Take, Title: v.Title, MadeBy: v.MadeBy, MadeByName: v.MadeByName, CreatedAt: v.CreatedAt,
		Revisions: make([]openapi.VideoRevision, 0, len(v.Revisions)),
	}

	if r, ok := v.Approved(); ok {
		out.Approved = ptr(r.Rev)
	}

	for _, r := range v.Revisions {
		rev := openapi.VideoRevision{
			Rev: r.Rev, State: openapi.VideoRevisionState(r.State), Lines: r.Lines, Captions: r.Captions,
			Aspect: openapi.VideoRevisionAspect(r.Aspect), LengthMs: r.LengthMs, Warnings: r.Warnings,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			Parent: nonZero(r.Parent), Voice: nonZero(r.Voice), Provider: nonZero(r.Provider),
			Model: nonZero(r.Model), UsedVoice: nonZero(r.UsedVoice), FailedStage: nonZero(r.FailedStage),
			FailedCause: nonZero(r.FailedCause), Note: nonZero(r.Note),
		}
		out.Revisions = append(out.Revisions, rev)
	}

	return out
}

// nonZero is a pointer to a value that is not its zero, nil for the zero.
func nonZero[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}

	return &v
}

// ListVideos lists the videos, the newest first.
func (s *Server) ListVideos(ctx context.Context, _ openapi.ListVideosRequestObject,
) (openapi.ListVideosResponseObject, error) {
	if s.videos == nil {
		return nil, errNoVideos
	}

	list, err := s.videos.List(ctx)
	if err != nil {
		return nil, videoError(err)
	}

	out := openapi.VideoList{Data: make([]openapi.Video, 0, len(list))}
	for _, v := range list {
		out.Data = append(out.Data, videoOf(v))
	}

	return openapi.ListVideos200JSONResponse(out), nil
}

// MakeVideo queues a revision of a take's video.
func (s *Server) MakeVideo(ctx context.Context, req openapi.MakeVideoRequestObject,
) (openapi.MakeVideoResponseObject, error) {
	if s.videos == nil {
		return nil, errNoVideos
	}

	b := req.Body
	in := videos.MakeRequest{
		Take: b.Take, Title: val(b.Title), Captions: b.Captions, Voice: val(b.Voice), By: videos.Owner,
		ByName: videos.Owner, RequestID: val(req.Params.IdempotencyKey),
	}

	if b.Aspect != nil {
		in.Aspect = string(*b.Aspect)
	}

	v, _, err := s.videos.Make(ctx, in)
	if err != nil {
		return nil, videoError(err)
	}

	return openapi.MakeVideo201JSONResponse(videoOf(v)), nil
}

// GetVideo reads a video.
func (s *Server) GetVideo(ctx context.Context, req openapi.GetVideoRequestObject,
) (openapi.GetVideoResponseObject, error) {
	if s.videos == nil {
		return nil, errNoVideos
	}

	v, err := s.videos.Get(ctx, req.VideoId)
	if err != nil {
		return nil, videoError(err)
	}

	return openapi.GetVideo200JSONResponse(videoOf(v)), nil
}

// DeleteVideo deletes a video and its files.
func (s *Server) DeleteVideo(ctx context.Context, req openapi.DeleteVideoRequestObject,
) (openapi.DeleteVideoResponseObject, error) {
	if s.videos == nil {
		return nil, errNoVideos
	}

	if err := s.videos.Delete(ctx, req.VideoId); err != nil {
		return nil, videoError(err)
	}

	return openapi.DeleteVideo204Response{}, nil
}

// ApproveVideoRevision approves a revision.
func (s *Server) ApproveVideoRevision(ctx context.Context, req openapi.ApproveVideoRevisionRequestObject,
) (openapi.ApproveVideoRevisionResponseObject, error) {
	if s.videos == nil {
		return nil, errNoVideos
	}

	v, err := s.videos.Approve(ctx, req.VideoId, req.Rev)
	if err != nil {
		return nil, videoError(err)
	}

	return openapi.ApproveVideoRevision200JSONResponse(videoOf(v)), nil
}

// RequestVideoChanges asks for changes to a revision.
func (s *Server) RequestVideoChanges(ctx context.Context, req openapi.RequestVideoChangesRequestObject,
) (openapi.RequestVideoChangesResponseObject, error) {
	if s.videos == nil {
		return nil, errNoVideos
	}

	in := videos.ChangeRequest{Note: val(req.Body.Note), By: videos.Owner, RequestID: val(req.Params.IdempotencyKey)}
	if req.Body.Lines != nil {
		in.Lines = append([]string{}, *req.Body.Lines...)
	}

	v, err := s.videos.RequestChanges(ctx, req.VideoId, req.Rev, in)
	if err != nil {
		return nil, videoError(err)
	}

	return openapi.RequestVideoChanges200JSONResponse(videoOf(v)), nil
}

// ReadVideoFile serves a made revision's MP4, with ranges.
func (s *Server) ReadVideoFile(ctx context.Context, req openapi.ReadVideoFileRequestObject,
) (openapi.ReadVideoFileResponseObject, error) {
	if s.videos == nil {
		return nil, errNoVideos
	}

	path, err := s.videos.File(ctx, req.VideoId, req.Rev)
	if err != nil {
		return nil, videoError(err)
	}

	return videoFile{path: path, rng: val(req.Params.Range), name: req.VideoId + "-" + fmt.Sprint(req.Rev) + ".mp4"},
		nil
}

// videoFile is a revision's MP4 served with the range asked for.
type videoFile struct {
	path, rng, name string
}

func (f videoFile) VisitReadVideoFileResponse(w http.ResponseWriter) error {
	return serveFile(w, f.path, f.rng, "video/mp4", f.name)
}

// ReadVideoThumbnail serves a made revision's first frame.
func (s *Server) ReadVideoThumbnail(ctx context.Context, req openapi.ReadVideoThumbnailRequestObject,
) (openapi.ReadVideoThumbnailResponseObject, error) {
	if s.videos == nil {
		return nil, errNoVideos
	}

	path, err := s.videos.Thumbnail(ctx, req.VideoId, req.Rev)
	if err != nil {
		return nil, videoError(err)
	}

	return thumbnail{path: path}, nil
}

type thumbnail struct{ path string }

func (t thumbnail) VisitReadVideoThumbnailResponse(w http.ResponseWriter) error {
	return serveFile(w, t.path, "", "image/jpeg", "")
}

// serveFile serves a file with http.ServeContent, so that a player asks for ranges, as a take's segment is served.
func serveFile(w http.ResponseWriter, path, rng, kind, name string) error {
	f, err := os.Open(path) //nolint:gosec // A video's file the store named.
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // Read only.

	info, err := f.Stat()
	if err != nil {
		return err
	}

	h := w.Header()
	h.Set("Content-Type", kind)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age=3600")

	if name != "" {
		h.Set("Content-Disposition", `inline; filename="`+name+`"`)
	}

	r := &http.Request{Method: http.MethodGet, Header: http.Header{}}
	if rng != "" {
		r.Header.Set("Range", rng)
	}

	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), f)

	return nil
}
