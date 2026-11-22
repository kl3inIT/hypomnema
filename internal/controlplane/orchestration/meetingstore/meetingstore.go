// Package meetingstore keeps the meetings (issue #721): a recording the owner
// uploaded, its transcript with speakers, its notes and the decisions and
// actions in them. It is a domain store of the orchestration database reached
// through the root package's Meetings(), as Omnara's storage facade reaches
// its stores (issue #404); it imports the generated store and never the root
// package, and writes no other domain's tables. Hearing a recording and
// writing its notes is the meetings package's; the recording's bytes are a
// file it keeps.
//
// What is kept follows MemoryOS's meeting tables (V108 meeting, meeting_speaker,
// meeting_utterance; V109 the minutes; V197 an action owned by a voice; V200
// the label the provider heard kept beside the voice a line is shown as) and
// Nojoin's speaker corrections (backend/models/pipeline.py:77-81): a voice is
// renamed for the whole meeting or one line is given to another voice, and two
// voices given one name become one (Nojoin's merge_into_speaker, Otter's and
// silent-notetaker's merge).
package meetingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// Where a meeting's transcription stands: a live one is recording until the owner ends it (issue #820).
const (
	Recording    = "recording"
	Transcribing = "transcribing"
	Transcribed  = "transcribed"
	Failed       = "failed"
)

// Where its notes stand: waiting for the transcript, being written, ready or failed.
const (
	NotesWaiting = "waiting"
	NotesWriting = "writing"
	NotesReady   = "ready"
	NotesFailed  = "failed"
)

// How a meeting came: a recording uploaded, or recorded live from the browser (issue #820).
const (
	SourceUpload = "upload"
	SourceLive   = "live"
)

// How a live meeting is held: online, the microphone and a shared tab; in person, the microphone only (MemoryOS's
// meeting kinds).
const (
	Online   = "online"
	InPerson = "in_person"
)

// The tracks a voice is heard on (MemoryOS's meeting tracks): an upload's file, a live meeting's microphone and its
// shared tab.
const (
	TrackFile = "file"
	TrackMic  = "mic"
	TrackTab  = "tab"
)

// The kinds of item in the notes.
const (
	Decision = "decision"
	Action   = "action"
)

// Limits of what the owner gives.
const (
	MaxTitle  = 200
	MaxName   = 100
	MaxPeople = 30
)

// Meeting is a meeting, with its notes' state and its voices.
type Meeting struct {
	ID    string
	Title string
	// Titled: the owner gave the title; otherwise the notes write one.
	Titled    bool
	Language  string
	People    []string
	FileName  string
	MediaType string
	Size      int64
	Duration  time.Duration
	State     string
	Failure   string
	Attempts  int
	Provider  string
	Model     string
	CreatedAt time.Time
	UpdatedAt time.Time
	// Source is how it came (SourceUpload, SourceLive); Kind how a live one is held (Online, InPerson).
	Source   string
	Kind     string
	Notes    Notes
	Speakers []Speaker
	// Actions counts the actions in the notes, OpenActions those not done.
	Actions     int
	OpenActions int
}

// Notes are what a model wrote of a meeting.
type Notes struct {
	State     string
	Failure   string
	Attempts  int
	Model     string
	Summary   string
	Sections  []Section
	WrittenAt *time.Time
}

// Section is one section of the notes, a topic of the meeting as written: its title and its points.
type Section struct {
	Title  string   `json:"title"`
	Points []string `json:"points"`
}

// Speaker is a voice of a meeting, one of a track's.
type Speaker struct {
	Track    string
	Label    string
	Position int
	// Name is what the owner called it; empty when it is still "Speaker N".
	Name string
	// Owner: the owner's own voice, an online meeting's microphone, shown as "You" and not renamed (issue #820).
	Owner bool
	// Talk is how long its lines last.
	Talk time.Duration
}

// You is how the owner's own voice is shown.
const You = "You"

// Shown is the voice's name, "You" for the owner's microphone online, or "Speaker N" by the order it first spoke in
// on its track (MemoryOS SpeakerNames, Nojoin's "Speaker N").
func (s Speaker) Shown() string {
	switch {
	case s.Name != "":
		return s.Name
	case s.Owner:
		return You
	default:
		return "Speaker " + strconv.Itoa(s.Position)
	}
}

