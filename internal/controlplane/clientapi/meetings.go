package clientapi

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/meetings"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/meetingstore"
)

// Meetings (issue #721): the owner uploads a recording, which the control plane keeps and hears with speakers in
// the background, and a gateway model writes its notes, decisions and actions; the owner names the voices, marks
// actions done and writes the notes again in another format. Every token reads them; only the owner changes them.

// Meetings is the meetings' service (meetings.Service).
type Meetings interface {
	Upload(ctx context.Context, u meetings.Upload) (meetingstore.Meeting, error)
	List(ctx context.Context, query string, limit int) ([]meetingstore.Meeting, error)
	Get(ctx context.Context, id string) (meetings.Detail, error)
	Delete(ctx context.Context, id string) error
	Recording(ctx context.Context, id string) (meetingstore.Meeting, *os.File, error)
	WriteAgain(ctx context.Context, id, format string) error
	NameSpeaker(ctx context.Context, id, label, name string) error
	SetLineSpeaker(ctx context.Context, id string, n int, name string) error
	SetDone(ctx context.Context, id, itemID string, done bool) (meetingstore.Item, error)
	Actions(ctx context.Context, who string, done *bool) ([]meetingstore.MeetingAction, error)
	Settings(ctx context.Context) (meetingstore.Settings, error)
	SetSettings(ctx context.Context, set meetingstore.Settings) error
	Formats(ctx context.Context) ([]meetings.Format, error)
	CreateFormat(ctx context.Context, name string, sections []meetings.FormatSection) (meetings.Format, error)
	UpdateFormat(ctx context.Context, id, name string, sections []meetings.FormatSection) (meetings.Format, error)
	DeleteFormat(ctx context.Context, id string) error
	Words(ctx context.Context) ([]meetings.Word, error)
	SetWords(ctx context.Context, words []string) error
	ReplaceWord(ctx context.Context, id string, fix meetingstore.Fix) (meetingstore.Fixed, error)
}

// WithMeetings gives the API the meetings.
func (s *Server) WithMeetings(m Meetings) *Server {
	s.meetings = m

	return s
}

var errNoMeetings = fmt.Errorf("%w: this control plane has no meetings", orchestration.ErrUnavailable)

const defaultMeetingsLimit = 200

// ListMeetings lists the meetings, the newest first.
func (s *Server) ListMeetings(ctx context.Context, req openapi.ListMeetingsRequestObject,
) (openapi.ListMeetingsResponseObject, error) {
	if s.meetings == nil {
		return nil, errNoMeetings
	}

	limit := defaultMeetingsLimit
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}

	all, err := s.meetings.List(ctx, val(req.Params.Q), limit)
	if err != nil {
		return nil, err
	}

	out := openapi.MeetingList{Data: make([]openapi.Meeting, 0, len(all))}
	for _, m := range all {
		out.Data = append(out.Data, meetingOut(m))
	}

	return openapi.ListMeetings200JSONResponse(out), nil
}

// UploadMeeting keeps a recording and starts its work.
func (s *Server) UploadMeeting(ctx context.Context, req openapi.UploadMeetingRequestObject,
) (openapi.UploadMeetingResponseObject, error) {
	if s.meetings == nil {
		return nil, errNoMeetings
	}

	p := req.Params

	m, err := s.meetings.Upload(ctx, meetings.Upload{
		Body: req.Body, FileName: p.FileName, MediaType: val(p.MediaType), Language: val(p.Language),
		Title: val(p.Title), People: val(p.People), Format: val(p.Format),
	})
	if err != nil {
		return nil, err
	}

	return openapi.UploadMeeting201JSONResponse(meetingOut(m)), nil
}

// GetMeeting returns a meeting with its transcript and notes.
func (s *Server) GetMeeting(ctx context.Context, req openapi.GetMeetingRequestObject,
) (openapi.GetMeetingResponseObject, error) {
	d, err := s.meetingDetail(ctx, req.MeetingId)
	if err != nil {
		return nil, err
	}

	return openapi.GetMeeting200JSONResponse(d), nil
}

