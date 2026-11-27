package speech

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Soniox's text-to-speech over its WebSocket, for the words' times (issue #1285, row 2), as pipecat speaks it
// (services/soniox/tts.py:449-470, 589-660) and docs.soniox.com /api-reference/tts/websocket-api says:
//
//   - one connection to wss://tts-rt.soniox.com/tts-websocket, its key sent with the connection as a bearer token;
//   - one stream: its config {stream_id, model, language, voice, audio_format, return_timestamps: true, speed?,
//     reduce_silence?, client_reference_id}, then the whole line as one text message with text_end;
//   - each answer's audio, base64, appended, and its timestamps' three arrays appended, until terminated; an
//     error_code ends the stream as that status's refusal;
//
// and its catalog: GET /v1/tts-models for the models and what each supports, GET /v1/models for the
// transcription models, GET /v1/shared-voices?model= for a model's built-in voices and GET /v1/voices for the
// account's cloned ones (docs.soniox.com /tts/concepts/voices; soniox-python api/async_voices.py,
// api/tts_models.py), each kept a day for each key as ElevenLabs's voices are.

// sonioxStreamID names the one stream of a connection.
const sonioxStreamID = "hypomnema"

type sonioxStreamAnswer struct {
	StreamID     string `json:"stream_id"`
	Audio        string `json:"audio"`
	Terminated   bool   `json:"terminated"`
	ErrorCode    int    `json:"error_code"`
	ErrorType    string `json:"error_type"`
	ErrorMessage string `json:"error_message"`
	Timestamps   *struct {
		Characters []string  `json:"characters"`
		Starts     []float64 `json:"character_start_times_seconds"`
		Ends       []float64 `json:"character_end_times_seconds"`
	} `json:"timestamps"`
}

// sonioxStream speaks text over the WebSocket once and returns its audio and its characters' times.
func (s *Service) sonioxStream(ctx context.Context, k Key, config map[string]any, text string,
) ([]byte, Alignment, error) {
	//nolint:bodyclose // The library closes the handshake's body.
	conn, resp, err := websocket.Dial(ctx, s.cfg.SonioxTTSWebSocketURL, &websocket.DialOptions{
		HTTPClient: s.client,
		HTTPHeader: http.Header{"Authorization": {"Bearer " + k.Value}, "User-Agent": {userAgent}},
	})
	if err != nil {
		if resp != nil && resp.StatusCode >= http.StatusBadRequest {
			return nil, Alignment{}, refusal(resp, nil, s.now())
		}

		return nil, Alignment{}, fmt.Errorf("no stream: %w", err)
	}
	defer conn.CloseNow() //nolint:errcheck // Closed after the stream ended or failed.

	conn.SetReadLimit(maxSpeechAnswer)

	start := map[string]any{"stream_id": sonioxStreamID, "return_timestamps": true}
	for key, v := range config {
		start[key] = v
	}

	for _, msg := range []map[string]any{start, {"stream_id": sonioxStreamID, "text": text, "text_end": true}} {
		raw, err := json.Marshal(msg)
		if err != nil {
			return nil, Alignment{}, err
		}

		if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
			return nil, Alignment{}, fmt.Errorf("sending the text: %w", err)
		}
	}

	var (
		audio     []byte
		alignment Alignment
	)

	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return nil, Alignment{}, fmt.Errorf("the stream ended before its speech: %w", err)
		}

		var answer sonioxStreamAnswer
		if err := json.Unmarshal(raw, &answer); err != nil {
			return nil, Alignment{}, fmt.Errorf("reading its answer: %w", err)
		}

		switch {
		case answer.ErrorCode != 0:
			return nil, Alignment{}, &providerError{
				status:  answer.ErrorCode,
				message: clip(strings.TrimSpace(answer.ErrorType+": "+answer.ErrorMessage), 200),
				noCredits: answer.ErrorCode == http.StatusPaymentRequired ||
					outOfCredit.MatchString(strings.ToLower(answer.ErrorMessage)),
			}
		case answer.Terminated:
			_ = conn.Close(websocket.StatusNormalClosure, "")

			return audio, alignment, nil
		}

		if answer.Audio != "" {
			chunk, err := base64.StdEncoding.DecodeString(answer.Audio)
			if err != nil {
				return nil, Alignment{}, fmt.Errorf("reading its audio: %w", err)
			}

			if len(audio)+len(chunk) > maxSpeechAnswer {
				return nil, Alignment{}, fmt.Errorf("the answer is larger than %d bytes", maxSpeechAnswer)
			}

			audio = append(audio, chunk...)
		}

		if t := answer.Timestamps; t != nil {
			alignment.Add(Alignment{Characters: t.Characters, Starts: t.Starts, Ends: t.Ends})
		}
	}
}