// Key names a voice within its meeting: its track and its label.
func (s Speaker) Key() string { return VoiceKey(s.Track, s.Label) }

// VoiceKey names the voice of a track's label.
func VoiceKey(track, label string) string { return track + "/" + label }

// ownerVoice says the voice is the owner's own: online, the microphone carries only the owner (MemoryOS's
// MeetingService.openTrack, which does not diarize it), and a voice the owner named for a line is not.
func ownerVoice(kind, track string, named bool) bool {
	return kind == Online && track == TrackMic && !named
}

// Line is a line of the transcript.
type Line struct {
	N int
	// Track is the track it was heard on; Speaker a voice of that track.
	Track      string
	Start, End time.Duration
	// Speaker is the voice the line is shown as; Heard the label the provider gave it.
	Speaker string
	Heard   string
	Text    string
}

// Item is a decision or an action of the notes.
type Item struct {
	ID       string
	Kind     string
	Position int
	Text     string
	// Owner is who does an action as the notes said it, when that is no voice of the meeting; OwnerSpeaker the
	// voice that does it.
	Owner string
	// OwnerTrack is OwnerSpeaker's track.
	OwnerTrack   string
	OwnerSpeaker string
	Due          string
	Done         bool
	Quote        string
	// Line is the line the item rests on, 0 for none; Start when it was said.
	Line  int
	Start *time.Duration
}

// MeetingAction is an action with its meeting, for the Actions page.
type MeetingAction struct {
	Item

	MeetingID    string
	MeetingTitle string
	MeetingAt    time.Time
	// Who is the voice's name or "Speaker N", or the owner as said; empty for nobody.
	Who string
}

// Settings are what the owner set on Settings → Meetings.
type Settings struct {
	// NotesModel is the gateway model that writes notes; empty: none picked.
	NotesModel string
}

// Store keeps the meetings.
type Store struct {
	db     *sql.DB
	clock  platform.Clock
	newID  func(prefix string) string
	signal func()
}

// New returns the Store over the orchestration database.
func New(db *sql.DB, clock platform.Clock, newID func(prefix string) string, signal func()) *Store {
	return &Store{db: db, clock: clock, newID: newID, signal: signal}
}

func (s *Store) write(ctx context.Context, fn func(q *store.Queries, now int64) error) error {
	return storeutil.Write(ctx, s.db, s.clock, s.signal, fn)
}

func (s *Store) read() *store.Queries { return store.New(s.db) }

// NewMeeting is a recording the owner uploaded.
type NewMeeting struct {
	// ID is the meeting's id, made with NewID before its recording is kept.
	ID        string
	Title     string
	Language  string
	People    []string
	FileName  string
	MediaType string
	Size      int64
}

// NewID is the id of a meeting about to be created.
func (s *Store) NewID() string { return s.newID("mtg") }

// Create records a meeting to transcribe.
func (s *Store) Create(ctx context.Context, nm NewMeeting) (Meeting, error) {
	title := strings.TrimSpace(nm.Title)
	titled := title != ""

	if !titled {
		title = strings.TrimSpace(strings.TrimSuffix(nm.FileName, fileExt(nm.FileName)))
	}

	if title == "" {
		title = "Meeting"
	}

	if err := checkText(title, MaxTitle, "a title"); err != nil {
		return Meeting{}, err
	}

	people, err := cleanList(nm.People, MaxPeople, MaxName, "people")
	if err != nil {
		return Meeting{}, err
	}

	if nm.ID == "" || nm.Size <= 0 || nm.FileName == "" {
		return Meeting{}, fmt.Errorf("%w: a meeting needs its recording", storeerr.ErrInvalid)
	}

	peopleJSON, err := json.Marshal(people)
	if err != nil {
		return Meeting{}, fmt.Errorf("encoding the people: %w", err)
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.InsertMeeting(ctx, store.InsertMeetingParams{
			ID: nm.ID, Title: title, Titled: flag(titled), Language: nm.Language, People: string(peopleJSON),
			FileName: nm.FileName, MediaType: nm.MediaType, Size: nm.Size, Now: now,
		}); err != nil {
			return fmt.Errorf("storing meeting %s: %w", nm.ID, err)
		}

		return q.InsertMeetingNotes(ctx, store.InsertMeetingNotesParams{MeetingID: nm.ID, Now: now})
	}); err != nil {
		return Meeting{}, err
	}

	return s.Get(ctx, nm.ID)
}

