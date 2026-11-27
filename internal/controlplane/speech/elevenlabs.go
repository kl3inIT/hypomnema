package speech

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// ElevenLabs, with the key in `xi-api-key` (OmniRoute's audioRegistry, 9router-go, Bifrost):
//
//   - speech-to-text is Scribe, a multipart POST /v1/speech-to-text with
//     model_id, file, language_code when one is given, diarize when speakers
//     are asked, and word timestamps, answered {language_code, text, words:
//     [{text, start, end, type, speaker_id}]}, as 9router-go builds it
//     (internal/handlers/media/elevenlabs_stt.go:145-187) and Bifrost reads it
//     (core/providers/elevenlabs/transcription.go:221: speaker_id is the
//     word's speaker, a blank spacing is no word);
//   - text-to-speech is POST /v1/text-to-speech/{voice_id}?output_format=…
//     {text, model_id, language_code, voice_settings: {speed}}, answered with
//     the audio's bytes, with LiteLLM's formats, OpenAI voice names and speed
//     (llms/elevenlabs/text_to_speech/transformation.py:43-62, 139-152); the
//     model is the owner's choice, eleven_v4 by default, and a video's lines
//     go to /with-timestamps for the words' times (issue #1283);
//   - its voices are GET /v1/voices, the account's own and the library voices
//     it saved, and its models GET /v1/models, each kept a day for each key
//     (9router-go internal/handlers/media/voices.go:287-340; openclaw
//     extensions/elevenlabs :421-465; open-webui routers/audio/__init__.py).

// elevenLabsFormats are LiteLLM's FORMAT_MAPPINGS of OpenAI's formats.
var elevenLabsFormats = map[string]string{
	"mp3": "mp3_44100_128", "pcm": "pcm_24000", "opus": "opus_48000_128", "wav": "wav_24000",
}

// elevenLabsVoices are LiteLLM's VOICE_MAPPINGS of OpenAI's voice names to
// ElevenLabs's voices; a request that names none gets Rachel, OmniRoute's
// default (audioSpeech.ts, resolveElevenLabsVoiceId).
var elevenLabsVoices = map[string]string{
	"alloy": "21m00Tcm4TlvDq8ikWAM", "amber": "5Q0t7uMcjvnagumLfvZi", "ash": "AZnzlk1XvdvUeBnXmlld",
	"august": "D38z5RcWu1voky8WS1ja", "blue": "2EiwWnXFnvU5JabPnv8n", "coral": "9BWtsMINqrJLrRacOk9x",
	"lily": "EXAVITQu4vr4xnSDxMaL", "onyx": "29vD33N1CtxCmqQRPOHJ", "sage": "CwhRBWXzGAHq8TQ4Fs17",
	"verse": "CYw3kZ02Hs0563khs1Fj",
}

const elevenLabsDefaultVoice = "21m00Tcm4TlvDq8ikWAM"

func xiKey(k Key) map[string]string { return map[string]string{"xi-api-key": k.Value} }

type elevenLabsWord struct {
	Text      string   `json:"text"`
	Start     *float64 `json:"start"`
	End       *float64 `json:"end"`
	Type      string   `json:"type"`
	SpeakerID string   `json:"speaker_id"`
}

// elevenLabsTranscribe runs one Scribe transcription with a key.
func (s *Service) elevenLabsTranscribe(ctx context.Context, k Key, req TranscribeRequest) (Transcript, error) {
	fields := []field{{"model_id", ElevenLabsSTTModel}, {"timestamps_granularity", "word"}}
	if req.Language != "" {
		fields = append(fields, field{"language_code", req.Language})
	}

	if req.Speakers {
		fields = append(fields, field{"diarize", "true"})
	}

	raw, err := s.postForm(ctx, s.cfg.ElevenLabsURL+"/v1/speech-to-text", xiKey(k), req.Audio, fields,
		maxTranscriptAnswer)
	if err != nil {
		return Transcript{}, err
	}

	var answer struct {
		LanguageCode string           `json:"language_code"`
		Text         string           `json:"text"`
		Words        []elevenLabsWord `json:"words"`
	}

	if err := json.Unmarshal(raw, &answer); err != nil {
		return Transcript{}, fmt.Errorf("reading its answer: %w", err)
	}

	return elevenLabsTranscript(answer.Text, answer.LanguageCode, answer.Words), nil
}

