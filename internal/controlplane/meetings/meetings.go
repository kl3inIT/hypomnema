// Package meetings turns a recording the owner uploads into a transcript with speakers, notes, decisions and
// actions (issue #721). It keeps the recording as a file, so that it plays back and a time seeks it, and works in
// the background after the upload: the gateway's speech service hears it with speakers, then a gateway chat model
// the owner picked writes the notes, sectioned by the meeting's topics (issue #771). The rows are orchestration/meetingstore's.
//
// What it does is learned from the references the issue compares: an upload of at most 128 MB with a language and,
// behind "More details", a title and the people (Fireflies, Meetily, Nojoin); the words to hear
// right are one list given to every transcription (words.go, issue #747); the recording kept on the server and deleted with the meeting (Fireflies, Otter,
// Nojoin, Hyprnote, OpenNoteTaker); the steps uploaded → transcribed → writing notes, and a failure that says why
// and where to fix it (Nojoin backend/worker/tasks/pipeline.py stage texts, Meetily, MemoryOS); one model call for
// the summary, decisions and actions (notes.go), checked after (checks.go); a run the control plane was stopped in
// taken up again when it starts, a bounded number of times (MemoryOS's minutes_attempts, the routines package's
// Interrupted).
package meetings

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/meetingstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
)

const (
	// MaxRecording bounds a recording: the gateway's audio routes' (speech.MaxAudioBytes, 9router-go's body cap).
	MaxRecording = speech.MaxAudioBytes
	// maxAttempts bounds how often a step the control plane was stopped in is taken up again.
	maxAttempts = 3
	// checkEvery is how often the loop looks for work when nothing wakes it.
	checkEvery = time.Minute
	// sniffBytes is what is read to tell a recording's type when the upload names none.
	sniffBytes = 512
)

// ErrInvalid says the request is not one that can be done.
var ErrInvalid = storeerr.ErrInvalid

// Speech hears a recording: the gateway's speech service.
type Speech interface {
	Transcribe(ctx context.Context, req speech.TranscribeRequest) (speech.Transcript, error)
}

// Models asks a gateway chat model in-process: the gateway's server.
type Models interface {
	Complete(ctx context.Context, keyID string, body []byte) ([]byte, error)
}

// Usage records a meeting's calls on the gateway key they are counted on, as a Bot's are on its chat's key.
type Usage interface {
	MeetingsKey(ctx context.Context) (string, error)
	RecordAudioCall(ctx context.Context, c modelgateway.AudioCall) error
}

// Config is what the Service needs.
type Config struct {
	// Dir holds the recordings, one file a meeting named by its id.
	Dir    string
	Store  *meetingstore.Store
	Speech Speech
	Models Models
	Usage  Usage
	// Wake is signaled after a commit, so that an upload or a "write again" is taken up at once; nil: each minute.
	Wake   func(ctx context.Context) <-chan struct{}
	Logger *slog.Logger
}

// Service keeps the meetings and does their work.
type Service struct {
	cfg Config

	mu sync.Mutex
	// working cancels the step a meeting is in, so that deleting it stops its work.
	working map[string]context.CancelFunc
}

// New returns a Service; the recordings' folder is made when missing.
func New(cfg Config) (*Service, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("making the recordings' folder: %w", err)
	}

	return &Service{cfg: cfg, working: map[string]context.CancelFunc{}}, nil
}

// Upload is a recording the owner sends, with what it was told about it.
type Upload struct {
	Body      io.Reader
	FileName  string
	MediaType string
	Language  string
	Title     string
	People    []string
}

var languagePattern = regexp.MustCompile(`^[a-z]{2}$`)

// Upload keeps the recording and records the meeting; its work starts in the background.
func (s *Service) Upload(ctx context.Context, u Upload) (meetingstore.Meeting, error) {
	u.Language = strings.ToLower(strings.TrimSpace(u.Language))
	if u.Language != "" && !languagePattern.MatchString(u.Language) {
		return meetingstore.Meeting{}, fmt.Errorf("%w: the language is a two-letter ISO 639-1 code, such as vi",
			ErrInvalid)
	}

	name := filepath.Base(strings.TrimSpace(u.FileName))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "recording"
	}

	id := s.cfg.Store.NewID()

	size, sniffed, err := s.keep(id, u.Body)
	if err != nil {
		return meetingstore.Meeting{}, err
	}

	m, err := s.cfg.Store.Create(ctx, meetingstore.NewMeeting{
		ID: id, Title: u.Title, Language: u.Language, People: u.People,
		FileName: name, MediaType: MediaType(u.MediaType, sniffed), Size: size,
	})
	if err != nil {
		_ = os.Remove(s.path(id))

		return meetingstore.Meeting{}, err
	}

	return m, nil
}

