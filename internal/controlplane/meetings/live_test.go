package meetings_test

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/meetings"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/meetingstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech/speechtest"
)

// liveWorld is a world whose speech service streams to a fake Soniox realtime API.
func liveWorld(t *testing.T, fake *speechtest.Realtime) *world {
	t.Helper()

	w := newWorld(t)
	w.speech = speech.New(speech.Config{
		Keys: func(context.Context) ([]speech.Key, error) {
			return []speech.Key{{Provider: speech.ProviderSoniox, Name: "speech-soniox", Value: "sk"}}, nil
		},
		SonioxRealtimeURL: fake.Serve(t), LiveBackoff: []time.Duration{10 * time.Millisecond},
	})
	w.svc = w.service(t)

	return w
}

// told keeps what a track told the socket.
type told struct {
	mu       sync.Mutex
	lines    []meetingstore.Line
	previews []string
	failed   []error
}

func (k *told) listener() meetings.TrackListener {
	return meetings.TrackListener{
		Preview: func(_, text string) {
			k.mu.Lock()
			defer k.mu.Unlock()

			k.previews = append(k.previews, text)
		},
		Line: func(l meetingstore.Line) {
			k.mu.Lock()
			defer k.mu.Unlock()

			k.lines = append(k.lines, l)
		},
		Failed: func(err error) {
			k.mu.Lock()
			defer k.mu.Unlock()

			k.failed = append(k.failed, err)
		},
	}
}

func (k *told) previewed(text string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()

	return slices.Contains(k.previews, text)
}

func (k *told) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()

	return len(k.lines)
}

// firstHalfSecond says, once a stream, that it has had half a second of audio.
func firstHalfSecond() func(stream, received int) bool {
	var (
		mu       sync.Mutex
		answered = map[int]bool{}
	)

	return func(stream, received int) bool {
		mu.Lock()
		defer mu.Unlock()

		if answered[stream] || received < speech.LiveBytesPerSecond/2 {
			return false
		}

		answered[stream] = true

		return true
	}
}

// pcm is d of PCM16 24 kHz mono whose every sample is v.
func pcm(d time.Duration, v int16) []byte {
	n := int(d / time.Millisecond * speech.LiveBytesPerSecond / 1000)
	out := make([]byte, n)

	for i := 0; i+1 < n; i += 2 {
		binary.LittleEndian.PutUint16(out[i:], uint16(v)) //nolint:gosec // a sample's bits, as PCM16 keeps them
	}

	return out
}

// sample reads the sample of a WAV at a time.
func sample(t *testing.T, wav []byte, at time.Duration) int16 {
	t.Helper()

	i := 44 + int(at/time.Millisecond)*speech.LiveBytesPerSecond/1000
	require.Less(t, i+1, len(wav))

	return int16(binary.LittleEndian.Uint16(wav[i:])) //nolint:gosec // a sample's bits, as PCM16 keeps them
}