// elevenLabsTranscript reads Scribe's words: a word is a word, a spacing
// gives the next word its leading space, a sound is no speech.
func elevenLabsTranscript(text, language string, raw []elevenLabsWord) Transcript {
	seconds := func(v *float64) time.Duration {
		if v == nil {
			return 0
		}

		return time.Duration(*v * float64(time.Second))
	}

	var (
		tokens []token
		words  []Word
		space  bool
	)

	for _, w := range raw {
		switch w.Type {
		case "spacing":
			space = true

			continue
		case "audio_event":
			tokens = append(tokens, token{text: w.Text, start: seconds(w.Start), end: seconds(w.End), event: true})

			continue
		}

		said := w.Text
		if space && len(tokens) > 0 {
			said = " " + said
		}

		space = false
		tk := token{text: said, start: seconds(w.Start), end: seconds(w.End), speaker: w.SpeakerID}
		tokens = append(tokens, tk)
		words = append(words, Word{Word: w.Text, Start: tk.start, End: tk.end, Speaker: tk.speaker})
	}

	return Transcript{
		Text: strings.TrimSpace(text), Language: language, Duration: lastEnd(tokens), Segments: segments(tokens),
		Words: words,
	}
}

// elevenLabsVoice is the voice id a request names: an OpenAI name mapped, an id as it is, none Rachel.
func elevenLabsVoice(voice string) string {
	voice = strings.TrimSpace(voice)
	if voice == "" {
		return elevenLabsDefaultVoice
	}

	if id, ok := elevenLabsVoices[strings.ToLower(voice)]; ok {
		return id
	}

	return voice
}

// elevenLabsSpeak makes speech with a key and a model: the audio, and when timings are asked and ElevenLabs sends
// its alignment, the words spoken with their times. language_code is sent when the request names a language its
// model covers (elevenLabsLanguage); voice_settings carry only the speed asked, as pipecat and the SDK send only
// what is set (issue #1283, rows 2 and 4). A rate limit or a failure on its side is asked again (retry.go).
func (s *Service) elevenLabsSpeak(ctx context.Context, k Key, model string, req SpeakRequest) ([]byte, []Word, error) {
	body := map[string]any{"text": req.Input, "model_id": model}
	if req.Speed > 0 {
		body["voice_settings"] = map[string]any{"speed": req.Speed}
	}

	if language := s.elevenLabsLanguage(ctx, k, model, req.Language); language != "" {
		body["language_code"] = language
	}

	endpoint := s.cfg.ElevenLabsURL + "/v1/text-to-speech/" + url.PathEscape(elevenLabsVoice(req.Voice))
	format := "?output_format=" + url.QueryEscape(elevenLabsFormats[req.Format])

	if !req.Timings {
		var audio []byte

		err := s.retrying(ctx, func(ctx context.Context) error {
			var err error
			audio, err = s.speak(ctx, endpoint+format, xiKey(k), body)

			return err
		})

		return audio, nil, err
	}

	// /with-timestamps answers the audio, base64, with the characters and when each is spoken (agent-demo-video
	// src/tts.ts:104-131; pipecat tts.py:1060-1070). alignment is read, not normalized_alignment, which drops
	// Vietnamese diacritics on some models (pipecat tts_base.py:371-400; measured on eleven_flash_v2_5 in #1283).
	var answer struct {
		Audio     string               `json:"audio_base64"`
		Alignment *elevenLabsAlignment `json:"alignment"`
	}

	if err := s.retrying(ctx, func(ctx context.Context) error {
		return s.call(ctx, http.MethodPost, endpoint+"/with-timestamps"+format, xiKey(k), body, &answer,
			maxSpeechAnswer)
	}); err != nil {
		return nil, nil, err
	}

	audio, err := base64.StdEncoding.DecodeString(answer.Audio)
	if err != nil {
		return nil, nil, fmt.Errorf("reading its audio: %w", err)
	}

	var words []Word
	if a := answer.Alignment; a != nil {
		words = alignmentWords(a.Characters, a.Starts, a.Ends)
	}

	return audio, words, nil
}

// elevenLabsAlignment is when each character of the text is spoken, in seconds.
type elevenLabsAlignment struct {
	Characters []string  `json:"characters"`
	Starts     []float64 `json:"character_start_times_seconds"`
	Ends       []float64 `json:"character_end_times_seconds"`
}

// alignmentWords builds the words spoken from ElevenLabs's alignment, as agent-demo-video's alignmentWords does
// (src/captions.ts:46-65): the characters are split on whitespace, and a word starts when its first character does
// and ends when its last does. An audio tag in brackets, such as [laughs], is performed, not said, so it is no word
// and splits the words beside it, as ElevenLabs's stripAudioTags drops it (changelog 2026-10-26); pipecat keeps a
// tag out of its words too (tts_base.py, _ends_in_audio_tag).
func alignmentWords(chars []string, starts, ends []float64) []Word {
	n := min(len(chars), len(starts), len(ends))

	var (
		out   []Word
		text  strings.Builder
		start float64
		end   float64
		inTag bool
	)

	flush := func() {
		if text.Len() > 0 {
			out = append(out, Word{Word: text.String(), Start: seconds(start), End: seconds(max(start, end))})
		}

		text.Reset()
	}

	for i := range n {
		ch := chars[i]

		switch {
		case inTag:
			inTag = !strings.Contains(ch, "]")

			continue
		case strings.HasPrefix(ch, "["):
			flush()

			inTag = !strings.Contains(ch, "]")

			continue
		case strings.TrimSpace(ch) == "":
			flush()

			continue
		}

		if text.Len() == 0 {
			start = starts[i]
		}

		text.WriteString(ch)
		end = ends[i]
	}

	flush()

	return out
}

