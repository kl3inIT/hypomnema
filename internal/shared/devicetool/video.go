package devicetool

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Filming the phone (issue #817): an agent films the node's phone while it
// drives an app with the device_* tools, and says what each step shows. The
// result is a take: one or more video segments and the steps, timed on one
// clock. The tools are autodemo's camera tools (src/mcp.js:282-321: start,
// say right before the action with where it taps and a chapter, stop), with
// a request id and a receipt as docs/conventions.md asks of a call that may
// be retried, and a status to read a take's state, as autodemo answers its
// stop with a job id to ask about (src/mcp.js:372-379). They are routed and
// switched as the device_* tools are.

// The video tools' names: each is VideoPrefix and what it does.
const (
	// VideoPrefix starts every video tool's name; Routed says which names go to the phone.
	VideoPrefix = "video_"

	VideoStart  = "video_start"
	VideoSay    = "video_say"
	VideoStop   = "video_stop"
	VideoStatus = "video_status"

	// VideoPause, VideoResume and VideoEnd are the control plane's own, never offered to an agent: the
	// owner's takeover pauses a take and "Hand back" resumes it; the end of the agent's work, of the phone or
	// of the control plane ends it.
	VideoPause  = "video_pause"
	VideoResume = "video_resume"
	VideoEnd    = "video_end"
)

// Routed says whether a tool's name is one of the phone's, which a worker's
// tools server and a Bot's pass to the phone's routes.
func Routed(name string) bool {
	return strings.HasPrefix(name, Prefix) || strings.HasPrefix(name, VideoPrefix)
}

// IsVideo says whether a tool is one of the video tools.
func IsVideo(name string) bool { return strings.HasPrefix(name, VideoPrefix) }

// Limits of the video tools' arguments.
const (
	// MaxTitle bounds a take's title and a chapter's.
	MaxTitle = 200
	// MaxLine bounds what video_say says.
	MaxLine = 1000
	// MaxRequestID bounds video_start's request id.
	MaxRequestID = 100
	// maxTakeID bounds a take's id.
	maxTakeID = 64
)

// videoTools are the video tools an agent is offered.
func videoTools() []Tool {
	return []Tool{
		tool(VideoStart, "Start filming the phone's screen as a take, for a demo video of an app. While it films, "+
			"no other agent can use the phone. Drive the app with the device_* tools, calling video_say right "+
			"before each action. Give a request_id of your own: calling again with it answers the same take.",
			`{"title":{"type":"string","description":"what the take shows"},`+
				`"request_id":{"type":"string","description":"your id for this start; a retry with it answers the same take"}}`,
			[]string{"title"}),
		tool(VideoSay, "Say what the next step shows, right before doing it: the line is stamped at this moment of "+
			"the take. With tap, where you are about to tap: "+point+". With chapter, a new chapter starts here.",
			`{"take":{"type":"string"},"text":{"type":"string"},"tap":{"type":"string"},`+
				`"chapter":{"type":"string"}}`,
			[]string{"take", "text"}),
		tool(VideoStop, "Stop filming the take. It answers at once; the take is then finished and uploaded, "+
			"which video_status follows.",
			`{"take":{"type":"string"}}`, []string{"take"}),
		tool(VideoStatus, "Read a take's state, length and steps.",
			`{"take":{"type":"string"}}`, []string{"take"}),
	}
}

// videoTakes are the arguments each video tool takes; internalTakes are
// what the control plane adds or sends of its own.
var (
	videoTakes = map[string][]string{
		VideoStart:  {"title", "request_id"},
		VideoSay:    {"take", "text", "tap", "chapter"},
		VideoStop:   {"take"},
		VideoStatus: {"take"},
	}
	internalTakes = map[string][]string{
		VideoStart:  {"title", "request_id", "take"},
		VideoPause:  {"take"},
		VideoResume: {"take"},
		VideoEnd:    {"take", "ended_by"},
	}
)

// videoArguments are every video tool's arguments.
type videoArguments struct {
	Title     string `json:"title"`
	RequestID string `json:"request_id"`
	Take      string `json:"take"`
	Text      string `json:"text"`
	Tap       string `json:"tap"`
	Chapter   string `json:"chapter"`
	EndedBy   string `json:"ended_by"`
}

// readVideo checks a video tool's arguments into c.
func (c *Call) readVideo(a videoArguments, internal bool) error {
	c.Take, c.Title, c.RequestID = strings.TrimSpace(a.Take), strings.TrimSpace(a.Title), a.RequestID

	// An agent's video_start names no take: the control plane gives it one, which the node is sent.
	if c.Tool != VideoStart || internal {
		if c.Take == "" {
			return bad("%s needs the take video_start answered", c.Tool)
		}

		if len(c.Take) > maxTakeID || !takeIDPattern(c.Take) {
			return bad("take is the id video_start answered, such as tk_0a1b2c")
		}
	}

	switch c.Tool {
	case VideoStart:
		if c.Title == "" || utf8.RuneCountInString(c.Title) > MaxTitle {
			return bad("title is what the take shows, at most %d characters", MaxTitle)
		}

		if len(c.RequestID) > MaxRequestID || strings.ContainsRune(c.RequestID, 0) {
			return bad("request_id is at most %d bytes", MaxRequestID)
		}
	case VideoSay:
		c.Text, c.Chapter = strings.TrimSpace(a.Text), strings.TrimSpace(a.Chapter)
		if c.Text == "" || utf8.RuneCountInString(c.Text) > MaxLine {
			return bad("text is the line said, at most %d characters", MaxLine)
		}

		if utf8.RuneCountInString(c.Chapter) > MaxTitle {
			return bad("chapter is at most %d characters", MaxTitle)
		}

		if a.Tap != "" {
			p, ok := readPoint(a.Tap)
			if !ok {
				return bad("tap is %s", point)
			}

			c.Target = &p
		}
	case VideoEnd:
		c.EndedBy = a.EndedBy
		if !containsString(EndedBy, a.EndedBy) {
			return bad("ended_by is one of %s", strings.Join(EndedBy, ", "))
		}
	}

	return nil
}

// takeIDPattern says whether s reads as a take's id: letters, digits, _ and -.
func takeIDPattern(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}

	return true
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}

	return false
}

// Describe is a take's state in a sentence, as an agent is told it: never a
// file, which only the owner reads.
func Describe(t Take) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Take %s (%q) is %s, %s long, with %d steps", t.ID, t.Title, t.State,
		Clock(t.DurationMs), len(t.Steps))

	if t.EndedBy != "" {
		fmt.Fprintf(&b, "; it ended by %s", t.EndedBy)
	}

	if t.Reason != "" {
		fmt.Fprintf(&b, ": %s", t.Reason)
	}

	b.WriteString(".")

	switch t.State {
	case TakeRecording:
		b.WriteString(" Call video_say right before each action, and video_stop when done.")
	case TakePaused:
		b.WriteString(" The owner took the phone over; it resumes when the owner hands it back.")
	case TakeFinalizing, TakeUploading:
		b.WriteString(" It is being finished and uploaded; video_status says when it is available.")
	case TakeAvailable:
		b.WriteString(" The owner can watch it now.")
	}

	return b.String()
}

// Clock is a time on a take's timeline as minutes and seconds.
func Clock(ms int64) string {
	s := max(ms, 0) / 1000

	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
