package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// The tools that run an agent's commands on a node's machine (issue #543):
// one definition in the control plane for a worker and a Bot's pi, as
// OpenClaw's exec takes a node, Omnara's run_command a machine, Nezha's
// server.exec a server and Coder's workspace_execute a workspace, each chosen
// per call. A command runs at once, with no approval, as theirs do by
// default (owner, 2026-11-15). A node's sudo password is the owner's vault's
// and goes to the node with the command; the agent never sees it.

// NodeLink reaches a node on its socket (nodeapi.Server).
type NodeLink interface {
	Exec(ctx context.Context, nodeID string, e nodeproto.Exec) (nodeproto.ExecAnswer, error)
}

// Nodes lists the nodes.
type Nodes interface {
	ListNodes(ctx context.Context) ([]orchestration.Node, error)
}

// SudoPassword is a node's sudo password from the owner's vault; "" when the
// owner keeps none for it.
type SudoPassword func(ctx context.Context, node orchestration.Node) (string, error)

// nodeExec is what the node tools need.
type nodeExec struct {
	nodes Nodes
	link  NodeLink
	sudo  SudoPassword
}

// WithNodeExec wires the node tools for the owner, workers and Bots' pis.
func (s *Server) WithNodeExec(nodes Nodes, link NodeLink, sudo SudoPassword) *Server {
	s.exec = &nodeExec{nodes: nodes, link: link, sudo: sudo}

	return s
}

// hasNodeTools says whether the caller gets the installed node tools.
func (s *Server) hasNodeTools(a agent) bool {
	return s.exec != nil && a.hasAgentTools()
}

var nodeTools = []string{agenttool.ToolNodeExec, agenttool.ToolNodeProcess, agenttool.ToolNodeProcessKill}

// nodeToolList is the node tools' definitions; none when they cannot be
// written.
func nodeToolList() []agenttool.BrowserTool {
	node := map[string]any{"type": "string", "description": "The node's name, as the Machines page shows it, or its id."}

	execSchema, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"node":    node,
			"command": map[string]any{"type": "string", "description": "A shell command, run with sh -c."},
			"cwd":     map[string]any{"type": "string", "description": "Where it runs; the node user's home when left out."},
			"timeout_seconds": map[string]any{
				"type": "integer", "minimum": 1, "maximum": int(nodeproto.ExecTimeoutMax.Seconds()),
				"description": "When to kill it; 60 when left out. Ignored in the background.",
			},
			"background": map[string]any{
				"type": "boolean", "description": "Start it and answer at once with its process_id; it runs with " +
					"no time limit until it ends or is killed.",
			},
		},
		"required": []string{"node", "command"},
	})
	if err != nil {
		return nil
	}

	processSchema, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"node": node,
			"process_id": map[string]any{
				"type": "string", "description": "As node_exec answered it; left out, your commands on the node " +
					"are listed.",
			},
			"wait_seconds": map[string]any{
				"type": "integer", "minimum": 0, "maximum": int(nodeproto.ExecWaitMax.Seconds()),
				"description": "Wait up to this long for it to end before answering; 0 answers at once.",
			},
		},
		"required": []string{"node"},
	})
	if err != nil {
		return nil
	}

	killSchema, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"node":       node,
			"process_id": map[string]any{"type": "string", "description": "As node_exec answered it."},
		},
		"required": []string{"node", "process_id"},
	})
	if err != nil {
		return nil
	}

	return []agenttool.BrowserTool{
		{
			Name: agenttool.ToolNodeExec,
			Description: "Run a shell command on a node's machine, as its node's user would over SSH, and get " +
				"its exit code and output (stdout and stderr together; the first and the last 32 KB of a longer " +
				"one). Use it for work on another machine of Hypomnema's; for the machine you are on, use your own " +
				"shell instead. It runs at once, without asking anyone, so check what a command removes before running " +
				"it. sudo works without a prompt where the owner keeps the node's sudo password; the password " +
				"is never shown to you. Nothing waits for input: use -y and non-interactive flags. A command " +
				"that takes longer than five minutes runs in the background (background: true): read it with " +
				"node_process and stop it with node_process_kill. It ends if the node restarts, as when it " +
				"updates; a restart of the control plane does not end it, and its result is read again for " +
				"you (if the restart cut your turn, node_process without a process_id lists it). Commands run one per call with no state kept between them: cd or export in one does " +
				"not carry to the next.",
			InputSchema: execSchema,
		},
		{
			Name: agenttool.ToolNodeProcess,
			Description: "Say what a command node_exec started in the background has come to: still running, " +
				"or its exit code, with its output so far. Use it to follow such a command instead of starting it " +
				"again. An ended one is kept for an hour. Without a process_id it lists your commands on the node " +
				"(the last 10, running first, with their ids): use that to find one a restart of the control " +
				"plane cut your turn off from, before running it again.",
			InputSchema: processSchema,
		},
		{
			Name: agenttool.ToolNodeProcessKill,
			Description: "Kill a command node_exec started in the background, and what it started. Use it for " +
				"one that is no longer wanted or has hung; one that ended needs nothing.",
			InputSchema: killSchema,
		},
	}
}

