package modelgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
)

// Speech-to-text and text-to-speech (issue #694), in OpenAI's shapes as every
// gateway serves them (OmniRoute open-sse/handlers/audioTranscription.ts and
// audioSpeech.ts, 9router-go's media routes, LiteLLM):
//
//   - POST /v1/audio/transcriptions takes OpenAI's multipart form (file,
//     model, language, prompt, response_format, timestamp_granularities[]),
//     with speakers asked under any of OmniRoute's spellings, which makes the
//     answer verbose_json (audioTranscription.ts:121-136, 460-479);
//   - POST /v1/audio/speech takes OpenAI's JSON and answers the audio's bytes;
//   - GET /v1/audio/voices lists the voices of the providers that are on
//     (9router-go internal/handlers/router.go:132-143).
//
// A key the gateway knows may ask, whatever models it may use, unless it is
// limited to other kinds (issue #664), as for images. Each request is one
// usage row on its key, as model audio/<the provider's model>, with the
// seconds heard or the characters spoken (OmniRoute
// src/lib/usage/mediaRequestAccounting.ts:89-91). The provider that answered,
// its model and why those before it did not are said in the answer, and in
// the headers of an answer that is not JSON, as OmniRoute names them
// (src/shared/constants/headers.ts: X-OmniRoute-Provider, -Model,
// -Account-Fallback-Reasons).

// ModelAudioPrefix starts the model an audio call is recorded under.
const ModelAudioPrefix = "audio/"

const (
	// MaxAudioRequestBytes bounds a transcription's body: 9router-go's 128 MB
	// for its media routes (internal/middleware/max_body.go:18-23).
	MaxAudioRequestBytes = speech.MaxAudioBytes
	// maxSpeechRequestBytes bounds a speech request: OpenAI's 4,096 characters, in JSON.
	maxSpeechRequestBytes = 64 << 10
)

// The headers that name who answered an audio request.
const (
	headerProvider = "X-Hypomnema-Provider"
	headerModel    = "X-Hypomnema-Model"
	headerFallback = "X-Hypomnema-Fallback-Reasons"
)

// diarizeFields are OmniRoute's spellings of asking for speakers (readDiarizationFlag).
var diarizeFields = []string{"enable_speaker_diarization", "diarization", "speaker_labels", "diarize"}

var truthy = regexp.MustCompile(`(?i)^(1|true|yes|on)$`)

// audioProviderOf is the provider a model's name picks, as an image model's
// name picks its provider (images.go): provider/anything, or one of the
// provider's own model names; any other name, such as whisper-1 or tts-1,
// picks none.
func audioProviderOf(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))

	if p, _, ok := strings.Cut(model, "/"); ok && slices.Contains(speech.Providers(), p) {
		return p
	}

	switch {
	case strings.HasPrefix(model, "stt-"), strings.HasPrefix(model, "tts-"):
		return speech.ProviderSoniox
	case strings.HasPrefix(model, "scribe"), strings.HasPrefix(model, "eleven_"):
		return speech.ProviderElevenLabs
	}

	return ""
}

// transcriptionForm is a transcription request as the form gives it.
type transcriptionForm struct {
	request       speech.TranscribeRequest
	format        string
	granularities []string
}

// readTranscriptionForm reads OpenAI's form. The prompt is taken and not sent:
// neither provider has OpenAI's prompt (LiteLLM's Soniox takes language and
// response_format only).
func readTranscriptionForm(r *http.Request) (transcriptionForm, error) {
	if err := r.ParseMultipartForm(multipartMemory); err != nil {
		return transcriptionForm{}, fmt.Errorf("%w: the body is OpenAI's multipart form with the recording as file: %w",
			ErrInvalid, err)
	}

	form := r.MultipartForm
	value := func(name string) string {
		if v := form.Value[name]; len(v) > 0 {
			return strings.TrimSpace(v[0])
		}

		return ""
	}

	files := form.File["file"]
	if len(files) == 0 {
		return transcriptionForm{}, fmt.Errorf("%w: the recording is the form's file", ErrInvalid)
	}

	out := transcriptionForm{
		request: speech.TranscribeRequest{
			Audio: formAudio(files[0]), Language: value("language"),
			Provider: cmpOr(value("provider"), audioProviderOf(value("model"))),
		},
		format: strings.ToLower(value("response_format")),
	}

	for _, name := range diarizeFields {
		if truthy.MatchString(value(name)) {
			out.request.Speakers = true
		}
	}

	for _, name := range []string{"timestamp_granularities[]", "timestamp_granularities"} {
		for _, g := range form.Value[name] {
			out.granularities = append(out.granularities, strings.ToLower(strings.TrimSpace(g)))
		}
	}

	switch {
	case out.request.Speakers:
		// Speakers are only seen in the verbose answer (OmniRoute: diarization implies the richer body).
		if out.format != "srt" && out.format != "vtt" {
			out.format = "verbose_json"
		}
	case out.format == "":
		out.format = "json"
	}

	if !slices.Contains([]string{"json", "text", "verbose_json", "srt", "vtt"}, out.format) {
		return transcriptionForm{}, fmt.Errorf("%w: response_format is json, text, verbose_json, srt or vtt", ErrInvalid)
	}

	return out, nil
}

