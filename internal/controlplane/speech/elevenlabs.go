package speech

import (
	"context"
	"encoding/json"
	"fmt"
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
//     {text, model_id, voice_settings: {speed}}, answered with the audio's
//     bytes, with LiteLLM's formats, OpenAI voice names and speed
//     (llms/elevenlabs/text_to_speech/transformation.py:43-62, 139-152);
//   - its voices are GET /v1/voices, kept a day for each key (9router-go
//     internal/handlers/media/voices.go:287-340).

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

// elevenLabsSpeak makes speech with a key.
func (s *Service) elevenLabsSpeak(ctx context.Context, k Key, req SpeakRequest) ([]byte, error) {
	body := map[string]any{"text": req.Input, "model_id": ElevenLabsTTSModel}
	if req.Speed > 0 {
		body["voice_settings"] = map[string]any{"speed": req.Speed}
	}

	endpoint := s.cfg.ElevenLabsURL + "/v1/text-to-speech/" + url.PathEscape(elevenLabsVoice(req.Voice)) +
		"?output_format=" + url.QueryEscape(elevenLabsFormats[req.Format])

	return s.speak(ctx, endpoint, xiKey(k), body)
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

	if err := s.call(ctx, http.MethodGet, s.cfg.ElevenLabsURL+"/v1/voices", xiKey(k), nil, &answer,
		maxSmallAnswer); err != nil {
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