// Scenario: an online meeting is recorded: the microphone, which is the owner and is not told apart by voice, and the
// shared tab, whose voices are. Each track's lines are kept as they are final, at the meeting's time; the tab's line,
// said first, is read first though the microphone's was kept first. Ending it keeps the last words, mixes the tracks
// into one WAV where the gap between them is silent, and moves the meeting to transcribed and its notes to writing,
// once; ending again changes nothing. Each track's audio is counted on the meetings' key.
func TestALiveMeetingRecordsTwoTracksAndEndsOnce(t *testing.T) {
	t.Parallel()

	first := firstHalfSecond()
	fake := &speechtest.Realtime{
		Answer: func(stream int, start map[string]any, received int) ([]string, bool) {
			if !first(stream, received) {
				return nil, false
			}

			if start["enable_speaker_diarization"] == false {
				// The microphone, opened at 0: a line at 3-4 s.
				return []string{speechtest.Tokens(true, true,
					speechtest.Token{Text: "Mine comes later.", Start: 3000, End: 4000})}, false
			}

			// The tab, opened at 2 s: a line at its 0-0.4 s, the meeting's 2-2.4 s.
			return []string{speechtest.Tokens(true, true,
				speechtest.Token{Text: "The tab speaks first.", Start: 0, End: 400, Speaker: "1"})}, false
		},
	}
	w := liveWorld(t, fake)
	ctx := t.Context()

	m, err := w.svc.StartLive(ctx, meetings.LiveStart{Kind: meetingstore.Online, Language: "en", People: []string{"Kai"}})
	require.NoError(t, err)
	assert.Equal(t, meetingstore.Recording, m.State)
	assert.Equal(t, meetingstore.SourceLive, m.Source)
	assert.Equal(t, meetingstore.NotesWaiting, m.Notes.State)

	_, err = w.svc.StartLive(ctx, meetings.LiveStart{Kind: meetingstore.InPerson})
	require.ErrorIs(t, err, orchestration.ErrConflict, "one meeting records at a time")

	mic, micTold := &told{}, &told{}
	micTrack, err := w.svc.OpenTrack(ctx, m.ID, meetingstore.TrackMic, 0, mic.listener())
	require.NoError(t, err)

	_, err = w.svc.OpenTrack(ctx, m.ID, meetingstore.TrackMic, 0, micTold.listener())
	require.ErrorIs(t, err, meetings.ErrBusy, "a second socket for an open track")

	tab := &told{}
	tabTrack, err := w.svc.OpenTrack(ctx, m.ID, meetingstore.TrackTab, 2*time.Second, tab.listener())
	require.NoError(t, err)

	require.NoError(t, micTrack.Append(pcm(time.Second, 1000)))
	require.Eventually(t, func() bool { return mic.count() == 1 }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, tabTrack.Append(pcm(500*time.Millisecond, 2000)))
	require.Eventually(t, func() bool { return tab.count() == 1 }, 5*time.Second, 10*time.Millisecond)

	d, err := w.svc.Get(ctx, m.ID)
	require.NoError(t, err)
	require.Len(t, d.Lines, 2)
	assert.Equal(t, "The tab speaks first.", d.Lines[0].Text, "lines are read by when they were said")
	assert.Equal(t, meetingstore.TrackTab, d.Lines[0].Track)
	assert.Equal(t, 2*time.Second, d.Lines[0].Start)
	assert.Equal(t, 2, d.Lines[0].N, "the number it was kept with stays its id")
	assert.Equal(t, "Mine comes later.", d.Lines[1].Text)
	assert.Equal(t, 2500*time.Millisecond, d.Meeting.Duration, "while it records, what is kept so far")

	require.Len(t, d.Meeting.Speakers, 2)
	assert.Equal(t, meetingstore.You, d.Meeting.Speakers[0].Shown(), "online, the microphone is the owner")
	assert.Equal(t, "Speaker 1", d.Meeting.Speakers[1].Shown())
	require.Error(t, w.svc.NameSpeaker(ctx, m.ID, meetingstore.TrackMic, "1", "Somebody"), "You is not renamed")
	require.NoError(t, w.svc.NameSpeaker(ctx, m.ID, meetingstore.TrackTab, "1", "Kai"))

	ended, err := w.svc.End(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, meetingstore.Transcribed, ended.State)
	assert.Equal(t, meetingstore.NotesWriting, ended.Notes.State)
	assert.Equal(t, 2500*time.Millisecond, ended.Duration)
	assert.Equal(t, speech.ProviderSoniox, ended.Provider)

	select {
	case <-micTrack.Done():
	default:
		t.Fatal("ending the meeting ends its tracks")
	}

	wav, err := os.ReadFile(filepath.Join(w.dir, m.ID))
	require.NoError(t, err)
	assert.Equal(t, "RIFF", string(wav[:4]))
	assert.Equal(t, int64(len(wav)), ended.Size)
	assert.Equal(t, int16(1000), sample(t, wav, 500*time.Millisecond), "the microphone")
	assert.Equal(t, int16(0), sample(t, wav, 1500*time.Millisecond), "the gap stays silent")
	assert.Equal(t, int16(2000), sample(t, wav, 2200*time.Millisecond), "the tab")

	_, err = os.Stat(filepath.Join(w.dir, m.ID+".mic.pcm"))
	require.ErrorIs(t, err, os.ErrNotExist, "the tracks' files go once mixed")

	f, rec, err := w.svc.Recording(ctx, m.ID)
	require.NoError(t, err)
	_ = rec.Close()
	assert.Equal(t, "audio/wav", f.MediaType)

	// The notes are written once: the work takes them up; ending again changes nothing.
	require.NoError(t, w.store.FailNotes(ctx, m.ID, "kept as failed, to see that ending again does not write them"))

	again, err := w.svc.End(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, meetingstore.NotesFailed, again.Notes.State, "ending again moves nothing")
	assert.Equal(t, meetingstore.Transcribed, again.State)

	transcribing, err := w.store.Transcribing(ctx)
	require.NoError(t, err)
	assert.NotContains(t, transcribing, m.ID, "never through transcribing, which the work would hear again")

	_, err = w.svc.OpenTrack(ctx, m.ID, meetingstore.TrackMic, 0, (&told{}).listener())
	require.ErrorIs(t, err, meetings.ErrNotRecording)

	w.usage.mu.Lock()
	defer w.usage.mu.Unlock()

	var audio time.Duration
	for _, c := range w.usage.calls {
		assert.Equal(t, speech.SonioxRealtimeModel, c.Model)
		audio += c.Audio
	}

	assert.Len(t, w.usage.calls, 2, "one call a track")
	assert.Equal(t, 1500*time.Millisecond, audio)
}

