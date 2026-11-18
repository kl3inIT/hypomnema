package meetings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/meetingstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
)

// A meeting recorded live from the browser (issue #820), as MemoryOS records one
// (memoryos-core/.../meeting/MeetingService.java openTrack and end; docs/specs/meeting.md "Recording a track"): the
// owner's microphone, and online a shared tab, each a track streamed to Soniox's realtime transcription
// (speech.Live), its lines kept as they are final. Unlike MemoryOS the audio is kept (owner, 2026-11-18): each
// track's PCM is written at its place on the meeting's clock, so a gap stays silent and audio sent again is written
// over itself, and the tracks are mixed into one WAV when the meeting ends (wav.go).

const (
	// MaxTrack bounds what one track records (MemoryOS MAX_TRACK, Soniox's 300 minutes a stream).
	MaxTrack = 5 * time.Hour
	// endWait bounds how long ending waits for a track's last words (MemoryOS's FINISH_TIMEOUT_MS in the browser).
	endWait = 20 * time.Second
)

// Errors a track is refused or ended with; the socket names them to the browser.
var (
	// ErrBusy: the track is already being recorded by another socket.
	ErrBusy = errors.New("the track is already being recorded")
	// ErrTooLong: the track recorded MaxTrack.
	ErrTooLong = errors.New("a track records at most five hours")
	// ErrNotRecording: the meeting is not being recorded.
	ErrNotRecording = fmt.Errorf("%w: the meeting is not being recorded", storeerr.ErrConflict)
)

// LiveStart is a meeting the owner starts recording.
type LiveStart struct {
	Kind     string
	Title    string
	Language string
	People   []string
}

// StartLive records a meeting that starts recording now; refused while another is recording.
func (s *Service) StartLive(ctx context.Context, ls LiveStart) (meetingstore.Meeting, error) {
	ls.Language = strings.ToLower(strings.TrimSpace(ls.Language))
	if ls.Language != "" && !languagePattern.MatchString(ls.Language) {
		return meetingstore.Meeting{}, fmt.Errorf("%w: the language is a two-letter ISO 639-1 code, such as vi",
			ErrInvalid)
	}

	return s.cfg.Store.CreateLive(ctx, meetingstore.NewLive{
		Kind: ls.Kind, Title: ls.Title, Language: ls.Language, People: ls.People,
	})
}

// Recordable says whether a track of a meeting may be recorded now: the meeting is live and recording, and an in
// person meeting records the microphone only.
func (s *Service) Recordable(ctx context.Context, id, track string) (meetingstore.Meeting, error) {
	m, err := s.cfg.Store.Get(ctx, id)
	if err != nil {
		return meetingstore.Meeting{}, err
	}

	switch {
	case m.Source != meetingstore.SourceLive || m.State != meetingstore.Recording:
		return meetingstore.Meeting{}, ErrNotRecording
	case track != meetingstore.TrackMic && track != meetingstore.TrackTab:
		return meetingstore.Meeting{}, fmt.Errorf("%w: a track is mic or tab", ErrInvalid)
	case track == meetingstore.TrackTab && m.Kind != meetingstore.Online:
		return meetingstore.Meeting{}, fmt.Errorf("%w: an in-person meeting records the microphone only", ErrInvalid)
	}

	return m, nil
}

// TrackListener is told what a track hears, one call at a time.
type TrackListener struct {
	// Preview is what is being said and is not final yet; empty when nothing is.
	Preview func(speaker, text string)
	// Line is a line kept.
	Line func(meetingstore.Line)
	// Failed: the track ended on its own; err says why (speech.ErrFailed, speech.ErrBehind, ErrNotRecording…).
	Failed func(err error)
}

// Track is one track being recorded.
type Track struct {
	svc     *Service
	id      string
	track   string
	live    *speech.Live
	file    *os.File
	opened  time.Time
	failure func(error)

	// ready is closed once live is set.
	ready chan struct{}

	mu     sync.Mutex
	pos    int64
	closed bool
	done   chan struct{}
}

func trackKey(id, track string) string { return id + "/" + track }

