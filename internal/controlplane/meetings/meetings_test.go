package meetings_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/meetings"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/meetingstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// recording is what the owner uploads in these scenarios.
const recording = "ID3-a-meeting-recording"

// fakeSoniox is Soniox's async API as the speech service asks it: the upload, a job, its transcript of two
// voices, and the deletes. It keeps the job it was asked, so a test reads the language and the words to hear right.
type fakeSoniox struct {
	mu   sync.Mutex
	job  map[string]any
	jobs int
}

func (f *fakeSoniox) serve(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/files":
			file, _, err := r.FormFile("file")
			if !assert.NoError(t, err) {
				return
			}

			data, _ := io.ReadAll(file)
			assert.Equal(t, recording, string(data), "the kept recording is what is sent")
			_, _ = io.WriteString(w, `{"id":"file-1"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/transcriptions":
			f.jobs++
			_ = json.NewDecoder(r.Body).Decode(&f.job)
			_, _ = io.WriteString(w, `{"id":"job-1"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/transcriptions/job-1":
			_, _ = io.WriteString(w, `{"status":"completed","audio_duration_ms":7000}`)
		case r.Method == http.MethodGet && r.URL.Path == "/transcriptions/job-1/transcript":
			_, _ = io.WriteString(w, `{"text":"","tokens":[
				{"text":"Hôm nay mình chốt","start_ms":0,"end_ms":1200,"speaker":"1","language":"vi"},
				{"text":" ra mắt thứ Sáu.","start_ms":1200,"end_ms":2000,"speaker":"1","language":"vi"},
				{"text":"Chào mọi người, mình là Lan.","start_ms":2500,"end_ms":4000,"speaker":"2","language":"vi"},
				{"text":" Em sẽ gửi số liệu giá trước thứ Tư.","start_ms":4000,"end_ms":6000,"speaker":"2","language":"vi"},
				{"text":"Ok vậy nhé.","start_ms":6200,"end_ms":7000,"speaker":"1","language":"vi"}]}`)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

// fakeModel is the gateway's chat model as the notes ask it: it keeps each request and answers the notes given.
type fakeModel struct {
	mu       sync.Mutex
	asked    []string
	keys     []string
	answer   string
	failWith error
}

func (m *fakeModel) Complete(_ context.Context, keyID string, body []byte) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.asked = append(m.asked, string(body))
	m.keys = append(m.keys, keyID)

	if m.failWith != nil {
		return nil, m.failWith
	}

	content, err := json.Marshal("```json\n" + m.answer + "\n```")
	if err != nil {
		return nil, err
	}

	return fmt.Appendf(nil, `{"choices":[{"message":{"role":"assistant","content":%s}}]}`, content), nil
}

// fakeUsage is the gateway's usage: the meetings' key and the audio calls recorded on it.
type fakeUsage struct {
	mu    sync.Mutex
	calls []modelgateway.AudioCall
}

func (u *fakeUsage) MeetingsKey(context.Context) (string, error) { return "gwk_meetings", nil }

func (u *fakeUsage) RecordAudioCall(_ context.Context, c modelgateway.AudioCall) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.calls = append(u.calls, c)

	return nil
}

// notes is what the fake model writes of the meeting: a decision on line 1, an action Speaker 2 takes on, and an
// action given to a group with a quote nobody said.
const notes = `{
  "decisions": [{"quote": "Hôm nay mình chốt ra mắt thứ Sáu", "line": 1, "text": "Launch on Friday"}],
  "actions": [
    {"quote": "Em sẽ gửi số liệu giá trước thứ Tư", "line": 2, "text": "Send the pricing numbers",
     "owner": "Speaker 2", "due": "thứ Tư"},
    {"quote": "words nobody said", "line": 3, "text": "Check the old redirects", "owner": "mọi người", "due": null}
  ],
  "sections": [{"title": "Discussion highlights", "points": ["Launch timing", " "]},
               {"title": "Something else", "points": ["dropped"]}],
  "summary": "The launch is on Friday; Lan sends the prices.",
  "title": "Launch sync"
}`

type world struct {
	orch   *orchestration.Service
	svc    *meetings.Service
	store  *meetingstore.Store
	dir    string
	soniox *fakeSoniox
	model  *fakeModel
	usage  *fakeUsage
	speech *speech.Service
}

func newWorld(t *testing.T) *world {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	n := 0
	orch := orchestration.New(db, platform.SystemClock{}, func(prefix string) string {
		n++

		return fmt.Sprintf("%s_%04d", prefix, n)
	})

	w := &world{
		orch: orch, store: orch.Meetings(), dir: filepath.Join(t.TempDir(), "meetings"), soniox: &fakeSoniox{},
		model: &fakeModel{answer: notes}, usage: &fakeUsage{},
	}
	srv := w.soniox.serve(t)
	w.speech = speech.New(speech.Config{
		Keys: func(context.Context) ([]speech.Key, error) {
			return []speech.Key{{Provider: speech.ProviderSoniox, Name: "speech-soniox", Value: "sk"}}, nil
		},
		SonioxURL: srv.URL, PollInterval: time.Millisecond,
	})
	w.svc = w.service(t)

	require.NoError(t, w.store.SetSettings(t.Context(), meetingstore.Settings{NotesModel: "notes-model"}))

	return w
}

// service is a Service over the world's store and folder, as the control plane makes one each time it starts.
func (w *world) service(t *testing.T) *meetings.Service {
	t.Helper()

	svc, err := meetings.New(meetings.Config{
		Dir: w.dir, Store: w.store, Speech: w.speech, Models: w.model, Usage: w.usage,
	})
	require.NoError(t, err)

	return svc
}

func (w *world) upload(t *testing.T, u meetings.Upload) meetingstore.Meeting {
	t.Helper()

	u.Body = strings.NewReader(recording)
	if u.FileName == "" {
		u.FileName = "weekly-sync.m4a"
	}

	m, err := w.svc.Upload(t.Context(), u)
	require.NoError(t, err)

	return m
}

// An uploaded recording is kept and heard with two voices, in its language and with the owner's words to hear
// right; the
// notes model then writes the summary, the format's sections, a decision and the actions, checked: the action a
// voice took on is that voice's, the one given to a group has nobody, and a quote nobody said is dropped. Both calls
// are counted on the meetings' gateway key, and a voice that introduced itself is offered its name.
func TestAnUploadIsHeardWithItsSpeakersAndWrittenUp(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	require.NoError(t, w.svc.SetWords(ctx, []string{"MapOps", "ĐKSD"}))

	m := w.upload(t, meetings.Upload{Language: "vi", People: []string{"Lan", "Minh"}})
	assert.Equal(t, meetingstore.Transcribing, m.State)
	assert.Equal(t, "weekly-sync", m.Title, "the file's name until the notes write one")
	assert.Equal(t, meetings.DefaultFormat, m.Notes.Format)
	assert.Equal(t, meetingstore.NotesWaiting, m.Notes.State)

	kept, err := os.ReadFile(filepath.Join(w.dir, m.ID))
	require.NoError(t, err)
	assert.Equal(t, recording, string(kept))

	w.svc.Work(ctx)

	assert.Equal(t, true, w.soniox.job["enable_speaker_diarization"])
	assert.Equal(t, []any{"vi"}, w.soniox.job["language_hints"])
	assert.Equal(t, map[string]any{"terms": []any{"MapOps", "ĐKSD", "coordinator"}}, w.soniox.job["context"],
		"the owner's words, then the Bot's name")

	d, err := w.svc.Get(ctx, m.ID)
	require.NoError(t, err)

	assert.Equal(t, meetingstore.Transcribed, d.Meeting.State)
	assert.Equal(t, 7*time.Second, d.Meeting.Duration)
	assert.Equal(t, "soniox", d.Meeting.Provider)
	require.Len(t, d.Lines, 3, "cut at each change of speaker")
	assert.Equal(t, "Chào mọi người, mình là Lan. Em sẽ gửi số liệu giá trước thứ Tư.", d.Lines[1].Text)
	assert.Equal(t, "2", d.Lines[1].Heard)

	require.Len(t, d.Meeting.Speakers, 2)
	assert.Equal(t, "Speaker 1", d.Meeting.Speakers[0].Shown())
	assert.Equal(t, 2800*time.Millisecond, d.Meeting.Speakers[0].Talk)
	assert.Equal(t, 3500*time.Millisecond, d.Meeting.Speakers[1].Talk)

	assert.Equal(t, []meetings.Suggestion{{Label: "2", Name: "Lan", Line: 2}}, d.Suggestions)

	n := d.Meeting.Notes
	assert.Equal(t, meetingstore.NotesReady, n.State)
	assert.Equal(t, "notes-model", n.Model)
	assert.Equal(t, "The launch is on Friday; Lan sends the prices.", n.Summary)
	assert.Equal(t, []meetingstore.Section{{Title: "Discussion highlights", Points: []string{"Launch timing"}}},
		n.Sections, "the format's sections only, empty points dropped")
	assert.Equal(t, "Launch sync", d.Meeting.Title)

	require.Len(t, d.Items, 3)
	assert.Equal(t, meetingstore.Action, d.Items[0].Kind)
	assert.Equal(t, "Send the pricing numbers", d.Items[0].Text)
	assert.Equal(t, "2", d.Items[0].OwnerSpeaker, "a voice of the meeting owns it")
	assert.Equal(t, "thứ Tư", d.Items[0].Due)
	assert.Equal(t, "Em sẽ gửi số liệu giá trước thứ Tư", d.Items[0].Quote)
	require.NotNil(t, d.Items[0].Start)
	assert.Equal(t, 2500*time.Millisecond, *d.Items[0].Start)
	assert.Empty(t, d.Items[1].Owner+d.Items[1].OwnerSpeaker, "a group is nobody")
	assert.Empty(t, d.Items[1].Quote, "a quote nobody said is dropped")
	assert.Equal(t, meetingstore.Decision, d.Items[2].Kind)

	require.Len(t, w.model.asked, 1)
	asked := w.model.asked[0]
	assert.Contains(t, asked, `"model":"notes-model"`)
	assert.Contains(t, asked, "Write in Vietnamese")
	assert.Contains(t, asked, "Discussion highlights")
	assert.Contains(t, asked, "[2] 00:02 Speaker 2: Chào mọi người")
	assert.Contains(t, asked, "Participants: Lan, Minh")
	assert.Equal(t, []string{"gwk_meetings"}, w.model.keys)

	require.Len(t, w.usage.calls, 1)
	assert.Equal(t, "gwk_meetings", w.usage.calls[0].KeyID)
	assert.Equal(t, 7*time.Second, w.usage.calls[0].Audio)

	list, err := w.svc.List(ctx, "số liệu", 10)
	require.NoError(t, err)
	require.Len(t, list, 1, "found by what was said")
	assert.Equal(t, 2, list[0].Actions)
	assert.Equal(t, 2, list[0].OpenActions)

	list, err = w.svc.List(ctx, "nothing like it", 10)
	require.NoError(t, err)
	assert.Empty(t, list)
}

// Naming a voice for the whole meeting names its lines and the actions it owns; giving one line a name makes it that
// voice's, or a new voice's; giving a voice the name another has makes the two one (Nojoin's scopes and merge).
func TestVoicesAreNamedForTheMeetingOrOneLineAndMergeByName(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	m := w.upload(t, meetings.Upload{})
	w.svc.Work(ctx)

	require.NoError(t, w.svc.NameSpeaker(ctx, m.ID, "2", "Lan"))

	actions, err := w.svc.Actions(ctx, "lan", nil)
	require.NoError(t, err)
	require.Len(t, actions, 1, "the action follows its voice's name")
	assert.Equal(t, "Lan", actions[0].Who)

	// Line 3 was heard as Speaker 1; the owner says Lan said it.
	require.NoError(t, w.svc.SetLineSpeaker(ctx, m.ID, 3, "lan"))

	d, err := w.svc.Get(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, "2", d.Lines[2].Speaker, "the line is Lan's")
	assert.Equal(t, "1", d.Lines[2].Heard, "what was heard is kept")
	assert.Equal(t, "1", d.Lines[0].Speaker, "the voice's other lines stay")

	// A name nobody has makes a voice for the line.
	require.NoError(t, w.svc.SetLineSpeaker(ctx, m.ID, 1, "Minh"))

	d, err = w.svc.Get(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, "m1", d.Lines[0].Speaker)
	require.Len(t, d.Meeting.Speakers, 2, "Speaker 1 has no line left and goes")
	assert.Equal(t, []string{"Lan", "Minh"}, shownNames(d.Meeting.Speakers))

	// Calling Minh "Lan" for the whole meeting merges the two.
	require.NoError(t, w.svc.NameSpeaker(ctx, m.ID, "m1", "Lan"))

	d, err = w.svc.Get(ctx, m.ID)
	require.NoError(t, err)
	require.Len(t, d.Meeting.Speakers, 1)
	assert.Equal(t, "Lan", d.Meeting.Speakers[0].Shown())
	assert.Equal(t, 6300*time.Millisecond, d.Meeting.Speakers[0].Talk)

	for _, l := range d.Lines {
		assert.Equal(t, "2", l.Speaker)
	}

	require.ErrorIs(t, w.svc.NameSpeaker(ctx, m.ID, "9", "Nobody"), orchestration.ErrNotFound)
	require.ErrorIs(t, w.svc.SetLineSpeaker(ctx, m.ID, 1, ""), orchestration.ErrInvalid)
}

func shownNames(speakers []meetingstore.Speaker) []string {
	out := make([]string, 0, len(speakers))
	for _, s := range speakers {
		out = append(out, s.Shown())
	}

	return out
}

// Writing the notes again in another format asks the model with that format's sections and keeps the new notes in
// place of the old; it is refused while they are being written. An action marked done stays across a read, and the
// Actions page filters by state.
func TestNotesAreWrittenAgainInAnotherFormat(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	m := w.upload(t, meetings.Upload{Title: "Weekly sync"})
	w.svc.Work(ctx)

	d, err := w.svc.Get(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, "Weekly sync", d.Meeting.Title, "the owner's title is kept")

	_, err = w.svc.SetDone(ctx, m.ID, d.Items[0].ID, true)
	require.NoError(t, err)

	open := false
	actions, err := w.svc.Actions(ctx, "", &open)
	require.NoError(t, err)
	assert.Len(t, actions, 1)

	w.model.answer = `{"decisions":[],"actions":[],"sections":[{"title":"Blockers","points":["None"]}],` +
		`"summary":"A stand-up."}`

	require.NoError(t, w.svc.WriteAgain(ctx, m.ID, "standup"))
	require.ErrorIs(t, w.svc.WriteAgain(ctx, m.ID, "standup"), orchestration.ErrConflict, "already being written")
	require.ErrorIs(t, w.svc.WriteAgain(ctx, m.ID, "nonsense"), orchestration.ErrInvalid)

	w.svc.Work(ctx)

	require.Len(t, w.model.asked, 2)
	assert.Contains(t, w.model.asked[1], "Yesterday's Accomplishments")
	assert.Contains(t, w.model.asked[1], "Blockers")
	assert.NotContains(t, w.model.asked[1], "a title for the meeting", "the owner titled it")

	d, err = w.svc.Get(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, "standup", d.Meeting.Notes.Format)
	assert.Equal(t, "A stand-up.", d.Meeting.Notes.Summary)
	assert.Equal(t, []string{"Yesterday's Accomplishments", "Today's Plan", "Blockers", "Team Updates"},
		sectionTitles(d.Meeting.Notes.Sections))
	assert.Empty(t, d.Items)
}

func sectionTitles(sections []meetingstore.Section) []string {
	out := make([]string, 0, len(sections))
	for _, s := range sections {
		out = append(out, s.Title)
	}

	return out
}

// Deleting a meeting removes its recording with its rows; it can no longer be read or played.
func TestDeletingAMeetingRemovesItsRecording(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	m := w.upload(t, meetings.Upload{})
	w.svc.Work(ctx)

	_, f, err := w.svc.Recording(ctx, m.ID)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	require.NoError(t, w.svc.Delete(ctx, m.ID))

	_, err = os.Stat(filepath.Join(w.dir, m.ID))
	require.ErrorIs(t, err, os.ErrNotExist)

	_, err = w.svc.Get(ctx, m.ID)
	require.ErrorIs(t, err, orchestration.ErrNotFound)

	_, _, err = w.svc.Recording(ctx, m.ID)
	require.ErrorIs(t, err, orchestration.ErrNotFound)

	entries, err := os.ReadDir(w.dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing is left in the folder")
}

// A transcription the control plane was stopped in is taken up again by the next start; one it keeps being stopped
// in ends failed with the reason, and the next meeting is still heard.
func TestAStepARestartInterruptedIsTakenUpAgain(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	m := w.upload(t, meetings.Upload{})

	// The control plane took it up, then stopped before an answer came.
	_, ok, err := w.store.ClaimTranscription(ctx, m.ID)
	require.NoError(t, err)
	require.True(t, ok)

	w.service(t).Work(ctx)

	d, err := w.svc.Get(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, meetingstore.Transcribed, d.Meeting.State)
	assert.Equal(t, meetingstore.NotesReady, d.Meeting.Notes.State)

	stuck := w.upload(t, meetings.Upload{})

	for range 3 {
		_, _, err := w.store.ClaimTranscription(ctx, stuck.ID)
		require.NoError(t, err)
	}

	w.service(t).Work(ctx)

	d, err = w.svc.Get(ctx, stuck.ID)
	require.NoError(t, err)
	assert.Equal(t, meetingstore.Failed, d.Meeting.State)
	assert.Contains(t, d.Meeting.Failure, "stopped 3 times")
	assert.Equal(t, 1, w.soniox.jobs, "the stuck one is not sent again")
}

// A failure says why and where to fix it: no speech-to-text key points at the Audio page, no notes model at the
// Meetings settings, and a model's refusal names the model; the transcript stays.
func TestAFailureSaysWhyAndWhereToFixIt(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	w.speech = speech.New(speech.Config{})
	w.svc = w.service(t)
	m := w.upload(t, meetings.Upload{})
	w.svc.Work(ctx)

	d, err := w.svc.Get(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, meetingstore.Failed, d.Meeting.State)
	assert.Contains(t, d.Meeting.Failure, "Settings → Gateway → Audio")

	w2 := newWorld(t)
	require.NoError(t, w2.store.SetSettings(ctx, meetingstore.Settings{}))
	m = w2.upload(t, meetings.Upload{})
	w2.svc.Work(ctx)

	d, err = w2.svc.Get(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, meetingstore.Transcribed, d.Meeting.State)
	assert.Len(t, d.Lines, 3, "the transcript stays")
	assert.Equal(t, meetingstore.NotesFailed, d.Meeting.Notes.State)
	assert.Contains(t, d.Meeting.Notes.Failure, "Settings → Meetings")

	require.NoError(t, w2.store.SetSettings(ctx, meetingstore.Settings{NotesModel: "notes-model"}))
	w2.model.failWith = &modelgateway.CompletionError{Status: http.StatusUnauthorized, Message: "bad key"}
	require.NoError(t, w2.svc.WriteAgain(ctx, m.ID, "general"))
	w2.svc.Work(ctx)

	d, err = w2.svc.Get(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, meetingstore.NotesFailed, d.Meeting.Notes.State)
	assert.Contains(t, d.Meeting.Notes.Failure, "notes-model")
	assert.Contains(t, d.Meeting.Notes.Failure, "bad key")
}

// An upload past the limit or empty, or in a format or language that is not one, is refused and keeps nothing.
func TestAnUploadThatCannotBeHeardIsRefused(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	for _, u := range []meetings.Upload{
		{Body: strings.NewReader(""), FileName: "empty.mp3"},
		{Body: strings.NewReader(recording), FileName: "a.mp3", Format: "poem"},
		{Body: strings.NewReader(recording), FileName: "a.mp3", Language: "vietnamese"},
		{Body: io.LimitReader(zeros{}, meetings.MaxRecording+1), FileName: "big.wav"},
	} {
		_, err := w.svc.Upload(ctx, u)
		require.ErrorIs(t, err, orchestration.ErrInvalid, u.FileName)
	}

	entries, err := os.ReadDir(w.dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)

	return len(p), nil
}