// opaque is the type of a recording that is neither sound nor video by its bytes.
const opaque = "application/octet-stream"

// MediaType is the type a recording is kept and served as: only sound or video, since the recording is served on
// the control plane's own origin and a page there would run as the owner. The type the upload declares is kept
// when it is audio/* or video/* and the bytes, as http.DetectContentType reads them, are not something else; the
// type read from the bytes is kept when it is sound or video; anything else is application/octet-stream. The
// player still plays such a file, and nothing renders it as a page.
func MediaType(declared, sniffed string) string {
	declared = strings.ToLower(strings.TrimSpace(declared))
	if base, _, err := mime.ParseMediaType(declared); err == nil {
		declared = base
	} else {
		declared = ""
	}

	sniffed, _, _ = strings.Cut(strings.ToLower(sniffed), ";")
	sniffed = strings.TrimSpace(sniffed)

	switch {
	case playable(declared) && (playable(sniffed) || sniffed == opaque):
		return declared
	case playable(sniffed):
		return sniffed
	default:
		return opaque
	}
}

// playable is a type the player takes: sound, video, or Ogg, which http.DetectContentType names as an application.
func playable(t string) bool {
	return strings.HasPrefix(t, "audio/") || strings.HasPrefix(t, "video/") || t == "application/ogg"
}

// keep writes a recording to its file, refusing an empty one and one past MaxRecording, and tells its type from
// its first bytes.
func (s *Service) keep(id string, body io.Reader) (int64, string, error) {
	tmp, err := os.CreateTemp(s.cfg.Dir, id+".*.part")
	if err != nil {
		return 0, "", fmt.Errorf("keeping the recording: %w", err)
	}

	done := false

	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	n, err := io.Copy(tmp, io.LimitReader(body, MaxRecording+1))

	var tooLarge *http.MaxBytesError

	switch {
	case errors.As(err, &tooLarge) || n > MaxRecording:
		return 0, "", fmt.Errorf("%w: a recording is at most %d MB", ErrInvalid, MaxRecording>>20)
	case err != nil:
		return 0, "", fmt.Errorf("receiving the recording: %w", err)
	case n == 0:
		return 0, "", fmt.Errorf("%w: the recording is empty", ErrInvalid)
	}

	head := make([]byte, sniffBytes)
	read, _ := tmp.ReadAt(head, 0)

	if err := tmp.Close(); err != nil {
		return 0, "", fmt.Errorf("keeping the recording: %w", err)
	}

	if err := os.Rename(tmp.Name(), s.path(id)); err != nil {
		return 0, "", fmt.Errorf("keeping the recording: %w", err)
	}

	done = true

	return n, http.DetectContentType(head[:read]), nil
}

func (s *Service) path(id string) string { return filepath.Join(s.cfg.Dir, filepath.Base(id)) }

// Recording opens a meeting's recording.
func (s *Service) Recording(ctx context.Context, id string) (meetingstore.Meeting, *os.File, error) {
	m, err := s.cfg.Store.Get(ctx, id)
	if err != nil {
		return meetingstore.Meeting{}, nil, err
	}

	f, err := os.Open(s.path(m.ID))
	if errors.Is(err, os.ErrNotExist) {
		return meetingstore.Meeting{}, nil, fmt.Errorf("%w: the recording of meeting %s", storeerr.ErrNotFound, id)
	}

	if err != nil {
		return meetingstore.Meeting{}, nil, fmt.Errorf("opening the recording: %w", err)
	}

	return m, f, nil
}

// Delete removes a meeting, its recording, transcript and notes, and stops its work.
func (s *Service) Delete(ctx context.Context, id string) error {
	if err := s.cfg.Store.Delete(ctx, id); err != nil {
		return err
	}

	s.mu.Lock()
	if cancel, ok := s.working[id]; ok {
		cancel()
	}
	s.mu.Unlock()

	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing the recording: %w", err)
	}

	return nil
}

// WriteAgain asks for the notes again.
func (s *Service) WriteAgain(ctx context.Context, id string) error {
	return s.cfg.Store.WriteAgain(ctx, id)
}