// nodeCall runs one of the node tools.
func (s *Server) nodeCall(w http.ResponseWriter, r *http.Request, a agent, asked agenttool.BrowserCall) {
	ctx := r.Context()

	var e nodeproto.Exec

	var nodeRef string

	switch asked.Tool {
	case agenttool.ToolNodeExec:
		var args agenttool.NodeExecArgs
		if json.Unmarshal(orEmpty(asked.Arguments), &args) != nil || strings.TrimSpace(args.Command) == "" {
			answerTool(w, "node_exec takes {node, command, cwd, timeout_seconds, background}", true)

			return
		}

		// A process id its tools server chose names the command on the node, for its result to be read again
		// when this answer is lost (issue #1060); one of another shape is the node's to make.
		if args.ProcessID != "" && !nodeproto.IsProcessID(args.ProcessID) {
			answerTool(w, "node_exec's process_id is the tools server's, as proc_ and hex", true)

			return
		}

		nodeRef = args.Node
		e = nodeproto.Exec{Action: nodeproto.ExecRun, Command: args.Command, Cwd: args.Cwd, ProcessID: args.ProcessID}

		if args.Background {
			e.Action = nodeproto.ExecStart
		} else {
			e.TimeoutSeconds = clampSeconds(args.TimeoutSeconds, nodeproto.ExecTimeoutDefault.Seconds(),
				nodeproto.ExecTimeoutMax.Seconds())
		}
	default:
		var args agenttool.NodeProcessArgs
		if json.Unmarshal(orEmpty(asked.Arguments), &args) != nil ||
			(args.ProcessID == "" && asked.Tool != agenttool.ToolNodeProcess) {
			answerTool(w, asked.Tool+" takes {node, process_id}", true)

			return
		}

		nodeRef = args.Node
		e = nodeproto.Exec{Action: nodeproto.ExecKill, ProcessID: args.ProcessID}

		switch {
		case asked.Tool == agenttool.ToolNodeProcess && args.ProcessID == "":
			// The agent's own commands on the node, for a turn a restart cut to find the one it ran (issue
			// #1060), as Coder's process list (agent/agentproc/api.go handleListProcesses).
			e.Action = nodeproto.ExecList
		case asked.Tool == agenttool.ToolNodeProcess:
			e.Action = nodeproto.ExecOutput
			e.WaitSeconds = clampSeconds(args.WaitSeconds, 0, nodeproto.ExecWaitMax.Seconds())
		}
	}

	// A process is the agent's that ran it, a chat's or a worker's (issue #1060), as a phone's last screen is.
	e.Agent = a.key

	n, connected, err := s.findNode(ctx, nodeRef)
	if err != nil {
		answerTool(w, err.Error(), true)

		return
	}

	if e.Command != "" {
		password, err := s.exec.sudo(ctx, n)
		if err != nil {
			s.logger.ErrorContext(ctx, "a node's sudo password could not be read",
				slog.String("event", "agentapi.sudo_failed"), slog.String("node", n.Name),
				slog.String("error", err.Error()))
		}

		e.SudoPassword = password
	}

	s.logger.InfoContext(ctx, "an agent's command on a node", slog.String("event", "agentapi.node_exec"),
		slog.String("agent", a.name), slog.String("node", n.Name), slog.String("action", e.Action))

	got, err := s.exec.link.Exec(ctx, n.ID, e)
	if err != nil {
		// A node listed as connected whose socket is not back yet, as right after a restart of the control plane,
		// is answered as one not connected, for the tools server to ask again (issue #1060).
		if errors.Is(err, orchestration.ErrNodeOffline) {
			answerTool(w, agenttool.NodeNotConnected(nodeRef, connected), true)

			return
		}

		answerTool(w, err.Error(), true)

		return
	}

	if e.Action == nodeproto.ExecList && got.Error == "" {
		answerTool(w, listText(got.Processes), false)

		return
	}

	text, failed := execText(got)
	answerTool(w, text, failed)
}

