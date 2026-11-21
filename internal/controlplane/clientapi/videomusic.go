package clientapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
)

// The owner's music library for videos (issue #910, rows 6a, 6d and 18): listed with its quota, a track played with
// ranges and deleted through the contract; uploaded on a route of its own, outside the contract's validator, which
// reads a whole body before a handler runs (server.go). The route checks who asks, the length and the room before it
// reads a byte of the body, as a node's take upload does (nodeapi/takes.go).

// musicUploadPath is where a track is uploaded.
const musicUploadPath = BasePath + "/videos/music"

// octetStream is the type a track's upload is sent as.
const octetStream = "application/octet-stream"

// musicBusyRetry is the Retry-After of an upload while another is received.
const musicBusyRetry = "5"

// uploadMusic receives a track: the owner's only, sent as application/octet-stream as a meeting's recording is
// (api/openapi.yaml, UploadMeeting), so that a page of another origin cannot send one as a simple request with the
// session's cookie; its Content-Length required and at most videos.MaxMusicBytes; the service refuses a second upload
// and one the quota or the disk cannot hold before it reads the body.
func (s *Server) uploadMusic(w http.ResponseWriter, r *http.Request) {
	// The connection is closed after the answer: Go's server otherwise reads a body the handler left unread, a chunked
	// one or one under 256 KB, before it answers a refusal, so a refusal would wait for the whole upload (net/http's
	// chunkWriter.writeHeader skips that read once the answer says Connection: close).
	w.Header().Set("Connection", "close")

	tok, _, err := s.authenticate(r.Context(), r)
	if err != nil {
		s.writeError(w, r, err)

		return
	}

	if tok.Kind != bearertoken.Owner {
		s.writeError(w, r, fmt.Errorf("%w: only the owner's token may do this", errForbidden))

		return
	}

	if s.videos == nil {
		s.writeError(w, r, errNoVideos)

		return
	}

	if kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || kind != octetStream {
		writeJSONError(w, http.StatusBadRequest, openapi.ValidationFailed,
			fmt.Sprintf("header Content-Type is %s, not %q", octetStream, r.Header.Get("Content-Type")))

		return
	}

	name, err := videos.CheckMusicName(r.URL.Query().Get("name"))
	if err != nil {
		s.writeError(w, r, videoError(err))

		return
	}

	size := r.ContentLength

	switch {
	case size <= 0:
		writeJSONError(w, http.StatusLengthRequired, openapi.InvalidRequest, "a track's upload needs its Content-Length")

		return
	case size > videos.MaxMusicBytes:
		writeJSONError(w, http.StatusRequestEntityTooLarge, openapi.RequestTooLarge,
			fmt.Sprintf("a track is at most %d MB", videos.MaxMusicBytes>>20))

		return
	}

	// The file takes longer to arrive than the server's read timeout, and its check longer than its write timeout.
	uploadDeadline(w)

	track, err := s.videos.UploadMusic(r.Context(), name, size, http.MaxBytesReader(w, r.Body, size))
	if err != nil {
		s.writeError(w, r, videoError(err))

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)

	_ = json.NewEncoder(w).Encode(musicTrackOf(track)) //nolint:errchkjson // The client left; no one to tell.
}

func musicTrackOf(t videos.Track) openapi.VideoMusicTrack {
	out := openapi.VideoMusicTrack{
		Id: t.ID, Name: t.Name, MediaType: t.MediaType, LengthMs: t.LengthMs, Bytes: t.Bytes, CreatedAt: t.CreatedAt,
	}
	if t.Missing {
		out.Missing = ptr(true)
	}

	return out
}

// musicLookOf is a look's music as the API's.
func musicLookOf(m *videos.MusicLook) *openapi.VideoMusic {
	if m == nil {
		return nil
	}

	out := &openapi.VideoMusic{
		Track: m.Track, GainDb: m.GainDB, Name: nonZero(m.Name), LengthMs: nonZero(m.LengthMs),
	}
	if f := m.FellBackFrom; f != nil {
		out.FellBackFrom = &openapi.VideoMusicSnapshot{Track: f.Track, Name: f.Name, LengthMs: f.LengthMs}
	}

	return out
}

// musicInput is a music section as sent.
func musicInput(in *openapi.VideoMusicInput) *videos.MusicInput {
	if in == nil {
		return nil
	}

	track := in.Track

	return &videos.MusicInput{Track: &track, GainDB: in.GainDb}
}

// musicCode is the code of the library's limits: an upload while another is received is rate_limited, with a
// Retry-After of 5 seconds; a library or a disk without room is insufficient_storage.
func musicCode(err error) (openapi.ErrorCode, bool) {
	switch {
	case errors.Is(err, videos.ErrMusicBusy):
		return openapi.RateLimited, true
	case errors.Is(err, videos.ErrDiskLow):
		return openapi.InsufficientStorage, true
	}

	return "", false
}

// ListVideoMusic is the music library.
func (s *Server) ListVideoMusic(ctx context.Context, _ openapi.ListVideoMusicRequestObject,
) (openapi.ListVideoMusicResponseObject, error) {
	if s.videos == nil {
		return nil, errNoVideos
	}

	lib, err := s.videos.Music(ctx)
	if err != nil {
		return nil, videoError(err)
	}

	out := openapi.VideoMusicLibrary{
		Tracks: make([]openapi.VideoMusicTrack, 0, len(lib.Tracks)), UsedBytes: lib.UsedBytes, QuotaBytes: lib.QuotaBytes,
	}
	for _, t := range lib.Tracks {
		out.Tracks = append(out.Tracks, musicTrackOf(t))
	}

	return openapi.ListVideoMusic200JSONResponse(out), nil
}

// DeleteVideoMusic deletes a track.
func (s *Server) DeleteVideoMusic(ctx context.Context, req openapi.DeleteVideoMusicRequestObject,
) (openapi.DeleteVideoMusicResponseObject, error) {
	if s.videos == nil {
		return nil, errNoVideos
	}

	if err := s.videos.DeleteMusic(ctx, req.MusicId); err != nil {
		return nil, videoError(err)
	}

	return openapi.DeleteVideoMusic204Response{}, nil
}

// ReadVideoMusicAudio serves a track's file as the type it was uploaded as, with ranges, for the Studio's player.
func (s *Server) ReadVideoMusicAudio(ctx context.Context, req openapi.ReadVideoMusicAudioRequestObject,
) (openapi.ReadVideoMusicAudioResponseObject, error) {
	if s.videos == nil {
		return nil, errNoVideos
	}

	path, kind, err := s.videos.MusicAudio(ctx, req.MusicId)
	if err != nil {
		return nil, videoError(err)
	}

	return musicAudio{path: path, kind: kind, rng: val(req.Params.Range)}, nil
}

// musicAudio is a track's file served with the range asked for.
type musicAudio struct {
	path, kind, rng string
}

func (a musicAudio) VisitReadVideoMusicAudioResponse(w http.ResponseWriter) error {
	return serveFile(w, a.path, a.rng, a.kind, "")
}