// Detail is a meeting with its transcript, its items and the names offered for its voices.
type Detail struct {
	Meeting     meetingstore.Meeting
	Lines       []meetingstore.Line
	Items       []meetingstore.Item
	Suggestions []Suggestion
}

// Get returns a meeting with all it holds.
func (s *Service) Get(ctx context.Context, id string) (Detail, error) {
	m, err := s.cfg.Store.Get(ctx, id)
	if err != nil {
		return Detail{}, err
	}

	lines, err := s.cfg.Store.Lines(ctx, id)
	if err != nil {
		return Detail{}, err
	}

	items, err := s.cfg.Store.Items(ctx, id)
	if err != nil {
		return Detail{}, err
	}

	return Detail{Meeting: m, Lines: lines, Items: items, Suggestions: suggest(lines, m.Speakers, m.People)}, nil
}

// Run does the meetings' work until ctx ends: the steps a restart interrupted first, then each step as it comes.
// One step runs at a time, as MemoryOS runs one meeting's minutes at a time on each replica.
func (s *Service) Run(ctx context.Context) {
	var wake <-chan struct{}
	if s.cfg.Wake != nil {
		wake = s.cfg.Wake(ctx)
	}

	tick := time.NewTicker(checkEvery)
	defer tick.Stop()

	for {
		s.Work(ctx)

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-wake:
		}
	}
}

// Work does the steps waiting when it is called: the transcriptions, then the notes, those of the meetings just
// transcribed included. A step left waiting by a failure of the control plane's own is taken up at the next call,
// not at once, so a database that keeps failing does not spin the loop.
func (s *Service) Work(ctx context.Context) {
	transcribe, err := s.cfg.Store.Transcribing(ctx)
	if err != nil {
		s.failed(ctx, "listing the meetings to transcribe", "", err)

		return
	}

	for _, id := range transcribe {
		if ctx.Err() != nil {
			return
		}

		s.step(ctx, id, s.transcribe)
	}

	write, err := s.cfg.Store.NotesToWrite(ctx)
	if err != nil {
		s.failed(ctx, "listing the notes to write", "", err)

		return
	}

	for _, id := range write {
		if ctx.Err() != nil {
			return
		}

		s.step(ctx, id, s.writeNotes)
	}
}

// step runs one meeting's step with a context its deletion cancels.
func (s *Service) step(ctx context.Context, id string, do func(ctx context.Context, id string)) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	s.mu.Lock()
	s.working[id] = cancel
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.working, id)
		s.mu.Unlock()
	}()

	do(ctx, id)
}

// transcribe hears a meeting's recording with speakers.
func (s *Service) transcribe(ctx context.Context, id string) {
	attempts, ok, err := s.cfg.Store.ClaimTranscription(ctx, id)
	if err != nil || !ok {
		s.failed(ctx, "taking up a transcription", id, err)

		return
	}

	if attempts > maxAttempts {
		s.fail(ctx, id, fmt.Sprintf("The control plane stopped %d times while hearing this recording. Upload it "+
			"again.", maxAttempts))

		return
	}

	m, err := s.cfg.Store.Get(ctx, id)
	if err != nil {
		s.failed(ctx, "reading a meeting to transcribe", id, err)

		return
	}

	path := s.path(id)
	if _, err := os.Stat(path); err != nil {
		s.fail(ctx, id, "The recording is no longer on the control plane. Upload it again.")

		return
	}

	words, err := s.sentWords(ctx)
	if err != nil {
		s.failed(ctx, "reading the words to hear right", id, err)

		return
	}

	started := time.Now()
	t, err := s.cfg.Speech.Transcribe(ctx, speech.TranscribeRequest{
		Audio: speech.Audio{
			Name: m.FileName, MIME: m.MediaType, Size: m.Size,
			Open: func() (io.ReadCloser, error) {
				return os.Open(path) //nolint:gosec // the meeting's own file in the recordings' folder
			},
		},
		Language: m.Language, Speakers: true, Terms: words,
	})

	s.record(ctx, modelgateway.AudioCall{
		Kind: speech.KindSTT, Provider: t.Provider, Model: t.Model, Failures: t.Errors, Err: err, Audio: t.Duration,
		Duration: time.Since(started),
	})

	if ctx.Err() != nil {
		// Stopped or deleted: a restart takes it up again, a deleted meeting has nothing left to do.
		return
	}

	if err != nil {
		s.fail(ctx, id, transcriptionFailure(err))

		return
	}

	heard := meetingstore.Heard{Duration: t.Duration, Language: t.Language, Provider: t.Provider, Model: t.Model}
	for _, seg := range t.Segments {
		heard.Lines = append(heard.Lines, meetingstore.HeardLine{
			Start: seg.Start, End: seg.End, Speaker: seg.Speaker, Text: seg.Text,
		})
	}

	if err := s.cfg.Store.SaveTranscript(ctx, id, heard); err != nil {
		s.failed(ctx, "keeping a transcript", id, err)
	}
}

