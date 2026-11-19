package clientapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
)

// Audio through the owner's Soniox and ElevenLabs keys (issue #694): a Bot
// and the owner turn a recording into text and text into speech with
// `hypomnema audio transcribe|speak`, as they make an image with `hypomnema
// image` (images.go), the call recorded on the chat's gateway key; and the
// Audio page of Settings → Gateway lists the providers with their switches
// and keys, as the Web search page does (web_search.go), and the order each
// kind asks them in with its model and usage, as the Images page does. A
// key's value is never answered.

// Audio hears and makes speech. chatID is the chat of the Bot's pi that asks,
// empty for another token: the control plane's wiring records the call on
// that chat's gateway key.
type Audio interface {
	Transcribe(ctx context.Context, chatID string, req speech.TranscribeRequest) (speech.Transcript, error)
	Speak(ctx context.Context, chatID string, req speech.SpeakRequest) (speech.Speech, error)
}

// AudioSettings is what the Audio page shows and changes.
type AudioSettings interface {
	Chain(ctx context.Context) ([]speech.Link, speech.Settings, error)
	AudioCounts(ctx context.Context, now time.Time) (map[string]map[string]modelgateway.AudioCount, error)
	SetAudioProvider(ctx context.Context, provider string, on bool) error
	SetAudioOrder(ctx context.Context, kind string, providers []string) error
	SetAudioVoice(ctx context.Context, provider, voice string) error
}

// WithAudio gives the API the gateway's audio and its settings.
func (s *Server) WithAudio(audio Audio, settings AudioSettings) *Server {
	s.audio, s.audioSettings = audio, settings

	return s
}

var errNoAudio = fmt.Errorf("%w: this control plane has no audio", orchestration.ErrUnavailable)

// maxTranscribeBody is a transcription's JSON: the recording, base64, and its fields.
const maxTranscribeBody = speech.MaxAudioBytes/3*4 + 1<<20

// TranscribeAudio turns a recording into text.
func (s *Server) TranscribeAudio(ctx context.Context, req openapi.TranscribeAudioRequestObject,
) (openapi.TranscribeAudioResponseObject, error) {
	if s.audio == nil {
		return nil, errNoAudio
	}

	b := req.Body
	data := b.Audio

	kind := val(b.MimeType)
	if kind == "" {
		kind = http.DetectContentType(data)
	}

	in := speech.TranscribeRequest{
		Audio: speech.Audio{
			Name: val(b.FileName), MIME: kind, Size: int64(len(data)),
			Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
		},
		Language: val(b.Language), Speakers: val(b.Speakers),
	}

	if b.Provider != nil {
		in.Provider = string(*b.Provider)
	}

	t, err := s.audio.Transcribe(ctx, chatOfCaller(ctx), in)
	if err != nil {
		return nil, err
	}

	out := openapi.TranscribeAudio200JSONResponse{
		Provider: openapi.AudioProvider(t.Provider), Model: t.Model, Text: t.Text,
		Duration: float32(t.Duration.Seconds()), Segments: make([]openapi.TranscriptSegment, 0, len(t.Segments)),
		Words: make([]openapi.TranscriptWord, 0, len(t.Words)), Errors: audioErrors(t.Errors),
	}

	if t.Language != "" {
		out.Language = ptr(t.Language)
	}

	for _, sg := range t.Segments {
		item := openapi.TranscriptSegment{
			Start: float32(sg.Start.Seconds()), End: float32(sg.End.Seconds()), Text: sg.Text,
		}
		if sg.Speaker != "" {
			item.Speaker = ptr(sg.Speaker)
		}

		out.Segments = append(out.Segments, item)
	}

	for _, w := range t.Words {
		item := openapi.TranscriptWord{Word: w.Word, Start: float32(w.Start.Seconds()), End: float32(w.End.Seconds())}
		if w.Speaker != "" {
			item.Speaker = ptr(w.Speaker)
		}

		out.Words = append(out.Words, item)
	}

	return out, nil
}

