package devicetool

import "time"

// A take (issue #817): what an agent filmed of the phone, one or more video
// segments and the steps it said, timed on one clock. The node writes it in
// take.json and the control plane keeps it once uploaded; both binaries read
// it as this file says.

// The states of a take.
const (
	TakeRecording  = "recording"
	TakePaused     = "paused"
	TakeFinalizing = "finalizing"
	TakeUploading  = "uploading"
	TakeAvailable  = "available"
	TakeFailed     = "failed"
)

// What ended a take.
const (
	EndedStop        = "stop"
	EndedWorkEnd     = "work_end"
	EndedPhoneEnd    = "phone_end"
	EndedStreamError = "stream_error"
	EndedWriterError = "writer_error"
	EndedDiskLow     = "disk_low"
	EndedRestart     = "restart"
)

// EndedBy are the ends of a take.
var EndedBy = []string{
	EndedStop, EndedWorkEnd, EndedPhoneEnd, EndedStreamError, EndedWriterError, EndedDiskLow, EndedRestart,
}

// Take is a take as take.json holds it, on the node and, once uploaded, on
// the control plane (issue #817, row 10): its segments in order, each placed
// on the take's timeline, the steps said, and where the owner paused it.
type Take struct {
	ID         string        `json:"id"`
	Title      string        `json:"title"`
	Agent      string        `json:"agent"`
	AgentName  string        `json:"agent_name,omitempty"`
	Node       string        `json:"node,omitempty"`
	RequestID  string        `json:"request_id,omitempty"`
	AppPackage string        `json:"app_package,omitempty"`
	AppVersion string        `json:"app_version,omitempty"`
	StartedAt  time.Time     `json:"started_at"`
	State      string        `json:"state"`
	EndedBy    string        `json:"ended_by,omitempty"`
	Reason     string        `json:"reason,omitempty"`
	DurationMs int64         `json:"duration_ms"`
	Segments   []TakeSegment `json:"segments"`
	Steps      []TakeStep    `json:"steps"`
	Pauses     []TakePause   `json:"pauses"`
}

// TakeSegment is one segment's file, where it starts on the take's timeline and how long it lasts.
// LastFrameMs is when its last frame starts, from the segment's start: the screen is still after it until the
// segment ends, and a player seeks into that still tail from just before it, as a browser cannot seek into a
// file's last sample.
type TakeSegment struct {
	File        string `json:"file"`
	AtMs        int64  `json:"at_ms"`
	LengthMs    int64  `json:"length_ms"`
	LastFrameMs int64  `json:"last_frame_ms"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"sha256"`
}

// TakeStep is one line said, when on the take's timeline, with where the agent was about to tap.
type TakeStep struct {
	AtMs    int64    `json:"at_ms"`
	Text    string   `json:"text"`
	Tap     *TakeTap `json:"tap,omitempty"`
	Chapter string   `json:"chapter,omitempty"`
}

// TakeTap is a point of the screen, in its pixels.
type TakeTap struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// TakePause is where the owner's takeover paused a take, on its timeline.
type TakePause struct {
	AtMs int64 `json:"at_ms"`
}

// Final says whether a take's state no longer changes on the node.
func (t Take) Final() bool {
	return t.State == TakeUploading || t.State == TakeAvailable || t.State == TakeFailed
}

// Running says whether a take is being filmed: recording, or paused by the owner.
func (t Take) Running() bool { return t.State == TakeRecording || t.State == TakePaused }