// transcriptionFailure says why a transcription failed and where to fix it.
func transcriptionFailure(err error) string {
	var (
		failed  *speech.FailedError
		resting *speech.RestingError
	)

	switch {
	case errors.Is(err, speech.ErrUnavailable):
		return "No speech-to-text provider is set up. Add a Soniox or ElevenLabs key on Settings → Gateway → Audio, " +
			"then upload the recording again."
	case errors.As(err, &resting):
		return "Every speech-to-text key is resting until " + resting.Until.UTC().Format(time.RFC3339) +
			". Upload the recording again after that, or add a key on Settings → Gateway → Audio."
	case errors.As(err, &failed):
		return "The speech-to-text providers could not hear it: " + err.Error() +
			". Check the keys on Settings → Gateway → Audio."
	case errors.Is(err, speech.ErrInvalid):
		return "The recording could not be sent: " + err.Error() + "."
	default:
		return "Hearing the recording failed: " + err.Error() + "."
	}
}

// writeNotes has the owner's notes model write a meeting's notes.
func (s *Service) writeNotes(ctx context.Context, id string) {
	attempts, ok, err := s.cfg.Store.ClaimNotes(ctx, id)
	if err != nil || !ok {
		s.failed(ctx, "taking up notes", id, err)

		return
	}

	if attempts > maxAttempts {
		s.failNotes(ctx, id, fmt.Sprintf("The control plane stopped %d times while writing these notes. Write "+
			"them again.", maxAttempts))

		return
	}

	set, err := s.cfg.Store.GetSettings(ctx)
	if err != nil {
		s.failed(ctx, "reading the meetings' settings", id, err)

		return
	}

	if set.NotesModel == "" {
		s.failNotes(ctx, id, "No model is picked to write notes. Pick one on Settings → Meetings, then write them "+
			"again.")

		return
	}

	d, err := s.Get(ctx, id)
	if err != nil {
		s.failed(ctx, "reading a meeting to write its notes", id, err)

		return
	}

	if len(d.Lines) == 0 {
		s.failNotes(ctx, id, "Nothing was heard in the recording, so there is nothing to write notes from.")

		return
	}

	shown := map[string]string{}
	for _, sp := range d.Meeting.Speakers {
		shown[sp.Label] = sp.Shown()
	}

	names, err := s.sentWords(ctx)
	if err != nil {
		s.failed(ctx, "reading the dictionary", id, err)

		return
	}

	body, err := request(set.NotesModel, subject{
		Title: d.Meeting.Title, Titled: d.Meeting.Titled, When: d.Meeting.CreatedAt, People: d.Meeting.People,
		Language: d.Meeting.Language, Names: names,
	}, d.Lines, shown)
	if err != nil {
		s.failed(ctx, "asking for notes", id, err)

		return
	}

	keyID, err := s.cfg.Usage.MeetingsKey(ctx)
	if err != nil {
		s.failed(ctx, "making the meetings' gateway key", id, err)

		return
	}

	completion, err := s.cfg.Models.Complete(ctx, keyID, body)
	if ctx.Err() != nil {
		return
	}

	if err != nil {
		s.failNotes(ctx, id, notesFailure(set.NotesModel, err))

		return
	}

	a, err := readAnswer(completion)
	if err != nil {
		s.failNotes(ctx, id, "The answer of "+set.NotesModel+" was not the notes asked for. Write them again, or "+
			"pick another model on Settings → Meetings.")

		return
	}

	w := written(a, d.Lines, d.Meeting.Speakers, d.Meeting.Titled)
	w.Model = set.NotesModel

	if err := s.cfg.Store.SaveNotes(ctx, id, w); err != nil {
		s.failed(ctx, "keeping notes", id, err)
	}
}

