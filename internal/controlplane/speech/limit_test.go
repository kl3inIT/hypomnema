package speech

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Soniox's text-to-speech keeps to 3 requests at once (issue #1285, row 6): a fourth waits until one ends.
func TestAFourthRequestWaitsForAPlace(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	l := newLimiter(sonioxTTSConcurrent, sonioxTTSPerMinute, limitWindow, func() time.Time { return now },
		func(context.Context, time.Duration) error { return nil })

	releases := make([]func(), 0, 3)

	for range 3 {
		release, err := l.acquire(t.Context())
		require.NoError(t, err)

		releases = append(releases, release)
	}

	got := make(chan struct{})

	go func() {
		release, err := l.acquire(t.Context())
		if assert.NoError(t, err) {
			release()
		}

		close(got)
	}()

	select {
	case <-got:
		t.Fatal("a fourth request ran while three did")
	case <-time.After(50 * time.Millisecond):
	}

	releases[0]()
	releases[0]() // A release counts once.

	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the fourth request did not run once a place was free")
	}

	third, err := l.acquire(t.Context())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = l.acquire(ctx)
	require.ErrorIs(t, err, context.Canceled, "a request whose caller left does not wait")

	third()
	releases[1]()
	releases[2]()
}

// ... and to 100 a minute: the 101st waits until the first of the minute is a minute old.
func TestTheHundredAndFirstRequestOfAMinuteWaits(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		now   = time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
		slept []time.Duration
	)

	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()

		return now
	}

	l := newLimiter(sonioxTTSConcurrent, sonioxTTSPerMinute, limitWindow, clock,
		func(_ context.Context, d time.Duration) error {
			mu.Lock()
			defer mu.Unlock()

			slept = append(slept, d)
			now = now.Add(d)

			return nil
		})

	for i := range sonioxTTSPerMinute {
		release, err := l.acquire(t.Context())
		require.NoError(t, err)
		release()

		if i == 0 {
			mu.Lock()
			now = now.Add(10 * time.Second)
			mu.Unlock()
		}
	}

	assert.Empty(t, slept)

	release, err := l.acquire(t.Context())
	require.NoError(t, err)
	release()

	assert.Equal(t, []time.Duration{50 * time.Second}, slept, "until the first start is a minute old")
}

// A line longer than Soniox's 5000 characters is refused before any call (issue #1285, row 6).
func TestALineOverSonioxsLimitIsRefusedBeforeTheCall(t *testing.T) {
	t.Parallel()

	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { asked++ }))
	t.Cleanup(srv.Close)

	svc := New(Config{SonioxURL: srv.URL, SonioxTTSURL: srv.URL})
	r := route{key: Key{Provider: ProviderSoniox, Name: "speech-soniox", Value: "v"}, model: SonioxTTSModel}

	_, _, err := svc.sonioxSpeak(t.Context(), r, SpeakRequest{Input: strings.Repeat("ả", sonioxMaxCharacters+1)})
	require.ErrorIs(t, err, ErrInvalid)
	assert.Contains(t, err.Error(), "5000 characters")
	assert.Zero(t, asked)
}

// A request asked again counts as a request of the minute too (review of #1302, P1): with 100 lines, the first
// refused once with a 500 then answered, the 101st request Soniox sees waits for the minute.
func TestTheMinuteLimitCountsTheRequestsAskedAgain(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		asked []time.Time
		now   = time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
		first = true
	)

	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()

		return now
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		if r.URL.Path != "/tts" {
			http.NotFound(w, r)

			return
		}

		asked = append(asked, now)

		if first {
			first = false

			http.Error(w, "busy", http.StatusInternalServerError)

			return
		}

		_, _ = w.Write([]byte("speech"))
	}))
	t.Cleanup(srv.Close)

	svc := New(Config{
		SonioxURL: srv.URL, SonioxTTSURL: srv.URL + "/tts", Now: clock, Jitter: func() float64 { return 0 },
		Sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			defer mu.Unlock()

			now = now.Add(d)

			return nil
		},
	})
	r := route{key: Key{Provider: ProviderSoniox, Name: "speech-soniox", Value: "v"}, model: SonioxTTSModel}

	for range sonioxTTSPerMinute {
		_, _, err := svc.sonioxSpeak(t.Context(), r, SpeakRequest{Input: "hi", Format: "mp3"})
		require.NoError(t, err)
	}

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, asked, sonioxTTSPerMinute+1)

	start := asked[0]
	within := 0

	for _, at := range asked {
		if at.Before(start.Add(limitWindow)) {
			within++
		}
	}

	assert.LessOrEqual(t, within, sonioxTTSPerMinute, "at most 100 requests reach Soniox in the first minute")
}