// formAudio is the form's file as a recording, opened again for each provider.
func formAudio(fh *multipart.FileHeader) speech.Audio {
	return speech.Audio{
		Name: fh.Filename, MIME: fh.Header.Get("Content-Type"), Size: fh.Size,
		Open: func() (io.ReadCloser, error) { return fh.Open() },
	}
}

type transcriptSegment struct {
	ID      int     `json:"id"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Text    string  `json:"text"`
	Speaker string  `json:"speaker,omitempty"`
}

type transcriptWord struct {
	Word    string  `json:"word"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Speaker string  `json:"speaker,omitempty"`
}

type audioError struct {
	Provider string `json:"provider"`
	Message  string `json:"message"`
}

type verboseTranscript struct {
	Task     string              `json:"task"`
	Text     string              `json:"text"`
	Language string              `json:"language,omitempty"`
	Duration float64             `json:"duration"`
	Provider string              `json:"provider"`
	Model    string              `json:"model"`
	Segments []transcriptSegment `json:"segments"`
	Words    []transcriptWord    `json:"words,omitempty"`
	Errors   []audioError        `json:"errors,omitempty"`
}

type plainTranscript struct {
	Text   string       `json:"text"`
	Errors []audioError `json:"errors,omitempty"`
}

func seconds(d time.Duration) float64 { return float64(d.Milliseconds()) / 1000 }

func audioErrors(failures []speech.Failure) []audioError {
	out := make([]audioError, 0, len(failures))
	for _, f := range failures {
		out = append(out, audioError{Provider: f.Provider, Message: f.Reason})
	}

	return out
}

// audioTranscriptions answers POST /v1/audio/transcriptions.
func (s *Server) audioTranscriptions(w http.ResponseWriter, r *http.Request) {
	k, ok := s.authenticateFor(w, r, EndpointAudio)
	if !ok {
		return
	}

	if s.speech == nil {
		writeError(w, http.StatusServiceUnavailable, typeServer, "audio_unavailable", "this gateway has no audio")

		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxAudioRequestBytes)

	in, err := readTranscriptionForm(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_request", err.Error())

		return
	}

	defer r.MultipartForm.RemoveAll() //nolint:errcheck // Temporary files; nothing to do when one stays.

	start := s.svc.clock.Now()
	t, err := s.speech.Transcribe(r.Context(), in.request)
	s.recordAudio(r.Context(), AudioCall{
		KeyID: k.ID, Kind: speech.KindSTT, Provider: t.Provider, Model: t.Model, Failures: t.Errors, Err: err,
		Audio: t.Duration, Duration: s.svc.clock.Now().Sub(start),
	})

	if err != nil {
		s.audioFailed(w, r, err)

		return
	}

	switch in.format {
	case "text", "srt", "vtt":
		answeredBy(w, t.Provider, t.Model, t.Errors)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, renderTranscript(t, in.format))
	case "verbose_json":
		writeJSON(w, http.StatusOK, verbose(t, slices.Contains(in.granularities, "word")))
	default:
		writeJSON(w, http.StatusOK, plainTranscript{Text: t.Text, Errors: audioErrors(t.Errors)})
	}
}

// verbose is OmniRoute's verbose_json (audioTranscription.ts:594-625): the
// segments, and the words when they were asked for.
func verbose(t speech.Transcript, words bool) verboseTranscript {
	out := verboseTranscript{
		Task: "transcribe", Text: t.Text, Language: t.Language, Duration: seconds(t.Duration), Provider: t.Provider,
		Model: t.Model, Segments: make([]transcriptSegment, 0, len(t.Segments)), Errors: audioErrors(t.Errors),
	}

	for i, sg := range t.Segments {
		out.Segments = append(out.Segments, transcriptSegment{
			ID: i, Start: seconds(sg.Start), End: seconds(sg.End), Text: sg.Text, Speaker: sg.Speaker,
		})
	}

	if words {
		out.Words = make([]transcriptWord, 0, len(t.Words))
		for _, wd := range t.Words {
			out.Words = append(out.Words, transcriptWord{
				Word: wd.Word, Start: seconds(wd.Start), End: seconds(wd.End), Speaker: wd.Speaker,
			})
		}
	}

	return out
}

