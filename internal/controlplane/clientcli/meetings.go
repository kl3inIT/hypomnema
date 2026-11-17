package clientcli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Meetings from the terminal (issue #721): everything the Meetings page does, as every page's operation is a
// command (docs/conventions.md, API). A recording is uploaded and heard in the background; `meetings show` prints
// where it stands, its notes, its decisions and actions, and with --transcript its lines.

func meetingsList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	params := &openapi.ListMeetingsParams{Q: optional(fs, "q")}
	if n := number(fs, "limit"); n > 0 {
		params.Limit = &n
	}

	resp, err := e.api.ListMeetingsWithResponse(ctx, params)
	if err != nil {
		return fmt.Errorf("listing meetings: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing meetings", resp.StatusCode(), resp.JSONDefault)
	}

	list := resp.JSON200.Data

	return e.print(fs, list, func() {
		if len(list) == 0 {
			e.printf("No meetings.\n")

			return
		}

		for _, m := range list {
			e.printf("%s  %s  %s  %s  %s\n", m.Id, m.CreatedAt.Local().Format("2006-01-02 15:04"),
				minutes(m.DurationMs), meetingState(m), m.Title)
		}
	})
}

// meetingState is where a meeting stands, in the steps the page shows.
func meetingState(m openapi.Meeting) string {
	switch {
	case m.State == openapi.MeetingStateTranscribing:
		return "transcribing"
	case m.State == openapi.MeetingStateFailed:
		return "failed"
	case m.Notes.State == openapi.MeetingNotesStateWriting:
		return "writing notes"
	case m.Notes.State == openapi.MeetingNotesStateFailed:
		return "notes failed"
	case m.OpenActions > 0:
		return fmt.Sprintf("ready, %d open of %d actions", m.OpenActions, m.Actions)
	default:
		return "ready"
	}
}

func minutes(ms int64) string {
	return strconv.FormatInt((ms+30_000)/60_000, 10) + " min"
}

// at is a time in the recording as m:ss, or h:mm:ss past an hour.
func at(ms int64) string { return clock(float32(ms) / 1000) }

func meetingsShow(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	resp, err := e.api.GetMeetingWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("reading meeting %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return apiError("reading meeting "+id, resp.StatusCode(), resp.JSONDefault)
	}

	d := resp.JSON200

	return e.print(fs, d, func() { printMeeting(e, d, boolean(fs, "transcript")) })
}

func printMeeting(e *env, d *openapi.MeetingDetail, lines bool) {
	m := d.Meeting

	e.printf("%s\n%s · %s · %s\n", m.Title, m.CreatedAt.Local().Format("2006-01-02 15:04"), minutes(m.DurationMs),
		meetingState(m))

	if f := val(m.Failure); f != "" {
		e.printf("Failed: %s\n", f)
	}

	if f := val(m.Notes.Failure); f != "" && m.Notes.State == openapi.MeetingNotesStateFailed {
		e.printf("Notes failed: %s\n", f)
	}

	if len(m.Speakers) > 0 {
		e.printf("\nSpeakers:\n")

		for _, s := range m.Speakers {
			e.printf("  %-6s %s, %d%%\n", s.Label, s.Name, int(s.Share*100+0.5))
		}
	}

	if m.Notes.Summary != "" {
		e.printf("\n%s\n", m.Notes.Summary)
	}

	for _, sec := range m.Notes.Sections {
		if len(sec.Points) == 0 {
			continue
		}

		e.printf("\n%s:\n", sec.Title)

		for _, p := range sec.Points {
			e.printf("  - %s\n", p)
		}
	}

	for _, kind := range []openapi.MeetingItemKind{openapi.Decision, openapi.Action} {
		header := false

		for _, it := range d.Items {
			if it.Kind != kind {
				continue
			}

			if !header {
				header = true

				e.printf("\n%ss:\n", strings.ToUpper(string(kind[:1]))+string(kind[1:]))
			}

			e.printf("  %s\n", meetingItemLine(it))
		}
	}

	if !lines {
		return
	}

	names := map[string]string{}
	for _, s := range m.Speakers {
		names[s.Label] = s.Name
	}

	e.printf("\nTranscript:\n")

	for _, l := range d.Lines {
		e.printf("[%d %s] %s: %s\n", l.N, at(l.StartMs), names[l.Speaker], l.Text)
	}
}

func meetingItemLine(it openapi.MeetingItem) string {
	line := it.Text

	if it.Kind == openapi.Action {
		box := "[ ]"
		if it.Done {
			box = "[x]"
		}

		line = box + " " + it.Id + " " + line

		if who := val(it.Owner); who != "" {
			line += " · " + who
		}

		if due := val(it.Due); due != "" {
			line += " · due " + due
		}
	}

	if it.StartMs != nil {
		line += " · at " + at(*it.StartMs)
	}

	return line
}