func (s *Server) meetingDetail(ctx context.Context, id string) (openapi.MeetingDetail, error) {
	if s.meetings == nil {
		return openapi.MeetingDetail{}, errNoMeetings
	}

	d, err := s.meetings.Get(ctx, id)
	if err != nil {
		return openapi.MeetingDetail{}, err
	}

	out := openapi.MeetingDetail{
		Meeting: meetingOut(d.Meeting), Lines: make([]openapi.MeetingLine, 0, len(d.Lines)),
		Items: make([]openapi.MeetingItem, 0, len(d.Items)), Suggestions: []openapi.SpeakerSuggestion{},
	}

	for _, l := range d.Lines {
		out.Lines = append(out.Lines, openapi.MeetingLine{
			N: l.N, StartMs: l.Start.Milliseconds(), EndMs: l.End.Milliseconds(), Speaker: l.Speaker,
			Heard: l.Heard, Text: l.Text,
		})
	}

	for _, it := range d.Items {
		out.Items = append(out.Items, itemOut(it, d.Meeting.Speakers))
	}

	for _, sg := range d.Suggestions {
		out.Suggestions = append(out.Suggestions, openapi.SpeakerSuggestion{Label: sg.Label, Name: sg.Name, Line: sg.Line})
	}

	return out, nil
}

// DeleteMeeting deletes a meeting and its recording.
func (s *Server) DeleteMeeting(ctx context.Context, req openapi.DeleteMeetingRequestObject,
) (openapi.DeleteMeetingResponseObject, error) {
	if s.meetings == nil {
		return nil, errNoMeetings
	}

	if err := s.meetings.Delete(ctx, req.MeetingId); err != nil {
		return nil, err
	}

	return openapi.DeleteMeeting204Response{}, nil
}

// GetMeetingRecording answers the recording, a part of it for a Range request.
func (s *Server) GetMeetingRecording(ctx context.Context, req openapi.GetMeetingRecordingRequestObject,
) (openapi.GetMeetingRecordingResponseObject, error) {
	if s.meetings == nil {
		return nil, errNoMeetings
	}

	m, f, err := s.meetings.Recording(ctx, req.MeetingId)
	if err != nil {
		return nil, err
	}

	return recordingResponse{ctx: ctx, meeting: m, file: f, ranges: callerOf(ctx).ranges}, nil
}

// recordingResponse serves a recording with http.ServeContent, which answers a Range request 206 with that part,
// so that the player seeks without loading the whole file. The generated octet-stream answer has no ranges.
type recordingResponse struct {
	ctx     context.Context //nolint:containedctx // the request's, which the generated Visit does not pass
	meeting meetingstore.Meeting
	file    *os.File
	ranges  http.Header
}

func (r recordingResponse) VisitGetMeetingRecordingResponse(w http.ResponseWriter) error {
	defer func() { _ = r.file.Close() }()

	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, "/", http.NoBody)
	if err != nil {
		return err
	}

	for name, values := range r.ranges {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}

	// Shown inline for the player, never as a page of this origin: sound or video only (meetings.MediaType, read
	// again for a row kept before the rule), nosniff and a sandboxing policy, as a workspace's /raw and an
	// artifact's download are sent (workspaces.go rawFile, artifacts.go download).
	h := w.Header()
	h.Set("Content-Type", meetings.MediaType(r.meeting.MediaType, r.meeting.MediaType))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": r.meeting.FileName}))
	h.Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, req, r.meeting.FileName, r.meeting.CreatedAt, r.file)

	return nil
}

// WriteMeetingNotes asks for the notes again.
func (s *Server) WriteMeetingNotes(ctx context.Context, req openapi.WriteMeetingNotesRequestObject,
) (openapi.WriteMeetingNotesResponseObject, error) {
	if s.meetings == nil {
		return nil, errNoMeetings
	}

	if err := s.meetings.WriteAgain(ctx, req.MeetingId, req.Body.Format); err != nil {
		return nil, err
	}

	d, err := s.meetings.Get(ctx, req.MeetingId)
	if err != nil {
		return nil, err
	}

	return openapi.WriteMeetingNotes202JSONResponse(meetingOut(d.Meeting)), nil
}

