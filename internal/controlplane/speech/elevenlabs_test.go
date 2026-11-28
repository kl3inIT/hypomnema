package speech_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
)

// elevenLabs is ElevenLabs's API as the tests need it: its models with their languages, and speech, plain or with
// timestamps. statuses are the refusals the next speech requests get, in turn, each with its Retry-After.
type elevenLabs struct {
	mu        sync.Mutex
	bodies    []map[string]any
	paths     []string
	statuses  []refusal
	alignment map[string]any
}

type refusal struct {
	status     int
	retryAfter string
}

func (f *elevenLabs) serve(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `[
				{"model_id":"eleven_v4","name":"Eleven v4","can_do_text_to_speech":true,
				 "languages":[{"language_id":"en","name":"English"},{"language_id":"vi","name":"Vietnamese"}]},
				{"model_id":"eleven_multilingual_v2","name":"Eleven Multilingual v2","can_do_text_to_speech":true,
				 "languages":[{"language_id":"en","name":"English"},{"language_id":"pt","name":"Portuguese"}]},
				{"model_id":"eleven_multilingual_sts_v2","name":"Speech to speech","can_do_text_to_speech":false}
			]`)

			return
		}

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.bodies = append(f.bodies, body)
		f.paths = append(f.paths, r.URL.Path+"?"+r.URL.RawQuery)

		if len(f.statuses) > 0 {
			next := f.statuses[0]
			f.statuses = f.statuses[1:]

			if next.retryAfter != "" {
				w.Header().Set("Retry-After", next.retryAfter)
			}

			http.Error(w, `{"detail":"busy"}`, next.status)

			return
		}

		if !strings.HasSuffix(r.URL.Path, "/with-timestamps") {
			_, _ = io.WriteString(w, "mp3")

			return
		}

		answer := map[string]any{"audio_base64": base64.StdEncoding.EncodeToString([]byte("mp3"))}
		if f.alignment != nil {
			answer["alignment"] = f.alignment
			answer["normalized_alignment"] = map[string]any{
				"characters": []string{"x"}, "character_start_times_seconds": []float64{0},
				"character_end_times_seconds": []float64{9},
			}
		}

		_ = json.NewEncoder(w).Encode(answer)
	}))
	t.Cleanup(srv.Close)

	return srv
}

// elevenLabsOnly is a Service that asks ElevenLabs alone, with one key and the model set, if any.
func elevenLabsOnly(t *testing.T, f *elevenLabs, model string, waits *[]time.Duration) *speech.Service {
	t.Helper()

	return speech.New(speech.Config{
		Keys: func(context.Context) ([]speech.Key, error) {
			return []speech.Key{{Provider: "elevenlabs", Name: "speech-elevenlabs", Value: "xi"}}, nil
		},
		Settings: func(context.Context) (speech.Settings, error) {
			set := speech.Settings{Off: []string{"soniox"}}
			if model != "" {
				set.Models = map[string]string{"elevenlabs": model}
			}

			return set, nil
		},
		ElevenLabsURL: f.serve(t).URL,
		Wait: func(_ context.Context, d time.Duration) error {
			*waits = append(*waits, d)

			return nil
		},
	})
}