// renderTranscript is the text, or subtitles a cue a segment, as LiteLLM
// renders SRT and WebVTT (litellm_core_utils/audio_utils/subtitle_utils.py,
// _render_srt and _render_vtt).
func renderTranscript(t speech.Transcript, format string) string {
	if format == "text" {
		return t.Text
	}

	stamp := func(d time.Duration, sep string) string {
		ms := max(d.Milliseconds(), 0)

		return fmt.Sprintf("%02d:%02d:%02d%s%03d", ms/3_600_000, ms/60_000%60, ms/1000%60, sep, ms%1000)
	}

	var b strings.Builder

	if format == "vtt" {
		b.WriteString("WEBVTT\n\n")
	}

	for i, sg := range t.Segments {
		sep := ","
		if format == "vtt" {
			sep = "."
		} else {
			fmt.Fprintf(&b, "%d\n", i+1)
		}

		fmt.Fprintf(&b, "%s --> %s\n%s\n\n", stamp(sg.Start, sep), stamp(sg.End, sep), sg.Text)
	}

	return b.String()
}

// answeredBy names who answered in the headers.
func answeredBy(w http.ResponseWriter, provider, model string, failures []speech.Failure) {
	w.Header().Set(headerProvider, provider)
	w.Header().Set(headerModel, model)

	if len(failures) > 0 {
		reasons := make([]string, 0, len(failures))
		for _, f := range failures {
			reasons = append(reasons, f.Provider+": "+strings.ReplaceAll(f.Reason, "\n", " "))
		}

		w.Header().Set(headerFallback, strings.Join(reasons, "; "))
	}
}

type speechBody struct {
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	Model          string  `json:"model"`
	ResponseFormat string  `json:"response_format"`
	Speed          float64 `json:"speed"`
	Language       string  `json:"language"`
	Provider       string  `json:"provider"`
}

// audioSpeech answers POST /v1/audio/speech with the audio's bytes.
func (s *Server) audioSpeech(w http.ResponseWriter, r *http.Request) {
	k, ok := s.authenticateFor(w, r, EndpointAudio)
	if !ok {
		return
	}

	if s.speech == nil {
		writeError(w, http.StatusServiceUnavailable, typeServer, "audio_unavailable", "this gateway has no audio")

		return
	}

	var in speechBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSpeechRequestBytes)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_request",
			`the body is OpenAI's speech request: {"input": "...", "voice": "...", "response_format": "mp3|wav|pcm|opus"}`)

		return
	}

	start := s.svc.clock.Now()
	out, err := s.speech.Speak(r.Context(), speech.SpeakRequest{
		Input: in.Input, Voice: in.Voice, Format: in.ResponseFormat, Speed: in.Speed, Language: in.Language,
		Provider: cmpOr(in.Provider, audioProviderOf(in.Model)),
	})
	s.recordAudio(r.Context(), AudioCall{
		KeyID: k.ID, Kind: speech.KindTTS, Provider: out.Provider, Model: out.Model, Failures: out.Errors, Err: err,
		Characters: int64(out.Characters), Duration: s.svc.clock.Now().Sub(start),
	})

	if err != nil {
		s.audioFailed(w, r, err)

		return
	}

	answeredBy(w, out.Provider, out.Model, out.Errors)
	w.Header().Set("Content-Type", out.MIME)
	w.Header().Set("Content-Length", strconv.Itoa(len(out.Audio)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out.Audio)
}

type voiceItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Language string `json:"language,omitempty"`
}

type voiceList struct {
	Object string       `json:"object"`
	Data   []voiceItem  `json:"data"`
	Errors []audioError `json:"errors,omitempty"`
}

