package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// An agent's videos (issue #818, row 11): video_make asks for a video of a
// take the agent filmed, answered at once with its id while the control
// plane's runner makes it, and video_get reads one of them; metadata only,
// never a file or a link. They are the control plane's own tools, listed and
// called on its tools route, apart from the phone and its lease; the
// phone's switch (device_*) names them, as it does every video_* tool.

// Videos are the videos agents ask for (videos.Service).
type Videos interface {
	Make(ctx context.Context, req videos.MakeRequest) (videos.Video, videos.Revision, error)
	Get(ctx context.Context, id string) (videos.Video, error)
}

// TakeOwners says who filmed a take (takes.Store).
type TakeOwners interface {
	Get(id string) (devicetool.Take, error)
}

type videoTools struct {
	videos Videos
	takes  TakeOwners
}

// WithVideos wires video_make, video_get and video_templates for the owner, workers and Bots' pis.
func (s *Server) WithVideos(v Videos, takes TakeOwners) *Server {
	s.videos = &videoTools{videos: v, takes: takes}

	return s
}

// hasVideoTools says whether the caller gets the tools for its own takes.
func (s *Server) hasVideoTools(a agent) bool {
	return s.videos != nil && a.hasAgentTools()
}

// videoToolNames are the names of the tools videoCall answers.
var videoToolNames = []string{devicetool.VideoMake, devicetool.VideoGet, devicetool.VideoTemplates}

func videoToolList() []agenttool.BrowserTool {
	return []agenttool.BrowserTool{
		{
			Name: devicetool.VideoMake,
			Description: "Make a take you filmed into a video for the owner to review: its dead time cut, a " +
				"Vietnamese voice saying each step's line, captions, and a ring at each tap. It answers at once with " +
				"the video's id; the video is made in the background, which video_get follows. Calling it again for " +
				"the same take makes a new revision. Give a request_id of your own: a retry with it answers the same " +
				"revision.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` +
				`"take":{"type":"string","description":"the take's id, from video_start"},` +
				`"template":{"type":"string","description":"a template's id from video_templates: its shape, captions, look and cards"},` +
				`"request_id":{"type":"string","description":"your id for this make; a retry with it answers the same revision"}},` +
				`"required":["take"]}`),
		},
		{
			Name: devicetool.VideoTemplates,
			Description: "List the templates video_make takes: each one's use case, length, shape, cards and the " +
				"sections of its script, each with its words at about 2.5 a second. Use it before you film a demo " +
				"for a use case a template has, and say one line a section, within its words.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		{
			Name: devicetool.VideoGet,
			Description: "Read a video of yours: the state of its latest revision (queued, voicing, composing, " +
				"captioning, review, approved, changes_requested or failed), its length, its lines and the owner's " +
				"note.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` +
				`"video":{"type":"string","description":"the video's id, from video_make"}},"required":["video"]}`),
		},
	}
}

// videoCall runs video_make or video_get for the agent.
func (s *Server) videoCall(w http.ResponseWriter, r *http.Request, a agent, asked agenttool.BrowserCall) {
	var args struct {
		Take      string `json:"take"`
		Template  string `json:"template"`
		RequestID string `json:"request_id"`
		Video     string `json:"video"`
	}

	if json.Unmarshal(orEmpty(asked.Arguments), &args) != nil {
		answerTool(w, asked.Tool+" takes a JSON object", true)

		return
	}

	if asked.Tool == devicetool.VideoTemplates {
		answerTool(w, describeTemplates(videos.Templates()), false)

		return
	}

	caller, err := s.caller(r.Context(), a)
	if err != nil {
		s.fail(w, r, err)

		return
	}

	if asked.Tool == devicetool.VideoMake {
		text, failed := s.videos.make(r.Context(), caller.Key, a.name, strings.TrimSpace(args.Take),
			strings.TrimSpace(args.Template), args.RequestID)
		answerTool(w, text, failed)

		return
	}

	text, failed := s.videos.get(r.Context(), caller.Key, strings.TrimSpace(args.Video))
	answerTool(w, text, failed)
}

// filmed says whether the agent filmed the take.
func (v *videoTools) filmed(key, take string) bool {
	t, err := v.takes.Get(take)

	return err == nil && t.Agent == key
}

func (v *videoTools) make(ctx context.Context, key, name, take, template, requestID string) (string, bool) {
	if take == "" || !v.filmed(key, take) {
		return "You have no take " + take + ".", true
	}

	if len(requestID) > devicetool.MaxRequestID {
		return fmt.Sprintf("request_id is at most %d bytes.", devicetool.MaxRequestID), true
	}

	vid, rev, err := v.videos.Make(ctx, videos.MakeRequest{
		Take: take, By: key, ByName: name, RequestID: requestID, Template: template,
	})
	if err != nil {
		return refusal(err), true
	}

	text := fmt.Sprintf("Video %s of take %s: revision %d is %s. It is made in the background; follow it with "+
		"video_get.", vid.ID, take, rev.Rev, rev.State)

	if t, ok := videos.TemplateByID(template); ok {
		text += " " + madeWith(t, rev)
	}

	return text, false
}