// sonioxTTSModel is a TTS model as Soniox's catalog gives it (soniox-python types/api.py:706-743).
type sonioxTTSModel struct {
	ID                       string `json:"id"`
	Name                     string `json:"name"`
	AliasedModelID           string `json:"aliased_model_id"`
	SupportsTimestamps       bool   `json:"supports_timestamps"`
	SupportsSilenceReduction bool   `json:"supports_silence_reduction"`
	Languages                []struct {
		Code string `json:"code"`
	} `json:"languages"`
}

// sonioxModelsRead is a key's Soniox TTS models, kept a day.
type sonioxModelsRead struct {
	models []sonioxTTSModel
	at     time.Time
}

// sonioxTTSModels lists the TTS models of Soniox's catalog with a key.
func (s *Service) sonioxTTSModels(ctx context.Context, k Key) ([]sonioxTTSModel, error) {
	s.mu.Lock()
	kept, ok := s.sonioxModels[k.Name]
	s.mu.Unlock()

	if ok && s.now().Sub(kept.at) < voicesKept {
		return kept.models, nil
	}

	var answer struct {
		Models []sonioxTTSModel `json:"models"`
	}

	if err := s.call(ctx, http.MethodGet, s.cfg.SonioxURL+"/tts-models", bearer(k), nil, &answer,
		maxSmallAnswer); err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.sonioxModels[k.Name] = sonioxModelsRead{models: answer.Models, at: s.now()}
	s.mu.Unlock()

	return answer.Models, nil
}

// sonioxCatalogTimeout bounds the catalog read a speech request makes before its first attempt, so that a catalog
// that does not answer never holds the speech past its own budget (review of #1302, round 2). Hypomnema's own
// bound: a GET of a small list.
const sonioxCatalogTimeout = 10 * time.Second

// sonioxModelOf is a TTS model as the catalog gives it; its ID is empty when the catalog could not be read in
// sonioxCatalogTimeout or does not name it, and the model is asked as if it supported what it is asked.
func (s *Service) sonioxModelOf(ctx context.Context, k Key, model string) sonioxTTSModel {
	ctx, cancel := context.WithTimeout(ctx, sonioxCatalogTimeout)
	defer cancel()

	models, err := s.sonioxTTSModels(ctx, k)
	if err != nil {
		return sonioxTTSModel{}
	}

	for _, m := range models {
		if m.ID == model {
			return m
		}
	}

	return sonioxTTSModel{}
}

// sonioxCatalog is Soniox's models for the owner's choice (issue #1285, row 1): its text-to-speech models of GET
// /v1/tts-models and its transcription models of async jobs of GET /v1/models, aliases left out.
func (s *Service) sonioxCatalog(ctx context.Context, k Key) ([]ModelInfo, error) {
	tts, err := s.sonioxTTSModels(ctx, k)
	if err != nil {
		return nil, err
	}

	out := make([]ModelInfo, 0, len(tts))

	for _, m := range tts {
		if m.AliasedModelID != "" {
			continue
		}

		info := ModelInfo{
			ID: m.ID, Name: cmpOr(m.Name, m.ID), Provider: ProviderSoniox, Kind: KindTTS, Languages: []string{},
		}
		for _, l := range m.Languages {
			info.Languages = append(info.Languages, l.Code)
		}

		out = append(out, info)
	}

	var answer struct {
		Models []struct {
			ID                string `json:"id"`
			Name              string `json:"name"`
			AliasedModelID    string `json:"aliased_model_id"`
			TranscriptionMode string `json:"transcription_mode"`
			Languages         []struct {
				Code string `json:"code"`
			} `json:"languages"`
		} `json:"models"`
	}

	if err := s.call(ctx, http.MethodGet, s.cfg.SonioxURL+"/models", bearer(k), nil, &answer,
		maxSmallAnswer); err != nil {
		return nil, err
	}

	for _, m := range answer.Models {
		if m.AliasedModelID != "" || m.TranscriptionMode != "async" {
			continue
		}

		info := ModelInfo{
			ID: m.ID, Name: cmpOr(m.Name, m.ID), Provider: ProviderSoniox, Kind: KindSTT, Languages: []string{},
		}
		for _, l := range m.Languages {
			info.Languages = append(info.Languages, l.Code)
		}

		out = append(out, info)
	}

	return out, nil
}