// Get returns a meeting with its notes and voices.
func (s *Store) Get(ctx context.Context, id string) (Meeting, error) {
	q := s.read()

	row, err := q.GetMeeting(ctx, id)
	if err != nil {
		return Meeting{}, storeutil.NotFound(err, "meeting", id)
	}

	notes, err := q.GetMeetingNotes(ctx, id)
	if err != nil {
		return Meeting{}, storeutil.NotFound(err, "the notes of meeting", id)
	}

	m := meetingOf(row)
	m.Notes = notesOf(notes)

	speakers, err := s.speakersOf(ctx, q, []string{id})
	if err != nil {
		return Meeting{}, err
	}

	m.Speakers = speakers[id]

	items, err := q.ListMeetingItems(ctx, id)
	if err != nil {
		return Meeting{}, fmt.Errorf("reading the items of meeting %s: %w", id, err)
	}

	for _, it := range items {
		if it.Kind == Action {
			m.Actions++

			if it.Done == 0 {
				m.OpenActions++
			}
		}
	}

	return m, nil
}

// List returns the meetings, the newest first; with a query, those whose title or transcript holds it.
func (s *Store) List(ctx context.Context, query string, limit int) ([]Meeting, error) {
	q := s.read()

	rows, err := q.ListMeetings(ctx, store.ListMeetingsParams{Query: strings.TrimSpace(query), Lim: int64(limit)})
	if err != nil {
		return nil, fmt.Errorf("listing meetings: %w", err)
	}

	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}

	speakers, err := s.speakersOf(ctx, q, ids)
	if err != nil {
		return nil, err
	}

	out := make([]Meeting, 0, len(rows))

	for _, r := range rows {
		m := meetingOf(store.Meeting{
			ID: r.ID, Title: r.Title, Titled: r.Titled, Language: r.Language, People: r.People, Terms: r.Terms,
			FileName: r.FileName, MediaType: r.MediaType, Size: r.Size, DurationMs: r.DurationMs, State: r.State,
			Failure: r.Failure, Attempts: r.Attempts, Provider: r.Provider, Model: r.Model, CreatedAt: r.CreatedAt,
			UpdatedAt: r.UpdatedAt, Source: r.Source, Kind: r.Kind,
		})
		// When the notes were written, which moves only meeting_notes: the Thư ký finds a meeting by it (#1023).
		m.Notes = Notes{State: r.NotesState, Summary: r.NotesSummary, WrittenAt: storeutil.OptionalMillis(r.NotesWrittenAt)}
		m.Speakers = speakers[r.ID]
		m.Actions, m.OpenActions = int(r.Actions), int(r.OpenActions)
		out = append(out, m)
	}

	return out, nil
}

func (s *Store) speakersOf(ctx context.Context, q *store.Queries, ids []string) (map[string][]Speaker, error) {
	out := map[string][]Speaker{}
	if len(ids) == 0 {
		return out, nil
	}

	rows, err := q.ListSpeakersOfMeetings(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("reading the voices of meetings: %w", err)
	}

	for _, r := range rows {
		out[r.MeetingID] = append(out[r.MeetingID], Speaker{
			Track: r.Track, Label: r.Label, Position: int(r.Position), Name: r.Name.String,
			Owner: ownerVoice(r.MeetingKind, r.Track, r.Name.Valid), Talk: time.Duration(r.TalkMs) * time.Millisecond,
		})
	}

	return out, nil
}

// Lines returns a meeting's transcript.
func (s *Store) Lines(ctx context.Context, id string) ([]Line, error) {
	rows, err := s.read().ListMeetingLines(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("reading the transcript of meeting %s: %w", id, err)
	}

	out := make([]Line, 0, len(rows))
	for _, r := range rows {
		out = append(out, lineOf(r))
	}

	return out, nil
}

// Items returns a meeting's decisions and actions, decisions first, each in the notes' order.
func (s *Store) Items(ctx context.Context, id string) ([]Item, error) {
	rows, err := s.read().ListMeetingItems(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("reading the items of meeting %s: %w", id, err)
	}

	out := make([]Item, 0, len(rows))
	for _, r := range rows {
		out = append(out, itemOf(r))
	}

	return out, nil
}

