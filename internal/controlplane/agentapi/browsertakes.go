package agentapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/browsertake"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// An agent filming its tab of the shared browser (issue #1189, row 4): the
// phone's video_start, video_say and video_stop for a page, given with the
// browser's tools and switched with them. They have names of their own, as the
// phone's video_* are routed to the phone, whose switch is off by default; a
// browser take is then read with video_status and video_list, and made with
// video_make, as a phone take is.

// BrowserTakes films the agents' tabs (browsertake.Recorder).
type BrowserTakes interface {
	Start(ctx context.Context, c browsertake.Caller, title, requestID string, viewport browser.Size,
	) (devicetool.Take, string, bool)
	Say(ctx context.Context, c browsertake.Caller, take, text, chapter, element string) (string, bool)
	Stop(ctx context.Context, c browsertake.Caller, take string) (string, bool)
}

// WithBrowserTakes gives the agents that have the browser its take's tools.
func (s *Server) WithBrowserTakes(t BrowserTakes) *Server {
	s.browserTakes = t

	return s
}

// browserTakeTools are the tools' names.
var browserTakeTools = []string{devicetool.BrowserVideoStart, devicetool.BrowserVideoSay, devicetool.BrowserVideoStop}

// hasBrowserTakes says whether the caller gets them: an agent with its own tab.
func (s *Server) hasBrowserTakes(a agent) bool {
	return s.browserTakes != nil && s.hasBrowser(a)
}

func browserTakeToolList() []agenttool.BrowserTool {
	return []agenttool.BrowserTool{
		{
			Name: devicetool.BrowserVideoStart,
			Description: "Start filming your own tab of the shared browser as a take, for a demo video of a web " +
				"page. Use it after agent_browser_open has opened the page. Give width and height for a viewport of " +
				"your own, such as 390 by 844 for a phone's layout (no phone is emulated: the page is that size); " +
				"without them the tab keeps its size. Drive the page with the " +
				"agent_browser_* tools, calling browser_video_say right before each action, then " +
				"browser_video_stop. Give a request_id of your own: a retry with it answers the same take.",
			InputSchema: json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{`+
				`"title":{"type":"string","description":"what the take shows"},`+
				`"width":{"type":"integer","minimum":100,"maximum":%d,"description":"the viewport's width in CSS pixels"},`+
				`"height":{"type":"integer","minimum":100,"maximum":%d,"description":"the viewport's height in CSS pixels"},`+
				`"request_id":{"type":"string","description":"your id for this start; a retry with it answers the same take"}},`+
				`"required":["title"]}`, browsertake.MaxViewport, browsertake.MaxViewport)),
		},
		{
			Name: devicetool.BrowserVideoSay,
			Description: "Say what the next step of a browser take shows, right before doing it: the line is " +
				"stamped at this moment of the take. Use it before each click or typing. With element, the " +
				"element you are about to click or type into, as agent_browser_click takes it (a ref such as @e3 " +
				"from your last snapshot, or a CSS selector): the video rings it. With chapter, a new chapter " +
				"starts here.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` +
				`"take":{"type":"string","description":"the take's id, from browser_video_start"},` +
				`"text":{"type":"string","description":"the line said"},` +
				`"element":{"type":"string","description":"the element about to be clicked or typed into: a ref or a selector"},` +
				`"chapter":{"type":"string"}},"required":["take","text"]}`),
		},
		{
			Name: devicetool.BrowserVideoStop,
			Description: "Stop filming a browser take. It answers at once; the frames are then encoded, which " +
				"video_status follows, and video_make makes the take into a video once it is available. Use it " +
				"when the demo is done.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` +
				`"take":{"type":"string","description":"the take's id, from browser_video_start"}},"required":["take"]}`),
		},
	}
}

// browserTakeCall runs one of the tools for the agent.
func (s *Server) browserTakeCall(w http.ResponseWriter, r *http.Request, a agent, asked agenttool.BrowserCall) {
	var args struct {
		Title     string `json:"title"`
		RequestID string `json:"request_id"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		Take      string `json:"take"`
		Text      string `json:"text"`
		Element   string `json:"element"`
		Chapter   string `json:"chapter"`
	}

	if json.Unmarshal(orEmpty(asked.Arguments), &args) != nil {
		answerTool(w, asked.Tool+" takes a JSON object", true)

		return
	}

	caller, err := s.caller(r.Context(), a)
	if err != nil {
		s.fail(w, r, err)

		return
	}

	c := browsertake.Caller{Key: caller.Key, Name: caller.Name, Work: caller.Work}
	take := strings.TrimSpace(args.Take)

	if why := browserTakeRefusal(asked.Tool, args.Title, args.RequestID, take, args.Text, args.Chapter,
		args.Element, args.Width, args.Height); why != "" {
		answerTool(w, why, true)

		return
	}

	var (
		text   string
		failed bool
	)

	switch asked.Tool {
	case devicetool.BrowserVideoStart:
		_, text, failed = s.browserTakes.Start(r.Context(), c, strings.TrimSpace(args.Title), args.RequestID,
			browser.Size{Width: args.Width, Height: args.Height})
	case devicetool.BrowserVideoSay:
		text, failed = s.browserTakes.Say(r.Context(), c, take, strings.TrimSpace(args.Text),
			strings.TrimSpace(args.Chapter), strings.TrimSpace(args.Element))
	default:
		text, failed = s.browserTakes.Stop(r.Context(), c, take)
	}

	answerTool(w, text, failed)
}

// maxElement bounds a step's element: a ref or a selector.
const maxElement = 1000

// browserTakeRefusal is why a call's arguments are refused, as the phone's video tools bound theirs; empty when
// they are not.
func browserTakeRefusal(tool, title, requestID, take, text, chapter, element string, width, height int) string {
	switch tool {
	case devicetool.BrowserVideoStart:
		title = strings.TrimSpace(title)

		switch {
		case title == "" || utf8.RuneCountInString(title) > devicetool.MaxTitle:
			return fmt.Sprintf("title is what the take shows, at most %d characters.", devicetool.MaxTitle)
		case len(requestID) > devicetool.MaxRequestID || strings.ContainsRune(requestID, 0):
			return fmt.Sprintf("request_id is at most %d bytes.", devicetool.MaxRequestID)
		case (width == 0) != (height == 0):
			return "Give width and height both, or neither."
		}

		return ""
	case devicetool.BrowserVideoSay:
		text = strings.TrimSpace(text)

		switch {
		case text == "" || utf8.RuneCountInString(text) > devicetool.MaxLine:
			return fmt.Sprintf("text is the line said, at most %d characters.", devicetool.MaxLine)
		case utf8.RuneCountInString(strings.TrimSpace(chapter)) > devicetool.MaxTitle:
			return fmt.Sprintf("chapter is at most %d characters.", devicetool.MaxTitle)
		case len(element) > maxElement:
			return fmt.Sprintf("element is a ref or a selector of at most %d bytes.", maxElement)
		}
	}

	if take == "" {
		return tool + " needs the take browser_video_start answered."
	}

	return ""
}
