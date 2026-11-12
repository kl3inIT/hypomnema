package nodeapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// The owner reads a Task's worktree (issue #255). The control plane asks the
// node on the socket it holds and waits for the node's answer on PathFiles,
// as a terminal's stream is asked for and met (terminal.go); Omnigent's
// server likewise waits on its runner's answer with a time limit.

// pendingAnswer is a files request a node has been sent.
type pendingAnswer struct {
	nodeID  string
	arrived chan workspacefs.Result
}

// Files asks a node about one of its worktrees. A node that is not connected,
// or does not answer within nodeproto.FilesAnswerWithin, is ErrUnavailable.
func (s *Server) Files(ctx context.Context, nodeID, worktree string, req workspacefs.Request,
) (workspacefs.Result, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return workspacefs.Result{}, fmt.Errorf("naming a files request: %w", err)
	}

	id := hex.EncodeToString(raw)
	pending := &pendingAnswer{nodeID: nodeID, arrived: make(chan workspacefs.Result, 1)}

	s.mu.Lock()
	sess := s.sessions[nodeID]
	if sess != nil {
		s.answers[id] = pending
	}
	s.mu.Unlock()

	if sess == nil {
		return workspacefs.Result{}, fmt.Errorf("%w: the machine is offline", orchestration.ErrUnavailable)
	}

	defer func() {
		s.mu.Lock()
		delete(s.answers, id)
		s.mu.Unlock()
	}()

	if err := sess.send(ctx, nodeproto.TypeFilesRequest, nodeproto.FilesRequest{
		RequestID: id, Worktree: worktree, Request: req,
	}); err != nil {
		return workspacefs.Result{}, err
	}

	select {
	case res := <-pending.arrived:
		return res, nil
	case <-time.After(nodeproto.FilesAnswerWithin):
		return workspacefs.Result{}, fmt.Errorf("%w: the machine did not answer within %s; it may run a version "+
			"that does not read files", orchestration.ErrUnavailable, nodeproto.FilesAnswerWithin)
	case <-ctx.Done():
		return workspacefs.Result{}, ctx.Err()
	}
}

// filesAnswer is a node's answer to a files request it was sent.
func (s *Server) filesAnswer(w http.ResponseWriter, r *http.Request) {
	n, _, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	id := r.URL.Query().Get("request")

	s.mu.Lock()
	pending := s.answers[id]
	if pending != nil && pending.nodeID == n.ID {
		delete(s.answers, id)
	} else {
		pending = nil
	}
	s.mu.Unlock()

	// A request nobody waits for any more, or another node's.
	if pending == nil {
		http.Error(w, "unknown files request", http.StatusNotFound)

		return
	}

	var res workspacefs.Result
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, nodeproto.MaxFilesAnswerBytes)).Decode(&res); err != nil {
		res = workspacefs.Result{Error: &workspacefs.Error{
			Code: workspacefs.CodeFailed, Message: "the machine's answer could not be read: " + err.Error(),
		}}
	}

	pending.arrived <- res

	w.WriteHeader(http.StatusNoContent)
}
