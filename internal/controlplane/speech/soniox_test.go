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
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
)

// soniox fakes Soniox's REST API, its TTS host and its TTS WebSocket. tts are the statuses the TTS host answers in
// turn, then 200; each with its Retry-After when retryAfter names one.
type soniox struct {
	mu         sync.Mutex
	tts        []int
	retryAfter map[int]string
	ttsAsked   []map[string]any
	jobs       []map[string]any
	starts     []map[string]any
	texts      []string
}

func (f *soniox) serve(t *testing.T) (api, tts, ws string) {
	t.Helper()

	apiSrv := httptest.NewServer(http.HandlerFunc(f.api))
	ttsSrv := httptest.NewServer(http.HandlerFunc(f.ttsHost))
	wsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.stream(t, w, r) }))

	t.Cleanup(apiSrv.Close)
	t.Cleanup(ttsSrv.Close)
	t.Cleanup(wsSrv.Close)

	return apiSrv.URL, ttsSrv.URL, "ws" + strings.TrimPrefix(wsSrv.URL, "http")
}

func (f *soniox) ttsHost(w http.ResponseWriter, r *http.Request) {
	var body map[string]any

	_ = json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	defer f.mu.Unlock()

	f.ttsAsked = append(f.ttsAsked, body)

	if len(f.tts) > 0 {
		status := f.tts[0]
		f.tts = f.tts[1:]

		if status != http.StatusOK {
			if ra := f.retryAfter[status]; ra != "" {
				w.Header().Set("Retry-After", ra)
			}

			http.Error(w, `{"error_type":"refused"}`, status)

			return
		}
	}

	_, _ = io.WriteString(w, "speech")
}

func (f *soniox) api(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.URL.Path == "/files" && r.Method == http.MethodPost:
		_, _ = io.WriteString(w, `{"id":"file-1"}`)
	case r.URL.Path == "/transcriptions" && r.Method == http.MethodPost:
		var job map[string]any

		_ = json.NewDecoder(r.Body).Decode(&job)

		f.mu.Lock()
		f.jobs = append(f.jobs, job)
		f.mu.Unlock()

		_, _ = io.WriteString(w, `{"id":"job-1"}`)
	case r.URL.Path == "/transcriptions/job-1":
		_, _ = io.WriteString(w, `{"status":"completed","audio_duration_ms":1000}`)
	case r.URL.Path == "/transcriptions/job-1/transcript":
		_, _ = io.WriteString(w, `{"text":"xin chào","tokens":[{"text":"xin","start_ms":0,"end_ms":300}]}`)
	case r.URL.Path == "/tts-models":
		_, _ = io.WriteString(w, `{"models":[{"id":"tts-rt-v2","name":"TTS v2","supports_timestamps":true,`+
			`"supports_silence_reduction":true},{"id":"tts-rt-v1","name":"TTS v1"},`+
			`{"id":"tts-rt-latest","name":"latest","aliased_model_id":"tts-rt-v2"}]}`)
	case r.URL.Path == "/models":
		_, _ = io.WriteString(w, `{"models":[{"id":"stt-async-v6","name":"Async v6","transcription_mode":"async"},`+
			`{"id":"stt-rt-v6","name":"Real-time v6","transcription_mode":"real_time"}]}`)
	case r.URL.Path == "/shared-voices":
		if r.URL.Query().Get("model") != "tts-rt-v2" {
			http.Error(w, `{"error_type":"invalid_request"}`, http.StatusBadRequest)

			return
		}

		if r.URL.Query().Get("cursor") == "" {
			_, _ = io.WriteString(w, `{"voices":[{"id":"Maya"}],"next_page_cursor":"p2"}`)

			return
		}

		_, _ = io.WriteString(w, `{"voices":[{"id":"Daniel"}],"next_page_cursor":null}`)
	case r.URL.Path == "/voices":
		_, _ = io.WriteString(w, `{"voices":[`+
			`{"id":"21b9c8e2-1c3a","name":"narrator","models":[{"model":"tts-rt-v2","status":"ready"}]},`+
			`{"id":"9f00","name":"draft","models":[{"model":"tts-rt-v2","status":"processing"}]}]}`)
	case r.Method == http.MethodDelete:
	default:
		http.NotFound(w, r)
	}
}

