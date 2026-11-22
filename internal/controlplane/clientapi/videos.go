package clientapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

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
	ChangeLook(ctx context.Context, id string, rev int, req videos.LookChange) (videos.Video, videos.Revision, error)
	Retry(ctx context.Context, id string, rev int, by, requestID string) (videos.Video, videos.Revision, error)
	Preview(ctx context.Context, req videos.PreviewRequest) ([]byte, error)
	Looks(ctx context.Context) (videos.Catalog, error)
	Music(ctx context.Context) (videos.Library, error)
	UploadMusic(ctx context.Context, name string, size int64, body io.Reader) (videos.Track, error)
	MusicAudio(ctx context.Context, id string) (string, string, error)
	DeleteMusic(ctx context.Context, id string) error
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
	case errors.Is(err, videos.ErrUnavailable):
		return fmt.Errorf("%w: %w", orchestration.ErrUnavailable, err)
	}

	return err
}

// videoCode is the code of a look sample's limits (issue #908, row 14): busy is rate_limited, with a Retry-After of a
// second; too long is timed_out.
func videoCode(err error) (openapi.ErrorCode, bool) {
	switch {
	case errors.Is(err, videos.ErrBusy):
		return openapi.RateLimited, true
	case errors.Is(err, videos.ErrTimedOut):
		return openapi.TimedOut, true
	}

	return "", false
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
			FailedCause: nonZero(r.FailedCause), Note: nonZero(r.Note), Look: lookOf(r.Look),
		}
		if r.Progress != nil {
			rev.Progress = &openapi.VideoProgress{ScenesDone: r.Progress.ScenesDone, ScenesTotal: r.Progress.ScenesTotal}
		}

		out.Revisions = append(out.Revisions, rev)
	}

	return out
}

func lookOf(l videos.Look) openapi.VideoLook {
	out := openapi.VideoLook{
		Captions: openapi.VideoCaptions{
			Preset: l.Captions.Preset, Position: openapi.VideoCaptionPosition(l.Captions.Position),
		},
		Frame: openapi.VideoFrame{
			Chrome: openapi.VideoChrome(l.Frame.Chrome), Padding: l.Frame.Padding, Radius: l.Frame.Radius,
			Shadow: l.Frame.Shadow, SafeTop: l.Frame.SafeTop,
		},
	}

	if l.Background != nil {
		out.Background = &openapi.VideoBackground{Preset: nonZero(l.Background.Preset)}
		if l.Background.Colors != nil {
			colors := append([]string{}, l.Background.Colors...)
			out.Background.Colors = &colors
		}
	}

	if l.FellBackFrom != "" {
		out.FellBackFrom = ptr(openapi.VideoChrome(l.FellBackFrom))
	}

	out.Music = musicLookOf(l.Music)

	return out
}

// lookInput is a look as sent, its sections and fields left out kept out.
func lookInput(in *openapi.VideoLookInput) *videos.LookInput {
	if in == nil {
		return nil
	}

	out := &videos.LookInput{}

	if c := in.Captions; c != nil {
		out.Captions = &videos.CaptionsInput{Preset: c.Preset}
		if c.Position != nil {
			position := string(*c.Position)
			out.Captions.Position = &position
		}
	}

	if f := in.Frame; f != nil {
		out.Frame = &videos.FrameInput{Padding: f.Padding, Radius: f.Radius, Shadow: f.Shadow, SafeTop: f.SafeTop}
		if f.Chrome != nil {
			chrome := string(*f.Chrome)
			out.Frame.Chrome = &chrome
		}
	}

	if b := in.Background; b != nil {
		out.Background = &videos.BackgroundInput{Preset: b.Preset}
		if b.Colors != nil {
			colors := append([]string{}, *b.Colors...)
			out.Background.Colors = &colors
		}
	}

	out.Music = musicInput(in.Music)

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
		ByName: videos.Owner, RequestID: val(req.Params.IdempotencyKey), Look: lookInput(b.Look),
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

// ChangeVideoLook queues a revision of a made one in another look.
func (s *Server) ChangeVideoLook(ctx context.Context, req openapi.ChangeVideoLookRequestObject,
) (openapi.ChangeVideoLookResponseObject, error) {
	if s.videos == nil {
		return nil, errNoVideos
	}

	v, _, err := s.videos.ChangeLook(ctx, req.VideoId, req.Rev, videos.LookChange{
		Look: lookInput(req.Body.Look), By: videos.Owner, RequestID: req.Params.IdempotencyKey,
	})
	if err != nil {
		return nil, videoError(err)
	}

	return openapi.ChangeVideoLook201JSONResponse(videoOf(v)), nil
}

// RetryVideoRevision queues a failed revision again, as it was asked.
func (s *Server) RetryVideoRevision(ctx context.Context, req openapi.RetryVideoRevisionRequestObject,
) (openapi.RetryVideoRevisionResponseObject, error) {
	if s.videos == nil {
		return nil, errNoVideos
	}

	v, _, err := s.videos.Retry(ctx, req.VideoId, req.Rev, videos.Owner, req.Params.IdempotencyKey)
	if err != nil {
		return nil, videoError(err)
	}

	return openapi.RetryVideoRevision201JSONResponse(videoOf(v)), nil
}

// ListVideoLooks is what a look is chosen from, and the saved default.
func (s *Server) ListVideoLooks(ctx context.Context, _ openapi.ListVideoLooksRequestObject,
) (openapi.ListVideoLooksResponseObject, error) {
	if s.videos == nil {
		return nil, errNoVideos
	}

	c, err := s.videos.Looks(ctx)
	if err != nil {
		return nil, videoError(err)
	}

	out := openapi.VideoLooks{
		Backgrounds: c.Backgrounds, Default: lookOf(c.Default),
		Chromes: make([]openapi.VideoChrome, 0, len(c.Chromes)), Presets: make([]openapi.VideoPreset, 0, len(c.Presets)),
	}

	for _, ch := range c.Chromes {
		out.Chromes = append(out.Chromes, openapi.VideoChrome(ch))
	}

	for _, p := range c.Presets {
		out.Presets = append(out.Presets, openapi.VideoPreset{Id: p.ID, Name: p.Name})
	}

	return openapi.ListVideoLooks200JSONResponse(out), nil
}

// PreviewVideoLook answers a look sample, a JPEG.
func (s *Server) PreviewVideoLook(ctx context.Context, req openapi.PreviewVideoLookRequestObject,
) (openapi.PreviewVideoLookResponseObject, error) {
	if s.videos == nil {
		return nil, errNoVideos
	}

	b := req.Body
	in := videos.PreviewRequest{Take: b.Take, Captions: b.Captions, Look: lookInput(b.Look), Rev: val(b.Rev)}

	if b.Aspect != nil {
		in.Aspect = string(*b.Aspect)
	}

	jpeg, err := s.videos.Preview(ctx, in)
	if err != nil {
		return nil, videoError(err)
	}

	return lookSample(jpeg), nil
}

// lookSample is a look sample's JPEG, never cached: the same request draws the default as it is now.
type lookSample []byte

func (j lookSample) VisitPreviewVideoLookResponse(w http.ResponseWriter) error {
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Length", strconv.Itoa(len(j)))
	w.WriteHeader(http.StatusOK)

	_, err := w.Write(j)

	return err
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