// notesFailure says why the notes model failed and where to fix it.
func notesFailure(model string, err error) string {
	var answered *modelgateway.CompletionError

	switch {
	case errors.Is(err, modelgateway.ErrNotFound):
		return "The gateway serves no chat model named " + model + ". Pick another on Settings → Meetings."
	case errors.As(err, &answered):
		return "The model " + model + " failed: " + answered.Error() + ". Write the notes again, or pick another " +
			"model on Settings → Meetings."
	default:
		return "Writing the notes failed: " + err.Error() + "."
	}
}

// record counts a transcription on the meetings' gateway key.
func (s *Service) record(ctx context.Context, c modelgateway.AudioCall) {
	ctx = context.WithoutCancel(ctx)

	keyID, err := s.cfg.Usage.MeetingsKey(ctx)
	if err == nil {
		c.KeyID = keyID
		err = s.cfg.Usage.RecordAudioCall(ctx, c)
	}

	if err != nil {
		s.cfg.Logger.ErrorContext(ctx, "recording a meeting's transcription",
			slog.String("event", "meetings.audio_usage_failed"), slog.String("error", err.Error()))
	}
}

func (s *Service) fail(ctx context.Context, id, reason string) {
	s.cfg.Logger.WarnContext(ctx, "a meeting's transcription failed",
		slog.String("event", "meetings.transcription_failed"), slog.String("meeting_id", id),
		slog.String("reason", reason))

	if err := s.cfg.Store.Fail(context.WithoutCancel(ctx), id, reason); err != nil {
		s.failed(ctx, "recording a failed transcription", id, err)
	}
}

func (s *Service) failNotes(ctx context.Context, id, reason string) {
	s.cfg.Logger.WarnContext(ctx, "a meeting's notes failed",
		slog.String("event", "meetings.notes_failed"), slog.String("meeting_id", id), slog.String("reason", reason))

	if err := s.cfg.Store.FailNotes(context.WithoutCancel(ctx), id, reason); err != nil {
		s.failed(ctx, "recording failed notes", id, err)
	}
}

// failed logs an error of the control plane's own, not the meeting's; a deleted meeting's is none.
func (s *Service) failed(ctx context.Context, doing, id string, err error) {
	if err == nil || ctx.Err() != nil || errors.Is(err, storeerr.ErrNotFound) {
		return
	}

	s.cfg.Logger.ErrorContext(ctx, doing,
		slog.String("event", "meetings.failed"), slog.String("meeting_id", id), slog.String("error", err.Error()))
}

// The rest are the store's, given through the Service so that the API has one door.

// List returns the meetings, the newest first; with a query, those whose title or transcript holds it.
func (s *Service) List(ctx context.Context, query string, limit int) ([]meetingstore.Meeting, error) {
	return s.cfg.Store.List(ctx, query, limit)
}

// NameSpeaker names a voice for the whole meeting; a name another voice has merges the two.
func (s *Service) NameSpeaker(ctx context.Context, id, label, name string) error {
	return s.cfg.Store.NameSpeaker(ctx, id, label, name)
}

// SetLineSpeaker gives one line to the voice called name.
func (s *Service) SetLineSpeaker(ctx context.Context, id string, n int, name string) error {
	return s.cfg.Store.SetLineSpeaker(ctx, id, n, name)
}

// SetDone marks an action of a meeting done or not.
func (s *Service) SetDone(ctx context.Context, id, itemID string, done bool) (meetingstore.Item, error) {
	of, err := s.cfg.Store.MeetingOfItem(ctx, itemID)
	if err != nil {
		return meetingstore.Item{}, err
	}

	if of != id {
		return meetingstore.Item{}, fmt.Errorf("%w: meeting %s has no item %s", storeerr.ErrNotFound, id, itemID)
	}

	return s.cfg.Store.SetDone(ctx, itemID, done)
}

// Actions returns the actions of every meeting; who keeps those of one person (as shown), done those done or not.
func (s *Service) Actions(ctx context.Context, who string, done *bool) ([]meetingstore.MeetingAction, error) {
	all, err := s.cfg.Store.Actions(ctx)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(all, func(a meetingstore.MeetingAction) bool {
		return (who != "" && meetingstore.Fold(a.Who) != meetingstore.Fold(who)) || (done != nil && a.Done != *done)
	}), nil
}

// Settings returns what the owner set.
func (s *Service) Settings(ctx context.Context) (meetingstore.Settings, error) {
	return s.cfg.Store.GetSettings(ctx)
}

// SetSettings keeps what the owner set.
func (s *Service) SetSettings(ctx context.Context, set meetingstore.Settings) error {
	return s.cfg.Store.SetSettings(ctx, set)
}
