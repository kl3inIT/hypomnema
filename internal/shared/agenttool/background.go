package agenttool

// PathBackground is where a Bot's pi runs and stops its background commands (issues #1058, #1084): the box's
// pi-background extension posts a BackgroundRequest with pi's own token. The control plane starts the command as a
// unit of its own outside pi's box, which a restart of the control plane leaves running, and wakes the chat when it
// ends, as OpenClaw's durable exec does (docs/tools/exec.md:94-100, 171-173).
const PathBackground = "/agent/v1/background"

// The kinds of BackgroundRequest.
const (
	BackgroundStart = "start"
	BackgroundStop  = "stop"
)

// BackgroundRequest starts a command in the background or stops one.
type BackgroundRequest struct {
	Kind string `json:"kind"`
	// Command is the script to start, pi's shell prefix included; Cwd is where it starts, the Bot's directory or
	// below; TimeoutSeconds is its deadline, the default when zero.
	Command        string `json:"command,omitempty"`
	Cwd            string `json:"cwd,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	// ID is the command to stop.
	ID string `json:"id,omitempty"`
}

// BackgroundAnswer is what a started command is called, the file its output goes to and its deadline; for a stop,
// whether it was running.
type BackgroundAnswer struct {
	ID             string `json:"id"`
	OutputFile     string `json:"output_file,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Stopped        bool   `json:"stopped,omitempty"`
}