// Scenario: a meeting held in person records the microphone only, and a track records at most five hours.
func TestALiveTrackKeepsToItsLimits(t *testing.T) {
	t.Parallel()

	w := liveWorld(t, &speechtest.Realtime{})
	ctx := t.Context()

	m, err := w.svc.StartLive(ctx, meetings.LiveStart{Kind: meetingstore.InPerson})
	require.NoError(t, err)

	_, err = w.svc.OpenTrack(ctx, m.ID, meetingstore.TrackTab, 0, (&told{}).listener())
	require.ErrorIs(t, err, meetings.ErrInvalid, "in person, no tab")

	tr, err := w.svc.OpenTrack(ctx, m.ID, meetingstore.TrackMic, meetings.MaxTrack-time.Second,
		(&told{}).listener())
	require.NoError(t, err)

	require.NoError(t, tr.Append(pcm(500*time.Millisecond, 0)))
	require.ErrorIs(t, tr.Append(pcm(time.Second, 0)), meetings.ErrTooLong)
	tr.Close(ctx)

	_, err = w.svc.OpenTrack(ctx, m.ID, meetingstore.TrackMic, meetings.MaxTrack, (&told{}).listener())
	require.ErrorIs(t, err, meetings.ErrTooLong)

	require.NoError(t, w.svc.Delete(ctx, m.ID))

	_, err = os.Stat(filepath.Join(w.dir, m.ID+".mic.pcm"))
	require.ErrorIs(t, err, os.ErrNotExist, "deleting a meeting removes its tracks' audio")
}

// Scenario: a meeting left recording (the tab was closed) is resumed: its length is where it goes on, and the
// microphone's line said before is not kept twice when a new stream hears the same time again.
func TestAResumedMeetingGoesOnWhereItWas(t *testing.T) {
	t.Parallel()

	first := firstHalfSecond()
	fake := &speechtest.Realtime{
		Answer: func(stream int, _ map[string]any, received int) ([]string, bool) {
			if first(stream, received) {
				return []string{speechtest.Tokens(true, true, speechtest.Token{Text: "Hello.", Start: 0, End: 400})},
					false
			}

			return nil, false
		},
	}
	w := liveWorld(t, fake)
	ctx := t.Context()

	m, err := w.svc.StartLive(ctx, meetings.LiveStart{Kind: meetingstore.InPerson})
	require.NoError(t, err)

	k := &told{}
	tr, err := w.svc.OpenTrack(ctx, m.ID, meetingstore.TrackMic, 0, k.listener())
	require.NoError(t, err)
	require.NoError(t, tr.Append(pcm(time.Second, 0)))
	require.Eventually(t, func() bool { return k.count() == 1 }, 5*time.Second, 10*time.Millisecond)
	tr.Close(ctx)

	// The control plane restarts; the page resumes at the length kept.
	w.svc = w.service(t)

	d, err := w.svc.Get(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, time.Second, d.Meeting.Duration)

	// A browser that reconnects sends its queued audio again from 0: the stream hears "Hello." at 0-0.4 s again,
	// which ends where the kept line ends, and it is not kept twice.
	again := &told{}
	tr, err = w.svc.OpenTrack(ctx, m.ID, meetingstore.TrackMic, 0, again.listener())
	require.NoError(t, err)
	require.NoError(t, tr.Append(pcm(time.Second, 0)))
	tr.Finish(ctx)

	lines, err := w.store.Lines(ctx, m.ID)
	require.NoError(t, err)
	assert.Len(t, lines, 1)
	assert.Zero(t, again.count())
}