// Actions returns the actions of every meeting, the newest meeting first.
func (s *Store) Actions(ctx context.Context) ([]MeetingAction, error) {
	rows, err := s.read().ListMeetingActions(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the actions of meetings: %w", err)
	}

	out := make([]MeetingAction, 0, len(rows))

	for _, r := range rows {
		a := MeetingAction{
			Item: itemOf(store.MeetingItem{
				ID: r.ID, MeetingID: r.MeetingID, Kind: r.Kind, Position: r.Position, Text: r.Text, Owner: r.Owner,
				OwnerSpeaker: r.OwnerSpeaker, OwnerTrack: r.OwnerTrack, Due: r.Due, Done: r.Done, Quote: r.Quote,
				Line: r.Line, StartMs: r.StartMs,
			}),
			MeetingID: r.MeetingID, MeetingTitle: r.MeetingTitle, MeetingAt: storeutil.Millis(r.MeetingCreatedAt),
		}

		switch {
		case r.OwnerSpeaker.Valid && r.SpeakerPosition.Valid:
			a.Who = Speaker{
				Name: r.SpeakerName.String, Position: int(r.SpeakerPosition.Int64),
				Owner: ownerVoice(r.MeetingKind, r.OwnerTrack.String, r.SpeakerName.Valid),
			}.Shown()
		default:
			a.Who = r.Owner.String
		}

		out = append(out, a)
	}

	return out, nil
}

// Transcribing lists the meetings whose transcription is to be done, the oldest first.
func (s *Store) Transcribing(ctx context.Context) ([]string, error) {
	ids, err := s.read().MeetingsTranscribing(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the meetings to transcribe: %w", err)
	}

	return ids, nil
}

// NotesToWrite lists the meetings whose notes are to be written, the longest waiting first.
func (s *Store) NotesToWrite(ctx context.Context) ([]string, error) {
	ids, err := s.read().NotesWriting(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the notes to write: %w", err)
	}

	return ids, nil
}

// ClaimTranscription counts one more attempt at a meeting's transcription and returns how many there have been;
// false when it is not to be transcribed.
func (s *Store) ClaimTranscription(ctx context.Context, id string) (int, bool, error) {
	var claimed bool

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.ClaimMeetingTranscription(ctx, store.ClaimMeetingTranscriptionParams{Now: now, ID: id})
		claimed = n == 1

		return err
	}); err != nil {
		return 0, false, fmt.Errorf("taking up meeting %s: %w", id, err)
	}

	if !claimed {
		return 0, false, nil
	}

	m, err := s.read().GetMeeting(ctx, id)
	if err != nil {
		return 0, false, storeutil.NotFound(err, "meeting", id)
	}

	return int(m.Attempts), true, nil
}

// Fail ends a meeting's transcription with why.
func (s *Store) Fail(ctx context.Context, id, reason string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		return q.FailMeeting(ctx, store.FailMeetingParams{Failure: clip(reason, 2000), Now: now, ID: id})
	})
}

// Heard is what a provider heard in a meeting.
type Heard struct {
	Duration time.Duration
	Language string
	Provider string
	Model    string
	Lines    []HeardLine
}

// HeardLine is a stretch one voice said.
type HeardLine struct {
	Start, End time.Duration
	// Speaker is the provider's label; empty when it told none.
	Speaker string
	Text    string
}

// noSpeaker labels the lines of a provider that told no speaker.
const noSpeaker = "1"

