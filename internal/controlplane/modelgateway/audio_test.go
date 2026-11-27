package modelgateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
)

// fakeSoniox is Soniox's REST API and its TTS host as the gateway uses them:
// an upload, a job polled until completed, its transcript of two speakers,
// and the deletes after; or, when down, a 503 for everything.
type fakeSoniox struct {
	mu      sync.Mutex
	down    bool
	polls   int
	job     map[string]any
	deleted []string
	auth    string
}

func (f *fakeSoniox) serve(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		f.auth = r.Header.Get("Authorization")

		if f.down {
			http.Error(w, `{"error_message":"overloaded"}`, http.StatusServiceUnavailable)

			return
		}

		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/files":
			file, _, err := r.FormFile("file")
			if err != nil {
				http.Error(w, "no file", http.StatusBadRequest)

				return
			}

			data, _ := io.ReadAll(file)
			assert.Equal(t, "RIFF-recording", string(data))
			_, _ = io.WriteString(w, `{"id":"file-1"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/transcriptions":
			_ = json.NewDecoder(r.Body).Decode(&f.job)
			_, _ = io.WriteString(w, `{"id":"job-1","status":"queued"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/transcriptions/job-1":
			f.polls++
			if f.polls < 2 {
				_, _ = io.WriteString(w, `{"id":"job-1","status":"processing"}`)

				return
			}

			_, _ = io.WriteString(w, `{"id":"job-1","status":"completed","audio_duration_ms":4200}`)
		case r.Method == http.MethodGet && r.URL.Path == "/transcriptions/job-1/transcript":
			_, _ = io.WriteString(w, `{"text":"Hello there. Hi.","tokens":[
				{"text":"Hel","start_ms":0,"end_ms":200,"speaker":"1","language":"en"},
				{"text":"lo","start_ms":200,"end_ms":400,"speaker":"1","language":"en"},
				{"text":" there.","start_ms":400,"end_ms":900,"speaker":"1","language":"en"},
				{"text":" Hi.","start_ms":1000,"end_ms":1400,"speaker":"2","language":"en"},
				{"text":"<end>","start_ms":1400,"end_ms":1400,"speaker":"2"}]}`)
		case r.Method == http.MethodDelete:
			f.deleted = append(f.deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/tts":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "tts-rt-v2", body["model"])
			assert.Equal(t, "Adrian", body["voice"], "an OpenAI voice is not Soniox's")
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = io.WriteString(w, "ID3-soniox-speech")
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

// fakeElevenLabs is ElevenLabs's Scribe, its speech and its voices.
func fakeElevenLabs(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "el-key", r.Header.Get("Xi-Api-Key"))

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/speech-to-text":
			assert.Equal(t, "scribe_v2", r.FormValue("model_id"))
			assert.Equal(t, "true", r.FormValue("diarize"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"language_code":"vie","text":"Xin chào. Chào.","words":[
				{"text":"Xin","start":0.0,"end":0.3,"type":"word","speaker_id":"speaker_0"},
				{"text":" ","start":0.3,"end":0.35,"type":"spacing","speaker_id":"speaker_0"},
				{"text":"chào.","start":0.35,"end":0.8,"type":"word","speaker_id":"speaker_0"},
				{"text":" ","start":0.8,"end":2.0,"type":"spacing","speaker_id":"speaker_1"},
				{"text":"Chào.","start":2.0,"end":2.5,"type":"word","speaker_id":"speaker_1"}]}`)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/text-to-speech/"):
			assert.Equal(t, "/v1/text-to-speech/21m00Tcm4TlvDq8ikWAM", r.URL.Path, "alloy is Rachel")
			assert.Equal(t, "mp3_44100_128", r.URL.Query().Get("output_format"))
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = io.WriteString(w, "ID3-elevenlabs-speech")
		case r.Method == http.MethodGet && r.URL.Path == "/v1/voices":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"voices":[{"voice_id":"v1","name":"Rachel","labels":{"language":"en"}}]}`)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

// audioGateway is a gateway whose audio asks the fake Soniox, then the fake
// ElevenLabs, each with one key.
func audioGateway(t *testing.T, soniox *fakeSoniox) chatGateway {
	t.Helper()

	sonioxURL, elevenLabs := soniox.serve(t).URL, fakeElevenLabs(t).URL

	// What the owner sets is the gateway's, made after the speech it serves.
	var gateway *modelgateway.Service

	voice := speech.New(speech.Config{
		Settings: func(ctx context.Context) (speech.Settings, error) { return gateway.AudioSettings(ctx) },
		Keys: func(context.Context) ([]speech.Key, error) {
			return []speech.Key{
				{Provider: "soniox", Name: "speech-soniox", Value: "sx-key"},
				{Provider: "elevenlabs", Name: "speech-elevenlabs", Value: "el-key"},
			}, nil
		},
		SonioxURL: sonioxURL, SonioxTTSURL: sonioxURL + "/tts", ElevenLabsURL: elevenLabs,
		PollInterval: time.Millisecond,
	})
	upstream, _ := provider(t, plain(http.StatusOK, answer))
	g := newChatGateway(t, modelgateway.Config{Speech: voice}, upstream)
	gateway = g.svc

	return g
}

// transcription posts a recording in OpenAI's form with the fields given.
func (g chatGateway) transcription(t *testing.T, key string, fields map[string]string) (int, http.Header, string) {
	t.Helper()

	var body bytes.Buffer

	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "call.wav")
	require.NoError(t, err)

	_, _ = io.WriteString(part, "RIFF-recording")

	for k, v := range fields {
		require.NoError(t, form.WriteField(k, v))
	}

	require.NoError(t, form.Close())

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/audio/transcriptions", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+key)

	rec := httptest.NewRecorder()
	g.mux.ServeHTTP(rec, req)

	return rec.Code, rec.Header(), rec.Body.String()
}

// Scenario: a client sends a call's recording asking who spoke. Soniox is
// asked first: the recording is uploaded, the job made with speakers on,
// polled until done, its transcript read, and the job and the file deleted.
// The answer is verbose_json, cut into one segment a speaker with the words
// and their speakers; the call is recorded on the key as audio/stt-async-v6
// with the seconds heard.
func TestARecordingIsTranscribedWithItsSpeakers(t *testing.T) {
	t.Parallel()

	soniox := &fakeSoniox{}
	g := audioGateway(t, soniox)

	status, _, body := g.transcription(t, g.key, map[string]string{
		"model": "whisper-1", "diarize": "true", "timestamp_granularities[]": "word",
	})
	require.Equal(t, http.StatusOK, status, body)
	assert.JSONEq(t, `{"task":"transcribe","text":"Hello there. Hi.","language":"en","duration":4.2,
		"provider":"soniox","model":"stt-async-v6",
		"segments":[{"id":0,"start":0,"end":0.9,"text":"Hello there.","speaker":"1"},
			{"id":1,"start":1,"end":1.4,"text":"Hi.","speaker":"2"}],
		"words":[{"word":"Hel","start":0,"end":0.2,"speaker":"1"},{"word":"lo","start":0.2,"end":0.4,"speaker":"1"},
			{"word":" there.","start":0.4,"end":0.9,"speaker":"1"},{"word":" Hi.","start":1,"end":1.4,"speaker":"2"}]}`,
		body)

	soniox.mu.Lock()
	assert.Equal(t, "Bearer sx-key", soniox.auth)
	assert.Equal(t, map[string]any{
		"model": "stt-async-v6", "file_id": "file-1", "enable_speaker_diarization": true,
		"enable_language_identification": true,
	}, soniox.job)
	assert.Equal(t, []string{"/transcriptions/job-1", "/files/file-1"}, soniox.deleted)
	soniox.mu.Unlock()

	u := g.lastUsage(t)
	assert.Equal(t, "audio/stt-async-v6", u.Model)
	assert.Equal(t, "soniox", u.Provider)
	assert.Equal(t, modelgateway.OutcomeOK, u.Outcome)
	assert.Equal(t, 4200*time.Millisecond, u.Audio)

	// Without speakers, OpenAI's json answer: the text only.
	status, _, body = g.transcription(t, g.key, nil)
	require.Equal(t, http.StatusOK, status, body)
	assert.JSONEq(t, `{"text":"Hello there. Hi."}`, body)

	status, _, body = g.transcription(t, g.key, map[string]string{"response_format": "srt"})
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "1\n00:00:00,000 --> 00:00:00,900\nHello there.\n\n2\n00:00:01,000 --> 00:00:01,400\nHi.\n\n", body)
}

// Scenario: Soniox is down. ElevenLabs is asked next and answers; the answer
// says Soniox failed and why, ElevenLabs's speaker ids are the speakers, and
// the call is recorded as ElevenLabs's Scribe after two attempts.
func TestATranscriptionFallsBackToElevenLabsAndSaysWhy(t *testing.T) {
	t.Parallel()

	g := audioGateway(t, &fakeSoniox{down: true})

	status, _, body := g.transcription(t, g.key, map[string]string{"enable_speaker_diarization": "yes"})
	require.Equal(t, http.StatusOK, status, body)

	var got struct {
		Provider string `json:"provider"`
		Segments []struct {
			Text    string `json:"text"`
			Speaker string `json:"speaker"`
		} `json:"segments"`
		Errors []struct {
			Provider string `json:"provider"`
			Message  string `json:"message"`
		} `json:"errors"`
	}

	require.NoError(t, json.Unmarshal([]byte(body), &got))
	assert.Equal(t, "elevenlabs", got.Provider)
	require.Len(t, got.Segments, 2)
	assert.Equal(t, "Xin chào.", got.Segments[0].Text)
	assert.Equal(t, "speaker_0", got.Segments[0].Speaker)
	assert.Equal(t, "speaker_1", got.Segments[1].Speaker)
	require.Len(t, got.Errors, 1)
	assert.Equal(t, "soniox", got.Errors[0].Provider)
	assert.Contains(t, got.Errors[0].Message, "answered 503")

	u := g.lastUsage(t)
	assert.Equal(t, "audio/scribe_v2", u.Model)
	assert.Equal(t, 2, u.Attempts)
	assert.Equal(t, 2500*time.Millisecond, u.Audio)
}

// Scenario: a client asks for speech with OpenAI's request. Soniox makes it
// and the bytes come back as audio/mpeg, with who made it in the headers;
// the call is recorded with the characters spoken. With Soniox off,
// ElevenLabs makes it, OpenAI's alloy as its Rachel. The voices of the
// providers that are on are listed.
func TestTextIsSpokenAndTheCharactersRecorded(t *testing.T) {
	t.Parallel()

	g := audioGateway(t, &fakeSoniox{})

	status, header, body := g.post(t, "/v1/audio/speech", g.key, `{"model":"tts-1","input":"Xin chào","voice":"alloy"}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "ID3-soniox-speech", body)
	assert.Equal(t, "audio/mpeg", header.Get("Content-Type"))
	assert.Equal(t, "soniox", header.Get("X-Hypomnema-Provider"))

	u := g.lastUsage(t)
	assert.Equal(t, "audio/tts-rt-v2", u.Model)
	assert.Equal(t, int64(8), u.Characters, "characters, not bytes")

	require.NoError(t, g.svc.SetAudioProvider(t.Context(), "soniox", false))

	status, header, body = g.post(t, "/v1/audio/speech", g.key, `{"input":"Hello","voice":"alloy"}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "ID3-elevenlabs-speech", body)
	assert.Equal(t, "elevenlabs", header.Get("X-Hypomnema-Provider"))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/audio/voices", nil)
	req.Header.Set("Authorization", "Bearer "+g.key)

	rec := httptest.NewRecorder()
	g.mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"object":"list","data":[{"id":"v1","name":"Rachel","provider":"elevenlabs","language":"en"}]}`,
		rec.Body.String())

	status, _, body = g.post(t, "/v1/audio/speech", g.key, `{"input":"Hello","response_format":"flac"}`)
	assert.Equal(t, http.StatusBadRequest, status, body)
}

// Scenario (issue #664): a key given only web search may not hear or make
// speech, nor list the voices: 403 before any provider is asked. A key that
// may use audio is served.
func TestAKeyWithoutAudioIsRefusedAudio(t *testing.T) {
	t.Parallel()

	soniox := &fakeSoniox{}
	g := audioGateway(t, soniox)

	_, searchKey, err := g.svc.AddKey(t.Context(), "searcher", nil, []modelgateway.Endpoint{modelgateway.EndpointSearch})
	require.NoError(t, err)

	status, _, body := g.transcription(t, searchKey, nil)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Contains(t, body, `"code":"endpoint_not_allowed"`)

	status, _, _ = g.post(t, "/v1/audio/speech", searchKey, `{"input":"hi"}`)
	assert.Equal(t, http.StatusForbidden, status)

	soniox.mu.Lock()
	assert.Empty(t, soniox.auth, "no provider was asked")
	soniox.mu.Unlock()

	_, audioKey, err := g.svc.AddKey(t.Context(), "listener", nil, []modelgateway.Endpoint{modelgateway.EndpointAudio})
	require.NoError(t, err)

	status, _, body = g.post(t, "/v1/audio/speech", audioKey, `{"input":"hi"}`)
	assert.Equal(t, http.StatusOK, status, body)
}

// Scenario: the owner orders text-to-speech ElevenLabs first and leaves
// speech-to-text as it was; each kind keeps its own order, and a provider
// switched off is off for both. An order that does not name each provider
// once is refused. Each kind's requests are counted with the seconds and
// characters they took.
func TestEachKindHasItsOwnOrderAndCounts(t *testing.T) {
	t.Parallel()

	g := audioGateway(t, &fakeSoniox{})
	ctx := t.Context()

	require.NoError(t, g.svc.SetAudioOrder(ctx, speech.KindTTS, []string{"elevenlabs", "soniox"}))
	require.NoError(t, g.svc.SetAudioProvider(ctx, "elevenlabs", false))

	set, err := g.svc.AudioSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"soniox", "elevenlabs"}, set.Order(speech.KindSTT))
	assert.Equal(t, []string{"elevenlabs", "soniox"}, set.Order(speech.KindTTS))
	assert.Equal(t, []string{"elevenlabs"}, set.Off)

	require.ErrorIs(t, g.svc.SetAudioOrder(ctx, speech.KindTTS, []string{"soniox"}), modelgateway.ErrInvalid)
	require.ErrorIs(t, g.svc.SetAudioOrder(ctx, "music", []string{"soniox", "elevenlabs"}), modelgateway.ErrInvalid)

	status, _, body := g.transcription(t, g.key, nil)
	require.Equal(t, http.StatusOK, status, body)

	status, _, body = g.post(t, "/v1/audio/speech", g.key, `{"input":"Hello"}`)
	require.Equal(t, http.StatusOK, status, body)

	counts, err := g.svc.AudioCounts(ctx, time.Now())
	require.NoError(t, err)
	assert.Equal(t, modelgateway.AudioTally{Answered: 1, Audio: 4200 * time.Millisecond},
		counts["soniox"][speech.KindSTT].Today)
	assert.Equal(t, modelgateway.AudioTally{Answered: 1, Characters: 5}, counts["soniox"][speech.KindTTS].Month)
}
