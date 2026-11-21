package agenttool

// ToolRoutines is a Bot's Routines as one tool (issues #211, #536), after
// Hermes's cronjob_manage (tools/cronjob_tools.py, CRONJOB_SCHEMA: one tool,
// an action) and OpenClaw's cron tool (src/agents/tools/cron-tool.ts): list,
// set (make or change one) and run. Only a Bot's pi gets it: a worker never
// does, as Hermes keeps cronjob_manage from its sub-agents
// (tools/delegate_tool_toolsets.py, DELEGATE_BLOCKED_TOOLS). The control plane
// lists and runs it on PathTools and PathToolsCall; a script is a file of the
// folder the pi works in, which its tools server reads into ScriptText
// (internal/shared/toolsmcp/routine.go), as show_artifact's reads its path.
const ToolRoutines = "routines"

// ToolRequestConnection is a Bot's ask for a Connection it was not given (issue #471, part D, the grant card): the
// owner answers on a card in its main chat, with Always for this Bot or Decline. Only a Bot's pi in its main chat
// gets it, with the names of the Connections it may ask for, as OpenMausBot's bot is told which apps it was assigned
// and asks for one with COMPOSIO_MANAGE_CONNECTIONS (server/cloud-connections-prompt.ts); it returns at once, as
// t3code's t3_environment_link returns pending (apps/server/src/mcp/toolkits/orchestrator/tools.ts).
const ToolRequestConnection = "request_connection"

// RequestConnectionCall is the arguments of a request_connection call.
type RequestConnectionCall struct {
	Use    string `json:"use"`
	Reason string `json:"reason"`
}

// The actions of ToolRoutines.
const (
	RoutinesList = "list"
	RoutinesSet  = "set"
	RoutinesRun  = "run"
)

// MaxRoutineScript bounds the script file a tools server reads, as the
// control plane bounds a Routine's script.
const MaxRoutineScript = 100_000

// RoutineCall is the arguments of a routines call as the control plane takes
// them: the agent's own, and the text its tools server read from Script. A
// field left out is nil: set keeps what the Routine has.
type RoutineCall struct {
	Action         string  `json:"action"`
	Name           string  `json:"name,omitempty"`
	EveryMinutes   *int    `json:"every_minutes,omitempty"`
	Cron           *string `json:"cron,omitempty"`
	Timezone       *string `json:"timezone,omitempty"`
	Prompt         *string `json:"prompt,omitempty"`
	Script         *string `json:"script,omitempty"`
	TimeoutSeconds *int    `json:"timeout_seconds,omitempty"`
	Summary        *string `json:"summary,omitempty"`
	Quiet          *bool   `json:"quiet,omitempty"`
	// ScriptText is what the tools server read from Script, never the agent's.
	ScriptText *string `json:"script_text,omitempty"`
}