// SaveTranscript keeps what was heard: the voices in the order they first spoke, the lines, and the notes are
// then to be written.
func (s *Store) SaveTranscript(ctx context.Context, id string, h Heard) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.DeleteMeetingItems(ctx, id); err != nil {
			return err
		}

		if err := q.DeleteMeetingTranscript(ctx, id); err != nil {
			return err
		}

		if err := q.DeleteMeetingSpeakers(ctx, id); err != nil {
			return err
		}

		seen := map[string]bool{}

		for i, l := range h.Lines {
			label := l.Speaker
			if label == "" {
				label = noSpeaker
			}

			label = clip(label, 32)

			if !seen[label] {
				seen[label] = true

				if err := q.InsertMeetingSpeaker(ctx, store.InsertMeetingSpeakerParams{
					MeetingID: id, Track: TrackFile, Label: label, Position: int64(len(seen)),
				}); err != nil {
					return fmt.Errorf("storing a voice of meeting %s: %w", id, err)
				}
			}

			if err := q.InsertMeetingLine(ctx, store.InsertMeetingLineParams{
				MeetingID: id, N: int64(i + 1), Track: TrackFile, StartMs: l.Start.Milliseconds(),
				EndMs: max(l.End, l.Start).Milliseconds(), Speaker: label, Heard: l.Speaker, Text: l.Text,
			}); err != nil {
				return fmt.Errorf("storing a line of meeting %s: %w", id, err)
			}
		}

		if err := q.TranscribedMeeting(ctx, store.TranscribedMeetingParams{
			DurationMs: h.Duration.Milliseconds(), Provider: h.Provider, Model: h.Model, Language: h.Language,
			Now: now, ID: id,
		}); err != nil {
			return err
		}

		return q.SetNotesState(ctx, store.SetNotesStateParams{State: NotesWriting, Now: now, MeetingID: id})
	})
}

// ClaimNotes counts one more attempt at writing a meeting's notes and returns how many there have been; false
// when they are not to be written.
func (s *Store) ClaimNotes(ctx context.Context, id string) (int, bool, error) {
	var claimed bool

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.ClaimNotes(ctx, store.ClaimNotesParams{Now: now, MeetingID: id})
		claimed = n == 1

		return err
	}); err != nil {
		return 0, false, fmt.Errorf("taking up the notes of meeting %s: %w", id, err)
	}

	if !claimed {
		return 0, false, nil
	}

	n, err := s.read().GetMeetingNotes(ctx, id)
	if err != nil {
		return 0, false, storeutil.NotFound(err, "the notes of meeting", id)
	}

	return int(n.Attempts), true, nil
}

// FailNotes ends the writing of a meeting's notes with why.
func (s *Store) FailNotes(ctx context.Context, id, reason string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		return q.SetNotesState(ctx, store.SetNotesStateParams{
			State: NotesFailed, Failure: clip(reason, 2000), Now: now, MeetingID: id,
		})
	})
}

// Written are notes a model wrote.
type Written struct {
	Model    string
	Title    string
	Summary  string
	Sections []Section
	Items    []Item
}

// SaveNotes keeps the notes and their items in place of any before, and the title they wrote for a meeting the
// owner did not title.
func (s *Store) SaveNotes(ctx context.Context, id string, w Written) error {
	sections, err := json.Marshal(w.Sections)
	if err != nil {
		return fmt.Errorf("encoding the sections: %w", err)
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.DeleteMeetingItems(ctx, id); err != nil {
			return err
		}

		for i, it := range w.Items {
			var start sql.NullInt64
			if it.Start != nil {
				start = sql.NullInt64{Int64: it.Start.Milliseconds(), Valid: true}
			}

			if err := q.InsertMeetingItem(ctx, store.InsertMeetingItemParams{
				ID: s.newID("mti"), MeetingID: id, Kind: it.Kind, Position: int64(i), Text: clip(it.Text, 2000),
				Owner: text(it.Owner), OwnerTrack: text(ownerTrack(it)), OwnerSpeaker: text(it.OwnerSpeaker),
				Due:   text(it.Due),
				Quote: text(clip(it.Quote, 2000)), Line: sql.NullInt64{Int64: int64(it.Line), Valid: it.Line > 0},
				StartMs: start,
			}); err != nil {
				return fmt.Errorf("storing an item of meeting %s: %w", id, err)
			}
		}

		if title := strings.TrimSpace(w.Title); title != "" && checkText(title, MaxTitle, "") == nil {
			if err := q.SetWrittenTitle(ctx, store.SetWrittenTitleParams{Title: title, Now: now, ID: id}); err != nil {
				return err
			}
		}

		// A voice that the notes no longer name as an action's owner and that says nothing goes.
		if err := q.DeleteSilentSpeakers(ctx, id); err != nil {
			return err
		}

		return q.SaveNotes(ctx, store.SaveNotesParams{
			Model: w.Model, Summary: w.Summary, Sections: string(sections), Now: sql.NullInt64{Int64: now, Valid: true},
			MeetingID: id,
		})
	})
}

