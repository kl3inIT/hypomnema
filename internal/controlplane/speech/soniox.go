package speech

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Soniox's async transcription, one blocking call, as MemoryOS
// (voice/SonioxApi.java:24-42, SonioxAsync.java:66-108) and LiteLLM
// (llms/soniox/audio_transcription/handler.py) run it:
//
//	POST   /v1/files                          the recording, multipart `file`
//	POST   /v1/transcriptions                 {model, file_id, enable_speaker_diarization?, language_hints?}
//	GET    /v1/transcriptions/{id}            each second until completed, or error, up to 30 minutes
//	GET    /v1/transcriptions/{id}/transcript {text, tokens: [{text, start_ms, end_ms, speaker?, language?}]}
//	DELETE /v1/transcriptions/{id}, /v1/files/{id}   always, best effort
//
// and its TTS as OmniRoute asks it (open-sse/handlers/audioSpeech.ts:324-356):
// POST https://tts-rt.soniox.com/tts {text, model, language, voice,
// audio_format}, answered with the audio's bytes. Every call carries the key
// as a bearer token.

// sonioxDefaultVoice and sonioxDefaultLanguage are OmniRoute's (audioSpeech.ts:298-299): Soniox's TTS needs both,
// and every Soniox voice speaks every language it has.
const (
	sonioxDefaultVoice    = "Adrian"
	sonioxDefaultLanguage = "en"
)

// openAIVoices are OpenAI's stock voices, which are not Soniox's and fall
// back to its default (OmniRoute's OPENAI_STOCK_VOICES).
var openAIVoices = []string{
	"alloy", "ash", "ballad", "cedar", "coral", "echo", "fable", "marin", "nova", "onyx", "sage", "shimmer", "verse",
}

func bearer(k Key) map[string]string { return map[string]string{"Authorization": "Bearer " + k.Value} }

type sonioxToken struct {
	Text         string `json:"text"`
	StartMs      int64  `json:"start_ms"`
	EndMs        int64  `json:"end_ms"`
	Speaker      any    `json:"speaker"`
	Language     string `json:"language"`
	IsAudioEvent bool   `json:"is_audio_event"`
}

// sonioxTranscribe runs one transcription with a key.
func (s *Service) sonioxTranscribe(ctx context.Context, k Key, req TranscribeRequest) (Transcript, error) {
	base := s.cfg.SonioxURL

	raw, err := s.postForm(ctx, base+"/files", bearer(k), req.Audio, nil, maxSmallAnswer)
	if err != nil {
		return Transcript{}, fmt.Errorf("uploading the recording: %w", err)
	}

	var created struct {
		ID string `json:"id"`
	}

	if json.Unmarshal(raw, &created) != nil || created.ID == "" {
		return Transcript{}, errors.New("the upload was answered with no file id")
	}

	file := created.ID

	// Cleanup is best effort and outlives a caller that left: a failed delete must not replace the outcome
	// (MemoryOS cleanUp; LiteLLM's SONIOX_DEFAULT_CLEANUP of the file and the transcription).
	defer s.sonioxDelete(ctx, k, "/files/"+url.PathEscape(file))

	job := map[string]any{"model": SonioxSTTModel, "file_id": file}
	if req.Speakers {
		job["enable_speaker_diarization"] = true
	}

	if req.Language != "" {
		job["language_hints"] = []string{req.Language}
		job["language_hints_strict"] = true
	} else {
		// With no hint the language is found and told on each token (OmniRoute's job body).
		job["enable_language_identification"] = true
	}

	created.ID = ""
	if err := s.call(ctx, http.MethodPost, base+"/transcriptions", bearer(k), job, &created,
		maxSmallAnswer); err != nil {
		return Transcript{}, fmt.Errorf("creating the transcription: %w", err)
	}

	if created.ID == "" {
		return Transcript{}, errors.New("the transcription was answered with no id")
	}

	transcription := base + "/transcriptions/" + url.PathEscape(created.ID)

	defer s.sonioxDelete(ctx, k, "/transcriptions/"+url.PathEscape(created.ID))

	duration, err := s.sonioxWait(ctx, k, transcription)
	if err != nil {
		return Transcript{}, err
	}

	var transcript struct {
		Text   string        `json:"text"`
		Tokens []sonioxToken `json:"tokens"`
	}

	if err := s.call(ctx, http.MethodGet, transcription+"/transcript", bearer(k), nil, &transcript,
		maxTranscriptAnswer); err != nil {
		return Transcript{}, fmt.Errorf("reading the transcript: %w", err)
	}

	return sonioxTranscript(transcript.Text, transcript.Tokens, duration, req.Language), nil
}

