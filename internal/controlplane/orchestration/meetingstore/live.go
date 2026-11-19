package meetingstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// A meeting recorded live from the browser (issue #820), kept as MemoryOS keeps one
// (memoryos-core/.../meeting/MeetingService.java, persistence/MeetingRepository.java): it is created recording, each
// line is appended as the provider finalizes it, and ending it moves it to transcribed and its notes to writing, once.

// NewLive is a meeting about to be recorded.
type NewLive struct {
	Kind     string
	Title    string
	Language string
	People   []string
}

// CreateLive records a meeting that starts recording; refused while another is recording, so that one meeting is
// recorded at a time (issue #820).
func (s *Store) CreateLive(ctx context.Context, nl NewLive) (Meeting, error) {
	if nl.Kind != Online && nl.Kind != InPerson {
		return Meeting{}, fmt.Errorf("%w: a live meeting is online or in_person", storeerr.ErrInvalid)
	}

	title := strings.TrimSpace(nl.Title)
	titled := title != ""

	if !titled {
		title = "Meeting"
	}

	if err := checkText(title, MaxTitle, "a title"); err != nil {
		return Meeting{}, err
	}

	people, err := cleanList(nl.People, MaxPeople, MaxName, "people")
	if err != nil {
		return Meeting{}, err
	}

	peopleJSON, err := json.Marshal(people)
	if err != nil {
		return Meeting{}, fmt.Errorf("encoding the people: %w", err)
	}

	id := s.NewID()

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.InsertLiveMeeting(ctx, store.InsertLiveMeetingParams{
			ID: id, Title: title, Titled: flag(titled), Language: nl.Language, People: string(peopleJSON),
			Kind: nl.Kind, Now: now,
		})
		if err != nil {
			return fmt.Errorf("storing meeting %s: %w", id, err)
		}

		if n == 0 {
			return fmt.Errorf("%w: another meeting is being recorded; resume or end it first", storeerr.ErrConflict)
		}

		return q.InsertMeetingNotes(ctx, store.InsertMeetingNotesParams{MeetingID: id, Now: now})
	}); err != nil {
		return Meeting{}, err
	}

	return s.Get(ctx, id)
}

// Recording returns the meetings being recorded, the oldest first: at most one, unless a restart left more.
func (s *Store) Recording(ctx context.Context) ([]string, error) {
	ids, err := s.read().MeetingRecording(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the meetings recording: %w", err)
	}

	return ids, nil
}

// TrackEnd is where the last line of a meeting's track ends; false when the track has none.
func (s *Store) TrackEnd(ctx context.Context, id, track string) (time.Duration, bool, error) {
	row, err := s.read().LastMeetingLine(ctx, store.LastMeetingLineParams{Track: track, MeetingID: id})
	if err != nil {
		return 0, false, fmt.Errorf("reading the last line of meeting %s: %w", id, err)
	}

	if row.TrackEndMs < 0 {
		return 0, false, nil
	}

	return time.Duration(row.TrackEndMs) * time.Millisecond, true, nil
}

// AppendLine keeps one line a track heard while the meeting records (MemoryOS MeetingRepository.insertUtterance), in
// place of SaveTranscript, which replaces every line: its voice is added to the track the first time it speaks, and
// it takes the next number, which stays its id. Refused once the meeting is no longer recording.
func (s *Store) AppendLine(ctx context.Context, id, track string, h HeardLine) (Line, error) {
	if track != TrackMic && track != TrackTab {
		return Line{}, fmt.Errorf("%w: a live track is mic or tab", storeerr.ErrInvalid)
	}

	label := clip(cmpOr(h.Speaker, noSpeaker), 32)
	text := strings.TrimSpace(h.Text)

	var line Line

	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		m, err := q.GetMeeting(ctx, id)
		if err != nil {
			return storeutil.NotFound(err, "meeting", id)
		}

		if m.State != Recording {
			return fmt.Errorf("%w: meeting %s is not recording", storeerr.ErrConflict, id)
		}

		speakers, err := speakersIn(ctx, q, id)
		if err != nil {
			return err
		}

		voices := onTrack(speakers, track)
		if !hasLabel(voices, label) {
			if err := q.InsertMeetingSpeaker(ctx, store.InsertMeetingSpeakerParams{
				MeetingID: id, Track: track, Label: label, Position: int64(nextPosition(voices)),
			}); err != nil {
				return fmt.Errorf("storing a voice of meeting %s: %w", id, err)
			}
		}

		last, err := q.LastMeetingLine(ctx, store.LastMeetingLineParams{Track: track, MeetingID: id})
		if err != nil {
			return fmt.Errorf("numbering a line of meeting %s: %w", id, err)
		}

		line = Line{
			N: int(last.N) + 1, Track: track, Start: max(h.Start, 0), End: max(h.End, h.Start, 0),
			Speaker: label, Heard: h.Speaker, Text: text,
		}

		if err := q.InsertMeetingLine(ctx, store.InsertMeetingLineParams{
			MeetingID: id, N: int64(line.N), Track: track, StartMs: line.Start.Milliseconds(),
			EndMs: line.End.Milliseconds(), Speaker: label, Heard: h.Speaker, Text: text,
		}); err != nil {
			return fmt.Errorf("storing a line of meeting %s: %w", id, err)
		}

		return nil
	})

	return line, err
}

// Ended is what a live meeting's end keeps: its recording and who heard it.
type Ended struct {
	Size     int64
	Duration time.Duration
	Provider string
	Model    string
}

// EndLive ends a meeting being recorded: in one transaction it moves from recording to transcribed and its notes from
// waiting to writing (MemoryOS MeetingService.end), never through transcribing, which the work would hear again;
// false when it was not recording, so the move happens once.
func (s *Store) EndLive(ctx context.Context, id string, e Ended) (bool, error) {
	var ended bool

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.EndLiveMeeting(ctx, store.EndLiveMeetingParams{
			Size: e.Size, DurationMs: e.Duration.Milliseconds(), Provider: e.Provider, Model: e.Model, Now: now, ID: id,
		})
		if err != nil {
			return fmt.Errorf("ending meeting %s: %w", id, err)
		}

		if n == 0 {
			return nil
		}

		ended = true

		return q.StartLiveNotes(ctx, store.StartLiveNotesParams{Now: now, MeetingID: id})
	})

	return ended, err
}

func hasLabel(speakers []Speaker, label string) bool {
	for _, s := range speakers {
		if s.Label == label {
			return true
		}
	}

	return false
}

func cmpOr(v, fallback string) string {
	if v == "" {
		return fallback
	}

	return v
}