// WriteAgain asks for a meeting's notes again (Granola's, Fireflies's and Otter's regenerate); not while they are
// being written or before the transcript is there.
func (s *Store) WriteAgain(ctx context.Context, id string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.WriteNotesAgain(ctx, store.WriteNotesAgainParams{Now: now, MeetingID: id})
		if err != nil {
			return err
		}

		if n == 1 {
			return nil
		}

		if _, err := q.GetMeeting(ctx, id); err != nil {
			return storeutil.NotFound(err, "meeting", id)
		}

		return fmt.Errorf("%w: the notes are being written, or the meeting is not transcribed", storeerr.ErrConflict)
	})
}

// NameSpeaker names a voice of a track for the whole meeting; an empty track is the one track holding the label. A
// name another voice of the track already has makes the two one: its lines and actions go to that voice (Nojoin's
// merge_into_speaker); the voices of another track are other people's (issue #820). An empty name gives the voice
// back its number. The owner's own voice online is "You" and is not renamed.
func (s *Store) NameSpeaker(ctx context.Context, id, track, label, name string) error {
	name = strings.TrimSpace(name)
	if name != "" {
		if err := checkText(name, MaxName, "a name"); err != nil {
			return err
		}
	}

	return s.write(ctx, func(q *store.Queries, _ int64) error {
		speakers, err := speakersIn(ctx, q, id)
		if err != nil {
			return err
		}

		self, err := findLabel(speakers, track, label)
		if err != nil {
			return fmt.Errorf("meeting %s: %w", id, err)
		}

		if self.Owner {
			return fmt.Errorf("%w: the microphone of an online meeting is your own voice", storeerr.ErrInvalid)
		}

		if name == "" {
			return q.NameMeetingSpeaker(ctx, store.NameMeetingSpeakerParams{
				MeetingID: id, Track: self.Track, Label: self.Label,
			})
		}

		if other, ok := findName(onTrack(speakers, self.Track), name); ok && other.Label != self.Label {
			return merge(ctx, q, id, self.Track, self.Label, other.Label)
		}

		return q.NameMeetingSpeaker(ctx, store.NameMeetingSpeakerParams{
			Name: sql.NullString{String: name, Valid: true}, MeetingID: id, Track: self.Track, Label: self.Label,
		})
	})
}

// SetLineSpeaker gives one line to the voice called name on the line's track: one of the track's, or a new one made
// for it.
func (s *Store) SetLineSpeaker(ctx context.Context, id string, n int, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: a line is given to a name", storeerr.ErrInvalid)
	}

	if err := checkText(name, MaxName, "a name"); err != nil {
		return err
	}

	return s.write(ctx, func(q *store.Queries, _ int64) error {
		line, err := q.GetMeetingLine(ctx, store.GetMeetingLineParams{MeetingID: id, N: int64(n)})
		if err != nil {
			return storeutil.NotFound(err, "line "+strconv.Itoa(n)+" of meeting", id)
		}

		speakers, err := speakersIn(ctx, q, id)
		if err != nil {
			return err
		}

		track := onTrack(speakers, line.Track)

		target, ok := findName(track, name)
		if !ok {
			target = Speaker{Track: line.Track, Label: freeLabel(track), Position: nextPosition(track), Name: name}

			if err := q.InsertMeetingSpeaker(ctx, store.InsertMeetingSpeakerParams{
				MeetingID: id, Track: target.Track, Label: target.Label, Position: int64(target.Position),
				Name: sql.NullString{String: name, Valid: true},
			}); err != nil {
				return fmt.Errorf("storing a voice of meeting %s: %w", id, err)
			}
		}

		if err := q.MoveMeetingLine(ctx, store.MoveMeetingLineParams{
			Speaker: target.Label, MeetingID: id, N: int64(n),
		}); err != nil {
			return err
		}

		return q.DeleteSilentSpeakers(ctx, id)
	})
}

// merge makes voice from one with voice to, both of one track.
func merge(ctx context.Context, q *store.Queries, id, track, from, to string) error {
	if err := q.MoveMeetingLines(ctx, store.MoveMeetingLinesParams{
		ToLabel: to, MeetingID: id, Track: track, FromLabel: from,
	}); err != nil {
		return err
	}

	if err := q.MoveMeetingItemOwners(ctx, store.MoveMeetingItemOwnersParams{
		ToLabel: sql.NullString{String: to, Valid: true}, MeetingID: id,
		Track: sql.NullString{String: track, Valid: true}, FromLabel: sql.NullString{String: from, Valid: true},
	}); err != nil {
		return err
	}

	return q.DeleteSilentSpeakers(ctx, id)
}