// sonioxVoicesOf lists the voices a key can speak a model with: the model's built-in voices, by name, then the
// account's cloned voices that are ready for it, by their name and id (issue #1285, row 5).
func (s *Service) sonioxVoicesOf(ctx context.Context, k Key, model string) ([]Voice, error) {
	cacheKey := k.Name + "\x00" + model

	s.mu.Lock()
	kept, ok := s.voices[cacheKey]
	s.mu.Unlock()

	if ok && s.now().Sub(kept.at) < voicesKept {
		return kept.voices, nil
	}

	var out []Voice

	if err := s.sonioxPages(ctx, k, "/shared-voices", url.Values{"model": {model}}, func(raw json.RawMessage) error {
		var page []struct {
			ID     string `json:"id"`
			Gender string `json:"gender"`
			Accent string `json:"accent"`
		}

		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}

		for _, v := range page {
			if v.ID != "" {
				out = append(out, Voice{ID: v.ID, Name: v.ID, Provider: ProviderSoniox})
			}
		}

		return nil
	}); err != nil {
		return nil, fmt.Errorf("listing the shared voices: %w", err)
	}

	if err := s.sonioxPages(ctx, k, "/voices", url.Values{}, func(raw json.RawMessage) error {
		var page []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Models []struct {
				Model  string `json:"model"`
				Status string `json:"status"`
			} `json:"models"`
		}

		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}

		for _, v := range page {
			ready := slices.ContainsFunc(v.Models, func(m struct {
				Model  string `json:"model"`
				Status string `json:"status"`
			},
			) bool {
				return m.Model == model && m.Status == "ready"
			})
			if v.ID != "" && ready {
				out = append(out, Voice{ID: v.ID, Name: cmpOr(v.Name, v.ID), Provider: ProviderSoniox})
			}
		}

		return nil
	}); err != nil {
		return nil, fmt.Errorf("listing the cloned voices: %w", err)
	}

	s.mu.Lock()
	s.voices[cacheKey] = voicesRead{voices: out, at: s.now()}
	s.mu.Unlock()

	return out, nil
}

// sonioxMaxPages bounds the pages of a list read.
const sonioxMaxPages = 20

// sonioxPages reads each page of a Soniox list: each page's voices are given to read, and the next page is asked
// with its cursor (soniox-python api/async_voices.py:34-56).
func (s *Service) sonioxPages(ctx context.Context, k Key, path string, query url.Values,
	read func(voices json.RawMessage) error,
) error {
	query.Set("limit", "200")

	for range sonioxMaxPages {
		var answer struct {
			Voices         json.RawMessage `json:"voices"`
			NextPageCursor string          `json:"next_page_cursor"`
		}

		if err := s.call(ctx, http.MethodGet, s.cfg.SonioxURL+path+"?"+query.Encode(), bearer(k), nil, &answer,
			maxSmallAnswer); err != nil {
			return err
		}

		if len(answer.Voices) > 0 {
			if err := read(answer.Voices); err != nil {
				return fmt.Errorf("reading its answer: %w", err)
			}
		}

		if answer.NextPageCursor == "" {
			return nil
		}

		query.Set("cursor", answer.NextPageCursor)
	}

	return errors.New("the list has more pages than are read")
}
