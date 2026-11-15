package nodeapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// OpenDeviceView asks the node that holds the phone for a view of its screen
// (issue #417, step 3) and returns the node's end once the node has opened
// it, as a terminal's is opened (terminal.go). Its binary messages are frames
// and its text messages the phone's state and size (nodeproto.DeviceView).
func (s *Server) OpenDeviceView(ctx context.Context, nodeID string) (*DeviceViewEnd, error) {
	end, err := s.openStream(ctx, nodeID, "phone view", func(streamID string) (nodeproto.Type, any) {
		return nodeproto.TypeDeviceView, nodeproto.DeviceView{StreamID: streamID}
	})
	if err != nil {
		return nil, err
	}

	return &DeviceViewEnd{end: end}, nil
}

// DeviceViewEnd is the node's side of one view of the phone.
type DeviceViewEnd struct{ end *NodeEnd }

// Read returns the next message: a frame, or else a text message.
func (e *DeviceViewEnd) Read(ctx context.Context) ([]byte, *nodeproto.DeviceViewMessage, error) {
	for {
		kind, data, err := e.end.conn.Read(ctx)
		if err != nil {
			return nil, nil, err
		}

		if kind == websocket.MessageBinary {
			return data, nil, nil
		}

		var m nodeproto.DeviceViewMessage
		if json.Unmarshal(data, &m) == nil {
			return nil, &m, nil
		}
	}
}

// Send passes one of the owner's input messages to the phone.
func (e *DeviceViewEnd) Send(ctx context.Context, in nodeproto.DeviceViewMessage) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}

	return e.end.conn.Write(ctx, websocket.MessageText, raw)
}

// Close ends the view and lets the node's request return.
func (e *DeviceViewEnd) Close(reason string) { e.end.Close(reason) }

// deviceView is the node's request for one view of its phone.
func (s *Server) deviceView(w http.ResponseWriter, r *http.Request) {
	s.present(w, r, "phone view", nodeproto.MaxDeviceFrameBytes)
}
