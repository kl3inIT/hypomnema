package nodeapi

import (
	"context"
	"fmt"

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