// hold is a gate a fake provider waits at until the test lets it go, once.
type hold struct {
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func newHold(t *testing.T) *hold {
	t.Helper()

	h := &hold{reached: make(chan struct{}, 8), release: make(chan struct{})}
	// Registered after the fake is served, so it runs first: the fake's server waits for its handlers.
	t.Cleanup(h.let)

	return h
}

func (h *hold) wait() {
	h.reached <- struct{}{}
	<-h.release
}

func (h *hold) let() { h.once.Do(func() { close(h.release) }) }

func (h *hold) reachedIn(t *testing.T) {
	t.Helper()

	select {
	case <-h.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the provider was not reached")
	}
}

// Scenario: the meeting is ended while its microphone's track is still opening (the provider is slow to answer).
// The end does not wait for it and nothing breaks: the meeting is transcribed, and the track, once its provider
// answers, is refused and closes what it opened.
func TestEndingWhileATrackOpensEndsIt(t *testing.T) {
	t.Parallel()

	gate := &hold{}
	fake := &speechtest.Realtime{Opening: func(int) { gate.wait() }}
	w := liveWorld(t, fake)
	gate = newHold(t)
	ctx := t.Context()

	m, err := w.svc.StartLive(ctx, meetings.LiveStart{Kind: meetingstore.InPerson})
	require.NoError(t, err)

	opened := make(chan error, 1)

	go func() {
		tr, err := w.svc.OpenTrack(ctx, m.ID, meetingstore.TrackMic, 0, (&told{}).listener())
		if tr != nil {
			tr.Close(ctx)
		}

		opened <- err
	}()

	gate.reachedIn(t)

	ended, err := w.svc.End(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, meetingstore.Transcribed, ended.State)

	gate.let()

	select {
	case err := <-opened:
		require.ErrorIs(t, err, meetings.ErrNotRecording, "a track that opened after the end is refused")
	case <-time.After(5 * time.Second):
		t.Fatal("the track did not finish opening")
	}

	w.usage.mu.Lock()
	defer w.usage.mu.Unlock()

	assert.Empty(t, w.usage.calls, "a track that never opened heard nothing")
}

// Scenario: an online meeting is ending: its microphone's last words are being kept. A tab's track asked for now,
// with a valid ticket, is refused: the end took the tracks there were, and a new one would lose its audio after the
// recording is mixed.
func TestNoTrackOpensOnceTheEndTookTheTracks(t *testing.T) {
	t.Parallel()

	gate := &hold{}
	fake := &speechtest.Realtime{End: func(int, map[string]any) []string {
		gate.wait()

		return nil
	}}
	w := liveWorld(t, fake)
	gate = newHold(t)
	ctx := t.Context()

	m, err := w.svc.StartLive(ctx, meetings.LiveStart{Kind: meetingstore.Online})
	require.NoError(t, err)

	mic, err := w.svc.OpenTrack(ctx, m.ID, meetingstore.TrackMic, 0, (&told{}).listener())
	require.NoError(t, err)
	require.NoError(t, mic.Append(pcm(100*time.Millisecond, 0)))

	ended := make(chan meetingstore.Meeting, 1)

	go func() {
		m, err := w.svc.End(ctx, m.ID)
		assert.NoError(t, err)

		ended <- m
	}()

	gate.reachedIn(t)

	tab, err := w.svc.OpenTrack(ctx, m.ID, meetingstore.TrackTab, time.Second, (&told{}).listener())
	if tab != nil {
		tab.Close(ctx)
	}

	require.ErrorIs(t, err, meetings.ErrNotRecording, "no track opens once the end took the tracks")

	gate.let()

	select {
	case m := <-ended:
		assert.Equal(t, meetingstore.Transcribed, m.State)
	case <-time.After(10 * time.Second):
		t.Fatal("the meeting did not end")
	}
}

// Scenario: the browser's socket drops while words are already final but no endpoint has closed their line yet
// (the preview shows them). The track closes, and those words are kept as a line: the browser does not send them
// again.
func TestADisconnectKeepsTheWordsAlreadyFinal(t *testing.T) {
	t.Parallel()

	first := firstHalfSecond()
	fake := &speechtest.Realtime{
		Answer: func(stream int, _ map[string]any, received int) ([]string, bool) {
			if !first(stream, received) {
				return nil, false
			}

			return []string{speechtest.Tokens(true, false,
				speechtest.Token{Text: "Already final.", Start: 0, End: 400})}, false
		},
	}
	w := liveWorld(t, fake)
	ctx := t.Context()

	m, err := w.svc.StartLive(ctx, meetings.LiveStart{Kind: meetingstore.InPerson})
	require.NoError(t, err)

	k := &told{}
	tr, err := w.svc.OpenTrack(ctx, m.ID, meetingstore.TrackMic, 0, k.listener())
	require.NoError(t, err)
	require.NoError(t, tr.Append(pcm(time.Second, 0)))
	require.Eventually(t, func() bool { return k.previewed("Already final.") }, 5*time.Second, 10*time.Millisecond)

	// The socket's read failed: the track is closed at once.
	tr.Close(ctx)

	require.Eventually(t, func() bool {
		d, err := w.svc.Get(ctx, m.ID)

		return err == nil && len(d.Lines) == 1
	}, 5*time.Second, 10*time.Millisecond, "the words already final are kept")

	d, err := w.svc.Get(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, "Already final.", d.Lines[0].Text)
}

// Scenario: the browser's socket drops while the words already final are still on their way to be kept (the
// listener is slow), and the meeting is ended at once. The end waits for them: the line is kept before the meeting
// is transcribed (review of #827, finding 2: the track was forgotten before its line was kept, and the line was
// refused once the meeting had ended).
func TestEndingRightAfterADisconnectKeepsTheWordsAlreadyFinal(t *testing.T) {
	t.Parallel()

	first := firstHalfSecond()
	fake := &speechtest.Realtime{
		Answer: func(stream int, _ map[string]any, received int) ([]string, bool) {
			if !first(stream, received) {
				return nil, false
			}

			return []string{speechtest.Tokens(true, false,
				speechtest.Token{Text: "Already final.", Start: 0, End: 400})}, false
		},
	}
	w := liveWorld(t, fake)
	gate := newHold(t)
	ctx := t.Context()

	m, err := w.svc.StartLive(ctx, meetings.LiveStart{Kind: meetingstore.InPerson})
	require.NoError(t, err)

	k := &told{}
	listener := k.listener()
	preview := listener.Preview
	listener.Preview = func(speaker, text string) {
		// The socket is slow to take the preview: what is queued after it waits.
		if text == "Already final." {
			gate.wait()
		}

		preview(speaker, text)
	}

	tr, err := w.svc.OpenTrack(ctx, m.ID, meetingstore.TrackMic, 0, listener)
	require.NoError(t, err)
	require.NoError(t, tr.Append(pcm(time.Second, 0)))
	gate.reachedIn(t)

	// The socket's read failed: the track is closed, and the meeting is ended right after.
	go tr.Close(ctx)

	require.Eventually(t, func() bool { return tr.Append(pcm(100*time.Millisecond, 0)) != nil },
		5*time.Second, time.Millisecond)

	time.AfterFunc(200*time.Millisecond, gate.let)

	ended, err := w.svc.End(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, meetingstore.Transcribed, ended.State)

	d, err := w.svc.Get(ctx, m.ID)
	require.NoError(t, err)
	require.Len(t, d.Lines, 1, "the words already final are kept before the meeting ends")
	assert.Equal(t, "Already final.", d.Lines[0].Text)
}

// Scenario: the meeting is ended just as its microphone's track takes the stream it opened. The end finishes that
// stream: the provider's connection is closed, and the track takes no more audio (review of #827, finding 3: the
// track was forgotten while its stream and its goroutines went on).
func TestEndingAsATrackOpensClosesItsStream(t *testing.T) {
	t.Parallel()

	fake := &speechtest.Realtime{}
	w := liveWorld(t, fake)
	gate := newHold(t)
	meetings.SetPublished(w.svc, gate.wait)

	ctx := t.Context()

	m, err := w.svc.StartLive(ctx, meetings.LiveStart{Kind: meetingstore.InPerson})
	require.NoError(t, err)

	type opened struct {
		track *meetings.Track
		err   error
	}

	out := make(chan opened, 1)

	go func() {
		tr, err := w.svc.OpenTrack(ctx, m.ID, meetingstore.TrackMic, 0, (&told{}).listener())
		out <- opened{tr, err}
	}()

	gate.reachedIn(t)

	ended, err := w.svc.End(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, meetingstore.Transcribed, ended.State)

	gate.let()

	var o opened

	select {
	case o = <-out:
	case <-time.After(5 * time.Second):
		t.Fatal("the track did not finish opening")
	}

	if o.err == nil {
		// Not closed by its socket here: the end alone has to close what the track opened.
		require.Error(t, o.track.Append(pcm(100*time.Millisecond, 0)), "an ended track takes no audio")
	} else {
		require.ErrorIs(t, o.err, meetings.ErrNotRecording)
	}

	require.Eventually(t, func() bool {
		s := fake.Streams()

		return len(s) == 1 && s[0].Closed
	}, 5*time.Second, 10*time.Millisecond, "the provider's stream is closed")
}

// Scenario: the meeting is ended while its microphone's track has words on their way that a slow socket does not
// take. The end waits for them once, at most its bound, and no more: the track's own wait for its last words shares
// that bound (review of #840: the track waited a second bound once its Finish had used the first, so End took twice
// as long as it says).
func TestEndingKeepsOneBoundForTheLastWords(t *testing.T) {
	t.Parallel()

	const bound = time.Second

	first := firstHalfSecond()
	fake := &speechtest.Realtime{
		Answer: func(stream int, _ map[string]any, received int) ([]string, bool) {
			if !first(stream, received) {
				return nil, false
			}

			return []string{speechtest.Tokens(true, false,
				speechtest.Token{Text: "Already final.", Start: 0, End: 400})}, false
		},
	}
	w := liveWorld(t, fake)
	meetings.SetLastWords(w.svc, bound)

	gate := newHold(t)
	ctx := t.Context()

	m, err := w.svc.StartLive(ctx, meetings.LiveStart{Kind: meetingstore.InPerson})
	require.NoError(t, err)

	k := &told{}
	listener := k.listener()
	preview := listener.Preview
	listener.Preview = func(speaker, text string) {
		// The socket does not take the preview until the test ends: what is queued after it waits.
		if text == "Already final." {
			gate.wait()
		}

		preview(speaker, text)
	}

	tr, err := w.svc.OpenTrack(ctx, m.ID, meetingstore.TrackMic, 0, listener)
	require.NoError(t, err)
	require.NoError(t, tr.Append(pcm(time.Second, 0)))
	gate.reachedIn(t)

	started := time.Now()
	ended, err := w.svc.End(ctx, m.ID)
	took := time.Since(started)

	require.NoError(t, err)
	assert.Equal(t, meetingstore.Transcribed, ended.State)
	assert.GreaterOrEqual(t, took, bound, "the end waits for the last words")
	assert.Less(t, took, bound*3/2, "the end waits for the last words once")
}
