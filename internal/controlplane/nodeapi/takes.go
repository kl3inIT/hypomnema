package nodeapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
	"github.com/kl3inIT/hypomnema/internal/controlplane/takes"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// A node uploads the takes its phone filmed (issue #817, row 11), a file a
// request on a route of its own: the take must be the node's, as a files
// answer must match a pending request of that node (files.go), and the route
// sets its own read deadline from the file's size rather than the shared
// server's 15 s (http.ResponseController, as the client API's uploads do).

// uploadFloor is the slowest a take's upload may be: 64 KiB a second, under
// the 145 to 400 KB a second the public address carried (issue #578).
const uploadFloor = 64 << 10

// TakeFiles takes the files a node uploads (takes.Store).
type TakeFiles interface {
	Receive(nodeID, id, name string, size int64, sum string, body io.Reader) error
}

// TakesHeld settles the takes a node says it holds at hello: those to end,
// and those to drop (phone.Phone.NodeTakes).
type TakesHeld func(ctx context.Context, nodeID string, held []nodeproto.TakeHeld) ([]nodeproto.TakeEnd, []string)

// WithTakes has the nodes' takes received, and settled at hello.
func (s *Server) WithTakes(files TakeFiles, held TakesHeld) *Server {
	s.takeFiles, s.takesHeld = files, held

	return s
}

// takeUpload is a node's upload of one file of a take.
func (s *Server) takeUpload(w http.ResponseWriter, r *http.Request) {
	n, _, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	if s.takeFiles == nil {
		http.Error(w, "this control plane keeps no take", http.StatusNotFound)

		return
	}

	size := r.ContentLength
	if size <= 0 || size > takes.MaxSegmentBytes {
		http.Error(w, "a take's file needs its Content-Length", http.StatusLengthRequired)

		return
	}

	rc := http.NewResponseController(w)
	at := time.Now().Add(time.Minute + time.Duration(size/uploadFloor)*time.Second)
	_ = rc.SetReadDeadline(at)
	_ = rc.SetWriteDeadline(at)

	id, name := r.PathValue("take"), r.PathValue("name")

	err := s.takeFiles.Receive(n.ID, id, name, size, r.Header.Get(nodeproto.TakeSumHeader),
		http.MaxBytesReader(w, r.Body, size))

	status := http.StatusOK

	switch {
	case err == nil:
	case errors.Is(err, takes.ErrDeleted):
		status = http.StatusGone
	case errors.Is(err, phone.ErrNoTake):
		status = http.StatusNotFound
	case errors.Is(err, takes.ErrNotYours):
		status = http.StatusForbidden
	case errors.Is(err, takes.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, takes.ErrBadFile):
		status = http.StatusBadRequest
	case errors.Is(err, takes.ErrDiskLow):
		status = http.StatusInsufficientStorage
	default:
		status = http.StatusInternalServerError
	}

	if err != nil {
		s.logger.WarnContext(r.Context(), "a node's take file was refused", slog.String("event", "nodeapi.take_refused"),
			slog.String("node_id", n.ID), slog.String("take", id), slog.String("file", name),
			slog.String("error", err.Error()))
		http.Error(w, err.Error(), status)

		return
	}

	w.WriteHeader(status)
}

// settleTakes is what a node is to do with the takes it said it holds.
func (s *Server) settleTakes(ctx context.Context, nodeID string, req nodeproto.HelloRequest,
) ([]nodeproto.TakeEnd, []string) {
	if s.takesHeld == nil || !req.ListsTakes {
		return nil, nil
	}

	return s.takesHeld(ctx, nodeID, req.Takes)
}