// NameMeetingSpeaker names a voice for the whole meeting.
func (s *Server) NameMeetingSpeaker(ctx context.Context, req openapi.NameMeetingSpeakerRequestObject,
) (openapi.NameMeetingSpeakerResponseObject, error) {
	if s.meetings == nil {
		return nil, errNoMeetings
	}

	if err := s.meetings.NameSpeaker(ctx, req.MeetingId, req.Label, req.Body.Name); err != nil {
		return nil, err
	}

	d, err := s.meetingDetail(ctx, req.MeetingId)
	if err != nil {
		return nil, err
	}

	return openapi.NameMeetingSpeaker200JSONResponse(d), nil
}

// SetMeetingLineSpeaker says who spoke one line.
func (s *Server) SetMeetingLineSpeaker(ctx context.Context, req openapi.SetMeetingLineSpeakerRequestObject,
) (openapi.SetMeetingLineSpeakerResponseObject, error) {
	if s.meetings == nil {
		return nil, errNoMeetings
	}

	if err := s.meetings.SetLineSpeaker(ctx, req.MeetingId, req.N, req.Body.Name); err != nil {
		return nil, err
	}

	d, err := s.meetingDetail(ctx, req.MeetingId)
	if err != nil {
		return nil, err
	}

	return openapi.SetMeetingLineSpeaker200JSONResponse(d), nil
}

// UpdateMeetingItem marks an action done or not done.
func (s *Server) UpdateMeetingItem(ctx context.Context, req openapi.UpdateMeetingItemRequestObject,
) (openapi.UpdateMeetingItemResponseObject, error) {
	if s.meetings == nil {
		return nil, errNoMeetings
	}

	it, err := s.meetings.SetDone(ctx, req.MeetingId, req.ItemId, req.Body.Done)
	if err != nil {
		return nil, err
	}

	m, err := s.meetings.Get(ctx, req.MeetingId)
	if err != nil {
		return nil, err
	}

	return openapi.UpdateMeetingItem200JSONResponse(itemOut(it, m.Meeting.Speakers)), nil
}

// ListMeetingActions lists the actions of every meeting.
func (s *Server) ListMeetingActions(ctx context.Context, req openapi.ListMeetingActionsRequestObject,
) (openapi.ListMeetingActionsResponseObject, error) {
	if s.meetings == nil {
		return nil, errNoMeetings
	}

	everyone, err := s.meetings.Actions(ctx, "", nil)
	if err != nil {
		return nil, err
	}

	people := []string{}
	for _, a := range everyone {
		if a.Who != "" && !slices.ContainsFunc(people, func(p string) bool {
			return meetingstore.Fold(p) == meetingstore.Fold(a.Who)
		}) {
			people = append(people, a.Who)
		}
	}

	slices.Sort(people)

	var done *bool

	switch val(req.Params.State) {
	case openapi.ListMeetingActionsParamsStateOpen:
		done = new(false)
	case openapi.ListMeetingActionsParamsStateDone:
		done = new(true)
	case openapi.ListMeetingActionsParamsStateAll:
	}

	shown, err := s.meetings.Actions(ctx, val(req.Params.Who), done)
	if err != nil {
		return nil, err
	}

	out := openapi.MeetingActionList{Data: make([]openapi.MeetingAction, 0, len(shown)), People: people}

	for _, a := range shown {
		out.Data = append(out.Data, openapi.MeetingAction{
			Id: a.ID, MeetingId: a.MeetingID, MeetingTitle: a.MeetingTitle, MeetingAt: a.MeetingAt, Text: a.Text,
			Who: optional(a.Who), Due: optional(a.Due), Done: a.Done, Quote: optional(a.Quote),
			StartMs: millis(a.Start),
		})
	}

	return openapi.ListMeetingActions200JSONResponse(out), nil
}

