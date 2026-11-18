package clientapi

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// The takes agents film of the phone (issue #817, row 12), the owner's only:
// listed, read, played and seeked segment by segment (http.ServeContent, so
// the browser asks for ranges), downloaded as a zip of the segments and
// take.json, and deleted. An agent is told a take's id, title, state, length
// and steps by its video tools, never a file.

// Takes are the takes the control plane keeps (takes.Store, with the phone to
// end a take still filmed before it is deleted).
type Takes interface {
	List() []devicetool.Take
	Get(id string) (devicetool.Take, error)
	// File is the path of a file of an available take: a segment or its take.json.
	File(id, name string) (string, error)
	Delete(ctx context.Context, id string) error
}

// WithTakes sets the takes the owner reads. Without them the routes answer
// that there are none.
func (s *Server) WithTakes(t Takes) *Server {
	s.takes = t

	return s
}

var errNoTakes = fmt.Errorf("%w: this control plane keeps no take", orchestration.ErrUnavailable)

// takeOf is a take as the API says it: the same fields as take.json.
func takeOf(t devicetool.Take) (openapi.Take, error) {
	raw, err := json.Marshal(t)
	if err != nil {
		return openapi.Take{}, err
	}

	var out openapi.Take
	if err := json.Unmarshal(raw, &out); err != nil {
		return openapi.Take{}, err
	}

	return out, nil
}

// takeOrNotFound is a take, or the API's not found when the store does not have it.
func (s *Server) takeOrNotFound(id string) (devicetool.Take, error) {
	if s.takes == nil {
		return devicetool.Take{}, errNoTakes
	}

	t, err := s.takes.Get(id)
	if err != nil {
		return devicetool.Take{}, fmt.Errorf("%w: take %s", orchestration.ErrNotFound, id)
	}

	return t, nil
}

// ListTakes lists the takes, the newest first.
func (s *Server) ListTakes(_ context.Context, _ openapi.ListTakesRequestObject,
) (openapi.ListTakesResponseObject, error) {
	if s.takes == nil {
		return nil, errNoTakes
	}

	out := openapi.TakeList{Data: []openapi.Take{}}

	for _, t := range s.takes.List() {
		take, err := takeOf(t)
		if err != nil {
			return nil, err
		}

		out.Data = append(out.Data, take)
	}

	return openapi.ListTakes200JSONResponse(out), nil
}

// GetTake reads a take.
func (s *Server) GetTake(_ context.Context, req openapi.GetTakeRequestObject) (openapi.GetTakeResponseObject, error) {
	t, err := s.takeOrNotFound(req.TakeId)
	if err != nil {
		return nil, err
	}

	take, err := takeOf(t)
	if err != nil {
		return nil, err
	}

	return openapi.GetTake200JSONResponse(take), nil
}

// DeleteTake deletes a take and its files.
func (s *Server) DeleteTake(ctx context.Context, req openapi.DeleteTakeRequestObject,
) (openapi.DeleteTakeResponseObject, error) {
	if _, err := s.takeOrNotFound(req.TakeId); err != nil {
		return nil, err
	}

	if err := s.takes.Delete(ctx, req.TakeId); err != nil {
		return nil, err
	}

	return openapi.DeleteTake204Response{}, nil
}

// ReadTakeSegment serves a segment of an available take, with ranges.
func (s *Server) ReadTakeSegment(_ context.Context, req openapi.ReadTakeSegmentRequestObject,
) (openapi.ReadTakeSegmentResponseObject, error) {
	if _, err := s.takeOrNotFound(req.TakeId); err != nil {
		return nil, err
	}

	path, err := s.takes.File(req.TakeId, req.File)
	if err != nil {
		return nil, fmt.Errorf("%w: segment %s of take %s", orchestration.ErrNotFound, req.File, req.TakeId)
	}

	rng := ""
	if req.Params.Range != nil {
		rng = *req.Params.Range
	}

	return segment{path: path, rng: rng}, nil
}

// segment is a segment's file served as video, with the range asked for.
type segment struct {
	path string
	rng  string
}

func (g segment) VisitReadTakeSegmentResponse(w http.ResponseWriter) error {
	f, err := os.Open(g.path)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // Read only.

	info, err := f.Stat()
	if err != nil {
		return err
	}

	h := w.Header()
	h.Set("Content-Type", "video/mp4")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age=3600")

	// http.ServeContent reads only the request's method and range headers.
	r := &http.Request{Method: http.MethodGet, Header: http.Header{}}
	if g.rng != "" {
		r.Header.Set("Range", g.rng)
	}

	http.ServeContent(w, r, filepath.Base(g.path), info.ModTime(), f)

	return nil
}

// GetTakeArchive sends an available take's segments and take.json as a zip,
// written as it is read.
func (s *Server) GetTakeArchive(_ context.Context, req openapi.GetTakeArchiveRequestObject,
) (openapi.GetTakeArchiveResponseObject, error) {
	t, err := s.takeOrNotFound(req.TakeId)
	if err != nil {
		return nil, err
	}

	if t.State != devicetool.TakeAvailable {
		return nil, fmt.Errorf("%w: take %s is %s", orchestration.ErrConflict, t.ID, t.State)
	}

	names := make([]string, 0, len(t.Segments)+1)
	for _, seg := range t.Segments {
		names = append(names, seg.File)
	}

	paths := make([]string, 0, len(names)+1)

	for _, name := range append(names, "take.json") {
		path, err := s.takes.File(t.ID, name)
		if err != nil {
			return nil, fmt.Errorf("%w: %s of take %s", orchestration.ErrNotFound, name, t.ID)
		}

		paths = append(paths, path)
	}

	return archive{name: t.ID + ".zip", paths: paths}, nil
}

// archive is a take's files as a zip, stored as they are: MP4 does not shrink.
type archive struct {
	name  string
	paths []string
}

func (a archive) VisitGetTakeArchiveResponse(w http.ResponseWriter) error {
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.name}))
	h.Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)

	zw := zip.NewWriter(w)

	for _, path := range a.paths {
		if err := addFile(zw, path); err != nil {
			return err
		}
	}

	return zw.Close()
}

func addFile(zw *zip.Writer, path string) error {
	f, err := os.Open(path) //nolint:gosec // A take's file the store named.
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // Read only.

	info, err := f.Stat()
	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}

	header.Method = zip.Store

	out, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}

	_, err = io.Copy(out, f)

	return err
}
