package speech_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
)

// sonioxTTS is Soniox's TTS host that answers each key as answers says, and
// counts what each key was asked.
type sonioxTTS struct {
	mu      sync.Mutex
	answers map[string]int
	asked   []string
}

func (f *sonioxTTS) serve(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.asked = append(f.asked, key)

		if status := f.answers[key]; status != 0 && status != http.StatusOK {
			w.Header().Set("Retry-After", "60")
			http.Error(w, `{"error":"slow down"}`, status)

			return
		}

		_, _ = io.WriteString(w, "speech")
	}))
	t.Cleanup(srv.Close)

	return srv
}

func sonioxKeys(names ...string) func(context.Context) ([]speech.Key, error) {
	return func(context.Context) ([]speech.Key, error) {
		out := make([]speech.Key, 0, len(names))
		for _, n := range names {
			out = append(out, speech.Key{Provider: "soniox", Name: n, Value: n + "-value"})
		}

		return out, nil
	}
}

// Scenario: the owner has two Soniox keys. The first is rate limited: it
// rests for the minute Soniox named and the second key answers at once;
// while the first rests the next call goes to the second only. When both
// rest, the call is refused until the first is free, and no key is asked.
func TestAKeyThatIsLimitedRestsWhileTheNextAnswers(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	tts := &sonioxTTS{answers: map[string]int{"speech-soniox-value": http.StatusTooManyRequests}}
	svc := speech.New(speech.Config{
		Keys: sonioxKeys("speech-soniox", "speech-soniox-2"), SonioxTTSURL: tts.serve(t).URL,
		Settings: func(context.Context) (speech.Settings, error) {
			return speech.Settings{Off: []string{"elevenlabs"}}, nil
		},
		Now: func() time.Time { return now },
	})

	out, err := svc.Speak(t.Context(), speech.SpeakRequest{Input: "hi"})
	require.NoError(t, err)
	assert.Equal(t, "speech", string(out.Audio))
	require.Len(t, out.Errors, 1)
	assert.Contains(t, out.Errors[0].Reason, "key speech-soniox: rate limited (429)")

	_, err = svc.Speak(t.Context(), speech.SpeakRequest{Input: "hi"})
	require.NoError(t, err)
	assert.Equal(t, []string{"speech-soniox-value", "speech-soniox-2-value", "speech-soniox-2-value"}, tts.asked)

	links, _, err := svc.Chain(t.Context())
	require.NoError(t, err)
	require.Len(t, links[0].Keys, 2)
	assert.Equal(t, speech.KeyState{
		Name: "speech-soniox", Rest: speech.RestRateLimited, RestingUntil: now.Add(time.Minute), LastUsed: now,
	}, links[0].Keys[0])
	assert.Empty(t, links[0].Keys[1].Rest)

	tts.mu.Lock()
	tts.answers["speech-soniox-2-value"] = http.StatusTooManyRequests
	tts.mu.Unlock()

	_, err = svc.Speak(t.Context(), speech.SpeakRequest{Input: "hi"})
	require.ErrorIs(t, err, speech.ErrFailed)

	_, err = svc.Speak(t.Context(), speech.SpeakRequest{Input: "hi"})

	var resting *speech.RestingError

	require.ErrorAs(t, err, &resting)
	assert.Equal(t, now.Add(time.Minute), resting.Until)
	assert.Len(t, tts.asked, 4, "no key was asked while both rest")
}

// Scenario: with no key for any provider that is on, a call says to add one;
// a request that cannot be asked is refused before any provider.
func TestACallWithoutAKeyOrWithABadRequestIsRefused(t *testing.T) {
	t.Parallel()

	svc := speech.New(speech.Config{})

	_, err := svc.Speak(t.Context(), speech.SpeakRequest{Input: "hi"})
	require.ErrorIs(t, err, speech.ErrUnavailable)

	for _, req := range []speech.SpeakRequest{
		{Input: " "}, {Input: "hi", Format: "flac"}, {Input: strings.Repeat("a", speech.MaxInputCharacters+1)},
		{Input: "hi", Provider: "google"},
	} {
		_, err = svc.Speak(t.Context(), req)
		require.ErrorIs(t, err, speech.ErrInvalid, req)
	}

	_, err = svc.Transcribe(t.Context(), speech.TranscribeRequest{})
	require.ErrorIs(t, err, speech.ErrInvalid)
}

// Scenario: a key's secret name says its provider and its number; a new key
// takes the lowest free number.
func TestKeyNames(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]struct {
		provider string
		n        int
		ok       bool
	}{
		"speech-soniox":       {"soniox", 1, true},
		"speech-elevenlabs-3": {"elevenlabs", 3, true},
		"speech-soniox-1":     {},
		"speech-soniox-02":    {},
		"speech-google":       {},
		"web-search-exa":      {},
	} {
		provider, n, ok := speech.ParseKeyName(name)
		assert.Equal(t, want.ok, ok, name)
		assert.Equal(t, want.provider, provider, name)
		assert.Equal(t, want.n, n, name)
	}

	assert.Equal(t, "speech-soniox-2", speech.NextKeyName("soniox", []string{"speech-soniox", "speech-soniox-3"}))
}