// stream answers a WebSocket stream: its line's audio in two parts, each with the times of its characters, an
// audio tag spoken first, then terminated.
func (f *soniox) stream(t *testing.T, w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		t.Errorf("accepting the stream: %v", err)

		return
	}
	defer conn.CloseNow() //nolint:errcheck // The test's own.

	ctx := r.Context()
	read := func(ctx context.Context) map[string]any {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return nil
		}

		var m map[string]any

		_ = json.Unmarshal(raw, &m)

		return m
	}

	start, text := read(ctx), read(ctx)
	line, _ := text["text"].(string)

	f.mu.Lock()
	f.starts = append(f.starts, start)
	f.texts = append(f.texts, r.Header.Get("Authorization"))
	f.mu.Unlock()

	spoken := []rune("[excited] " + line)
	half := len(spoken) / 2

	for k, part := range [][]rune{spoken[:half], spoken[half:]} {
		chars, starts, ends := []string{}, []float64{}, []float64{}

		for i, c := range part {
			at := float64(k*half+i) / 10
			chars, starts, ends = append(chars, string(c)), append(starts, at), append(ends, at+0.1)
		}

		msg, _ := json.Marshal(map[string]any{ //nolint:errchkjson // Strings, numbers and their lists.
			"stream_id": start["stream_id"], "audio": base64.StdEncoding.EncodeToString([]byte("mp3-")),
			"timestamps": map[string]any{
				"characters": chars, "character_start_times_seconds": starts, "character_end_times_seconds": ends,
			},
		})
		_ = conn.Write(ctx, websocket.MessageText, msg)
	}

	//nolint:errchkjson // Plain values.
	end, _ := json.Marshal(map[string]any{"stream_id": start["stream_id"], "terminated": true})
	_ = conn.Write(ctx, websocket.MessageText, end)
}

func sonioxOnly(context.Context) (speech.Settings, error) {
	return speech.Settings{Off: []string{speech.ProviderElevenLabs}}, nil
}

// newSoniox is a speech service on the fake with one Soniox key, and the waits it slept.
func newSoniox(t *testing.T, f *soniox, settings func(context.Context) (speech.Settings, error),
) (*speech.Service, *[]time.Duration) {
	t.Helper()

	api, tts, ws := f.serve(t)

	var (
		mu    sync.Mutex
		slept []time.Duration
	)

	svc := speech.New(speech.Config{
		Keys: sonioxKeys("speech-soniox"), Settings: settings,
		SonioxURL: api, SonioxTTSURL: tts, SonioxTTSWebSocketURL: ws, PollInterval: time.Millisecond,
		Sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			defer mu.Unlock()

			slept = append(slept, d)

			return nil
		},
		Jitter: func() float64 { return 0 },
	})

	return svc, &slept
}

// A refusal that may pass, 408, 429, 500 or 503, is asked again twice (issue #1285, row 4): after the Retry-After
// it names, else after 1 s, then 2 s. A third is the answer; another refusal is not asked again, nor one whose
// Retry-After is longer than 30 s: that key rests instead.
func TestARefusalThatMayPassIsAskedAgainTwice(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		statuses   []int
		retryAfter map[int]string
		ok         bool
		asked      int
		slept      []time.Duration
	}{
		"503 twice, then speech": {[]int{503, 503}, nil, true, 3, []time.Duration{time.Second, 2 * time.Second}},
		"429 with a Retry-After": {[]int{429}, map[int]string{429: "7"}, true, 2, []time.Duration{7 * time.Second}},
		"408, then speech":       {[]int{408}, nil, true, 2, []time.Duration{time.Second}},
		"500 three times":        {[]int{500, 500, 500}, nil, false, 3, []time.Duration{time.Second, 2 * time.Second}},
		"400 once":               {[]int{400}, nil, false, 1, nil},
		"429 for a minute":       {[]int{429}, map[int]string{429: "60"}, false, 1, nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := &soniox{tts: c.statuses, retryAfter: c.retryAfter}
			svc, slept := newSoniox(t, f, sonioxOnly)

			out, err := svc.Speak(t.Context(), speech.SpeakRequest{Input: "hi"})
			if c.ok {
				require.NoError(t, err)
				assert.Equal(t, "speech", string(out.Audio))
			} else {
				require.ErrorIs(t, err, speech.ErrFailed)
			}

			assert.Len(t, f.ttsAsked, c.asked)
			assert.Equal(t, c.slept, *slept)
		})
	}
}