// SpeakAudio turns text into speech.
func (s *Server) SpeakAudio(ctx context.Context, req openapi.SpeakAudioRequestObject,
) (openapi.SpeakAudioResponseObject, error) {
	if s.audio == nil {
		return nil, errNoAudio
	}

	b := req.Body
	in := speech.SpeakRequest{Input: b.Input, Voice: val(b.Voice), Language: val(b.Language)}

	if b.Format != nil {
		in.Format = string(*b.Format)
	}

	if b.Speed != nil {
		in.Speed = float64(*b.Speed)
	}

	if b.Provider != nil {
		in.Provider = string(*b.Provider)
	}

	out, err := s.audio.Speak(ctx, chatOfCaller(ctx), in)
	if err != nil {
		return nil, err
	}

	return openapi.SpeakAudio200JSONResponse{
		Provider: openapi.AudioProvider(out.Provider), Model: out.Model, Format: out.Format, MimeType: out.MIME,
		Audio: out.Audio, Characters: out.Characters, Errors: audioErrors(out.Errors),
	}, nil
}

func audioErrors(failures []speech.Failure) *[]openapi.WebSearchError {
	if len(failures) == 0 {
		return nil
	}

	out := make([]openapi.WebSearchError, 0, len(failures))
	for _, f := range failures {
		out = append(out, openapi.WebSearchError{Provider: f.Provider, Message: f.Reason})
	}

	return &out
}

