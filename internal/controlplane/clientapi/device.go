package clientapi

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// The Android phone a node holds (issue #417): the owner sees each phone's
// state and asks its node to start or stop it. The state is the node's own,
// which the node API keeps as the node last said it.

// Devices reaches the nodes' phones through the node API.
type Devices interface {
	// DeviceState is the phone's state a node said last, and false when it said none.
	DeviceState(nodeID string) (nodeproto.DeviceState, bool)
	StartDevice(ctx context.Context, nodeID string) error
	StopDevice(ctx context.Context, nodeID string) error
}

// WithDevices sets how the nodes' phones are reached. Without it the routes
// answer that there are none.
func (s *Server) WithDevices(d Devices) *Server {
	s.devices = d

	return s
}

var errNoDevices = fmt.Errorf("%w: this control plane reaches no phone", orchestration.ErrUnavailable)

// GetDevice lists the nodes that hold a phone.
func (s *Server) GetDevice(ctx context.Context, _ openapi.GetDeviceRequestObject,
) (openapi.GetDeviceResponseObject, error) {
	if s.devices == nil {
		return nil, errNoDevices
	}

	nodes, err := s.svc.ListNodes(ctx)
	if err != nil {
		return nil, err
	}

	out := openapi.DeviceList{Data: []openapi.DeviceNode{}}

	for _, n := range nodes {
		if n.Device != "" {
			out.Data = append(out.Data, s.deviceNode(n))
		}
	}

	return openapi.GetDevice200JSONResponse(out), nil
}

// StartNodeDevice asks a node to start its phone.
func (s *Server) StartNodeDevice(ctx context.Context, req openapi.StartNodeDeviceRequestObject,
) (openapi.StartNodeDeviceResponseObject, error) {
	n, err := s.deviceAsk(ctx, req.NodeId, true)
	if err != nil {
		return nil, err
	}

	return openapi.StartNodeDevice202JSONResponse(s.deviceNode(n)), nil
}

// StopNodeDevice asks a node to stop its phone.
func (s *Server) StopNodeDevice(ctx context.Context, req openapi.StopNodeDeviceRequestObject,
) (openapi.StopNodeDeviceResponseObject, error) {
	n, err := s.deviceAsk(ctx, req.NodeId, false)
	if err != nil {
		return nil, err
	}

	return openapi.StopNodeDevice202JSONResponse(s.deviceNode(n)), nil
}

// deviceAsk asks a node that holds a phone and is connected to start or
// stop it.
func (s *Server) deviceAsk(ctx context.Context, nodeID string, start bool) (orchestration.Node, error) {
	if s.devices == nil {
		return orchestration.Node{}, errNoDevices
	}

	n, err := s.svc.GetNode(ctx, nodeID)
	if err != nil {
		return orchestration.Node{}, err
	}

	switch {
	case n.Device == "":
		return orchestration.Node{}, fmt.Errorf("%w: node %s holds no phone: it was not made with --device android",
			orchestration.ErrConflict, n.Name)
	case !n.Connected:
		return orchestration.Node{}, fmt.Errorf("%w: node %s is not connected", orchestration.ErrUnavailable, n.Name)
	}

	ask := s.devices.StopDevice
	if start {
		ask = s.devices.StartDevice
	}

	if err := ask(ctx, n.ID); err != nil {
		return orchestration.Node{}, err
	}

	return n, nil
}

// deviceNode is a node's phone as the API shows it; the state of a node
// that is not connected is left out, being of an earlier time.
func (s *Server) deviceNode(n orchestration.Node) openapi.DeviceNode {
	out := openapi.DeviceNode{
		NodeId: n.ID, Name: n.Name, Device: openapi.DeviceNodeDevice(n.Device), Connected: n.Connected, Kvm: n.KVM,
		Installed: []openapi.DeviceComponent{},
	}

	st, ok := s.devices.DeviceState(n.ID)
	if !ok {
		return out
	}

	for _, c := range st.Installed {
		out.Installed = append(out.Installed, openapi.DeviceComponent{Name: c.Name, Version: c.Version})
	}

	if n.Connected {
		out.State, out.Reason = ptr(openapi.DeviceNodeState(st.State)), ptr(st.Reason)
	}

	return out
}