// GetMeetingSettings returns the notes model and the formats.
func (s *Server) GetMeetingSettings(ctx context.Context, _ openapi.GetMeetingSettingsRequestObject,
) (openapi.GetMeetingSettingsResponseObject, error) {
	out, err := s.meetingSettings(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.GetMeetingSettings200JSONResponse(out), nil
}

// SetMeetingSettings picks the notes model.
func (s *Server) SetMeetingSettings(ctx context.Context, req openapi.SetMeetingSettingsRequestObject,
) (openapi.SetMeetingSettingsResponseObject, error) {
	if s.meetings == nil {
		return nil, errNoMeetings
	}

	if err := s.meetings.SetSettings(ctx, meetingstore.Settings{NotesModel: req.Body.NotesModel}); err != nil {
		return nil, err
	}

	out, err := s.meetingSettings(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.SetMeetingSettings200JSONResponse(out), nil
}

func (s *Server) meetingSettings(ctx context.Context) (openapi.MeetingSettings, error) {
	if s.meetings == nil {
		return openapi.MeetingSettings{}, errNoMeetings
	}

	set, err := s.meetings.Settings(ctx)
	if err != nil {
		return openapi.MeetingSettings{}, err
	}

	formats, err := s.meetings.Formats(ctx)
	if err != nil {
		return openapi.MeetingSettings{}, err
	}

	out := openapi.MeetingSettings{NotesModel: set.NotesModel, Formats: make([]openapi.MeetingFormat, 0, len(formats))}
	for _, f := range formats {
		out.Formats = append(out.Formats, formatOut(f))
	}

	return out, nil
}

func formatOut(f meetings.Format) openapi.MeetingFormat {
	sections := make([]openapi.MeetingFormatSection, 0, len(f.Sections))
	for _, sec := range f.Sections {
		sections = append(sections, openapi.MeetingFormatSection{Title: sec.Title, About: sec.About})
	}

	return openapi.MeetingFormat{Id: f.ID, Name: f.Name, Sections: sections, Builtin: f.Builtin}
}

func formatIn(in []openapi.MeetingFormatSectionInput) []meetings.FormatSection {
	out := make([]meetings.FormatSection, 0, len(in))
	for _, sec := range in {
		out = append(out, meetings.FormatSection{Title: sec.Title, About: val(sec.About)})
	}

	return out
}

// CreateMeetingFormat keeps a notes format the owner made (issue #747).
func (s *Server) CreateMeetingFormat(ctx context.Context, req openapi.CreateMeetingFormatRequestObject,
) (openapi.CreateMeetingFormatResponseObject, error) {
	if s.meetings == nil {
		return nil, errNoMeetings
	}

	f, err := s.meetings.CreateFormat(ctx, req.Body.Name, formatIn(req.Body.Sections))
	if err != nil {
		return nil, err
	}

	return openapi.CreateMeetingFormat201JSONResponse(formatOut(f)), nil
}

// UpdateMeetingFormat changes a notes format the owner made.
func (s *Server) UpdateMeetingFormat(ctx context.Context, req openapi.UpdateMeetingFormatRequestObject,
) (openapi.UpdateMeetingFormatResponseObject, error) {
	if s.meetings == nil {
		return nil, errNoMeetings
	}

	f, err := s.meetings.UpdateFormat(ctx, req.FormatId, req.Body.Name, formatIn(req.Body.Sections))
	if err != nil {
		return nil, err
	}

	return openapi.UpdateMeetingFormat200JSONResponse(formatOut(f)), nil
}

// DeleteMeetingFormat removes a notes format the owner made.
func (s *Server) DeleteMeetingFormat(ctx context.Context, req openapi.DeleteMeetingFormatRequestObject,
) (openapi.DeleteMeetingFormatResponseObject, error) {
	if s.meetings == nil {
		return nil, errNoMeetings
	}

	if err := s.meetings.DeleteFormat(ctx, req.FormatId); err != nil {
		return nil, err
	}

	return openapi.DeleteMeetingFormat204Response{}, nil
}

// ListMeetingWords lists the words to hear right (issue #747).
func (s *Server) ListMeetingWords(ctx context.Context, _ openapi.ListMeetingWordsRequestObject,
) (openapi.ListMeetingWordsResponseObject, error) {
	out, err := s.meetingWords(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.ListMeetingWords200JSONResponse(out), nil
}

// SetMeetingWords sets the owner's words to hear right.
func (s *Server) SetMeetingWords(ctx context.Context, req openapi.SetMeetingWordsRequestObject,
) (openapi.SetMeetingWordsResponseObject, error) {
	if s.meetings == nil {
		return nil, errNoMeetings
	}

	if err := s.meetings.SetWords(ctx, req.Body.Words); err != nil {
		return nil, err
	}

	out, err := s.meetingWords(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.SetMeetingWords200JSONResponse(out), nil
}

func (s *Server) meetingWords(ctx context.Context) (openapi.MeetingWords, error) {
	if s.meetings == nil {
		return openapi.MeetingWords{}, errNoMeetings
	}

	words, err := s.meetings.Words(ctx)
	if err != nil {
		return openapi.MeetingWords{}, err
	}

	out := openapi.MeetingWords{
		Data: make([]openapi.MeetingWord, 0, len(words)), MaxOwn: meetingstore.MaxWords, MaxSent: meetings.MaxSentWords,
	}
	for _, w := range words {
		out.Data = append(out.Data, openapi.MeetingWord{Word: w.Text, From: openapi.MeetingWordSource(w.From), Sent: w.Sent})
	}

	return out, nil
}

// FixMeetingWord replaces a word heard wrong in a line or the whole meeting.
func (s *Server) FixMeetingWord(ctx context.Context, req openapi.FixMeetingWordRequestObject,
) (openapi.FixMeetingWordResponseObject, error) {
	if s.meetings == nil {
		return nil, errNoMeetings
	}

	fixed, err := s.meetings.ReplaceWord(ctx, req.MeetingId, meetingstore.Fix{
		From: req.Body.From, To: req.Body.To, Line: val(req.Body.Line),
	})
	if err != nil {
		return nil, err
	}

	d, err := s.meetingDetail(ctx, req.MeetingId)
	if err != nil {
		return nil, err
	}

	return openapi.FixMeetingWord200JSONResponse(openapi.MeetingWordFixed{
		Detail: d, Replaced: fixed.Replaced, Learned: fixed.Learned,
	}), nil
}

func meetingOut(m meetingstore.Meeting) openapi.Meeting {
	var total time.Duration
	for _, sp := range m.Speakers {
		total += sp.Talk
	}

	speakers := make([]openapi.MeetingSpeaker, 0, len(m.Speakers))

	for _, sp := range m.Speakers {
		share := 0.0
		if total > 0 {
			share = float64(sp.Talk) / float64(total)
		}

		speakers = append(speakers, openapi.MeetingSpeaker{
			Label: sp.Label, Name: sp.Shown(), Named: sp.Name != "", TalkMs: sp.Talk.Milliseconds(), Share: float32(share),
		})
	}

	sections := make([]openapi.MeetingSection, 0, len(m.Notes.Sections))
	for _, sec := range m.Notes.Sections {
		sections = append(sections, openapi.MeetingSection{Title: sec.Title, Points: append([]string{}, sec.Points...)})
	}

	people := m.People
	if people == nil {
		people = []string{}
	}

	return openapi.Meeting{
		Id: m.ID, Title: m.Title, Titled: m.Titled, Language: optional(m.Language), People: people,
		FileName: m.FileName, MediaType: m.MediaType, Size: m.Size, DurationMs: m.Duration.Milliseconds(),
		State: openapi.MeetingState(m.State), Failure: optional(m.Failure), Provider: optional(m.Provider),
		Model: optional(m.Model), CreatedAt: m.CreatedAt,
		Notes: openapi.MeetingNotes{
			Format: m.Notes.Format, State: openapi.MeetingNotesState(m.Notes.State), Failure: optional(m.Notes.Failure),
			Model: optional(m.Notes.Model), Summary: m.Notes.Summary, Sections: sections, WrittenAt: m.Notes.WrittenAt,
		},
		Speakers: speakers, Actions: m.Actions, OpenActions: m.OpenActions,
	}
}

func itemOut(it meetingstore.Item, speakers []meetingstore.Speaker) openapi.MeetingItem {
	out := openapi.MeetingItem{
		Id: it.ID, Kind: openapi.MeetingItemKind(it.Kind), Text: it.Text, Due: optional(it.Due), Done: it.Done,
		Quote: optional(it.Quote), StartMs: millis(it.Start), Owner: optional(it.Owner),
		OwnerSpeaker: optional(it.OwnerSpeaker),
	}

	if it.Line > 0 {
		out.Line = new(it.Line)
	}

	for _, sp := range speakers {
		if sp.Label == it.OwnerSpeaker && it.OwnerSpeaker != "" {
			out.Owner = new(sp.Shown())
		}
	}

	return out
}

func millis(d *time.Duration) *int64 {
	if d == nil {
		return nil
	}

	return new(d.Milliseconds())
}