func seconds(v float64) time.Duration { return time.Duration(v * float64(time.Second)) }

// elevenLabsLanguage is the language_code a request is sent with: its language as an ISO 639-1 code (en-US is en),
// when the model covers it, as the models list says (GET /v1/models), and none otherwise, with a warning, as
// pipecat's elevenlabs_language_code does (tts_base.py:194-225). A model known to take no language_code gets none,
// whatever it covers: those older than v2.5 that pipecat's table leaves out on purpose (tts_base.py:176-191:
// "ElevenLabs documents language_code as unsupported" for multilingual v2). When the list cannot be read, or does
// not name the model, the language is sent and ElevenLabs judges it: a language asked is not dropped on a guess.
func (s *Service) elevenLabsLanguage(ctx context.Context, k Key, model, language string) string {
	language = baseLanguage(language)

	switch {
	case language == "":
		return ""
	case slices.Contains(elevenLabsNoLanguageCode, model):
		s.cfg.Logger.WarnContext(ctx, "the language is not sent: the ElevenLabs model takes no language_code",
			slog.String("event", "speech.language_not_taken"), slog.String("model", model),
			slog.String("language", language))

		return ""
	}

	models, err := s.elevenLabsModelsOf(ctx, k)
	if err != nil {
		s.cfg.Logger.WarnContext(ctx, "the ElevenLabs models could not be read; the language is sent unchecked",
			slog.String("event", "speech.elevenlabs_models_failed"), slog.String("key", k.Name),
			slog.String("error", err.Error()))

		return language
	}

	i := slices.IndexFunc(models, func(m ModelInfo) bool { return m.ID == model })
	if i < 0 {
		return language
	}

	if slices.ContainsFunc(models[i].Languages, func(l string) bool { return baseLanguage(l) == language }) {
		return language
	}

	s.cfg.Logger.WarnContext(ctx, "the language is not sent: the ElevenLabs model does not cover it",
		slog.String("event", "speech.language_not_covered"), slog.String("model", model),
		slog.String("language", language), slog.Int("languages", len(models[i].Languages)))

	return ""
}

// elevenLabsNoLanguageCode are the models that take no language_code: ElevenLabs's before v2.5, which pipecat's
// ELEVENLABS_MODEL_LANGUAGES leaves out so that none is sent (tts_base.py:176-191).
var elevenLabsNoLanguageCode = []string{
	"eleven_multilingual_v2", "eleven_multilingual_v1", "eleven_monolingual_v1", "eleven_turbo_v2", "eleven_flash_v2",
}

// baseLanguage is a language code without its region, lowercase: en-US and en_us are en, as ISO 639-1 names it.
func baseLanguage(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if i := strings.IndexAny(code, "-_"); i >= 0 {
		return code[:i]
	}

	return code
}

// ModelInfo is a provider's text-to-speech model, as its list names it.
type ModelInfo struct {
	ID       string
	Name     string
	Provider string
	// Languages are the ISO codes it covers, as the provider lists them.
	Languages []string
}

// modelsRead is a key's models, kept a day, as its voices are.
type modelsRead struct {
	models []ModelInfo
	at     time.Time
}

// elevenLabsModelsOf lists the text-to-speech models a key may use, with the languages each covers: GET
// /v1/models, as open-webui lists them for its setting (routers/audio/__init__.py:1588-1601), kept a day.
func (s *Service) elevenLabsModelsOf(ctx context.Context, k Key) ([]ModelInfo, error) {
	s.mu.Lock()
	kept, ok := s.models[k.Name]
	s.mu.Unlock()

	if ok && s.now().Sub(kept.at) < voicesKept {
		return kept.models, nil
	}

	var answer []struct {
		ModelID           string `json:"model_id"`
		Name              string `json:"name"`
		CanDoTextToSpeech bool   `json:"can_do_text_to_speech"`
		Languages         []struct {
			LanguageID string `json:"language_id"`
		} `json:"languages"`
	}

	if err := s.retrying(ctx, func(ctx context.Context) error {
		return s.call(ctx, http.MethodGet, s.cfg.ElevenLabsURL+"/v1/models", xiKey(k), nil, &answer, maxSmallAnswer)
	}); err != nil {
		return nil, err
	}

	out := make([]ModelInfo, 0, len(answer))
	for _, m := range answer {
		if m.ModelID == "" || !m.CanDoTextToSpeech {
			continue
		}

		info := ModelInfo{ID: m.ModelID, Name: m.Name, Provider: ProviderElevenLabs, Languages: []string{}}
		for _, l := range m.Languages {
			if l.LanguageID != "" {
				info.Languages = append(info.Languages, l.LanguageID)
			}
		}

		out = append(out, info)
	}

	s.mu.Lock()
	s.models[k.Name] = modelsRead{models: out, at: s.now()}
	s.mu.Unlock()

	return out, nil
}