// listText is ExecList's answer as the agent reads it: a line a process.
func listText(processes []nodeproto.ExecProcess) string {
	if len(processes) == 0 {
		return "no process of yours on this node"
	}

	var b strings.Builder

	for i, p := range processes {
		if i > 0 {
			b.WriteString("\n")
		}

		state := "running"
		if !p.Running && p.ExitCode != nil {
			state = "exit code " + strconv.Itoa(*p.ExitCode)
		}

		fmt.Fprintf(&b, "process_id %s, %s, started %s: %s", p.ProcessID, state,
			time.UnixMilli(p.StartedAt).UTC().Format(time.RFC3339), p.Command)
	}

	return b.String()
}

// findNode is the node named, by name or id, and the names of those connected; one that is not connected is
// refused with them.
func (s *Server) findNode(ctx context.Context, ref string) (orchestration.Node, string, error) {
	nodes, err := s.exec.nodes.ListNodes(ctx)
	if err != nil {
		return orchestration.Node{}, "", errors.New("the nodes could not be listed")
	}

	var connected []string

	for _, n := range nodes {
		if n.Connected {
			connected = append(connected, n.Name)
		}
	}

	slices.Sort(connected)

	i := slices.IndexFunc(nodes, func(n orchestration.Node) bool { return n.Name == ref || n.ID == ref })
	names := strings.Join(connected, ", ")

	switch {
	case i < 0:
		return orchestration.Node{}, names, fmt.Errorf("no node %q; connected now: %s", ref, names)
	case !nodes[i].Connected:
		return orchestration.Node{}, names, errors.New(agenttool.NodeNotConnected(ref, names))
	default:
		return nodes[i], names, nil
	}
}

// execText is a node's answer as the agent reads it, and whether it failed:
// an error, a time that ran out, or an exit code other than 0.
func execText(got nodeproto.ExecAnswer) (string, bool) {
	if got.Error != "" {
		return got.Error, true
	}

	var b strings.Builder

	failed := false

	switch {
	case got.Running && got.ProcessID != "":
		b.WriteString("running in the background, process_id " + got.ProcessID)
	case got.TimedOut:
		b.WriteString("killed: its time ran out")

		failed = true
	case got.ExitCode != nil:
		b.WriteString("exit code " + strconv.Itoa(*got.ExitCode))

		failed = *got.ExitCode != 0
	}

	if got.ProcessID != "" && !got.Running {
		b.WriteString(" (process_id " + got.ProcessID + ")")
	}

	switch {
	case got.CutBytes > 0:
		fmt.Fprintf(&b, "\noutput, %d bytes in the middle left out:\n%s", got.CutBytes, got.Output)
	case got.Output != "":
		b.WriteString("\noutput:\n" + got.Output)
	case !got.Running:
		b.WriteString("\nno output")
	}

	return b.String(), failed
}

// clampSeconds is asked, at most most, and fallback when not given.
func clampSeconds(asked int, fallback, most float64) int {
	if asked <= 0 {
		return int(fallback)
	}

	return min(asked, int(most))
}
