// Package nodeproto is the wire protocol between a node and the control plane
// (issue #32), shared by both binaries. It follows Omnara's daemon protocol
// (internal/daemonprotocol/protocol.go at 0ef1711): the node registers over
// HTTP, then holds one WebSocket on which every message is one JSON envelope
// {type, payload} of at most MaxMessageBytes. The node's reports carry a
// report id, are acknowledged one by one, and are resent until they are; the
// control plane's receipts make a resent report harmless.
package nodeproto

import (
	"encoding/json"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// Paths, relative to the control plane's URL.
const (
	PathHello  = "/node/v1/hello"
	PathSocket = "/node/v1/socket"
	// PathSkills is where a node fetches a skill's archive, by digest: only one
	// an offer it holds names.
	PathSkills = "/node/v1/skills/"
	// PathTerminal is the WebSocket a node opens for one terminal connection,
	// with the stream it was given as ?stream= (issue #116).
	PathTerminal = "/node/v1/terminal"
	// PathFiles is where a node posts the answer to a files_request, with the
	// request's id as ?request= (issue #255).
	PathFiles = "/node/v1/files"
	// PathDevice is where a node posts the answer to a device_call, with the
	// call's id as ?request= (issue #417), and the WebSocket it opens for a view
	// of its phone, with the stream it was given as ?stream= (step 3).
	PathDevice = "/node/v1/device"
	// PathExec is where a node posts the answer to an exec, with the request's
	// id as ?request= (issue #543).
	PathExec = "/node/v1/exec"
)

// DeviceAnswerWithin is how long the control plane waits for a node's answer
// to a device_call: a phone that is stopped boots first (5 minutes at most,
// internal/node/device.BootWithin), then the tool runs (2 minutes at most).
const DeviceAnswerWithin = 8 * time.Minute

// MaxDeviceAnswerBytes bounds a node's answer to a device_call: a screenshot
// as base64 is the largest.
const MaxDeviceAnswerBytes = 16 << 20

// FilesAnswerWithin is how long the control plane waits for a node's answer
// about a worktree: git's own limit, and a little more.
const FilesAnswerWithin = workspacefs.GitTimeout + 5*time.Second

// MaxFilesAnswerBytes bounds a node's answer: a file read whole, as JSON.
const MaxFilesAnswerBytes = 4*workspacefs.MaxRead + 1<<20

// An agent's command on a node (issue #543): how long one may run, and how
// long a background process's output is waited for at most (Nezha's
// server.exec: 30 s by default, 300 s at most).
const (
	ExecTimeoutDefault = 60 * time.Second
	ExecTimeoutMax     = 300 * time.Second
	ExecWaitMax        = 300 * time.Second
)

// ExecAnswerSlack is how much longer than a command's own time the control
// plane waits for the node's answer: the kill, and the post.
const ExecAnswerSlack = 30 * time.Second

// MaxExecAnswerBytes bounds a node's answer to an exec: an output of
// ExecOutputBytes as JSON, which may escape every byte.
const MaxExecAnswerBytes = 1 << 20

// ExecOutputBytes is how much of a command's output an answer holds: the
// first and the last half of it, as Coder's head-and-tail buffer keeps the
// start and the end (agent/agentproc/headtail.go), at Nezha's 64 KB.
const ExecOutputBytes = 64 << 10

// TerminalMeetWithin is how long the two ends of a terminal connection have
// to meet at the control plane (Nezha io_stream.go: 10 s).
const TerminalMeetWithin = 10 * time.Second

// Limits and intervals (Omnara client_socket.go, daemon_socket_hub.go).
const (
	MaxMessageBytes = 1 << 20
	// HeartbeatEvery is how often the node sends a heartbeat; the control
	// plane may set another interval in its heartbeat_ack.
	HeartbeatEvery = 10 * time.Second
	// PingEvery and PingTimeout bound the WebSocket ping.
	PingEvery   = 20 * time.Second
	PingTimeout = 10 * time.Second
	// ReadTimeout drops a connection that has been silent this long.
	ReadTimeout = 70 * time.Second
	// DrainEvery: the control plane looks for work to offer at least this often.
	DrainEvery = 30 * time.Second
)

// Type names a message.
type Type string

// Messages from the node.
const (
	TypeHeartbeat Type = "heartbeat"
	TypeReport    Type = "report"
	TypeItems     Type = "transcript_items"
)

// Messages from the control plane.
const (
	TypeHeartbeatAck Type = "heartbeat_ack"
	TypeReportAck    Type = "report_ack"
	TypeOffer        Type = "dispatch_offer"
	TypeStop         Type = "stop"
	TypeAnswer       Type = "answer"
	TypeItemsAck     Type = "transcript_items_ack"
	TypeError        Type = "error"
	// TypeTerminalOpen asks the node to open a terminal connection.
	TypeTerminalOpen Type = "terminal_open"
	// TypeFilesRequest asks the node about a worktree; it answers on PathFiles.
	TypeFilesRequest Type = "files_request"
	// TypeDeviceStart and TypeDeviceStop ask the node to start or stop the
	// phone it holds (issue #417). They carry nothing and are not answered:
	// the node says the phone's state at hello and in its heartbeats, at once
	// when it changes.
	TypeDeviceStart Type = "device_start"
	TypeDeviceStop  Type = "device_stop"
	// TypeDeviceCall runs one of an agent's phone tools on the node that holds
	// the phone (issue #417, step 2); the node answers on PathDevice, as it
	// answers a files_request.
	TypeDeviceCall Type = "device_call"
	// TypeDeviceView asks the node to present a view of its phone's screen for
	// the owner (issue #417, step 3); the node opens a connection to PathDevice,
	// as it does to PathTerminal for a terminal.
	TypeDeviceView Type = "device_view"
	// TypeExec runs a command on the node for an agent, or asks about or ends
	// one it runs in the background (issue #543); the node answers on
	// PathExec, as it answers a device_call.
	TypeExec Type = "exec"
)

// Message is one envelope.
type Message struct {
	Type    Type            `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Encode wraps a payload in an envelope.
func Encode(t Type, payload any) (Message, error) {
	if payload == nil {
		return Message{Type: t}, nil
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return Message{}, err
	}

	return Message{Type: t, Payload: raw}, nil
}

// RuntimeInfo is one CLI the node found on its PATH.
type RuntimeInfo struct {
	Runtime string `json:"runtime"`
	Path    string `json:"path"`
	Version string `json:"version"`
}

// SystemInfo is the machine the node runs on. A zero field was not found.
type SystemInfo struct {
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	CPUs        int    `json:"cpus"`
	MemoryBytes int64  `json:"memory_bytes"`
	// DiskBytes is the size of the volume that holds the worktree root.
	DiskBytes int64 `json:"disk_bytes"`
}

// SystemStats is what the node measured for one heartbeat.
type SystemStats struct {
	// CPUPercent is the share of all CPUs in use since the heartbeat before, 0 to 100.
	CPUPercent      float64 `json:"cpu_percent"`
	MemoryUsedBytes int64   `json:"memory_used_bytes"`
	// DiskUsedBytes is of the volume that holds the worktree root.
	DiskUsedBytes int64 `json:"disk_used_bytes"`
	// Load1 is the one-minute load average; zero where the system has none.
	Load1 float64 `json:"load1"`
	// GPUs are the machine's GPUs, by index; none for a machine without one.
	GPUs []GPUStats `json:"gpus,omitempty"`
}

// GPUStats is what one GPU measured, after Beszel's GPUData.
type GPUStats struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	// UsagePercent is how busy the GPU is, 0 to 100.
	UsagePercent     float64 `json:"usage_percent"`
	MemoryUsedBytes  int64   `json:"memory_used_bytes"`
	MemoryTotalBytes int64   `json:"memory_total_bytes"`
	// TemperatureCelsius and PowerWatts are nil when the GPU does not say.
	TemperatureCelsius *float64 `json:"temperature_celsius,omitempty"`
	PowerWatts         *float64 `json:"power_watts,omitempty"`
}

// Heartbeat is the node's heartbeat. A node that measures nothing sends none.
type Heartbeat struct {
	Stats *SystemStats `json:"stats,omitempty"`
	// Reaches lists the git hosts the node reached when it last tried, of
	// those the control plane named. Nil is a node that has not tried yet.
	Reaches *[]string `json:"reaches,omitempty"`
	// Tools lists the tools the control plane named, each with the version
	// the node has or why it could not install it. Nil is a node that has
	// not tried yet.
	Tools *[]ToolState `json:"tools,omitempty"`
	// Runtimes are the agent CLIs the node has now, when they changed since it
	// said hello: a runtime the control plane had it install is found once
	// installed (issue #107). Nil: unchanged.
	Runtimes *[]RuntimeInfo `json:"runtimes,omitempty"`
	// Device is the state of the phone the node holds (issue #417); nil for a
	// node that holds none.
	Device *DeviceState `json:"device,omitempty"`
}

// Tool is a command-line tool the control plane tells a node to have, for
// this node's system: one release file, pinned by its checksum (issue #103).
// The node installs it itself, under its own home; a worker never installs
// one.
type Tool struct {
	// Name is the command: gws.
	Name    string `json:"name"`
	Version string `json:"version"`
	// URL is the release file, a .tar.gz, fetched over HTTPS.
	URL string `json:"url"`
	// SHA256 is the checksum of that file, in hex. A file that does not match
	// is not unpacked.
	SHA256 string `json:"sha256"`
	// Binary is the path of the command inside the file.
	Binary string `json:"binary"`
}

// ToolState is what a node has of one Tool.
type ToolState struct {
	Name string `json:"name"`
	// Version is the version installed; empty when the install failed.
	Version string `json:"version,omitempty"`
	// Error says why the install failed.
	Error string `json:"error,omitempty"`
}

// Claim is a Dispatch the node still holds when it connects.
type Claim struct {
	DispatchID string `json:"dispatch_id"`
	Epoch      int    `json:"epoch"`
}

// HelloRequest is the node's registration, sent at every connect.
type HelloRequest struct {
	InstanceID    string        `json:"instance_id"`
	DaemonVersion string        `json:"daemon_version"`
	WorktreeRoot  string        `json:"worktree_root"`
	Runtimes      []RuntimeInfo `json:"runtimes"`
	System        SystemInfo    `json:"system"`
	Claims        []Claim       `json:"claims"`
	// Terminal says the node opens a terminal when asked: its build can, and
	// its own configuration has not turned it off.
	Terminal bool `json:"terminal,omitempty"`
	// Device is the device the node holds, as its own configuration says:
	// DeviceAndroid, or empty for none (issue #417).
	Device string `json:"device,omitempty"`
	// KVM says /dev/kvm can be read and written by the node's user, which an
	// Android emulator needs (issue #417).
	KVM bool `json:"kvm,omitempty"`
	// DeviceState is the state of the phone the node holds; nil for none.
	DeviceState *DeviceState `json:"device_state,omitempty"`
	// StoreGuides says the node writes no guide of an Integration or of the
	// phone itself: they come with an offer's skills (issue #536). A node of
	// an earlier release, which writes them, does not say it and is sent
	// none of the skills that require something (Skill.Requires).
	StoreGuides bool `json:"store_guides,omitempty"`
	// Held lists every Dispatch the node holds, when ListsHeld is set: the
	// ones in Claims, and the ones whose accept or final report is still
	// queued (Omnara's daemon claims its preparing and terminal processes too,
	// daemonprotocol/protocol.go:559-562). The control plane then ends as lost
	// a Dispatch of the node's that may still run and is in neither list
	// (issue #590). A node of an earlier release does not set ListsHeld, and
	// nothing is ended; a hello that only checks an install does not either.
	Held      []string `json:"held,omitempty"`
	ListsHeld bool     `json:"lists_held,omitempty"`
}

// DeviceAndroid is a node that holds an Android emulator (issue #417).
const DeviceAndroid = "android"

// The states of a node's phone (issue #417).
const (
	// DeviceAbsent: not installed yet; it is at the first start.
	DeviceAbsent     = "absent"
	DeviceInstalling = "installing"
	DeviceStopped    = "stopped"
	DeviceStarting   = "starting"
	DeviceRunning    = "running"
	// DeviceFailed: the last start failed, or the emulator ended by itself; Reason says why.
	DeviceFailed = "failed"
)

// DeviceState is the state of a node's phone, with the version of each
// component the node installed.
type DeviceState struct {
	State     string            `json:"state"`
	Reason    string            `json:"reason,omitempty"`
	Installed []DeviceComponent `json:"installed,omitempty"`
}

// DeviceComponent is one installed part of the phone: the emulator, the
// phone image, adb's platform-tools, Android CLI or scrcpy-server.
type DeviceComponent struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ValidTerminalSize says whether a terminal may have this size (Nezha
// terminal_session.go: 2 to 1000 columns, 2 to 500 rows).
func ValidTerminalSize(rows, cols int) bool {
	return rows >= 2 && rows <= 500 && cols >= 2 && cols <= 1000
}

// TerminalResize is the text message that changes a terminal's size, in
// Coder's field names. The browser sends it to the control plane and the
// control plane to the node.
type TerminalResize struct {
	Height int `json:"height"`
	Width  int `json:"width"`
}

// TerminalOpen asks the node to connect a terminal session. SessionID names
// the shell, which stays across connections; StreamID names this one
// connection, which the node presents when it dials PathTerminal.
type TerminalOpen struct {
	StreamID  string `json:"stream_id"`
	SessionID string `json:"session_id"`
	Rows      int    `json:"rows"`
	Cols      int    `json:"cols"`
}

// FilesRequest asks a node about a file or folder of one of its worktrees
// (issue #255). The answer is a workspacefs.Result, which the node posts to
// PathFiles rather than sending on the socket, whose messages are smaller
// than a file may be: as Omnigent's runner streams a response's body.
type FilesRequest struct {
	RequestID string              `json:"request_id"`
	Worktree  string              `json:"worktree"`
	Request   workspacefs.Request `json:"request"`
}

// DeviceCall is one call of a phone tool (internal/shared/devicetool) for an
// agent. Agent names the agent as the control plane knows it ("d-<dispatch>"),
// so that the node keeps each agent's last screen apart; it is no credential.
type DeviceCall struct {
	RequestID string          `json:"request_id"`
	Agent     string          `json:"agent"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// The actions of an Exec.
const (
	// ExecRun runs the command and answers when it ends, or when its time
	// is up and it was killed.
	ExecRun = "run"
	// ExecStart starts the command in the background and answers at once
	// with its ProcessID.
	ExecStart = "start"
	// ExecOutput answers about a background process: once it has ended, or
	// after WaitSeconds while it runs.
	ExecOutput = "output"
	// ExecKill kills a background process and what it started.
	ExecKill = "kill"
)

// Exec is one agent's command on a node (issue #543), or a question about
// one it started in the background. SudoPassword is the node's sudo
// password from the owner's vault, given to sudo through its askpass
// helper; it is never logged or answered.
type Exec struct {
	RequestID      string `json:"request_id"`
	Action         string `json:"action"`
	Command        string `json:"command,omitempty"`
	Cwd            string `json:"cwd,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	ProcessID      string `json:"process_id,omitempty"`
	WaitSeconds    int    `json:"wait_seconds,omitempty"`
	SudoPassword   string `json:"sudo_password,omitempty"`
}

// ExecAnswer is what came of an Exec. Output is stdout and stderr together,
// as a terminal shows them; CutBytes is how much of its middle was left out.
// A command that could not be started, or a process not known, is Error.
type ExecAnswer struct {
	ProcessID string `json:"process_id,omitempty"`
	Running   bool   `json:"running,omitempty"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	TimedOut  bool   `json:"timed_out,omitempty"`
	Output    string `json:"output,omitempty"`
	CutBytes  int64  `json:"cut_bytes,omitempty"`
	Error     string `json:"error,omitempty"`
}

// DeviceAnswer is a phone tool's result as MCP has it: what the tool says,
// text or an image, and whether it failed. A phone that could not be started
// is a result that failed, with the reason.
type DeviceAnswer struct {
	Content []DeviceContent `json:"content"`
	IsError bool            `json:"isError"`
}

// DeviceContent is one part of a DeviceAnswer: text, or a PNG as base64.
type DeviceContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// DeviceView asks the node to present a view of its phone's screen (issue
// #417, step 3) on a connection of its own to PathDevice, with StreamID as
// ?stream=, as a TerminalOpen asks for a terminal's: both ends meet within
// TerminalMeetWithin.
//
// On that connection the node sends the screen as binary messages, one H.264
// access unit each in Annex-B with serve-emu's 16-byte SEMU header before it
// (DeviceFrameHeader), a key frame with the stream's SPS and PPS before it, and
// a DeviceViewMessage of type "state" or "screen" as text. The control plane
// sends DeviceViewMessages of the input types as text: the owner's, while the
// owner holds the phone.
type DeviceView struct {
	StreamID string `json:"stream_id"`
}

// The types of a DeviceViewMessage. From the node: the phone's state, and the
// screen's size in pixels. To it: serve-emu's input messages (LLP 0008, and
// T3 Code's Android client): a touch at a point from 0 to 1 of the screen, an
// Android key code, a text, the four buttons, and a fresh key frame asked for.
const (
	DeviceViewState   = "state"
	DeviceViewScreen  = "screen"
	DeviceViewTouch   = "touch"
	DeviceViewKey     = "key"
	DeviceViewText    = "text"
	DeviceViewBack    = "back"
	DeviceViewHome    = "home"
	DeviceViewRecents = "recents"
	DeviceViewPower   = "power"
	DeviceViewReset   = "reset-video"
)

// DeviceViewMessage is one text message of a phone's view.
type DeviceViewMessage struct {
	Type string `json:"type"`
	// State and Reason are the phone's, for "state".
	State  string `json:"state,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Width and Height are the screen's, for "screen".
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	// Action is down, move or up, and X and Y the point from 0 to 1, for "touch".
	Action string  `json:"action,omitempty"`
	X      float64 `json:"x,omitempty"`
	Y      float64 `json:"y,omitempty"`
	// Keycode is Android's KEYCODE_*, for "key".
	Keycode int `json:"keycode,omitempty"`
	// Text is what is typed, for "text".
	Text string `json:"text,omitempty"`
}

// DeviceFrameHeader is the size of serve-emu's SEMU header (version 1): the
// magic "SEMU", the version 1, a flags byte whose bit 0 marks a key frame, two
// bytes of zero, and the frame's time in microseconds as a big-endian uint64.
const DeviceFrameHeader = 16

// MaxDeviceFrameBytes bounds one message of a phone's view: a frame, as
// serve-emu bounds a frame's payload (16 MiB), with its header and SPS/PPS.
const MaxDeviceFrameBytes = 16<<20 + 4096

// Disposition kinds: what the node does with a claimed Dispatch.
const (
	DispositionRetain   = "retain"
	DispositionReattach = "reattach"
	DispositionRelease  = "release"
)

// Disposition answers one Claim.
type Disposition struct {
	DispatchID string `json:"dispatch_id"`
	Kind       string `json:"kind"`
}

// HelloResponse answers the registration.
type HelloResponse struct {
	NodeID           string        `json:"node_id"`
	Dispositions     []Disposition `json:"dispositions"`
	HeartbeatAfterMs int64         `json:"heartbeat_after_ms"`
	// GitHosts are the git hosts the control plane knows; the node tries each
	// and says in its heartbeat which it reaches.
	GitHosts []string `json:"git_hosts,omitempty"`
	// Tools are the tools the node should have; it installs a missing one and
	// says in its heartbeat what it has.
	Tools []Tool `json:"tools,omitempty"`
	// ReleaseURL is where the node's releases are: the manifests and binaries
	// the install script downloads. A node that updates itself looks there.
	ReleaseURL string `json:"release_url,omitempty"`
	// ControlPlaneVersion is the control plane's own version. It updates itself
	// to each release within minutes, so a node older than it looks for its
	// release at once (issue #783, after Tailscale's ClientVersion).
	ControlPlaneVersion string `json:"control_plane_version,omitempty"`
}

// HeartbeatAck answers a heartbeat with the interval to the next one.
type HeartbeatAck struct {
	HeartbeatAfterMs int64 `json:"heartbeat_after_ms"`
	// GitHosts are the git hosts the control plane knows now.
	GitHosts []string `json:"git_hosts,omitempty"`
	// Tools are the tools the node should have now.
	Tools []Tool `json:"tools,omitempty"`
}

// Offer is a queued Dispatch for the node, with what it needs to prepare it.
// The node accepts it with a report of kind accept.
type Offer struct {
	DispatchID string `json:"dispatch_id"`
	TaskID     string `json:"task_id"`
	RunID      string `json:"run_id"`
	Profile    string `json:"profile"`
	// Instruction is the profile's, added to the worker's built-in instructions.
	Instruction string `json:"instruction,omitempty"`
	// Memory is what the agent keeps across its workers, and OwnerMemory what
	// every worker and Bot is told about the owner (issue #21): entries one per
	// line, as they were when the offer was made.
	Memory      string `json:"memory,omitempty"`
	OwnerMemory string `json:"owner_memory,omitempty"`
	Spec        string `json:"spec"`
	Repo        string `json:"repo,omitempty"`
	// RepoHost and RepoKind say where Repo is and how that host is used
	// (internal/shared/githost).
	RepoHost string `json:"repo_host,omitempty"`
	RepoKind string `json:"repo_kind,omitempty"`
	// RepoToken is the host's token for this Dispatch, when the owner gave the
	// host one: git, gh and glab of this worker use it, and nothing else. The
	// node keeps it with the offer until the Dispatch ends; there is no proxy
	// on the node (docs/decisions/2026-11-13-worker-credentials-in-the-environment.md).
	RepoToken string `json:"repo_token,omitempty"`
	// RepoSSHURL is where the node clones and fetches Repo from over SSH,
	// when the owner registered it with one (issue #625). Git then runs
	// `hypomnema-node gitssh` as its ssh, which asks the agent API for the
	// control plane's key with AgentKey on each call; the node keeps no key.
	RepoSSHURL string `json:"repo_ssh_url,omitempty"`
	// ModelKey is the model gateway's key for this Dispatch, when AccountRef
	// is "gateway": the worker's CLI calls <control plane URL>/v1 with it (a
	// Claude Code worker its /v1/messages), for the offer's Model only. It is made as the offer is sent and revoked when
	// the Dispatch settles; the node keeps it with the offer until then.
	ModelKey string `json:"model_key,omitempty"`
	// ModelAPI is the wire a worker that can choose one speaks to the gateway for the model: "anthropic-messages"
	// for a Claude subscription's, "openai-responses" for a Codex subscription's, empty for chat (issue #549).
	ModelAPI string `json:"model_api,omitempty"`
	// Uses are the Connections the Run's workers may use ("google:work");
	// AgentKey is this Dispatch's own key to the control plane's agent API,
	// with which the tool launcher asks for a token of one of them. It is
	// revoked when the Dispatch settles.
	Uses     []string `json:"uses,omitempty"`
	AgentKey string   `json:"agent_key,omitempty"`
	// Device is the device the control plane gives every worker, DeviceAndroid
	// while a node holds a phone (issue #417), as it gives the shared browser;
	// the worker is then told of the phone's tools and their guide.
	Device         string `json:"device,omitempty"`
	Base           string `json:"base,omitempty"`
	ConversationID string `json:"conversation_id"`
	Worktree       string `json:"worktree"`
	Runtime        string `json:"runtime"`
	AccountRef     string `json:"account_ref"`
	CLISessionRef  string `json:"cli_session_ref,omitempty"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	// Skills are the skills the worker is given (issue #21). The node fetches
	// each revision it does not have from PathSkills/<digest> and puts it
	// where the worker's CLI reads skills.
	Skills []Skill `json:"skills,omitempty"`
	// Tools are the names of Hypomnema's tools the worker gets, after its
	// profile's switches, and the commands it reaches an account with
	// through the service proxy (issue #723, decision 2.3; issue #789): the
	// node writes the "Tools" block of its rules from them
	// (agenttool.ToolGuide). A node of an earlier release ignores them.
	Tools []string `json:"tools,omitempty"`
}

// Skill is one skill a worker is given, at one revision; its digest names
// its files (internal/shared/skillpack).
type Skill struct {
	Name     string `json:"name"`
	Revision int    `json:"revision"`
	Digest   string `json:"digest"`
	// Requires is what the agent had to have to be given it
	// ("integration:superset", issue #536): an Integration's guide is one. A
	// node that writes guides of its own is not sent one
	// (HelloRequest.StoreGuides).
	Requires []string `json:"requires,omitempty"`
}

// Stop asks the node to stop a Dispatch's worker.
type Stop struct {
	DispatchID string `json:"dispatch_id"`
	Epoch      int    `json:"epoch"`
}

// Report kinds: the node calls of the orchestration core (issue #19).
const (
	ReportAccept          = "accept"
	ReportStarted         = "started"
	ReportHeartbeat       = "heartbeat"
	ReportEffect          = "effect"
	ReportResource        = "resource"
	ReportReleaseResource = "release_resource"
	ReportQuestion        = "question"
	ReportAnswerAck       = "answer_ack"
	ReportFinished        = "finished"
	ReportExited          = "exited"
	ReportStopped         = "stopped"
	ReportReattach        = "reattach"
)

// Report is one node call about a Dispatch. ReportID is its request id: a
// report sent again with the same id and payload gets the same answer.
type Report struct {
	ReportID   string          `json:"report_id"`
	Kind       string          `json:"kind"`
	DispatchID string          `json:"dispatch_id"`
	Epoch      int             `json:"epoch"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

// Acknowledgement statuses (Omnara).
const (
	// AckCommitted: the report was applied, now or before; Response is its answer.
	AckCommitted = "committed"
	// AckRejected: the report can never apply (a stale epoch, a settled
	// Dispatch, an invalid payload); do not resend it.
	AckRejected = "permanent_reject"
	// AckTransient: it failed for a reason that may pass; resend it.
	AckTransient = "transient_error"
)

// ReportAck answers one report.
type ReportAck struct {
	ReportID string          `json:"report_id"`
	Status   string          `json:"status"`
	Response json.RawMessage `json:"response,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// Error is sent before the control plane closes the socket.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error codes.
const (
	// ErrorReplaced: another connection of the same node took over.
	ErrorReplaced = "presence_replaced"
	ErrorInvalid  = "invalid_message"
)

// Started is the payload of a started report.
type Started struct {
	ProcessRef    string `json:"process_ref"`
	CLISessionRef string `json:"cli_session_ref"`
}

// Effect is the payload of an effect report.
type Effect struct {
	Seq     int    `json:"seq"`
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	Phase   string `json:"phase"`
	Payload string `json:"payload,omitempty"`
}

// Resource is the payload of a resource or release_resource report.
type Resource struct {
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	Custody string `json:"custody,omitempty"`
}

// Option is one choice of a question.
type Option struct {
	Label      string `json:"label"`
	AllowsText bool   `json:"allows_text,omitempty"`
}

// Ask is the payload of a question report, and a Gate in a finished one.
type Ask struct {
	Text    string   `json:"text"`
	Options []Option `json:"options,omitempty"`
	// To is "coordinator" or "owner".
	To string `json:"to"`
}

// AnswerAck is the payload of an answer_ack report.
type AnswerAck struct {
	QuestionID string `json:"question_id"`
}

// Evidence is one piece of proof in a result, such as a pull request.
type Evidence struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// Exit is how the worker process ended.
type Exit struct {
	Kind   string `json:"kind"`
	Code   *int   `json:"code,omitempty"`
	Signal string `json:"signal,omitempty"`
}

// Usage is what the worker spent.
type Usage struct {
	Input      int64 `json:"input,omitempty"`
	Output     int64 `json:"output,omitempty"`
	CacheRead  int64 `json:"cache_read,omitempty"`
	CacheWrite int64 `json:"cache_write,omitempty"`
}

// Finished is the payload of a finished report. Class is empty for success,
// else a failure class (start_failed, needs_auth, rate_limited, worker_error).
type Finished struct {
	Class    string     `json:"class,omitempty"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	Summary  string     `json:"summary"`
	Evidence []Evidence `json:"evidence,omitempty"`
	Exit     Exit       `json:"exit"`
	Usage    *Usage     `json:"usage,omitempty"`
	Gate     *Ask       `json:"gate,omitempty"`
}

// Reattach is the payload of a reattach report.
type Reattach struct {
	ProcessRef string `json:"process_ref"`
}

// Answer is the owner's or the coordinator's answer: an option, a text, or both.
type Answer struct {
	Option *int   `json:"option,omitempty"`
	Text   string `json:"text,omitempty"`
}

// Question is the payload of an answer message: an answered question a
// worker waits for. The node passes it on, then acknowledges it with an
// answer_ack report.
type Question struct {
	ID          string   `json:"id"`
	DispatchID  string   `json:"dispatch_id"`
	Mode        string   `json:"mode"`
	AddressedTo string   `json:"addressed_to"`
	Text        string   `json:"text"`
	Options     []Option `json:"options"`
	Status      string   `json:"status"`
	Answer      *Answer  `json:"answer,omitempty"`
	AnsweredBy  string   `json:"answered_by,omitempty"`
}

// Accepted is the response to an accept or reattach report.
type Accepted struct {
	Epoch          int       `json:"epoch"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	Recovery       bool      `json:"recovery"`
	CLISessionRef  string    `json:"cli_session_ref,omitempty"`
	// Gate is the answered decision the Dispatch resumes from.
	Gate *Question `json:"gate,omitempty"`
	// FollowUp is more work the coordinator or the owner asked of a Task that
	// had succeeded (issue #666); the worker continues with it.
	FollowUp string `json:"follow_up,omitempty"`
}

// Beat is the response to a heartbeat report.
type Beat struct {
	LeaseExpiresAt time.Time  `json:"lease_expires_at"`
	StopRequested  bool       `json:"stop_requested"`
	Answers        []Question `json:"answers"`
}

// Asked is the response to a question report.
type Asked struct {
	QuestionID string `json:"question_id"`
}

// A worker's transcript (issue #43): what it said and did, as items the node
// sends in order while the worker runs and after. The node reads them from the
// worker's journal with a cursor that moves when the control plane
// acknowledges a batch, as Omnigent's forwarder
// (omnigent/harnesses/claude_native/forwarder.py,
// runner/transports/ws_tunnel/event_delivery.py at cf1021e).

// Item kinds, the same for every runtime.
const (
	// ItemMessage is the worker's text.
	ItemMessage = "message"
	// ItemReasoning is its thinking, when the runtime gives it.
	ItemReasoning = "reasoning"
	// ItemToolCall is a tool it called: CallID, Tool and Input.
	ItemToolCall = "tool_call"
	// ItemToolResult is what the call answered: CallID, Text and Failed.
	ItemToolResult = "tool_result"
	// ItemNotice is something of the runtime, such as a rate limit.
	ItemNotice = "notice"
)

// Bounds of a transcript batch. One batch is at most MaxBatchItems items and
// MaxBatchBytes (Omnigent: 32 events, 256 KiB); the node cuts an item to
// MaxItemBytes (inferred; Omnara keeps 50 KiB of a tool result inline).
const (
	MaxItemBytes  = 64 << 10
	MaxBatchItems = 32
	MaxBatchBytes = 256 << 10
)

// Item is one entry of a worker's transcript.
type Item struct {
	// Seq numbers a Dispatch's items from 1.
	Seq    int64           `json:"seq"`
	Kind   string          `json:"kind"`
	Text   string          `json:"text,omitempty"`
	CallID string          `json:"call_id,omitempty"`
	Tool   string          `json:"tool,omitempty"`
	Input  json.RawMessage `json:"input,omitempty"`
	Failed bool            `json:"failed,omitempty"`
}

// Items is the payload of a transcript_items message: the next items of one
// Dispatch, in order.
type Items struct {
	DispatchID string `json:"dispatch_id"`
	Items      []Item `json:"items"`
}

// ItemsAck answers a transcript_items message. Seq is the newest item the
// control plane holds for the Dispatch; Status is an acknowledgement status.
type ItemsAck struct {
	DispatchID string `json:"dispatch_id"`
	Seq        int64  `json:"seq"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}
