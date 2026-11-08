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
)

// Paths, relative to the control plane's URL.
const (
	PathHello  = "/node/v1/hello"
	PathSocket = "/node/v1/socket"
)

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
)

// Messages from the control plane.
const (
	TypeHeartbeatAck Type = "heartbeat_ack"
	TypeReportAck    Type = "report_ack"
	TypeOffer        Type = "dispatch_offer"
	TypeStop         Type = "stop"
	TypeAnswer       Type = "answer"
	TypeError        Type = "error"
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
	Claims        []Claim       `json:"claims"`
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
}

// HeartbeatAck answers a heartbeat with the interval to the next one.
type HeartbeatAck struct {
	HeartbeatAfterMs int64 `json:"heartbeat_after_ms"`
}

// Offer is a queued Dispatch for the node, with what it needs to prepare it.
// The node accepts it with a report of kind accept.
type Offer struct {
	DispatchID     string `json:"dispatch_id"`
	TaskID         string `json:"task_id"`
	RunID          string `json:"run_id"`
	Role           string `json:"role"`
	Spec           string `json:"spec"`
	Repo           string `json:"repo,omitempty"`
	Base           string `json:"base,omitempty"`
	ConversationID string `json:"conversation_id"`
	Worktree       string `json:"worktree"`
	Runtime        string `json:"runtime"`
	AccountRef     string `json:"account_ref"`
	CLISessionRef  string `json:"cli_session_ref,omitempty"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
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
	ReportProgress        = "progress"
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
