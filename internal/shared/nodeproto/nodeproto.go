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
)

// FilesAnswerWithin is how long the control plane waits for a node's answer
// about a worktree: git's own limit, and a little more.
const FilesAnswerWithin = workspacefs.GitTimeout + 5*time.Second

// MaxFilesAnswerBytes bounds a node's answer: a file read whole, as JSON.
const MaxFilesAnswerBytes = 4*workspacefs.MaxRead + 1<<20

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
}

// DeviceAndroid is a node that holds an Android emulator (issue #417).
const DeviceAndroid = "android"

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
	// ModelKey is the model gateway's key for this Dispatch, when AccountRef
	// is "gateway": the worker's CLI calls <control plane URL>/v1 with it (a
	// Claude Code worker its /v1/messages), for the offer's Model only. It is made as the offer is sent and revoked when
	// the Dispatch settles; the node keeps it with the offer until then.
	ModelKey string `json:"model_key,omitempty"`
	// AccountToken is the token of the account the worker runs under, when the
	// control plane keeps it: a Claude Code worker gets it as
	// CLAUDE_CODE_OAUTH_TOKEN. Without it the node uses the token file of its
	// own configuration. The node keeps it with the offer until the Dispatch ends.
	AccountToken string `json:"account_token,omitempty"`
	// Uses are the Connections the Run's workers may use ("google:work");
	// AgentKey is this Dispatch's own key to the control plane's agent API,
	// with which the tool launcher asks for a token of one of them. It is
	// revoked when the Dispatch settles.
	Uses           []string `json:"uses,omitempty"`
	AgentKey       string   `json:"agent_key,omitempty"`
	Base           string   `json:"base,omitempty"`
	ConversationID string   `json:"conversation_id"`
	Worktree       string   `json:"worktree"`
	Runtime        string   `json:"runtime"`
	AccountRef     string   `json:"account_ref"`
	CLISessionRef  string   `json:"cli_session_ref,omitempty"`
	Model          string   `json:"model,omitempty"`
	Effort         string   `json:"effort,omitempty"`
	// Skills are the skills the worker is given (issue #21). The node fetches
	// each revision it does not have from PathSkills/<digest> and puts it
	// where the worker's CLI reads skills.
	Skills []Skill `json:"skills,omitempty"`
}

// Skill is one skill a worker is given, at one revision; its digest names
// its files (internal/shared/skillpack).
type Skill struct {
	Name     string `json:"name"`
	Revision int    `json:"revision"`
	Digest   string `json:"digest"`
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