func meetingsUpload(ctx context.Context, e *env, fs *flag.FlagSet) error {
	path := fs.Arg(0)

	f, err := os.Open(path) //nolint:gosec // the caller names the recording it wants uploaded
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read only

	params := &openapi.UploadMeetingParams{
		FileName: filepath.Base(path), Language: optional(fs, "language"), Title: optional(fs, "title"),
		People: commaList(str(fs, "people")), Format: optional(fs, "format"),
	}

	if kind := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); kind != "" {
		params.MediaType = &kind
	}

	resp, err := e.api.UploadMeetingWithBodyWithResponse(ctx, params, "application/octet-stream", f)
	if err != nil {
		return fmt.Errorf("uploading %s: %w", path, err)
	}

	if resp.JSON201 == nil {
		return apiError("uploading "+path, resp.StatusCode(), resp.JSONDefault)
	}

	m := resp.JSON201

	return e.print(fs, m, func() {
		e.printf("%s %s: transcribing; `hypomnema meetings show %s` says how it goes.\n", m.Id, m.Title, m.Id)
	})
}

func commaList(s string) *[]string {
	var out []string

	for v := range strings.SplitSeq(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}

	if len(out) == 0 {
		return nil
	}

	return &out
}

func meetingsRemove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	resp, err := e.api.DeleteMeetingWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("deleting meeting %s: %w", id, err)
	}

	if resp.StatusCode() != http.StatusNoContent {
		return apiError("deleting meeting "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, map[string]string{"deleted": id}, func() { e.printf("Deleted %s and its recording.\n", id) })
}