// audioVoices answers GET /v1/audio/voices.
func (s *Server) audioVoices(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticateFor(w, r, EndpointAudio); !ok {
		return
	}

	if s.speech == nil {
		writeError(w, http.StatusServiceUnavailable, typeServer, "audio_unavailable", "this gateway has no audio")

		return
	}

	voices, failures, err := s.speech.Voices(r.Context())
	if err != nil {
		s.internal(w, r, "modelgateway.voices_failed", err)

		return
	}

	out := voiceList{Object: "list", Data: make([]voiceItem, 0, len(voices)), Errors: audioErrors(failures)}
	for _, v := range voices {
		out.Data = append(out.Data, voiceItem{ID: v.ID, Name: v.Name, Provider: v.Provider, Language: v.Language})
	}

	writeJSON(w, http.StatusOK, out)
}

// audioFailed answers an audio request no provider served.
func (s *Server) audioFailed(w http.ResponseWriter, r *http.Request, err error) {
	var resting *speech.RestingError

	switch {
	case r.Context().Err() != nil:
		// The client left.
	case errors.As(err, &resting):
		wait := max(1, int(resting.Until.Sub(s.svc.clock.Now()).Round(time.Second).Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(wait))
		writeError(w, http.StatusTooManyRequests, typeRateLimit, "audio_rate_limited", err.Error())
	case errors.Is(err, speech.ErrInvalid):
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_request", err.Error())
	case errors.Is(err, speech.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, typeServer, "no_audio_provider", err.Error())
	case errors.Is(err, speech.ErrFailed):
		writeError(w, http.StatusBadGateway, typeServer, "upstream_failed", err.Error())
	default:
		s.internal(w, r, "modelgateway.audio_failed", err)
	}
}

// AudioCall is one audio request, to be recorded on a key.
type AudioCall struct {
	KeyID    string
	VideoID  string
	VideoRev int
	// Kind is speech.KindSTT or speech.KindTTS.
	Kind string
	// Provider and Model answered; empty when none did.
	Provider string
	Model    string
	// Failures are the providers asked before, or all of them when none answered.
	Failures []speech.Failure
	Err      error
	// Audio is the length heard; Characters what was spoken.
	Audio      time.Duration
	Characters int64
	Duration   time.Duration
}

// RecordAudioCall records an audio request on its key as model audio/<the
// provider's model>, with the seconds heard or the characters spoken: for one
// that failed, the last provider asked and every provider's reason, nothing
// heard or spoken; a request refused before any provider was asked is not
// recorded, as an image request's is not.
func (s *Service) RecordAudioCall(ctx context.Context, c AudioCall) error {
	u := Usage{
		KeyID: c.KeyID, Provider: c.Provider, UpstreamModel: c.Model, Attempts: len(c.Failures) + 1,
		Outcome: OutcomeOK, Status: http.StatusOK, Audio: c.Audio, Characters: c.Characters, Duration: c.Duration,
		VideoID: c.VideoID, VideoRev: c.VideoRev,
	}

	var (
		failed  *speech.FailedError
		resting *speech.RestingError
	)

	switch {
	case c.Err == nil:
	case errors.Is(c.Err, speech.ErrInvalid):
		return nil
	case errors.As(c.Err, &failed) && len(failed.Failures) > 0:
		last := failed.Failures[len(failed.Failures)-1]
		u.Provider, u.UpstreamModel, u.Attempts = last.Provider, last.Model, len(failed.Failures)
		u.Outcome, u.Status, u.Error = OutcomeUpstreamError, http.StatusBadGateway, c.Err.Error()
	case errors.As(c.Err, &resting):
		u.Outcome, u.Status, u.Error = OutcomeUpstreamError, http.StatusTooManyRequests, c.Err.Error()
	case errors.Is(c.Err, context.Canceled):
		u.Outcome, u.Status, u.Error = OutcomeClientGone, 0, c.Err.Error()
	default:
		u.Outcome, u.Status, u.Error = OutcomeUpstreamError, 0, c.Err.Error()
	}

	if c.Err != nil {
		u.Audio, u.Characters = 0, 0
	}

	if u.UpstreamModel == "" {
		// None was asked: the row says of which kind.
		u.UpstreamModel = c.Kind
	}

	u.Model = ModelAudioPrefix + u.UpstreamModel

	return s.recordUsage(ctx, u)
}

// recordAudio records an audio request, whether or not the client stayed.
func (s *Server) recordAudio(ctx context.Context, c AudioCall) {
	if err := s.svc.RecordAudioCall(context.WithoutCancel(ctx), c); err != nil {
		s.logger.ErrorContext(ctx, "recording an audio call",
			slog.String("event", "modelgateway.audio_usage_failed"), slog.String("error", err.Error()))
	}
}
