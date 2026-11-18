package speech_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech/speechtest"
)

// heard keeps what a live stream told its listener.
type heard struct {
	mu       sync.Mutex
	segments []speech.Segment
	previews []string
	failed   []error
}

func (h *heard) listener() speech.LiveListener {
	return speech.LiveListener{
		Preview: func(_, text string) {
			h.mu.Lock()
			defer h.mu.Unlock()

			h.previews = append(h.previews, text)
		},
		Segment: func(s speech.Segment) {
			h.mu.Lock()
			defer h.mu.Unlock()

			h.segments = append(h.segments, s)
		},
		Failed: func(err error) {
			h.mu.Lock()
			defer h.mu.Unlock()

			h.failed = append(h.failed, err)
		},
	}
}

func (h *heard) said() []speech.Segment {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]speech.Segment(nil), h.segments...)
}

// fakeClock is a clock the test moves.
type fakeClock struct{ at atomic.Int64 }

func (c *fakeClock) now() time.Time { return time.Unix(0, c.at.Load()) }

func (c *fakeClock) add(d time.Duration) { c.at.Add(int64(d)) }

func liveService(url string, clock *fakeClock) *speech.Service {
	cfg := speech.Config{
		Keys: func(context.Context) ([]speech.Key, error) {
			return []speech.Key{{Provider: speech.ProviderSoniox, Name: "speech-soniox", Value: "sk-live"}}, nil
		},
		SonioxRealtimeURL: url, LiveBackoff: []time.Duration{10 * time.Millisecond},
	}
	if clock != nil {
		cfg.Now = clock.now
	}

	return speech.New(cfg)
}

// tenth is a tenth of a second of PCM16 24 kHz mono.
func tenth() []byte { return make([]byte, speech.LiveBytesPerSecond/10) }

// Scenario: a track is streamed to Soniox's realtime API: the key goes with the connection, the start asks PCM16
// 24 kHz mono with endpoints, diarization, the language and the words to hear right; each frame reaches the provider
// as it was sent; the preview follows the speech not yet final; the final tokens become a segment at the meeting's
// time (the stream's own plus the offset), cut at the endpoint; the stream is ended with an empty text frame, after
// which the last words are given.
func TestALiveTrackIsRelayedAndHeardAtTheMeetingsTime(t *testing.T) {
	t.Parallel()

	fake := &speechtest.Realtime{
		Answer: func(_ int, _ map[string]any, received int) ([]string, bool) {
			switch received {
			case len(tenth()):
				return []string{speechtest.Tokens(false, false,
					speechtest.Token{Text: "Hello", Start: 0, End: 300, Speaker: "1"})}, false
			case 3 * len(tenth()):
				return []string{speechtest.Tokens(true, true,
					speechtest.Token{Text: "Hello", Start: 0, End: 300, Speaker: "1"},
					speechtest.Token{Text: " there.", Start: 300, End: 800, Speaker: "1"})}, false
			}

			return nil, false
		},
		End: func(int, map[string]any) []string {
			return []string{speechtest.Tokens(true, false,
				speechtest.Token{Text: "Bye.", Start: 900, End: 1200, Speaker: "2"})}
		},
	}
	svc := liveService(fake.Serve(t), nil)
	h := &heard{}

	live, err := svc.Live(t.Context(), speech.LiveRequest{
		Language: "vi", Speakers: true, Terms: []string{"MapOps"}, Offset: 2 * time.Second, After: -1,
	}, h.listener())
	require.NoError(t, err)

	frames := [][]byte{tenth(), tenth(), tenth(), tenth()}
	for i, f := range frames {
		f[0] = byte(i + 1)
		require.NoError(t, live.Append(f))
	}

	require.Eventually(t, func() bool { return len(h.said()) == 1 }, 5*time.Second, 10*time.Millisecond)

	live.Finish(t.Context())

	assert.Equal(t, []speech.Segment{
		{Start: 2 * time.Second, End: 2800 * time.Millisecond, Text: "Hello there.", Speaker: "1"},
		{Start: 2900 * time.Millisecond, End: 3200 * time.Millisecond, Text: "Bye.", Speaker: "2"},
	}, h.said())
	assert.Contains(t, h.previews, "Hello")
	assert.Empty(t, h.failed)
	assert.Equal(t, 400*time.Millisecond, live.Sent())

	streams := fake.Streams()
	require.Len(t, streams, 1)

	s := streams[0]
	assert.Equal(t, "Bearer sk-live", s.Auth)
	assert.Equal(t, speech.SonioxRealtimeModel, s.Start["model"])
	assert.Equal(t, "pcm_s16le", s.Start["audio_format"])
	assert.InDelta(t, 24000, s.Start["sample_rate"], 0)
	assert.InDelta(t, 1, s.Start["num_channels"], 0)
	assert.Equal(t, true, s.Start["enable_endpoint_detection"])
	assert.Equal(t, true, s.Start["enable_speaker_diarization"])
	assert.Equal(t, []any{"vi"}, s.Start["language_hints"])
	assert.Equal(t, map[string]any{"terms": []any{"MapOps"}}, s.Start["context"])
	assert.NotContains(t, s.Start, "api_key", "the key goes with the connection only")

	var want []byte
	for _, f := range frames {
		want = append(want, f...)
	}

	assert.Equal(t, want, s.Audio, "each frame reaches the provider, in order")
	assert.Equal(t, []string{""}, s.Texts, "the stream is ended with an empty text frame")
}

