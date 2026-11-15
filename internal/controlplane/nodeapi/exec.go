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
)

// pendingExec is an agent's command a node has been sent (issue #543).
type pendingExec struct {
	nodeID  string
	arrived chan nodeproto.ExecAnswer
	// sess is the connection it went on; lost is closed when that connection ends, as a device call's is.
	sess *session
	lost chan struct{}
}

// Exec has a node run an agent's command, or answer about or kill one it
// runs in the background (issue #543), as DeviceCall runs a phone tool: the
// request goes on the socket and the node posts its answer to PathExec. A
// node that is not connected, goes away meanwhile, or does not answer in
// time is ErrUnavailable. The request may carry the sudo password: nothing
// of it is logged.
func (s *Server) Exec(ctx context.Context, nodeID string, e nodeproto.Exec) (nodeproto.ExecAnswer, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nodeproto.ExecAnswer{}, fmt.Errorf("naming a command: %w", err)
	}

	e.RequestID = hex.EncodeToString(raw)
	pending := &pendingExec{nodeID: nodeID, arrived: make(chan nodeproto.ExecAnswer, 1), lost: make(chan struct{})}

	s.mu.Lock()
	sess := s.sessions[nodeID]
	if sess != nil {
		pending.sess = sess
		s.execs[e.RequestID] = pending
	}
	s.mu.Unlock()

	if sess == nil {
		return nodeproto.ExecAnswer{}, fmt.Errorf("%w: the machine is offline", orchestration.ErrUnavailable)
	}

	defer func() {
		s.mu.Lock()
		delete(s.execs, e.RequestID)
		s.mu.Unlock()
	}()

	if err := sess.send(ctx, nodeproto.TypeExec, e); err != nil {
		return nodeproto.ExecAnswer{}, err
	}

	within := execWithin(e)

	wait := time.NewTimer(within)
	defer wait.Stop()

	select {
	case answer := <-pending.arrived:
		return answer, nil
	case <-pending.lost:
		return nodeproto.ExecAnswer{}, fmt.Errorf("%w: the machine went away while the command ran, as when its "+
			"node restarts to update; a command in the background ended with it", orchestration.ErrUnavailable)
	case <-wait.C:
		return nodeproto.ExecAnswer{}, fmt.Errorf("%w: the machine did not answer within %s; its node may run a "+
			"version that does not run commands", orchestration.ErrUnavailable, within)
	case <-ctx.Done():
		return nodeproto.ExecAnswer{}, ctx.Err()
	}
}

// execWithin is how long the node's answer is waited for: the command's own
// time, or the wait for a background one's output, and the slack.
func execWithin(e nodeproto.Exec) time.Duration {
	switch e.Action {
	case nodeproto.ExecRun:
		return min(time.Duration(e.TimeoutSeconds)*time.Second, nodeproto.ExecTimeoutMax) + nodeproto.ExecAnswerSlack
	case nodeproto.ExecOutput:
		return min(time.Duration(e.WaitSeconds)*time.Second, nodeproto.ExecWaitMax) + nodeproto.ExecAnswerSlack
	default:
		return nodeproto.ExecAnswerSlack
	}
}

// execAnswer is a node's answer to a command it was sent.
func (s *Server) execAnswer(w http.ResponseWriter, r *http.Request) {
	n, _, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	id := r.URL.Query().Get("request")

	s.mu.Lock()
	pending := s.execs[id]
	if pending != nil && pending.nodeID == n.ID {
		delete(s.execs, id)
	} else {
		pending = nil
	}
	s.mu.Unlock()

	// A command nobody waits for any more, or another node's.
	if pending == nil {
		http.Error(w, "unknown command", http.StatusNotFound)

		return
	}

	var answer nodeproto.ExecAnswer
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, nodeproto.MaxExecAnswerBytes)).Decode(&answer); err != nil {
		answer = nodeproto.ExecAnswer{Error: "the machine's answer could not be read: " + err.Error()}
	}

	pending.arrived <- answer

	w.WriteHeader(http.StatusNoContent)
}

// loseExecs ends the commands sent on a connection that has ended. The
// caller holds s.mu.
func (s *Server) loseExecs(sess *session) {
	for id, pending := range s.execs {
		if pending.sess == sess {
			delete(s.execs, id)
			close(pending.lost)
		}
	}
}