// Scenario: the owner has chosen no ElevenLabs model, so speech is made with eleven_v4 (issue #1283, row 1); a
// line in Vietnamese is sent with language_code vi, which v4 covers, and a line with no language with none.
// When the owner chose a model that does not cover Vietnamese, the language is left out rather than refused, and
// multilingual v2, which takes no language_code, gets none; a language with a region is sent as its ISO 639-1 code.
func TestElevenLabsSpeaksWithTheChosenModelAndALanguageItCovers(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "eleven_v4", speech.Settings{}.Model("elevenlabs", speech.KindTTS), "the default model")
	assert.Equal(t, "scribe_v2", speech.Settings{Models: map[string]string{"elevenlabs": "eleven_v3"}}.
		Model("elevenlabs", speech.KindSTT), "the choice is of the speech model only")

	f := &elevenLabs{}
	var waits []time.Duration

	svc := elevenLabsOnly(t, f, "", &waits)

	out, err := svc.Speak(t.Context(), speech.SpeakRequest{Input: "Ủa", Language: "vi"})
	require.NoError(t, err)
	assert.Equal(t, "eleven_v4", out.Model)

	_, err = svc.Speak(t.Context(), speech.SpeakRequest{Input: "Hello"})
	require.NoError(t, err)

	old := elevenLabsOnly(t, f, "eleven_multilingual_v2", &waits)

	out, err = old.Speak(t.Context(), speech.SpeakRequest{Input: "Ủa", Language: "vi"})
	require.NoError(t, err)
	assert.Equal(t, "eleven_multilingual_v2", out.Model)

	_, err = old.Speak(t.Context(), speech.SpeakRequest{Input: "Hello", Language: "en"})
	require.NoError(t, err)

	_, err = svc.Speak(t.Context(), speech.SpeakRequest{Input: "Hello", Language: "en-US"})
	require.NoError(t, err)

	require.Len(t, f.bodies, 5)
	assert.Equal(t, map[string]any{"text": "Ủa", "model_id": "eleven_v4", "language_code": "vi"}, f.bodies[0])
	assert.Equal(t, map[string]any{"text": "Hello", "model_id": "eleven_v4"}, f.bodies[1])
	assert.Equal(t, map[string]any{"text": "Ủa", "model_id": "eleven_multilingual_v2"}, f.bodies[2],
		"a language the model does not cover is left out")
	assert.Equal(t, map[string]any{"text": "Hello", "model_id": "eleven_multilingual_v2"}, f.bodies[3],
		"multilingual v2 takes no language_code, though it lists the language (pipecat)")
	assert.Equal(t, "en", f.bodies[4]["language_code"], "sent as its ISO 639-1 code, without the region")
	assert.Equal(t, "/v1/text-to-speech/21m00Tcm4TlvDq8ikWAM?output_format=mp3_44100_128", f.paths[0])
	assert.Empty(t, waits)
}

// Scenario: a video's line is spoken with its timings (issue #1283, row 3): ElevenLabs's alignment is split into
// words on whitespace, each from its first character's start to its last's end; the audio tag [excited] is no
// word, Vietnamese diacritics stay, and normalized_alignment is not read. An answer without alignment has no words.
func TestElevenLabsTimingsAreTheAlignmentSplitIntoWords(t *testing.T) {
	t.Parallel()

	text := "[excited] Ủa,  bánh mì[laughs]kìa?"
	chars := strings.Split(text, "")
	starts := make([]float64, len(chars))
	ends := make([]float64, len(chars))

	for i := range chars {
		starts[i] = float64(i) / 4
		ends[i] = float64(i)/4 + 0.125
	}

	f := &elevenLabs{alignment: map[string]any{
		"characters": chars, "character_start_times_seconds": starts, "character_end_times_seconds": ends,
	}}

	var waits []time.Duration

	svc := elevenLabsOnly(t, f, "", &waits)

	out, err := svc.Speak(t.Context(), speech.SpeakRequest{Input: text, Language: "vi", Timings: true})
	require.NoError(t, err)
	assert.Equal(t, "mp3", string(out.Audio))
	assert.Equal(t, "/v1/text-to-speech/21m00Tcm4TlvDq8ikWAM/with-timestamps?output_format=mp3_44100_128",
		f.paths[0])

	at := func(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
	assert.Equal(t, []speech.Word{
		{Word: "Ủa,", Start: at(2.5), End: at(3.125)},
		{Word: "bánh", Start: at(3.75), End: at(4.625)},
		{Word: "mì", Start: at(5), End: at(5.375)},
		{Word: "kìa?", Start: at(7.5), End: at(8.375)},
	}, out.Words)

	f.alignment = nil

	out, err = svc.Speak(t.Context(), speech.SpeakRequest{Input: text, Timings: true})
	require.NoError(t, err)
	assert.Equal(t, "mp3", string(out.Audio))
	assert.Nil(t, out.Words)
}

// Scenario: ElevenLabs is rate limited, then fails on its side (issue #1283, row 6): it is asked again twice, first
// after the Retry-After it named, then after 2 seconds, and the third answer is spoken. When it fails three times in
// a row, the failure counts as before: the provider is not asked a fourth time and the call fails.
func TestElevenLabsIsAskedAgainTwiceAfterALimitOrAFailureOnItsSide(t *testing.T) {
	t.Parallel()

	f := &elevenLabs{statuses: []refusal{{http.StatusTooManyRequests, "3"}, {http.StatusServiceUnavailable, ""}}}

	var waits []time.Duration

	svc := elevenLabsOnly(t, f, "", &waits)

	out, err := svc.Speak(t.Context(), speech.SpeakRequest{Input: "hi"})
	require.NoError(t, err)
	assert.Equal(t, "mp3", string(out.Audio))
	assert.Len(t, f.bodies, 3)
	assert.Equal(t, []time.Duration{3 * time.Second, 2 * time.Second}, waits)

	f.statuses = []refusal{{http.StatusBadGateway, ""}, {http.StatusBadGateway, ""}, {http.StatusBadGateway, ""}}
	waits = nil

	_, err = svc.Speak(t.Context(), speech.SpeakRequest{Input: "hi"})
	require.ErrorIs(t, err, speech.ErrFailed)
	assert.Len(t, f.bodies, 6)
	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second}, waits)

	f.statuses = []refusal{{http.StatusBadRequest, ""}}
	waits = nil

	_, err = svc.Speak(t.Context(), speech.SpeakRequest{Input: "hi"})
	require.ErrorIs(t, err, speech.ErrFailed)
	assert.Len(t, f.bodies, 7, "a refusal of the request is not asked again")
	assert.Empty(t, waits)
}

