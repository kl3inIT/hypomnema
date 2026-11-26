package devicetool

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Filming the phone (issue #817): an agent films the node's phone while it
// drives an app with the device_* tools, and says what each step shows. The
// result is a take: one or more video segments and the steps, timed on one
// clock. The tools are autodemo's camera tools (src/mcp.js:282-321: start,
// say right before the action with where it taps and a chapter, stop), with
// a request id and a receipt as docs/conventions.md asks of a call that may
// be retried, and a status to read a take's state, as autodemo answers its
// stop with a job id to ask about (src/mcp.js:372-379), and a list of the
// agent's own takes, the owner's `GET /takes` restricted to it (issue #851).
// They are routed and switched as the device_* tools are.

// The video tools' names: each is VideoPrefix and what it does.
const (
	// VideoPrefix starts every video tool's name; Routed says which names go to the phone.
	VideoPrefix = "video_"

	VideoStart  = "video_start"
	VideoSay    = "video_say"
	VideoStop   = "video_stop"
	VideoStatus = "video_status"
	// VideoList lists the calling agent's own takes (issue #851), answered by the control plane.
	VideoList = "video_list"

	// VideoPause, VideoResume and VideoEnd are the control plane's own, never offered to an agent: the
	// owner's takeover pauses a take and "Hand back" resumes it; the end of the agent's work, of the phone or
	// of the control plane ends it.
	VideoPause  = "video_pause"
	VideoResume = "video_resume"
	VideoEnd    = "video_end"

	// VideoMake and VideoGet make a take into a video and read it (issue #818, row 11). They are the control
	// plane's own tools, not the phone's: a video is made apart from the phone and its lease, so they are neither
	// routed to the phone nor among IsVideo's.
	VideoMake = "video_make"
	VideoGet  = "video_get"
)

// The browser take's tools (issue #1189, row 4): start, say and stop filming the agent's own tab of the shared
// browser, the phone's video_start, video_say and video_stop for a page. They are the control plane's own and the
// browser's switch names them, so neither Routed nor IsVideo is true of them; video_make and video_get read a
// browser take as they read a phone's, and so do video_status and video_list, which are listed on the phone's
// route, so only while a node holds the phone.
const (
	BrowserVideoStart = "browser_video_start"
	BrowserVideoSay   = "browser_video_say"
	BrowserVideoStop  = "browser_video_stop"
)

// Routed says whether a tool's name is one of the phone's, which a worker's
// tools server and a Bot's pass to the phone's routes.
func Routed(name string) bool {
	return strings.HasPrefix(name, Prefix) || IsVideo(name)
}

// IsVideo says whether a tool is one of the phone's video tools.
func IsVideo(name string) bool {
	return strings.HasPrefix(name, VideoPrefix) && name != VideoMake && name != VideoGet
}

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
	// MaxListed is how many takes video_list answers, the newest.
	MaxListed = 20
)

// TakeStates are a take's states, as video_list filters by them.
var TakeStates = []string{TakeRecording, TakePaused, TakeFinalizing, TakeUploading, TakeAvailable, TakeFailed}

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
		tool(VideoList, "List your own takes, the newest first (at most 20): id, title, state, what ended it, "+
			"when it started and how long it is. Use it to find a take of yours still filming, as after an "+
			"interrupted turn.",
			`{"state":{"type":"string","enum":["recording","paused","finalizing","uploading","available","failed"],`+
				`"description":"only takes in this state"}}`, nil),
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
		VideoList:   {"state"},
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
	State     string `json:"state"`
}

// readVideo checks a video tool's arguments into c.
func (c *Call) readVideo(a videoArguments, internal bool) error {
	c.Take, c.Title, c.RequestID = strings.TrimSpace(a.Take), strings.TrimSpace(a.Title), a.RequestID

	if c.Tool == VideoList {
		c.State = a.State
		if a.State != "" && !containsString(TakeStates, a.State) {
			return bad("state is one of %s", strings.Join(TakeStates, ", "))
		}

		return nil
	}

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

	b.WriteString(next(t))

	return b.String()
}

// next is what an agent is told to do or wait for after a take's state: a browser take is encoded on the control
// plane, with nothing to upload (issue #1189).
func next(t Take) string {
	switch {
	case t.State == TakeAvailable:
		return " The owner can watch it now."
	case t.Browser() && t.State == TakeRecording:
		return " Call " + BrowserVideoSay + " right before each action, and " + BrowserVideoStop + " when done."
	case t.Browser() && t.State == TakeFinalizing:
		return " Its frames are being encoded; video_make makes it into a video once it is available."
	case t.State == TakeRecording:
		return " Call video_say right before each action, and video_stop when done."
	case t.State == TakePaused:
		return " The owner took the phone over; it resumes when the owner hands it back."
	case t.State == TakeFinalizing, t.State == TakeUploading:
		return " It is being finished and uploaded; video_status says when it is available."
	}

	return ""
}

// List is an agent's takes in lines, as video_list answers them: id, title,
// state, what ended it, when it started and how long it is; never a file.
func List(takes []Take, state string) string {
	if len(takes) == 0 {
		if state != "" {
			return "You have no take that is " + state + "."
		}

		return "You have no take."
	}

	var b strings.Builder

	b.WriteString("Your takes, the newest first:")

	for _, t := range takes {
		fmt.Fprintf(&b, "\n- %s %q: %s", t.ID, t.Title, t.State)

		if t.Browser() {
			b.WriteString(", of the browser")
		}

		if t.EndedBy != "" {
			fmt.Fprintf(&b, ", ended by %s", t.EndedBy)
		}

		fmt.Fprintf(&b, ", started %s, %s long", t.StartedAt.UTC().Format(time.RFC3339), Clock(t.DurationMs))
	}

	return b.String()
}

// Clock is a time on a take's timeline as minutes and seconds.
func Clock(ms int64) string {
	s := max(ms, 0) / 1000

	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