// GetAudio answers the audio providers.
func (s *Server) GetAudio(ctx context.Context, _ openapi.GetAudioRequestObject,
) (openapi.GetAudioResponseObject, error) {
	out, err := s.audioProviders(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.GetAudio200JSONResponse(out), nil
}

// SetAudioProvider switches a provider on or off and answers the providers.
func (s *Server) SetAudioProvider(ctx context.Context, req openapi.SetAudioProviderRequestObject,
) (openapi.SetAudioProviderResponseObject, error) {
	if s.audioSettings == nil {
		return nil, errNoAudio
	}

	if err := s.audioSettings.SetAudioProvider(ctx, string(req.Provider), req.Body.On); err != nil {
		return nil, gatewayKeyError(err)
	}

	out, err := s.audioProviders(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.SetAudioProvider200JSONResponse(out), nil
}

// SetAudioOrder keeps a kind's order and answers the providers.
func (s *Server) SetAudioOrder(ctx context.Context, req openapi.SetAudioOrderRequestObject,
) (openapi.SetAudioOrderResponseObject, error) {
	if s.audioSettings == nil {
		return nil, errNoAudio
	}

	order := make([]string, 0, len(req.Body.Providers))
	for _, p := range req.Body.Providers {
		order = append(order, string(p))
	}

	if err := s.audioSettings.SetAudioOrder(ctx, string(req.Kind), order); err != nil {
		return nil, gatewayKeyError(err)
	}

	out, err := s.audioProviders(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.SetAudioOrder200JSONResponse(out), nil
}

// SetAudioVoice keeps the voice a provider speaks a video with and answers the providers.
func (s *Server) SetAudioVoice(ctx context.Context, req openapi.SetAudioVoiceRequestObject,
) (openapi.SetAudioVoiceResponseObject, error) {
	if s.audioSettings == nil {
		return nil, errNoAudio
	}

	if err := s.audioSettings.SetAudioVoice(ctx, string(req.Provider), req.Body.Voice); err != nil {
		return nil, gatewayKeyError(err)
	}

	out, err := s.audioProviders(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.SetAudioVoice200JSONResponse(out), nil
}

// AddAudioKey keeps one more key of a provider in the vault, under the lowest
// free name. The key is not logged and is not in the answer.
func (s *Server) AddAudioKey(ctx context.Context, req openapi.AddAudioKeyRequestObject,
) (openapi.AddAudioKeyResponseObject, error) {
	key := strings.TrimSpace(val(req.Body.Key))

	switch {
	case s.audioSettings == nil:
		return nil, errNoAudio
	case s.secrets == nil:
		return nil, errNoSecrets
	case key == "":
		return nil, fmt.Errorf("%w: the key is empty", orchestration.ErrInvalid)
	}

	secrets, err := s.secrets.List(ctx)
	if err != nil {
		return nil, err
	}

	taken := make([]string, 0, len(secrets))
	for _, i := range secrets {
		taken = append(taken, i.Name)
	}

	name := speech.NextKeyName(string(req.Provider), taken)
	if _, err := s.secrets.Put(ctx, name, map[string]string{"value": key}); err != nil {
		return nil, err
	}

	out, err := s.audioProviders(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.AddAudioKey201JSONResponse{Name: name, Audio: out}, nil
}

// RemoveAudioKey removes one of a provider's keys; a key that is not there is removed already.
func (s *Server) RemoveAudioKey(ctx context.Context, req openapi.RemoveAudioKeyRequestObject,
) (openapi.RemoveAudioKeyResponseObject, error) {
	switch provider, _, ok := speech.ParseKeyName(req.Name); {
	case s.audioSettings == nil:
		return nil, errNoAudio
	case s.secrets == nil:
		return nil, errNoSecrets
	case !ok || provider != string(req.Provider):
		return nil, fmt.Errorf("%w: %q is not a name of %s's keys", orchestration.ErrInvalid, req.Name, req.Provider)
	}

	if err := s.secrets.Delete(ctx, req.Name); err != nil && !errors.Is(err, orchestration.ErrNotFound) {
		return nil, err
	}

	out, err := s.audioProviders(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.RemoveAudioKey200JSONResponse(out), nil
}

// audioProviders are the providers with their keys and state, and each kind's order with its model and usage.
func (s *Server) audioProviders(ctx context.Context) (openapi.Audio, error) {
	if s.audioSettings == nil {
		return openapi.Audio{}, errNoAudio
	}

	links, set, err := s.audioSettings.Chain(ctx)
	if err != nil {
		return openapi.Audio{}, err
	}

	counts, err := s.audioSettings.AudioCounts(ctx, s.now())
	if err != nil {
		return openapi.Audio{}, err
	}

	out := openapi.Audio{Providers: make([]openapi.AudioProviderLink, 0, len(links))}
	on := map[string]bool{}

	for _, l := range links {
		on[l.Provider] = l.On
		link := openapi.AudioProviderLink{
			Provider: openapi.AudioProvider(l.Provider), On: l.On, Keys: make([]openapi.AudioKey, 0, len(l.Keys)),
			RestingUntil: timePtr(l.RestingUntil), Rest: restPtr(l.Rest),
		}

		switch {
		case !l.On:
			link.State = openapi.AudioProviderLinkStateOff
		case len(l.Keys) == 0:
			link.State = openapi.AudioProviderLinkStateNotSetUp
		case !l.RestingUntil.IsZero():
			link.State = openapi.AudioProviderLinkStateResting
		default:
			link.State = openapi.AudioProviderLinkStateReady
		}

		for _, k := range l.Keys {
			key := openapi.AudioKey{
				Name: k.Name, State: openapi.AudioKeyStateReady, RestingUntil: timePtr(k.RestingUntil),
				Rest: restPtr(k.Rest), LastUsedAt: timePtr(k.LastUsed),
			}
			if !k.RestingUntil.IsZero() {
				key.State = openapi.AudioKeyStateResting
			}

			link.Keys = append(link.Keys, key)
		}

		if v := set.Voices[l.Provider]; v != "" {
			link.Voice = ptr(v)
		}

		out.Providers = append(out.Providers, link)
	}

	kindLinks := func(kind string) []openapi.AudioKindLink {
		order := set.Order(kind)
		list := make([]openapi.AudioKindLink, 0, len(order))

		for _, p := range order {
			c := counts[p][kind]
			list = append(list, openapi.AudioKindLink{
				Provider: openapi.AudioProvider(p), On: on[p], Model: speech.Model(p, kind),
				Usage: openapi.AudioUsage{Today: audioTally(c.Today), Month: audioTally(c.Month)},
			})
		}

		return list
	}

	out.Stt, out.Tts = kindLinks(speech.KindSTT), kindLinks(speech.KindTTS)

	return out, nil
}

func audioTally(t modelgateway.AudioTally) openapi.AudioTally {
	return openapi.AudioTally{
		Answered: t.Answered, Failed: t.Failed, Seconds: float32(t.Audio.Seconds()), Characters: t.Characters,
	}
}

// audioCode is the code of an audio request's failure; ok is false for any other error.
func audioCode(err error) (openapi.ErrorCode, bool) {
	var resting *speech.RestingError

	switch {
	case errors.As(err, &resting):
		return openapi.RateLimited, true
	case errors.Is(err, speech.ErrInvalid):
		return openapi.InvalidRequest, true
	case errors.Is(err, speech.ErrUnavailable), errors.Is(err, speech.ErrFailed), errors.Is(err, errNoAudio):
		return openapi.Unavailable, true
	}

	return "", false
}

// audioWait is how long an audio request refused because every provider rests should wait, for its Retry-After.
func (s *Server) audioWait(err error) (string, bool) {
	var resting *speech.RestingError
	if !errors.As(err, &resting) {
		return "", false
	}

	return retryAfter(resting.Until.Sub(s.now())), true
}