// roundTrip answers a request in memory.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Scenario: with the default budget and wait, ElevenLabs is rate limited twice, each time asking for 60 seconds
// (review of #1300): both retries happen, the third answer is spoken, after the two minutes the waits took and the
// three seconds the answers did.
func TestBothRetriesFitTheSpeechBudget(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		posts := 0
		start := time.Now()
		svc := speech.New(speech.Config{
			Keys: func(context.Context) ([]speech.Key, error) {
				return []speech.Key{{Provider: "elevenlabs", Name: "speech-elevenlabs", Value: "xi"}}, nil
			},
			Settings: func(context.Context) (speech.Settings, error) {
				return speech.Settings{Off: []string{"soniox"}}, nil
			},
		})
		speech.SetTransport(svc, roundTrip(func(r *http.Request) (*http.Response, error) {
			time.Sleep(time.Second) // Each answer takes a second.

			posts++
			answer := &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("mp3")),
				Request: r,
			}
			if posts <= 2 {
				answer.StatusCode = http.StatusTooManyRequests
				answer.Header.Set("Retry-After", "60")
				answer.Body = io.NopCloser(strings.NewReader(`{"detail":"busy"}`))
			}

			return answer, nil
		}))

		out, err := svc.Speak(t.Context(), speech.SpeakRequest{Input: "hi"})
		require.NoError(t, err)
		assert.Equal(t, "mp3", string(out.Audio))
		assert.Equal(t, 3, posts)
		assert.Equal(t, 2*time.Minute+3*time.Second, time.Since(start))
	})
}

// Scenario: Settings → Audio lists ElevenLabs's speech models, each with the languages it covers; a model that does
// not speak text is not listed.
func TestElevenLabsModelsAreListedWithTheirLanguages(t *testing.T) {
	t.Parallel()

	var waits []time.Duration

	svc := elevenLabsOnly(t, &elevenLabs{}, "", &waits)

	models, failures, err := svc.Models(t.Context())
	require.NoError(t, err)
	assert.Empty(t, failures)
	assert.Equal(t, []speech.ModelInfo{
		{ID: "eleven_v4", Name: "Eleven v4", Provider: "elevenlabs", Kind: "tts", Languages: []string{"en", "vi"}},
		{
			ID: "eleven_multilingual_v2", Name: "Eleven Multilingual v2", Provider: "elevenlabs",
			Kind: "tts", Languages: []string{"en", "pt"},
		},
	}, models)
}

// A video's speed is Soniox's 0.7-1.3 (issue #1285, row 3); ElevenLabs takes 0.7-1.2 and Eleven v4 applies none
// (ElevenLabs's "Control speaking speed"; review of #1302): a model that applies it is sent the speed moved into its
// range, Eleven v4 is sent none.
func TestElevenLabsIsSentASpeedItApplies(t *testing.T) {
	t.Parallel()

	for model, want := range map[string]any{"eleven_multilingual_v2": 1.2, "eleven_v4": nil} {
		f := &elevenLabs{}
		var waits []time.Duration
		svc := elevenLabsOnly(t, f, model, &waits)

		_, err := svc.Speak(t.Context(), speech.SpeakRequest{Input: "hi", Speed: 1.3})
		require.NoError(t, err, model)
		require.Len(t, f.bodies, 1, model)

		settings, _ := f.bodies[0]["voice_settings"].(map[string]any)
		assert.Equal(t, want, settings["speed"], model)
	}
}