// OpenTrack starts recording a track at offset on the meeting's clock: the time of its first sample, so a socket that
// reconnects, or a meeting resumed, goes on where it was. One socket records a track at a time.
func (s *Service) OpenTrack(ctx context.Context, id, track string, offset time.Duration, l TrackListener,
) (*Track, error) {
	m, err := s.Recordable(ctx, id, track)
	if err != nil {
		return nil, err
	}

	if offset < 0 || offset >= MaxTrack {
		return nil, ErrTooLong
	}

	t := &Track{
		svc: s, id: id, track: track, opened: time.Now(), done: make(chan struct{}), ready: make(chan struct{}),
		failure: l.Failed,
	}

	s.mu.Lock()
	if _, open := s.tracks[trackKey(id, track)]; open {
		s.mu.Unlock()

		return nil, ErrBusy
	}

	s.tracks[trackKey(id, track)] = t
	s.mu.Unlock()

	ok := false

	defer func() {
		if !ok {
			s.forget(t)
		}
	}()

	words, err := s.sentWords(ctx)
	if err != nil {
		return nil, err
	}

	after, heard, err := s.cfg.Store.TrackEnd(ctx, id, track)
	if err != nil {
		return nil, err
	}

	if !heard {
		after = -1
	}

	// The track's audio, at its place on the meeting's clock; a byte is two of a sample, kept whole.
	t.pos = offsetBytes(offset)

	t.file, err = os.OpenFile(s.trackPath(id, track), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("keeping the track's audio: %w", err)
	}

	// Online, the microphone carries only the owner; everything else is told apart by voice (MemoryOS's openTrack).
	diarize := !(m.Kind == meetingstore.Online && track == meetingstore.TrackMic)

	// The track outlives the request that opened it only as long as its socket; what it keeps is not cut short.
	kept := context.WithoutCancel(ctx)

	t.live, err = s.cfg.Speech.Live(ctx, speech.LiveRequest{
		Language: m.Language, Speakers: diarize, Terms: words, Offset: offset, After: after,
	}, speech.LiveListener{
		Preview: l.Preview,
		Segment: func(seg speech.Segment) {
			line, err := s.cfg.Store.AppendLine(kept, id, track, meetingstore.HeardLine{
				Start: seg.Start, End: seg.End, Speaker: seg.Speaker, Text: seg.Text,
			})
			if err != nil {
				// The meeting was deleted or ended meanwhile: nothing more is kept.
				go t.fail(kept, ErrNotRecording)

				return
			}

			if l.Line != nil {
				l.Line(line)
			}
		},
		Failed: func(err error) { go t.fail(kept, err) },
	})
	if err != nil {
		_ = t.file.Close()

		return nil, err
	}

	ok = true

	close(t.ready)

	return t, nil
}

// Done is closed when the track has ended.
func (t *Track) Done() <-chan struct{} { return t.done }

// Append records audio: it is written at its place in the track's file and sent to the provider.
func (t *Track) Append(pcm []byte) error {
	t.mu.Lock()

	if t.closed {
		t.mu.Unlock()

		return ErrNotRecording
	}

	if t.pos+int64(len(pcm)) > offsetBytes(MaxTrack) {
		t.mu.Unlock()

		return ErrTooLong
	}

	_, err := t.file.WriteAt(pcm, t.pos)
	t.pos += int64(len(pcm))
	t.mu.Unlock()

	if err != nil {
		return fmt.Errorf("keeping the track's audio: %w", err)
	}

	return t.live.Append(pcm)
}

// Finish ends the track after its last words are kept, waiting at most until ctx ends.
func (t *Track) Finish(ctx context.Context) {
	t.live.Finish(ctx)
	t.end(ctx)
}

// Close ends the track at once.
func (t *Track) Close(ctx context.Context) {
	t.live.Close()
	t.end(ctx)
}

func (t *Track) fail(ctx context.Context, err error) {
	<-t.ready
	t.live.Close()

	if t.end(ctx) && t.failure != nil {
		t.failure(err)
	}
}

// end closes the track's file, records the audio heard on the meetings' key and lets another socket record the
// track; true the first time.
func (t *Track) end(ctx context.Context) bool {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()

		return false
	}

	t.closed = true
	_ = t.file.Close()
	t.mu.Unlock()

	t.svc.forget(t)

	// The audio heard is counted as each track closes (MemoryOS's usage when a stream closes), as an upload's is.
	t.svc.record(ctx, modelgateway.AudioCall{
		Kind: speech.KindSTT, Provider: t.live.Provider(), Model: t.live.Model(), Audio: t.live.Sent(),
		Duration: time.Since(t.opened),
	})

	close(t.done)

	return true
}