// Models lists the text-to-speech models the owner may choose from, for Settings → Audio: ElevenLabs's, when it
// is on and has a key, read with the first key that answers. Soniox has one model, so no list. A list that cannot
// be read is left out, and its reason returned.
func (s *Service) Models(ctx context.Context) ([]ModelInfo, []Failure, error) {
	set, err := s.settings(ctx)
	if err != nil {
		return nil, nil, err
	}

	keys, err := s.keys(ctx)
	if err != nil {
		return nil, nil, err
	}

	if slices.Contains(set.Off, ProviderElevenLabs) {
		return []ModelInfo{}, nil, nil
	}

	var failures []Failure

	for _, k := range s.leastRecentlyUsed(keys[ProviderElevenLabs]) {
		models, err := s.elevenLabsModelsOf(ctx, k)
		if err != nil {
			failures = append(failures, Failure{
				Provider: ProviderElevenLabs, Reason: "key " + k.Name + ": " + reasonOf(err),
			})

			continue
		}

		return models, failures, nil
	}

	return []ModelInfo{}, failures, nil
}

// Voice is a voice speech may be made with.
type Voice struct {
	ID       string
	Name     string
	Provider string
	// Language is the voice's language as its provider labels it; empty when it does not.
	Language string
}

// voicesRead is a key's voices, kept a day (9router-go's voicesCacheTTL).
type voicesRead struct {
	voices []Voice
	at     time.Time
}

const voicesKept = 24 * time.Hour

// elevenLabsVoicesOf lists the voices a key's account has.
func (s *Service) elevenLabsVoicesOf(ctx context.Context, k Key) ([]Voice, error) {
	s.mu.Lock()
	kept, ok := s.voices[k.Name]
	s.mu.Unlock()

	if ok && s.now().Sub(kept.at) < voicesKept {
		return kept.voices, nil
	}

	var answer struct {
		Voices []struct {
			VoiceID string            `json:"voice_id"`
			Name    string            `json:"name"`
			Labels  map[string]string `json:"labels"`
		} `json:"voices"`
	}

	if err := s.retrying(ctx, func(ctx context.Context) error {
		return s.call(ctx, http.MethodGet, s.cfg.ElevenLabsURL+"/v1/voices", xiKey(k), nil, &answer, maxSmallAnswer)
	}); err != nil {
		return nil, err
	}

	out := make([]Voice, 0, len(answer.Voices))
	for _, v := range answer.Voices {
		if v.VoiceID != "" {
			out = append(out, Voice{
				ID: v.VoiceID, Name: v.Name, Provider: ProviderElevenLabs, Language: v.Labels["language"],
			})
		}
	}

	s.mu.Lock()
	s.voices[k.Name] = voicesRead{voices: out, at: s.now()}
	s.mu.Unlock()

	return out, nil
}

// Voices lists the voices of the text-to-speech providers that are on and
// have a key, in the owner's order: 9router's GET /v1/audio/voices
// (9router-go internal/handlers/router.go:132-143). Soniox has no list in
// the references; its one voice they name, OmniRoute's default, is given. A
// provider whose list cannot be read is left out, and its reason returned.
func (s *Service) Voices(ctx context.Context) ([]Voice, []Failure, error) {
	set, err := s.settings(ctx)
	if err != nil {
		return nil, nil, err
	}

	keys, err := s.keys(ctx)
	if err != nil {
		return nil, nil, err
	}

	var (
		out      []Voice
		failures []Failure
	)

	for _, p := range set.Order(KindTTS) {
		if slices.Contains(set.Off, p) || len(keys[p]) == 0 {
			continue
		}

		if p == ProviderSoniox {
			out = append(out, Voice{ID: sonioxDefaultVoice, Name: sonioxDefaultVoice, Provider: ProviderSoniox})

			continue
		}

		for _, k := range s.leastRecentlyUsed(keys[p]) {
			voices, err := s.elevenLabsVoicesOf(ctx, k)
			if err != nil {
				failures = append(failures, Failure{Provider: p, Reason: "key " + k.Name + ": " + reasonOf(err)})

				continue
			}

			out = append(out, voices...)

			break
		}

	}

	return out, failures, nil
}