// A 402 says Soniox's prepaid balance is spent, in those words (issue #1285, row 4).
func TestAnEmptySonioxBalanceIsSaid(t *testing.T) {
	t.Parallel()

	f := &soniox{tts: []int{http.StatusPaymentRequired}}
	svc, _ := newSoniox(t, f, sonioxOnly)

	_, err := svc.Speak(t.Context(), speech.SpeakRequest{Input: "hi"})
	require.ErrorIs(t, err, speech.ErrFailed)
	assert.Contains(t, err.Error(), "Soniox balance is empty")
	assert.Len(t, f.ttsAsked, 1, "an empty balance is not asked again")
}

// The models are the providers' newest unless the owner chose one (issue #1285, row 1): Soniox transcribes on
// stt-async-v6 and meetings stream to stt-rt-v6; a transcription is asked with the model the owner chose.
func TestTheModelsAreTheNewestUnlessTheOwnerChose(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "stt-async-v6", speech.Settings{}.Model(speech.ProviderSoniox, speech.KindSTT))
	assert.Equal(t, "tts-rt-v2", speech.Settings{}.Model(speech.ProviderSoniox, speech.KindTTS))
	assert.Equal(t, "stt-rt-v6", speech.SonioxRealtimeModel)
	assert.Equal(t, []string{"stt-async-v6", "stt-async-v5"}, speech.KnownModels(speech.ProviderSoniox, speech.KindSTT))

	chosen := ""
	f := &soniox{}
	svc, _ := newSoniox(t, f, func(context.Context) (speech.Settings, error) {
		set := speech.Settings{Off: []string{speech.ProviderElevenLabs}}
		if chosen != "" {
			set.Models = map[speech.Slot]string{{Provider: speech.ProviderSoniox, Kind: speech.KindSTT}: chosen}
		}

		return set, nil
	})

	recording := speech.RecordingOf([]byte("ID3 audio"), "a.mp3", "audio/mpeg")

	got, err := svc.Transcribe(t.Context(), speech.TranscribeRequest{Audio: recording, Language: "vi"})
	require.NoError(t, err)
	assert.Equal(t, "stt-async-v6", got.Model)

	chosen = "stt-async-v7"
	got, err = svc.Transcribe(t.Context(), speech.TranscribeRequest{Audio: recording, Language: "vi"})
	require.NoError(t, err)
	assert.Equal(t, "stt-async-v7", got.Model)

	require.Len(t, f.jobs, 2)
	assert.Equal(t, "stt-async-v6", f.jobs[0]["model"])
	assert.Equal(t, "stt-async-v7", f.jobs[1]["model"])
}

