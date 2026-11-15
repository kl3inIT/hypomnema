package agenttool

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

// NodeExecArgs are ToolNodeExec's arguments.
type NodeExecArgs struct {
	Node           string `json:"node"`
	Command        string `json:"command"`
	Cwd            string `json:"cwd,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Background     bool   `json:"background,omitempty"`
}

// NodeProcessArgs are ToolNodeProcess's and ToolNodeProcessKill's arguments.
type NodeProcessArgs struct {
	Node        string `json:"node"`
	ProcessID   string `json:"process_id"`
	WaitSeconds int    `json:"wait_seconds,omitempty"`
}
