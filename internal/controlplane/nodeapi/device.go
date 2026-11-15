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

// The phone a node holds (issue #417). The control plane asks on the socket
// the node holds, as it asks for a stop (nodeproto.TypeStop), and the node
// says what came of it in the phone's state, at hello and in its heartbeats.
// The state is the node's: the control plane keeps the last one each node
// said in memory, as it keeps the sockets, and stores none of it.

// deviceSaid records the phone's state a node said; nil is a node that
// holds none.
func (s *Server) deviceSaid(nodeID string, st *nodeproto.DeviceState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if st == nil {
		delete(s.devices, nodeID)

		return
	}

	s.devices[nodeID] = *st
}

// DeviceState is the phone's state a node said last, and false when it said
// none since the control plane started.
func (s *Server) DeviceState(nodeID string) (nodeproto.DeviceState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, ok := s.devices[nodeID]

	return st, ok
}

// StartDevice asks a node to start its phone; StopDevice to stop it. A node
// that is not connected is ErrUnavailable.
func (s *Server) StartDevice(ctx context.Context, nodeID string) error {
	return s.askDevice(ctx, nodeID, nodeproto.TypeDeviceStart)
}

// StopDevice asks a node to stop its phone.
func (s *Server) StopDevice(ctx context.Context, nodeID string) error {
	return s.askDevice(ctx, nodeID, nodeproto.TypeDeviceStop)
}

func (s *Server) askDevice(ctx context.Context, nodeID string, t nodeproto.Type) error {
	s.mu.Lock()
	sess := s.sessions[nodeID]
	s.mu.Unlock()

	if sess == nil {
		return fmt.Errorf("%w: the machine is offline", orchestration.ErrUnavailable)
	}

	return sess.send(ctx, t, nil)
}

// Online says whether a node holds its socket to this control plane now.
func (s *Server) Online(nodeID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.sessions[nodeID] != nil
}

// pendingDevice is a device call a node has been sent.
type pendingDevice struct {
	nodeID  string
	arrived chan nodeproto.DeviceAnswer
	// sess is the connection the call went on, and lost is closed when that
	// connection ends: a node that restarts (to update, say) never answers
	// what its earlier process was sent, so the call fails at once rather
	// than at its time limit, as gRPC fails a call whose connection is lost
	// with UNAVAILABLE (trial on AWS, 2026-11-15).
	sess *session
	lost chan struct{}
}

// DeviceCall runs one phone tool on the node that holds the phone (issue
// #417, step 2), as Files asks a node about a worktree: the call goes on the
// socket and the node posts the tool's result to PathDevice. A node that is
// not connected, or does not answer within nodeproto.DeviceAnswerWithin, is
// ErrUnavailable.
func (s *Server) DeviceCall(ctx context.Context, nodeID, agent, tool string, arguments json.RawMessage,
) (nodeproto.DeviceAnswer, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nodeproto.DeviceAnswer{}, fmt.Errorf("naming a device call: %w", err)
	}

	id := hex.EncodeToString(raw)
	pending := &pendingDevice{nodeID: nodeID, arrived: make(chan nodeproto.DeviceAnswer, 1), lost: make(chan struct{})}

	s.mu.Lock()
	sess := s.sessions[nodeID]
	if sess != nil {
		pending.sess = sess
		s.deviceCalls[id] = pending
	}
	s.mu.Unlock()

	if sess == nil {
		return nodeproto.DeviceAnswer{}, fmt.Errorf("%w: the phone's machine is offline", orchestration.ErrUnavailable)
	}

	defer func() {
		s.mu.Lock()
		delete(s.deviceCalls, id)
		s.mu.Unlock()
	}()

	call := nodeproto.DeviceCall{RequestID: id, Agent: agent, Tool: tool, Arguments: arguments}
	if err := sess.send(ctx, nodeproto.TypeDeviceCall, call); err != nil {
		return nodeproto.DeviceAnswer{}, err
	}

	wait := time.NewTimer(nodeproto.DeviceAnswerWithin + 30*time.Second)
	defer wait.Stop()

	select {
	case answer := <-pending.arrived:
		return answer, nil
	case <-pending.lost:
		return nodeproto.DeviceAnswer{}, fmt.Errorf("%w: the phone's machine went away during the call, "+
			"as when it restarts to update; call again", orchestration.ErrUnavailable)
	case <-wait.C:
		return nodeproto.DeviceAnswer{}, fmt.Errorf("%w: the phone's machine did not answer within %s; it may run "+
			"a version that has no phone tools", orchestration.ErrUnavailable, nodeproto.DeviceAnswerWithin)
	case <-ctx.Done():
		return nodeproto.DeviceAnswer{}, ctx.Err()
	}
}

// deviceAnswer is a node's answer to a device call it was sent.
func (s *Server) deviceAnswer(w http.ResponseWriter, r *http.Request) {
	n, _, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	id := r.URL.Query().Get("request")

	s.mu.Lock()
	pending := s.deviceCalls[id]
	if pending != nil && pending.nodeID == n.ID {
		delete(s.deviceCalls, id)
	} else {
		pending = nil
	}
	s.mu.Unlock()

	// A call nobody waits for any more, or another node's.
	if pending == nil {
		http.Error(w, "unknown device call", http.StatusNotFound)

		return
	}

	var answer nodeproto.DeviceAnswer
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, nodeproto.MaxDeviceAnswerBytes)).Decode(&answer); err != nil {
		answer = nodeproto.DeviceAnswer{Content: []nodeproto.DeviceContent{{
			Type: "text", Text: "the phone's answer could not be read: " + err.Error(),
		}}, IsError: true}
	}

	pending.arrived <- answer

	w.WriteHeader(http.StatusNoContent)
}

// loseDeviceCalls ends the calls sent on a connection that has ended. The
// caller holds s.mu.
func (s *Server) loseDeviceCalls(sess *session) {
	for id, pending := range s.deviceCalls {
		if pending.sess == sess {
			delete(s.deviceCalls, id)
			close(pending.lost)
		}
	}
}
