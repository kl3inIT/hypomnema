package agenttool

import (
	"fmt"
	"strings"
)

// The tools that run an agent's commands on a node's machine (issue #543),
// listed on PathTools for a worker and a Bot's pi.
const (
	// ToolNodeExec runs a command on a node, or starts it in the background.
	ToolNodeExec = "node_exec"
	// ToolNodeProcess says what a background command has come to.
	ToolNodeProcess = "node_process"
	// ToolNodeProcessKill kills a background command.
	ToolNodeProcessKill = "node_process_kill"
)

// NodeRereadable says whether a node tool's result is read again when the control plane restarts while it runs
// (issue #1060): node_exec's and node_process's. A kill is not asked again: it is the agent's to repeat.
func NodeRereadable(tool string) bool {
	return tool == ToolNodeExec || tool == ToolNodeProcess
}

// NodeExecArgs are ToolNodeExec's arguments. ProcessID is not in the tool's
// schema: the tools server sets it (nodeproto.NewProcessID) before it asks, so
// that the command's result can be read with ToolNodeProcess when the control
// plane restarts while it runs (issue #1060).
type NodeExecArgs struct {
	Node           string `json:"node"`
	Command        string `json:"command"`
	Cwd            string `json:"cwd,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Background     bool   `json:"background,omitempty"`
	ProcessID      string `json:"process_id,omitempty"`
}

// NodeProcessArgs are ToolNodeProcess's and ToolNodeProcessKill's arguments.
type NodeProcessArgs struct {
	Node        string `json:"node"`
	ProcessID   string `json:"process_id"`
	WaitSeconds int    `json:"wait_seconds,omitempty"`
}

// NodeNotConnected is the node tools' answer for a node the control plane
// knows that has no connection now, as when it reconnects after the control
// plane restarted; IsNodeNotConnected reads it back, for the tools server to
// ask again (issue #1060).
func NodeNotConnected(ref, connected string) string {
	return fmt.Sprintf("node %q %s; connected now: %s", ref, notConnected, connected)
}

// IsNodeNotConnected says whether a node tool's answer is NodeNotConnected's.
func IsNodeNotConnected(text string) bool {
	return strings.HasPrefix(text, "node ") && strings.Contains(text, notConnected+"; connected now:")
}

const notConnected = "is not connected"