// SetDone marks an action done or not done.
func (s *Store) SetDone(ctx context.Context, itemID string, done bool) (Item, error) {
	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		n, err := q.SetMeetingItemDone(ctx, store.SetMeetingItemDoneParams{Done: flag(done), ID: itemID})
		if err != nil {
			return err
		}

		if n == 0 {
			return fmt.Errorf("%w: no action %s", storeerr.ErrNotFound, itemID)
		}

		return nil
	}); err != nil {
		return Item{}, err
	}

	row, err := s.read().GetMeetingItem(ctx, itemID)
	if err != nil {
		return Item{}, storeutil.NotFound(err, "action", itemID)
	}

	return itemOf(row), nil
}

// MeetingOfItem returns the meeting an item is in.
func (s *Store) MeetingOfItem(ctx context.Context, itemID string) (string, error) {
	row, err := s.read().GetMeetingItem(ctx, itemID)
	if err != nil {
		return "", storeutil.NotFound(err, "item", itemID)
	}

	return row.MeetingID, nil
}

// Delete removes a meeting with its transcript and notes.
func (s *Store) Delete(ctx context.Context, id string) error {
	return s.write(ctx, func(q *store.Queries, _ int64) error {
		n, err := q.DeleteMeeting(ctx, id)
		if err != nil {
			return fmt.Errorf("deleting meeting %s: %w", id, err)
		}

		if n == 0 {
			return fmt.Errorf("%w: meeting %s", storeerr.ErrNotFound, id)
		}

		return nil
	})
}

// GetSettings returns what the owner set; nothing set is the zero Settings.
func (s *Store) GetSettings(ctx context.Context) (Settings, error) {
	row, err := s.read().GetMeetingSettings(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{}, nil
	}

	if err != nil {
		return Settings{}, fmt.Errorf("reading the meetings' settings: %w", err)
	}

	return Settings{NotesModel: row.NotesModel}, nil
}

// SetSettings keeps what the owner set.
func (s *Store) SetSettings(ctx context.Context, set Settings) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		return q.SetMeetingSettings(ctx, store.SetMeetingSettingsParams{
			NotesModel: strings.TrimSpace(set.NotesModel), Now: now,
		})
	})
}

func speakersIn(ctx context.Context, q *store.Queries, id string) ([]Speaker, error) {
	m, err := q.GetMeeting(ctx, id)
	if err != nil {
		return nil, storeutil.NotFound(err, "meeting", id)
	}

	rows, err := q.ListMeetingSpeakers(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("reading the voices of meeting %s: %w", id, err)
	}

	out := make([]Speaker, 0, len(rows))
	for _, r := range rows {
		out = append(out, Speaker{
			Track: r.Track, Label: r.Label, Position: int(r.Position), Name: r.Name.String,
			Owner: ownerVoice(m.Kind, r.Track, r.Name.Valid),
		})
	}

	return out, nil
}

// findLabel finds the voice of a label on a track; with no track, on the one track that has the label.
func findLabel(speakers []Speaker, track, label string) (Speaker, error) {
	var found []Speaker

	for _, s := range speakers {
		if s.Label == label && (track == "" || s.Track == track) {
			found = append(found, s)
		}
	}

	switch len(found) {
	case 0:
		return Speaker{}, fmt.Errorf("%w: no voice %s", storeerr.ErrNotFound, label)
	case 1:
		return found[0], nil
	default:
		return Speaker{}, fmt.Errorf("%w: voice %s is on more than one track; name the track", storeerr.ErrInvalid,
			label)
	}
}

// onTrack are the voices of one track.
func onTrack(speakers []Speaker, track string) []Speaker {
	out := make([]Speaker, 0, len(speakers))

	for _, s := range speakers {
		if s.Track == track {
			out = append(out, s)
		}
	}

	return out
}

// findName finds the voice shown by a name, compared as MemoryOS's MinutesChecks.fold does: composed, lower case,
// one space between words.
func findName(speakers []Speaker, name string) (Speaker, bool) {
	for _, s := range speakers {
		if Fold(s.Shown()) == Fold(name) {
			return s, true
		}
	}

	return Speaker{}, false
}