// Scenario: the provider's stream fails after a line was given. A new stream is opened, the last five seconds are
// replayed into it and its times are shifted by the audio sent before them; the replay hears the given line again,
// and it is not given twice; the new speech after it is.
func TestAReconnectReplaysWithoutGivingALineTwice(t *testing.T) {
	t.Parallel()

	second := 10 * len(tenth())
	fake := &speechtest.Realtime{
		Answer: func(stream int, _ map[string]any, received int) ([]string, bool) {
			switch {
			case stream == 0 && received == 2*second:
				// Seconds 0-2 of the first stream: one line, then the stream breaks.
				return []string{speechtest.Tokens(true, true,
					speechtest.Token{Text: "First line.", Start: 500, End: 1500, Speaker: "1"})}, true
			case stream == 1 && received == 3*second:
				// The new stream heard the replay (the first stream's seconds 0-2) and a second after it.
				return []string{speechtest.Tokens(true, true,
					speechtest.Token{Text: "First line.", Start: 500, End: 1500, Speaker: "1"},
					speechtest.Token{Text: " Second line.", Start: 2200, End: 2900, Speaker: "1"})}, false
			}

			return nil, false
		},
	}
	svc := liveService(fake.Serve(t), nil)
	h := &heard{}

	live, err := svc.Live(t.Context(), speech.LiveRequest{Offset: 10 * time.Second, After: -1}, h.listener())
	require.NoError(t, err)

	for range 20 {
		require.NoError(t, live.Append(tenth()))
	}

	require.Eventually(t, func() bool { return len(fake.Streams()) == 2 }, 5*time.Second, 5*time.Millisecond)

	for range 10 {
		require.NoError(t, live.Append(tenth()))
	}

	require.Eventually(t, func() bool { return len(h.said()) == 2 }, 5*time.Second, 10*time.Millisecond)
	live.Finish(t.Context())

	assert.Equal(t, []speech.Segment{
		{Start: 10500 * time.Millisecond, End: 11500 * time.Millisecond, Text: "First line.", Speaker: "1"},
		{Start: 12200 * time.Millisecond, End: 12900 * time.Millisecond, Text: "Second line.", Speaker: "1"},
	}, h.said(), "the replayed line is not given twice, and the new stream's times are the meeting's")

	streams := fake.Streams()
	assert.Len(t, streams[1].Audio, 3*second, "the two seconds kept are replayed, then the new audio")
	assert.Empty(t, h.failed)
}

// Scenario: a track is opened where its last kept line ended (a browser that reconnected): a final line that ends
// at or before it is not given again.
func TestALineKeptBeforeIsNotGivenAgain(t *testing.T) {
	t.Parallel()

	fake := &speechtest.Realtime{
		Answer: func(_ int, _ map[string]any, received int) ([]string, bool) {
			if received == len(tenth()) {
				return []string{speechtest.Tokens(true, true,
					speechtest.Token{Text: "Old.", Start: 0, End: 400},
					speechtest.Token{Text: "New.", Start: 600, End: 900})}, false
			}

			return nil, false
		},
	}
	svc := liveService(fake.Serve(t), nil)
	h := &heard{}

	live, err := svc.Live(t.Context(), speech.LiveRequest{Offset: 5 * time.Second, After: 5400 * time.Millisecond},
		h.listener())
	require.NoError(t, err)
	require.NoError(t, live.Append(tenth()))
	require.Eventually(t, func() bool { return len(h.said()) == 1 }, 5*time.Second, 10*time.Millisecond)
	live.Finish(t.Context())

	assert.Equal(t, "New.", h.said()[0].Text)
}