func meetingsNotes(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	format := str(fs, "format")
	if format == "" {
		return fmt.Errorf("%w: meetings notes <meeting_id> --format <format>", ErrUsage)
	}

	resp, err := e.api.WriteMeetingNotesWithResponse(ctx, id, openapi.WriteMeetingNotesRequest{Format: format})
	if err != nil {
		return fmt.Errorf("writing the notes of %s: %w", id, err)
	}

	if resp.JSON202 == nil {
		return apiError("writing the notes of "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON202, func() { e.printf("Writing the notes of %s again as %s.\n", id, format) })
}

func meetingsSpeaker(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id, label := fs.Arg(0), fs.Arg(1)

	resp, err := e.api.NameMeetingSpeakerWithResponse(ctx, id, label, openapi.SpeakerName{Name: fs.Arg(2)})
	if err != nil {
		return fmt.Errorf("naming a voice of %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return apiError("naming a voice of "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200.Meeting.Speakers, func() { printSpeakers(e, resp.JSON200.Meeting.Speakers) })
}

func meetingsLine(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	n, err := strconv.Atoi(fs.Arg(1))
	if err != nil || n < 1 {
		return fmt.Errorf("%w: the line is a number from 1", ErrUsage)
	}

	resp, err := e.api.SetMeetingLineSpeakerWithResponse(ctx, id, n, openapi.SpeakerName{Name: fs.Arg(2)})
	if err != nil {
		return fmt.Errorf("saying who spoke line %d of %s: %w", n, id, err)
	}

	if resp.JSON200 == nil {
		return apiError("saying who spoke a line of "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200.Meeting.Speakers, func() { printSpeakers(e, resp.JSON200.Meeting.Speakers) })
}

func printSpeakers(e *env, speakers []openapi.MeetingSpeaker) {
	for _, s := range speakers {
		e.printf("%-6s %s, %d%%\n", s.Label, s.Name, int(s.Share*100+0.5))
	}
}

func meetingsDone(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id, item := fs.Arg(0), fs.Arg(1)
	done := !boolean(fs, "undo")

	resp, err := e.api.UpdateMeetingItemWithResponse(ctx, id, item, openapi.MeetingItemUpdate{Done: done})
	if err != nil {
		return fmt.Errorf("marking action %s: %w", item, err)
	}

	if resp.JSON200 == nil {
		return apiError("marking action "+item, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("%s\n", meetingItemLine(*resp.JSON200)) })
}

func meetingsActions(ctx context.Context, e *env, fs *flag.FlagSet) error {
	params := &openapi.ListMeetingActionsParams{Who: optional(fs, "who")}
	if s := str(fs, "state"); s != "" {
		state := openapi.ListMeetingActionsParamsState(s)
		params.State = &state
	}

	resp, err := e.api.ListMeetingActionsWithResponse(ctx, params)
	if err != nil {
		return fmt.Errorf("listing actions: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing actions", resp.StatusCode(), resp.JSONDefault)
	}

	list := resp.JSON200

	return e.print(fs, list, func() {
		if len(list.Data) == 0 {
			e.printf("No actions.\n")

			return
		}

		for _, a := range list.Data {
			box := "[ ]"
			if a.Done {
				box = "[x]"
			}

			line := box + " " + a.Text
			if who := val(a.Who); who != "" {
				line += " · " + who
			}

			if due := val(a.Due); due != "" {
				line += " · due " + due
			}

			line += " · " + a.MeetingTitle
			if a.StartMs != nil {
				line += " at " + at(*a.StartMs)
			}

			e.printf("%s (%s %s)\n", line, a.MeetingId, a.Id)
		}
	})
}

func meetingsRecording(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	info, err := e.api.GetMeetingWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("reading meeting %s: %w", id, err)
	}

	if info.JSON200 == nil {
		return apiError("reading meeting "+id, info.StatusCode(), info.JSONDefault)
	}

	resp, err := e.api.GetMeetingRecording(ctx, id)
	if err != nil {
		return fmt.Errorf("downloading the recording of %s: %w", id, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read only

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading the recording of %s: HTTP %d", id, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("downloading the recording of %s: %w", id, err)
	}

	name := filepath.Base(info.JSON200.Meeting.FileName)
	if out := str(fs, "out"); out != "" {
		name = out
	}

	if err := saveNew(name, data); err != nil {
		return err
	}

	return e.print(fs, map[string]any{"file": name, "size": len(data)}, func() {
		e.printf("saved the recording of %s as %s (%s)\n", id, name, size(int64(len(data))))
	})
}

func meetingsSettings(ctx context.Context, e *env, fs *flag.FlagSet) error {
	var out *openapi.MeetingSettings

	if model := fs.Lookup("notes-model"); model != nil && isSet(fs, "notes-model") {
		resp, err := e.api.SetMeetingSettingsWithResponse(ctx, openapi.MeetingSettingsUpdate{
			NotesModel: model.Value.String(),
		})
		if err != nil {
			return fmt.Errorf("setting the notes model: %w", err)
		}

		if resp.JSON200 == nil {
			return apiError("setting the notes model", resp.StatusCode(), resp.JSONDefault)
		}

		out = resp.JSON200
	} else {
		resp, err := e.api.GetMeetingSettingsWithResponse(ctx)
		if err != nil {
			return fmt.Errorf("reading the meetings' settings: %w", err)
		}

		if resp.JSON200 == nil {
			return apiError("reading the meetings' settings", resp.StatusCode(), resp.JSONDefault)
		}

		out = resp.JSON200
	}

	return e.print(fs, out, func() {
		model := out.NotesModel
		if model == "" {
			model = "none picked"
		}

		e.printf("Notes model: %s\nFormats:\n", model)

		for _, f := range out.Formats {
			titles := make([]string, 0, len(f.Sections))
			for _, s := range f.Sections {
				titles = append(titles, s.Title)
			}

			mine := ""
			if !f.Builtin {
				mine = " (yours)"
			}

			e.printf("  %-24s %s%s: %s\n", f.Id, f.Name, mine, strings.Join(titles, ", "))
		}
	})
}

// The words to hear right and the owner's notes formats (issue #747), as Settings → Meetings has them.

func meetingsWords(ctx context.Context, e *env, fs *flag.FlagSet) error {
	add, remove := commaList(str(fs, "add")), commaList(str(fs, "remove"))

	read, err := e.api.ListMeetingWordsWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("reading the words to hear right: %w", err)
	}

	if read.JSON200 == nil {
		return apiError("reading the words to hear right", read.StatusCode(), read.JSONDefault)
	}

	out := read.JSON200

	if add != nil || remove != nil {
		own := []string{}

		for _, w := range out.Data {
			if w.From == openapi.MeetingWordSourceOwner && !holds(remove, w.Word) {
				own = append(own, w.Word)
			}
		}

		if add != nil {
			for _, w := range *add {
				if !holds(&own, w) {
					own = append(own, w)
				}
			}
		}

		set, err := e.api.SetMeetingWordsWithResponse(ctx, openapi.MeetingWordsUpdate{Words: own})
		if err != nil {
			return fmt.Errorf("setting the words to hear right: %w", err)
		}

		if set.JSON200 == nil {
			return apiError("setting the words to hear right", set.StatusCode(), set.JSONDefault)
		}

		out = set.JSON200
	}

	return e.print(fs, out, func() {
		if len(out.Data) == 0 {
			e.printf("No words yet.\n")

			return
		}

		for _, w := range out.Data {
			sent := ""
			if !w.Sent {
				sent = " (not sent: past the first " + strconv.Itoa(out.MaxSent) + ")"
			}

			e.printf("%-12s %s%s\n", w.From, w.Word, sent)
		}
	})
}

// holds says whether a list holds a word, in any case.
func holds(list *[]string, word string) bool {
	if list == nil {
		return false
	}

	for _, w := range *list {
		if strings.EqualFold(strings.TrimSpace(w), strings.TrimSpace(word)) {
			return true
		}
	}

	return false
}

func meetingsFix(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	body := openapi.MeetingWordFix{From: fs.Arg(1), To: fs.Arg(2)}
	if n := number(fs, "line"); n > 0 {
		body.Line = &n
	}

	resp, err := e.api.FixMeetingWordWithResponse(ctx, id, body)
	if err != nil {
		return fmt.Errorf("fixing a word of %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return apiError("fixing a word of "+id, resp.StatusCode(), resp.JSONDefault)
	}

	fixed := resp.JSON200

	return e.print(fs, fixed, func() {
		e.printf("Replaced %d times.\n", fixed.Replaced)

		if fixed.Learned {
			e.printf("%q is in your words to hear right.\n", fs.Arg(2))
		} else {
			e.printf("%q is not in your words to hear right: the list is full or the words too long.\n", fs.Arg(2))
		}

		e.printf("The notes are as they were; `hypomnema meetings notes %s --format %s` writes them again.\n", id,
			fixed.Detail.Meeting.Notes.Format)
	})
}

// sectionsFlag is --section, given once a section as "Title: what it holds".
type sectionsFlag struct {
	list []openapi.MeetingFormatSectionInput
}

func (f *sectionsFlag) String() string {
	if f == nil {
		return ""
	}

	titles := make([]string, 0, len(f.list))
	for _, s := range f.list {
		titles = append(titles, s.Title)
	}

	return strings.Join(titles, ", ")
}

func (f *sectionsFlag) Set(v string) error {
	title, about, _ := strings.Cut(v, ": ")

	sec := openapi.MeetingFormatSectionInput{Title: strings.TrimSpace(title)}
	if about = strings.TrimSpace(about); about != "" {
		sec.About = &about
	}

	f.list = append(f.list, sec)

	return nil
}

func sectionsOf(fs *flag.FlagSet) []openapi.MeetingFormatSectionInput {
	if f, ok := fs.Lookup("section").Value.(*sectionsFlag); ok {
		return f.list
	}

	return nil
}

func meetingsFormatAdd(ctx context.Context, e *env, fs *flag.FlagSet) error {
	sections := sectionsOf(fs)
	if len(sections) == 0 {
		return fmt.Errorf("%w: meetings format add <name> --section \"Title: what it holds\" …", ErrUsage)
	}

	resp, err := e.api.CreateMeetingFormatWithResponse(ctx, openapi.MeetingFormatInput{
		Name: fs.Arg(0), Sections: sections,
	})
	if err != nil {
		return fmt.Errorf("making a notes format: %w", err)
	}

	if resp.JSON201 == nil {
		return apiError("making a notes format", resp.StatusCode(), resp.JSONDefault)
	}

	f := resp.JSON201

	return e.print(fs, f, func() { e.printf("Made %s %s.\n", f.Id, f.Name) })
}

func meetingsFormatEdit(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	read, err := e.api.GetMeetingSettingsWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("reading the notes formats: %w", err)
	}

	if read.JSON200 == nil {
		return apiError("reading the notes formats", read.StatusCode(), read.JSONDefault)
	}

	var had *openapi.MeetingFormat

	for i := range read.JSON200.Formats {
		if read.JSON200.Formats[i].Id == id {
			had = &read.JSON200.Formats[i]
		}
	}

	if had == nil {
		return fmt.Errorf("%w: no notes format %s", ErrUsage, id)
	}

	in := openapi.MeetingFormatInput{Name: had.Name, Sections: sectionsOf(fs)}
	if name := str(fs, "name"); name != "" {
		in.Name = name
	}

	if len(in.Sections) == 0 {
		for _, s := range had.Sections {
			in.Sections = append(in.Sections, openapi.MeetingFormatSectionInput{Title: s.Title, About: &s.About})
		}
	}

	resp, err := e.api.UpdateMeetingFormatWithResponse(ctx, id, in)
	if err != nil {
		return fmt.Errorf("changing notes format %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return apiError("changing notes format "+id, resp.StatusCode(), resp.JSONDefault)
	}

	f := resp.JSON200

	return e.print(fs, f, func() { e.printf("Changed %s %s.\n", f.Id, f.Name) })
}

func meetingsFormatRemove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	resp, err := e.api.DeleteMeetingFormatWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("deleting notes format %s: %w", id, err)
	}

	if resp.StatusCode() != http.StatusNoContent {
		return apiError("deleting notes format "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, map[string]string{"deleted": id}, func() { e.printf("Deleted %s.\n", id) })
}

func isSet(fs *flag.FlagSet, name string) bool {
	set := false

	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})

	return set
}