// Fold is a name or a text compared without its case and extra spaces, composed.
func Fold(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(norm.NFC.String(s))), " ")
}

// freeLabel is the next label of a voice the owner made: m1, m2…, as MemoryOS labels manual voices M1, M2 (V200).
func freeLabel(speakers []Speaker) string {
	for i := 1; ; i++ {
		label := "m" + strconv.Itoa(i)
		if !hasLabel(speakers, label) {
			return label
		}
	}
}

func nextPosition(speakers []Speaker) int {
	next := 1
	for _, s := range speakers {
		next = max(next, s.Position+1)
	}

	return next
}

func meetingOf(r store.Meeting) Meeting {
	return Meeting{
		ID: r.ID, Title: r.Title, Titled: r.Titled == 1, Language: r.Language, People: list(r.People),
		FileName: r.FileName, MediaType: r.MediaType, Size: r.Size,
		Duration: time.Duration(r.DurationMs) * time.Millisecond, State: r.State, Failure: r.Failure,
		Attempts: int(r.Attempts), Provider: r.Provider, Model: r.Model, CreatedAt: storeutil.Millis(r.CreatedAt),
		UpdatedAt: storeutil.Millis(r.UpdatedAt), Source: r.Source, Kind: r.Kind,
	}
}

func notesOf(r store.MeetingNote) Notes {
	var sections []Section
	_ = json.Unmarshal([]byte(r.Sections), &sections)

	return Notes{
		State: r.State, Failure: r.Failure, Attempts: int(r.Attempts), Model: r.Model,
		Summary: r.Summary, Sections: sections, WrittenAt: storeutil.OptionalMillis(r.WrittenAt),
	}
}

func lineOf(r store.MeetingLine) Line {
	return Line{
		N: int(r.N), Track: r.Track, Start: time.Duration(r.StartMs) * time.Millisecond,
		End: time.Duration(r.EndMs) * time.Millisecond, Speaker: r.Speaker, Heard: r.Heard, Text: r.Text,
	}
}

func itemOf(r store.MeetingItem) Item {
	it := Item{
		ID: r.ID, Kind: r.Kind, Position: int(r.Position), Text: r.Text, Owner: r.Owner.String,
		OwnerTrack: r.OwnerTrack.String, OwnerSpeaker: r.OwnerSpeaker.String, Due: r.Due.String, Done: r.Done == 1,
		Quote: r.Quote.String,
		Line:  int(r.Line.Int64),
	}

	if r.StartMs.Valid {
		start := time.Duration(r.StartMs.Int64) * time.Millisecond
		it.Start = &start
	}

	return it
}

func list(raw string) []string {
	var out []string
	_ = json.Unmarshal([]byte(raw), &out)

	return out
}

// cleanList trims each entry and drops empty and repeated ones.
func cleanList(in []string, most, longest int, what string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}

	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[Fold(v)] {
			continue
		}

		if err := checkText(v, longest, what); err != nil {
			return nil, err
		}

		seen[Fold(v)] = true
		out = append(out, v)
	}

	if len(out) > most {
		return nil, fmt.Errorf("%w: at most %d %s", storeerr.ErrInvalid, most, what)
	}

	return out, nil
}

func checkText(v string, longest int, what string) error {
	if utf8.RuneCountInString(v) > longest {
		return fmt.Errorf("%w: %s is at most %d characters", storeerr.ErrInvalid, what, longest)
	}

	if strings.IndexFunc(v, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: %s is one line", storeerr.ErrInvalid, what)
	}

	return nil
}

func clip(s string, most int) string {
	if utf8.RuneCountInString(s) <= most {
		return s
	}

	return string([]rune(s)[:most])
}

func fileExt(name string) string {
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[i:]
	}

	return ""
}

// ownerTrack is the track of an item's voice: an upload's file when none is said.
func ownerTrack(it Item) string {
	switch {
	case it.OwnerSpeaker == "":
		return ""
	case it.OwnerTrack == "":
		return TrackFile
	default:
		return it.OwnerTrack
	}
}

func text(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func flag(b bool) int64 {
	if b {
		return 1
	}

	return 0
}
