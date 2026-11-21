package toolsmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// A node's command outlives a restart of the control plane, as when it updates (issue #1060): the node runs it and
// keeps its result, and only the answer on its way through the control plane is lost. The tools server names a
// node_exec's process before it asks (nodeproto.NewProcessID), as Coder's agent names a process after the tool call
// that started it (agent/agentproc/api.go:92-98 handleStartProcess); when the answer does not arrive, it reads the result
// with node_process once the control plane is back, as Coder's execute tool reads a process again after a transport
// error (codersdk/toolsdk/workspacetools/execute.go:346-361 waitForProcess), and never runs the command again. A node_process
// whose answer is lost is asked again the same way (execute.go:251-262 ProcessOutput). What cannot be read in the call's time
// is an error that names the process (execute.go:361: "use process_output with ID … to retry").

// Asking again after a restart waits from half a second, doubled up to eight, as OpenClaw's Control UI retries a
// Gateway that restarts (docs/gateway/restart-recovery.md:295-296, "Startup retries … back off from 500 milliseconds to
// eight seconds").
const (
	reaskFirst = 500 * time.Millisecond
	reaskMost  = 8 * time.Second
)

// NodeCall runs node_exec or node_process on path within CallTimeout, and reads the result again when the control
// plane restarts while it runs. Any other tool is ToolCall's.
func (a *Agent) NodeCall(ctx context.Context, path, tool string, arguments json.RawMessage) (json.RawMessage, error) {
	ask, reask, lost := arguments, json.RawMessage(nil), func(error) error { return nil }
	said := func(raw json.RawMessage) json.RawMessage { return raw }

	switch tool {
	case agenttool.ToolNodeExec:
		var args agenttool.NodeExecArgs
		if json.Unmarshal(orEmptyObject(arguments), &args) != nil {
			break
		}

		args.ProcessID = nodeproto.NewProcessID()

		raw, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}

		wait := 0
		if !args.Background {
			wait = execSeconds(args.TimeoutSeconds)
		}

		reread, err := json.Marshal(agenttool.NodeProcessArgs{Node: args.Node, ProcessID: args.ProcessID,
			WaitSeconds: wait})
		if err != nil {
			return nil, err
		}

		ask, reask = raw, reread
		lost = func(err error) error {
			return fmt.Errorf("the control plane restarted, as when it updates, before node_exec's answer came, "+
				"and its result could not be read: %w. The command may still run: read it with node_process, node %s, "+
				"process_id %s", err, args.Node, args.ProcessID)
		}
		said = func(raw json.RawMessage) json.RawMessage {
			return withFirstLine(raw, fmt.Sprintf("the control plane restarted, as when it updates, before "+
				"node_exec's answer came; node_process on node %s answered for process_id %s:", args.Node,
				args.ProcessID))
		}
	case agenttool.ToolNodeProcess:
		var args agenttool.NodeProcessArgs
		if json.Unmarshal(orEmptyObject(arguments), &args) != nil {
			break
		}

		reask = arguments
		lost = func(err error) error {
			return fmt.Errorf("the control plane restarted while node_process waited, as when it updates, and "+
				"could not be asked again: %w. Ask again with node_process, node %s, process_id %s", err, args.Node,
				args.ProcessID)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()

	raw, err := a.toolCall(ctx, path, CallTimeout, maxAnswerBytes, tool, ask)
	if reask == nil || !answerLost(err) {
		return raw, err
	}

	raw, err = a.reask(ctx, path, reask)
	if err != nil {
		return nil, lost(err)
	}

	return said(raw), nil
}

// reask asks node_process until the control plane answers and the node is connected again, or ctx ends; its error
// is the last reason it had no answer.
func (a *Agent) reask(ctx context.Context, path string, args json.RawMessage) (json.RawMessage, error) {
	wait := reaskFirst

	var last error

	for {
		select {
		case <-ctx.Done():
			if last == nil {
				last = ctx.Err()
			}

			return nil, last
		case <-time.After(wait):
		}

		wait = min(wait*2, reaskMost)

		raw, err := a.toolCall(ctx, path, CallTimeout, maxAnswerBytes, agenttool.ToolNodeProcess, args)

		var u unreachable

		switch {
		case errors.As(err, &u):
			last = err
		case err != nil:
			return nil, err
		default:
			text, _ := firstText(raw)
			if !agenttool.IsNodeNotConnected(text) {
				return raw, nil
			}

			last = errors.New(text)
		}
	}
}

// answerLost says whether a call's answer was lost after it may have reached the control plane: the connection
// ended without it. One that could not even be opened never reached it, and its command never ran.
func answerLost(err error) bool {
	var u unreachable
	if !errors.As(err, &u) {
		return false
	}

	var op *net.OpError

	return !errors.As(err, &op) || op.Op != "dial"
}

// withFirstLine is a tool result with line said before its text.
func withFirstLine(raw json.RawMessage, line string) json.RawMessage {
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil {
		return raw
	}

	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return raw
	}

	first, ok := content[0].(map[string]any)
	if !ok || first["type"] != "text" {
		return raw
	}

	text, _ := first["text"].(string)
	first["text"] = line + "\n" + text

	out, err := json.Marshal(result)
	if err != nil {
		return raw
	}

	return out
}

// firstText is a tool result's first text, and whether it has one.
func firstText(raw json.RawMessage) (string, bool) {
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}

	if json.Unmarshal(raw, &result) != nil || len(result.Content) == 0 || result.Content[0].Type != "text" {
		return "", false
	}

	return result.Content[0].Text, true
}

// execSeconds is how long a node_exec may run, as the control plane reads its timeout_seconds.
func execSeconds(asked int) int {
	if asked <= 0 {
		return int(nodeproto.ExecTimeoutDefault.Seconds())
	}

	return min(asked, int(nodeproto.ExecTimeoutMax.Seconds()))
}

func orEmptyObject(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}

	return raw
}