// Scenario: nobody speaks and no audio flows; the stream is kept open with a keepalive after five seconds, as
// Soniox closes a connection idle for 40.
func TestASilentStreamIsKeptAlive(t *testing.T) {
	t.Parallel()

	fake := &speechtest.Realtime{}
	clock := &fakeClock{}
	clock.add(time.Hour)
	svc := liveService(fake.Serve(t), clock)

	live, err := svc.Live(t.Context(), speech.LiveRequest{After: -1}, (&heard{}).listener())
	require.NoError(t, err)

	t.Cleanup(live.Close)

	clock.add(6 * time.Second)
	require.Eventually(t, func() bool {
		s := fake.Streams()

		return len(s) == 1 && len(s[0].Texts) > 0
	}, 5*time.Second, 20*time.Millisecond)

	assert.JSONEq(t, `{"type":"keepalive"}`, fake.Streams()[0].Texts[0])
}

// Scenario: no Soniox key is set: a live stream cannot start, and says where to add one.
func TestALiveStreamNeedsASonioxKey(t *testing.T) {
	t.Parallel()

	svc := speech.New(speech.Config{})

	_, err := svc.Live(t.Context(), speech.LiveRequest{}, speech.LiveListener{})
	require.ErrorIs(t, err, speech.ErrUnavailable)
}

// heldReconnect is a fake whose first stream breaks once it has had first of audio, and whose next stream's
// handshake waits until the test lets it go: the stream is reconnecting meanwhile.
func heldReconnect(t *testing.T, first int, answer func(stream, received int) []string,
) (fake *speechtest.Realtime, reached <-chan struct{}, let func()) {
	t.Helper()

	in := make(chan struct{}, 1)
	release := make(chan struct{})
	fake = &speechtest.Realtime{
		Opening: func(connection int) {
			if connection == 1 {
				in <- struct{}{}
				<-release
			}
		},
		Answer: func(stream int, _ map[string]any, received int) ([]string, bool) {
			if stream == 0 {
				return nil, received == first
			}

			if answer != nil {
				return answer(stream, received), false
			}

			return nil, false
		},
	}

	return fake, in, sync.OnceFunc(func() { close(release) })
}

func reachedIn(t *testing.T, reached <-chan struct{}) {
	t.Helper()

	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("no new stream was asked for")
	}
}

// Scenario: the provider's stream breaks after six seconds, and ten more seconds come while the new one is awaited.
// The new stream is sent the last five seconds sent, then all ten not yet sent, and its times are the meeting's.
func TestAReconnectKeepsTheAudioNotYetSent(t *testing.T) {
	t.Parallel()

	second := 10 * len(tenth())
	fake, reached, let := heldReconnect(t, 6*second, func(_, received int) []string {
		if received == 15*second {
			return []string{speechtest.Tokens(true, true,
				speechtest.Token{Text: "Late words.", Start: 14000, End: 14500, Speaker: "1"})}
		}

		return nil
	})
	svc := liveService(fake.Serve(t), nil)
	t.Cleanup(let)

	h := &heard{}

	live, err := svc.Live(t.Context(), speech.LiveRequest{After: -1}, h.listener())
	require.NoError(t, err)

	for range 60 {
		require.NoError(t, live.Append(tenth()))
	}

	reachedIn(t, reached)

	for range 100 {
		require.NoError(t, live.Append(tenth()))
	}

	let()

	require.Eventually(t, func() bool {
		s := fake.Streams()

		return len(s) == 2 && len(s[1].Audio) == 15*second
	}, 5*time.Second, 10*time.Millisecond, "five seconds replayed, then the ten not yet sent")
	require.Eventually(t, func() bool { return len(h.said()) == 1 }, 5*time.Second, 10*time.Millisecond)
	live.Finish(t.Context())

	assert.Equal(t, []speech.Segment{
		{Start: 15 * time.Second, End: 15500 * time.Millisecond, Text: "Late words.", Speaker: "1"},
	}, h.said(), "the new stream starts at the replay, a second into the meeting")
	assert.Empty(t, h.failed)
}

// Scenario: the new stream is slow to come: thirty seconds wait for it, and audio past them is refused as behind.
func TestAReconnectFallsBehindPastThirtySeconds(t *testing.T) {
	t.Parallel()

	second := 10 * len(tenth())
	fake, reached, let := heldReconnect(t, second, nil)
	svc := liveService(fake.Serve(t), nil)
	t.Cleanup(let)

	live, err := svc.Live(t.Context(), speech.LiveRequest{After: -1}, (&heard{}).listener())
	require.NoError(t, err)
	t.Cleanup(live.Close)

	for range 10 {
		require.NoError(t, live.Append(tenth()))
	}

	reachedIn(t, reached)

	for range 300 {
		require.NoError(t, live.Append(tenth()))
	}

	require.ErrorIs(t, live.Append(tenth()), speech.ErrBehind)
}