// Speech asked with its words' times is spoken over Soniox's WebSocket (issue #1285, rows 2-4): the stream asks the
// times, the speed and the request's reference, carries the key with the connection, and the words come back in the
// line's spelling, its tag left out. Plain speech stays on REST, with its speed; a speed out of 0.7-1.3 is refused
// before any call when Soniox is named.
func TestTimedSpeechIsSpokenOverTheWebSocket(t *testing.T) {
	t.Parallel()

	f := &soniox{}
	svc, _ := newSoniox(t, f, sonioxOnly)

	out, err := svc.Speak(t.Context(), speech.SpeakRequest{
		Input: "Ủa, bánh mì giảm!", Language: "vi", Voice: "Maya", Speed: 1.2, Timestamps: true, Reference: "video-1",
		ReduceSilence: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "mp3-mp3-", string(out.Audio))
	assert.Equal(t, []string{"Ủa,", "bánh", "mì", "giảm!"}, words(out.Words))
	assert.Equal(t, time.Second, out.Words[0].Start, "after the tag's ten characters")

	require.Len(t, f.starts, 1)
	start := f.starts[0]
	assert.Equal(t, true, start["return_timestamps"])
	assert.InDelta(t, 1.2, start["speed"], 1e-9)
	assert.Equal(t, true, start["reduce_silence"], "tts-rt-v2 supports it")
	assert.Equal(t, "video-1", start["client_reference_id"])
	assert.Equal(t, "tts-rt-v2", start["model"])
	assert.Equal(t, "Bearer speech-soniox-value", f.texts[0])
	assert.Empty(t, f.ttsAsked, "no REST call")

	plain, err := svc.Speak(t.Context(), speech.SpeakRequest{Input: "hi", Speed: 0.8})
	require.NoError(t, err)
	assert.Empty(t, plain.Words)
	require.Len(t, f.ttsAsked, 1)
	assert.InDelta(t, 0.8, f.ttsAsked[0]["speed"], 1e-9)
	assert.NotEmpty(t, f.ttsAsked[0]["client_reference_id"], "a new reference")

	_, err = svc.Speak(t.Context(), speech.SpeakRequest{Input: "hi", Speed: 1.5, Provider: speech.ProviderSoniox})
	require.ErrorIs(t, err, speech.ErrInvalid)
	assert.Contains(t, err.Error(), "0.7 to 1.3")
	assert.Len(t, f.ttsAsked, 1, "no call")
}

// Soniox's voices are its model's built-in voices, each page, then the account's cloned voices ready for the model,
// by name (issue #1285, row 5); its models are its catalog's, without aliases.
func TestSonioxVoicesAndModelsComeFromItsCatalog(t *testing.T) {
	t.Parallel()

	f := &soniox{}
	svc, _ := newSoniox(t, f, sonioxOnly)

	voices, failures, err := svc.Voices(t.Context())
	require.NoError(t, err)
	assert.Empty(t, failures)
	assert.Equal(t, []speech.Voice{
		{ID: "Maya", Name: "Maya", Provider: "soniox"}, {ID: "Daniel", Name: "Daniel", Provider: "soniox"},
		{ID: "21b9c8e2-1c3a", Name: "narrator", Provider: "soniox"},
	}, voices)

	tts, err := svc.Models(t.Context(), speech.ProviderSoniox, speech.KindTTS)
	require.NoError(t, err)
	assert.Equal(t, []speech.ModelChoice{{ID: "tts-rt-v2", Name: "TTS v2"}, {ID: "tts-rt-v1", Name: "TTS v1"}}, tts)

	stt, err := svc.Models(t.Context(), speech.ProviderSoniox, speech.KindSTT)
	require.NoError(t, err)
	assert.Equal(t, []speech.ModelChoice{{ID: "stt-async-v6", Name: "Async v6"}}, stt)
}

// A 503 that names a Retry-After over 30 s, within the call's time, is waited for and asked again (review of #1302,
// P1), as ElevenLabs's are: the one key is not failed for a request that would pass.
func TestASonioxRetryAfterOverThirtySecondsIsWaited(t *testing.T) {
	t.Parallel()

	f := &soniox{tts: []int{http.StatusServiceUnavailable}, retryAfter: map[int]string{503: "31"}}
	svc, slept := newSoniox(t, f, sonioxOnly)

	out, err := svc.Speak(t.Context(), speech.SpeakRequest{Input: "hi"})
	require.NoError(t, err)
	assert.Equal(t, "speech", string(out.Audio))
	assert.Len(t, f.ttsAsked, 2)
	assert.Equal(t, []time.Duration{31 * time.Second}, *slept)
}