// madeWith says how a revision made with a template was made, and how long its lines are against the template's
// length, at its words a second with its cards, as aidemo's lint estimates a storyboard's narration
// (src/lint.ts:199-215).
func madeWith(t videos.Template, rev videos.Revision) string {
	words := 0
	for _, l := range rev.Lines {
		words += len(strings.Fields(l))
	}

	var cardsMs int64
	if c := rev.Look.Cards; c != nil {
		cardsMs = c.TitleMs() + c.EndMs()
	}

	s := float64(words)/videos.WordsPerSecond + float64(cardsMs)/1000

	return fmt.Sprintf("It is made with the template %s: %s, %s. Its %d lines have %d words, about %.0f s with its "+
		"cards; the template is %s.", t.ID, rev.Aspect, cardsOf(rev.Look.Cards), len(rev.Lines), words, s,
		lengthOf(t.Length))
}

// describeTemplates is the templates as an agent reads them: each one's id, name, length, shapes, captions and
// cards, its use case, then its sections with their words.
func describeTemplates(list []videos.Template) string {
	var b strings.Builder

	b.WriteString("The templates video_make takes as template. Say one line a section with video_say, within its " +
		"words (about 2.5 words a second); a template's cards show the take's title.")

	for _, t := range list {
		captions := ""
		if !t.Captions {
			captions = ", no captions"
		}

		fmt.Fprintf(&b, "\n\n%s: %s, %s, %s%s, %s. %s", t.ID, t.Name, lengthOf(t.Length), strings.Join(t.Aspects, " or "),
			captions, cardsOf(t.Cards()), t.UseCase)

		for i, sec := range t.Sections {
			fmt.Fprintf(&b, "\n%d. %s, about %d words (%.0f s): %s", i+1, sec.Name, sec.Words, sec.Seconds(), sec.Purpose)
		}
	}

	return b.String()
}

func lengthOf(l videos.TemplateLength) string {
	if l.MinS == l.MaxS {
		return fmt.Sprintf("%d s", l.MinS)
	}

	return fmt.Sprintf("%d-%d s", l.MinS, l.MaxS)
}

// cardsOf names the cards a video has.
func cardsOf(c *compose.Cards) string {
	switch {
	case c == nil || c.Title == nil && c.End == nil:
		return "no cards"
	case c.End == nil:
		return "a title card"
	case c.Title == nil:
		return "an end card"
	}

	return "a title card and an end card"
}

func (v *videoTools) get(ctx context.Context, key, id string) (string, bool) {
	vid, err := v.videos.Get(ctx, id)
	if err != nil || !v.filmed(key, vid.Take) {
		return "You have no video " + id + ".", true
	}

	return describeVideo(vid), false
}

// refusal is why a make was refused, as the agent reads it.
func refusal(err error) string {
	switch {
	case errors.Is(err, videos.ErrIdempotency):
		return "This request_id was used for another make: give a new one."
	case errors.Is(err, videos.ErrNotFound), errors.Is(err, videos.ErrConflict), errors.Is(err, videos.ErrInvalid):
		_, why, _ := strings.Cut(err.Error(), ": ")

		return "The video could not be made: " + why + "."
	}

	return "The video could not be made: " + err.Error() + "."
}

// describeVideo is a video as an agent reads it: its latest revision's state,
// length, lines and note, and the approved one when another.
func describeVideo(v videos.Video) string {
	r := v.Latest()

	var b strings.Builder

	fmt.Fprintf(&b, "Video %s %q of take %s, revision %d: %s", v.ID, v.Title, v.Take, r.Rev, r.State)

	switch r.State {
	case videos.Failed:
		fmt.Fprintf(&b, " at %s: %s", r.FailedStage, r.FailedCause)
	case videos.Review, videos.Approved, videos.ChangesRequested:
		fmt.Fprintf(&b, ", %s long", (time.Duration(r.LengthMs) * time.Millisecond).Round(100*time.Millisecond))
	}

	b.WriteString(".")

	if a, ok := v.Approved(); ok && a.Rev != r.Rev {
		fmt.Fprintf(&b, " Revision %d is approved.", a.Rev)
	}

	for i := len(v.Revisions) - 1; i >= 0; i-- {
		if n := v.Revisions[i].Note; n != "" {
			fmt.Fprintf(&b, "\nThe owner's note on revision %d: %s", v.Revisions[i].Rev, n)

			break
		}
	}

	b.WriteString("\nLines:")

	for i, l := range r.Lines {
		fmt.Fprintf(&b, "\n%d. %s", i+1, l)
	}

	return b.String()
}
