package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/kl3inIT/hypomnema/internal/node/nodeexec"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// An agent's command on this machine (issue #543). The control plane asks on
// the socket, and the node answers with an HTTP request of its own, as it
// answers a device_call: a command runs for minutes, and its output is larger
// than a socket message may be.

// Commands runs an agent's commands on the machine; *nodeexec.Manager is the
// node's.
type Commands interface {
	Run(ctx context.Context, req nodeexec.Request) nodeproto.ExecAnswer
	Start(req nodeexec.Request) nodeproto.ExecAnswer
	Output(ctx context.Context, agent, id string, wait time.Duration) nodeproto.ExecAnswer
	Kill(agent, id string) nodeproto.ExecAnswer
	List(agent string) []nodeproto.ExecProcess
}

// answerExec answers an exec. It does not hold up the socket: a command runs
// for minutes. Nothing of the request is logged: it may carry the sudo
// password.
func (d *Daemon) answerExec(payload json.RawMessage) error {
	var e nodeproto.Exec
	if err := json.Unmarshal(payload, &e); err != nil {
		return fmt.Errorf("decoding an exec: %w", err)
	}

	if d.base == nil {
		return nil
	}

	go func() {
		answer := d.exec(d.base, e)

		// The answer is posted with its own time, so that one made as the command's time ran out still arrives.
		post, cancel := context.WithTimeout(d.base, answerPostWithin)
		defer cancel()

		if err := d.postAnswer(post, nodeproto.PathExec, e.RequestID, answer); err != nil && post.Err() == nil {
			d.logger.WarnContext(post, "exec answer not sent",
				slog.String("event", "node.exec_answer_failed"), slog.String("error", err.Error()))
		}
	}()

	return nil
}

func (d *Daemon) exec(ctx context.Context, e nodeproto.Exec) nodeproto.ExecAnswer {
	if d.cfg.Commands == nil {
		return nodeproto.ExecAnswer{Error: "this node does not run commands"}
	}

	req := nodeexec.Request{
		ID: e.ProcessID, Agent: e.Agent, Command: e.Command, Cwd: e.Cwd, Timeout: time.Duration(e.TimeoutSeconds) * time.Second,
		SudoPassword: e.SudoPassword,
	}

	switch e.Action {
	case nodeproto.ExecRun:
		return d.cfg.Commands.Run(ctx, req)
	case nodeproto.ExecStart:
		return d.cfg.Commands.Start(req)
	case nodeproto.ExecOutput:
		return d.cfg.Commands.Output(ctx, e.Agent, e.ProcessID, time.Duration(e.WaitSeconds)*time.Second)
	case nodeproto.ExecList:
		return nodeproto.ExecAnswer{Processes: d.cfg.Commands.List(e.Agent)}
	case nodeproto.ExecKill:
		return d.cfg.Commands.Kill(e.Agent, e.ProcessID)
	default:
		return nodeproto.ExecAnswer{Error: fmt.Sprintf("this node does not know the action %q", e.Action)}
	}
}
