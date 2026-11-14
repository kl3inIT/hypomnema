package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// An agent's phone tool (issue #417, step 2). The control plane asks on the
// socket, and the node answers with an HTTP request of its own, as it
// answers a files_request: a screenshot is larger than a socket message.

// answerPostWithin bounds posting an answer: the half minute the control
// plane waits beyond nodeproto.DeviceAnswerWithin.
const answerPostWithin = 30 * time.Second

// answerDevice answers a device_call. It does not hold up the socket: the
// phone may first have to boot, which takes a minute.
func (d *Daemon) answerDevice(payload json.RawMessage) error {
	var call nodeproto.DeviceCall
	if err := json.Unmarshal(payload, &call); err != nil {
		return fmt.Errorf("decoding a device call: %w", err)
	}

	if d.base == nil {
		return nil
	}

	go func() {
		ctx, cancel := context.WithTimeout(d.base, nodeproto.DeviceAnswerWithin)
		defer cancel()

		answer := nodeproto.DeviceAnswer{
			Content: []nodeproto.DeviceContent{{Type: "text", Text: "This node holds no phone."}}, IsError: true,
		}
		if d.cfg.Phone != nil {
			answer = d.cfg.Phone.Call(ctx, call.Agent, call.Tool, call.Arguments)
		}

		// The answer is posted with its own time, so that one made as the call's time ran out still arrives.
		post, cancelPost := context.WithTimeout(d.base, answerPostWithin)
		defer cancelPost()

		if err := d.postAnswer(post, nodeproto.PathDevice, call.RequestID, answer); err != nil && post.Err() == nil {
			d.logger.WarnContext(post, "device answer not sent",
				slog.String("event", "node.device_answer_failed"), slog.String("error", err.Error()))
		}
	}()

	return nil
}