func (s *Service) forget(t *Track) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tracks[trackKey(t.id, t.track)] == t {
		delete(s.tracks, trackKey(t.id, t.track))
	}
}

// openTracks are the tracks of a meeting being recorded now.
func (s *Service) openTracks(id string) []*Track {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []*Track

	for _, track := range []string{meetingstore.TrackMic, meetingstore.TrackTab} {
		if t, ok := s.tracks[trackKey(id, track)]; ok {
			out = append(out, t)
		}
	}

	return out
}

// End ends a meeting being recorded (MemoryOS MeetingService.end): the tracks still open keep their last words
// first, waiting at most endWait, then the tracks' audio becomes the meeting's recording, and the meeting moves to
// transcribed with its notes to be written, once. Ending one that has ended answers it as it is.
func (s *Service) End(ctx context.Context, id string) (meetingstore.Meeting, error) {
	s.ending.Lock()
	defer s.ending.Unlock()

	m, err := s.cfg.Store.Get(ctx, id)
	if err != nil {
		return meetingstore.Meeting{}, err
	}

	if m.Source != meetingstore.SourceLive {
		return meetingstore.Meeting{}, fmt.Errorf("%w: only a meeting recorded live is ended", ErrInvalid)
	}

	if m.State != meetingstore.Recording {
		return m, nil
	}

	finishing, cancel := context.WithTimeout(context.WithoutCancel(ctx), endWait)

	var wg sync.WaitGroup
	for _, t := range s.openTracks(id) {
		wg.Go(func() { t.Finish(finishing) })
	}

	wg.Wait()
	cancel()

	size, duration, err := mix(s.path(id), s.trackPaths(id))
	if err != nil {
		return meetingstore.Meeting{}, fmt.Errorf("keeping the recording: %w", err)
	}

	ended, err := s.cfg.Store.EndLive(ctx, id, meetingstore.Ended{
		Size: size, Duration: duration, Provider: speech.ProviderSoniox, Model: speech.SonioxRealtimeModel,
	})
	if err != nil {
		return meetingstore.Meeting{}, err
	}

	if ended {
		s.removeTracks(id)
		s.cfg.Logger.InfoContext(ctx, "a live meeting ended", slog.String("event", "meetings.live_ended"),
			slog.String("meeting_id", id), slog.Duration("recorded", duration))
	}

	return s.cfg.Store.Get(ctx, id)
}

// recorded is how much of a meeting being recorded is kept: its longest track.
func (s *Service) recorded(id string) time.Duration {
	var longest int64

	for _, p := range s.trackPaths(id) {
		if info, err := os.Stat(p); err == nil {
			longest = max(longest, info.Size())
		}
	}

	return time.Duration(longest/2) * time.Second / speech.LiveSampleRate
}

// withRecorded gives a meeting being recorded the length kept so far, where a resumed recording goes on.
func (s *Service) withRecorded(m meetingstore.Meeting) meetingstore.Meeting {
	if m.State == meetingstore.Recording {
		m.Duration = s.recorded(m.ID)
	}

	return m
}

func (s *Service) trackPath(id, track string) string {
	return filepath.Join(s.cfg.Dir, filepath.Base(id)+"."+track+".pcm")
}

func (s *Service) trackPaths(id string) []string {
	return []string{s.trackPath(id, meetingstore.TrackMic), s.trackPath(id, meetingstore.TrackTab)}
}

func (s *Service) removeTracks(id string) {
	for _, p := range s.trackPaths(id) {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.cfg.Logger.Warn("removing a track's audio", slog.String("event", "meetings.track_remove_failed"),
				slog.String("meeting_id", id), slog.String("error", err.Error()))
		}
	}
}

// offsetBytes is where a time on the meeting's clock is in a track's file, on a whole sample.
func offsetBytes(d time.Duration) int64 {
	return int64(d/time.Millisecond) * speech.LiveBytesPerSecond / 1000 &^ 1
}