// sonioxWait polls a transcription until it is completed and returns the
// recording's length (LiteLLM reads audio_duration_ms there).
func (s *Service) sonioxWait(ctx context.Context, k Key, transcription string) (time.Duration, error) {
	deadline := s.now().Add(s.cfg.PollFor)

	for {
		var status struct {
			Status          string `json:"status"`
			ErrorMessage    string `json:"error_message"`
			ErrorType       string `json:"error_type"`
			AudioDurationMs int64  `json:"audio_duration_ms"`
		}

		if err := s.call(ctx, http.MethodGet, transcription, bearer(k), nil, &status, maxSmallAnswer); err != nil {
			return 0, fmt.Errorf("asking after the transcription: %w", err)
		}

		switch status.Status {
		case "completed":
			return time.Duration(status.AudioDurationMs) * time.Millisecond, nil
		case "error", "failed":
			return 0, fmt.Errorf("the transcription failed: %s", clip(cmpOr(status.ErrorMessage, status.ErrorType,
				"no reason given"), 200))
		}

		if !s.now().Before(deadline) {
			return 0, fmt.Errorf("the transcription was not done within %s", s.cfg.PollFor)
		}

		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(s.cfg.PollInterval):
		}
	}
}

func cmpOr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}

// sonioxDelete removes a file or a transcription, within its own short time, even after the caller left.
func (s *Service) sonioxDelete(ctx context.Context, k Key, path string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	if err := s.call(ctx, http.MethodDelete, s.cfg.SonioxURL+path, bearer(k), nil, nil, maxSmallAnswer); err != nil {
		s.cfg.Logger.WarnContext(ctx, "removing a Soniox file or transcription",
			slog.String("event", "speech.soniox_cleanup_failed"), slog.String("path", path),
			slog.String("error", err.Error()))
	}
}

// sonioxTranscript reads the tokens: the text as Soniox gives it, else the
// tokens joined (OmniRoute); a word a token (OmniRoute, LiteLLM); the
// language when one is named on the tokens, else the hint given.
func sonioxTranscript(text string, raw []sonioxToken, duration time.Duration, hint string) Transcript {
	tokens := make([]token, 0, len(raw))
	words := make([]Word, 0, len(raw))

	var (
		joined   strings.Builder
		language = hint
	)

	for _, t := range raw {
		tk := token{
			text: t.Text, start: time.Duration(t.StartMs) * time.Millisecond,
			end: time.Duration(t.EndMs) * time.Millisecond, speaker: speakerOf(t.Speaker), event: t.IsAudioEvent,
		}
		tokens = append(tokens, tk)
		joined.WriteString(t.Text)

		if language == "" && t.Language != "" {
			language = t.Language
		}

		if t.Text == "" || t.Text == "<fin>" || t.Text == "<end>" || t.IsAudioEvent {
			continue
		}

		words = append(words, Word{Word: t.Text, Start: tk.start, End: tk.end, Speaker: tk.speaker})
	}

	if text == "" {
		text = joined.String()
	}

	if duration <= 0 {
		duration = lastEnd(tokens)
	}

	return Transcript{
		Text: strings.TrimSpace(text), Language: language, Duration: duration, Segments: segments(tokens),
		Words: words,
	}
}

// speakerOf is a speaker label, a string or a number as the provider gives it.
func speakerOf(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	default:
		return ""
	}
}

// sonioxSpeak makes speech with a key: OpenAI's pcm is Soniox's pcm_s16le.
func (s *Service) sonioxSpeak(ctx context.Context, k Key, req SpeakRequest) ([]byte, error) {
	format := req.Format
	if format == "pcm" {
		format = "pcm_s16le"
	}

	voice := strings.TrimSpace(req.Voice)
	if voice == "" || slices.Contains(openAIVoices, strings.ToLower(voice)) {
		voice = sonioxDefaultVoice
	}

	language := strings.TrimSpace(req.Language)
	if language == "" {
		language = sonioxDefaultLanguage
	}

	body := map[string]any{
		"text": req.Input, "model": SonioxTTSModel, "language": language, "voice": voice, "audio_format": format,
	}

	return s.speak(ctx, s.cfg.SonioxTTSURL, bearer(k), body)
}

// speak posts a JSON body and returns the audio a success carries.
func (s *Service) speak(ctx context.Context, endpoint string, headers map[string]string, body any) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(encoded)))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/*")

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	return s.send(req, maxSpeechAnswer)
}
