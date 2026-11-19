// Package speech turns speech into text and text into speech for every agent
// (issue #694): the model gateway serves it on /v1/audio/transcriptions,
// /v1/audio/speech and /v1/audio/voices, and a Bot and the owner reach it
// through `hypomnema audio transcribe|speak`. It holds no key and stores
// nothing itself: the caller's wiring gives it the providers' keys from the
// vault and what the owner set, and records each call on a gateway key.
//
// What it does is learned from the gateways that have it:
//
//   - the providers are Soniox, then ElevenLabs (owner, 2026-11-17), each
//     with the owner's keys, as OmniRoute's audioRegistry.ts lists them by
//     kind (open-sse/config/audioRegistry.ts:203, 465);
//   - speech-to-text and text-to-speech each ask the providers that are on
//     in the owner's order for that kind, and the first that answers wins;
//     the answer says why those before it did not, as the gateway's images
//     (modelgateway/images.go) and web search (websearch/websearch.go) do;
//   - a provider's keys are vault secrets, several per provider, taken least
//     recently used first, each resting on its own after a refusal
//     (pool.go, rest.go), as web search's keys do (websearch/pool.go, issue
//     #393);
//   - Soniox's job-based transcription is one blocking call that uploads,
//     creates the job, polls each second up to 30 minutes, reads the
//     transcript, then deletes the job and the file (soniox.go): LiteLLM
//     llms/soniox/audio_transcription/handler.py and MemoryOS
//     voice/SonioxAsync.java;
//   - a transcript is cut into segments at a speaker change, a pause over
//     800 ms or 400 characters, as MemoryOS SonioxAsync.group does
//     (transcript.go).
package speech

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// The providers.
const (
	ProviderSoniox     = "soniox"
	ProviderElevenLabs = "elevenlabs"
)

// Providers are the providers in their default order, for speech-to-text and
// text-to-speech alike (owner, 2026-11-17: Soniox, then ElevenLabs).
func Providers() []string { return []string{ProviderSoniox, ProviderElevenLabs} }

// The kinds of request.
const (
	KindSTT = "stt"
	KindTTS = "tts"
)

// Kinds are both kinds, in the order shown.
func Kinds() []string { return []string{KindSTT, KindTTS} }

// The one model each provider is asked with for each kind (issue #694):
// Soniox's async transcription and its TTS (OmniRoute audioRegistry.ts:203,
// 465; MemoryOS SonioxAsync.DEFAULT_MODEL), ElevenLabs's Scribe and its
// multilingual voice model (9router-go internal/providers/registry_models.go:
// 139; OmniRoute audioRegistry.ts:486, Bifrost types.go:13).
const (
	SonioxSTTModel     = "stt-async-v5"
	SonioxTTSModel     = "tts-rt-v2"
	ElevenLabsSTTModel = "scribe_v2"
	ElevenLabsTTSModel = "eleven_multilingual_v2"
)

// Model is the model a provider is asked with for a kind.
func Model(provider, kind string) string {
	switch {
	case provider == ProviderSoniox && kind == KindSTT:
		return SonioxSTTModel
	case provider == ProviderSoniox:
		return SonioxTTSModel
	case kind == KindSTT:
		return ElevenLabsSTTModel
	default:
		return ElevenLabsTTSModel
	}
}

const (
	// MaxAudioBytes bounds a recording: 9router-go's body cap for its media
	// routes (internal/middleware/max_body.go:18-23).
	MaxAudioBytes = 128 << 20
	// MaxInputCharacters bounds what is spoken: OpenAI's /v1/audio/speech
	// takes at most 4,096 characters, the shape the route speaks.
	MaxInputCharacters = 4096

	// transcriptionBudget bounds a transcription: Soniox's job is polled up
	// to 30 minutes (LiteLLM's SONIOX_DEFAULT_MAX_POLL_ATTEMPTS of 1 s), and
	// the upload and transcript take their own time.
	transcriptionBudget = 35 * time.Minute
	// speechTimeout bounds one provider's spoken answer.
	speechTimeout = 2 * time.Minute
	// maxSpeechAnswer bounds the audio a provider sends back.
	maxSpeechAnswer = 64 << 20
	// maxTranscriptAnswer bounds a transcript's JSON: MemoryOS's
	// MAX_RECORDING_BYTES for five hours' tokens.
	maxTranscriptAnswer = 64 << 20
	// maxSmallAnswer bounds a status, an id or a voices list.
	maxSmallAnswer = 8 << 20
)

// Errors callers match with errors.Is.
var (
	// ErrInvalid: the request is not one that can be asked.
	ErrInvalid = errors.New("invalid request")
	// ErrUnavailable: no provider is on with a key for this kind.
	ErrUnavailable = errors.New("no speech provider is set up")
	// ErrFailed: every provider asked failed; the error says why each did.
	ErrFailed = errors.New("no speech provider answered")
)

// RestingError says every provider the call could use rests, and when the
// first is free again.
type RestingError struct{ Until time.Time }

func (e *RestingError) Error() string {
	return "every speech provider is resting; try again after " + e.Until.UTC().Format(time.RFC3339)
}

// Failure is why a provider did not answer.
type Failure struct {
	Provider string
	Model    string
	Reason   string
}

// FailedError is a request no provider answered: each one's reason, and the
// last one asked.
type FailedError struct {
	Failures []Failure
}

func (e *FailedError) Error() string { return ErrFailed.Error() + ": " + failureText(e.Failures) }

func (e *FailedError) Unwrap() error { return ErrFailed }

func failureText(failures []Failure) string {
	if len(failures) == 0 {
		return "none could be asked"
	}

	parts := make([]string, 0, len(failures))
	for _, f := range failures {
		parts = append(parts, f.Provider+" ("+f.Reason+")")
	}

	return strings.Join(parts, "; ")
}

// Key is one of the owner's API keys for a provider, as the vault holds it.
type Key struct {
	Provider string
	// Name is the vault secret's name (KeyName); it names the key everywhere.
	Name  string
	Value string
}

// Settings are what the owner set on Settings → Gateway → Audio.
type Settings struct {
	// Off are the providers switched off: they are neither asked nor may be named.
	Off []string
	// STT and TTS are the owner's order for each kind; a provider left out
	// comes after, in the default order. Empty: the default order.
	STT []string
	TTS []string
	// Voices are the voice each provider speaks a video with when none is asked (issue #818, row 5); a
	// provider not named speaks with its own default.
	Voices map[string]string
}

// Order is the providers in the owner's order for a kind.
func (set Settings) Order(kind string) []string {
	asked := set.STT
	if kind == KindTTS {
		asked = set.TTS
	}

	var out []string

	for _, p := range append(slices.Clone(asked), Providers()...) {
		if slices.Contains(Providers(), p) && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}

	return out
}

// Config is what the Service needs; the zero value has no key and asks no one.
type Config struct {
	// Keys gives the owner's keys, read at each call so that a key the owner
	// adds is used at once. nil: no key.
	Keys func(ctx context.Context) ([]Key, error)
	// Settings gives what the owner set, read at each call. nil: every
	// provider on, in the default order.
	Settings func(ctx context.Context) (Settings, error)
	// The providers' hosts; empty: theirs. Tests name their own.
	SonioxURL     string
	SonioxTTSURL  string
	ElevenLabsURL string
	// PollInterval is how often a Soniox job is asked after; zero: each
	// second (LiteLLM's SONIOX_DEFAULT_POLL_INTERVAL, MemoryOS's POLL_INTERVAL).
	PollInterval time.Duration
	// PollFor bounds the polling of one job; zero: 30 minutes (LiteLLM's
	// SONIOX_DEFAULT_MAX_POLL_ATTEMPTS of 1 s).
	PollFor time.Duration
	// Now is the clock rests read; nil: the wall clock.
	Now    func() time.Time
	Logger *slog.Logger
}

// The providers' own hosts: Soniox's REST API (LiteLLM's SONIOX_API_BASE with
// /v1, MemoryOS's base URL), its TTS host (OmniRoute audioRegistry.ts:466),
// and ElevenLabs's API (Bifrost, 9router-go, LiteLLM's TTS_BASE_URL).
const (
	sonioxURL     = "https://api.soniox.com/v1"
	sonioxTTSURL  = "https://tts-rt.soniox.com/tts"
	elevenLabsURL = "https://api.elevenlabs.io"

	pollEvery = time.Second
	pollFor   = 30 * time.Minute
)

// Service asks the speech providers.
type Service struct {
	cfg    Config
	client *http.Client
	now    func() time.Time

	mu      sync.Mutex
	resting map[string]rest
	// lastUsed is when each key was last asked, for least recently used first.
	lastUsed map[string]time.Time
	voices   map[string]voicesRead
}

// New returns a Service.
func New(cfg Config) *Service {
	for _, d := range []struct {
		field *string
		value string
	}{{&cfg.SonioxURL, sonioxURL}, {&cfg.SonioxTTSURL, sonioxTTSURL}, {&cfg.ElevenLabsURL, elevenLabsURL}} {
		if *d.field == "" {
			*d.field = d.value
		}

		*d.field = strings.TrimRight(*d.field, "/")
	}

	if cfg.PollInterval <= 0 {
		cfg.PollInterval = pollEvery
	}

	if cfg.PollFor <= 0 {
		cfg.PollFor = pollFor
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	//nolint:forcetypeassert // the standard library's own type
	transport := http.DefaultTransport.(*http.Transport).Clone()

	return &Service{
		cfg: cfg, client: &http.Client{Transport: transport}, now: now,
		resting: map[string]rest{}, lastUsed: map[string]time.Time{}, voices: map[string]voicesRead{},
	}
}

// Audio is a recording to transcribe: Open gives its bytes from the start,
// again for each provider asked.
type Audio struct {
	Name string
	MIME string
	Size int64
	Open func() (io.ReadCloser, error)
}

// TranscribeRequest is a recording and how it is to be heard.
type TranscribeRequest struct {
	Audio Audio
	// Language is an ISO-639-1 hint, such as vi; empty: the provider finds it.
	Language string
	// Speakers asks who spoke when.
	Speakers bool
	// Terms are words to hear right, such as names and jargon. Soniox is given them as its job's context.terms, as
	// MemoryOS (voice/SonioxAsync.java:82) and LiteLLM (llms/soniox/audio_transcription/transformation.py, the
	// context parameter) send them; ElevenLabs is not, as no reference sends it any.
	Terms []string
	// Provider asks only this provider; empty: the first that answers.
	Provider string
}

// Segment is a stretch of the transcript one speaker said.
type Segment struct {
	Start, End time.Duration
	Text       string
	// Speaker is the provider's label for who said it; empty when speakers were not asked.
	Speaker string
}

// Word is one word said, with when.
type Word struct {
	Word       string
	Start, End time.Duration
	Speaker    string
}

// Transcript is what was said, and who answered.
type Transcript struct {
	Text     string
	Language string
	Duration time.Duration
	Segments []Segment
	Words    []Word
	Provider string
	Model    string
	// Errors are the providers asked before Provider answered, and why each did not.
	Errors []Failure
}

// SpeakRequest is text to speak and how.
type SpeakRequest struct {
	Input string
	Voice string
	// Format is mp3, wav, pcm or opus; empty: mp3.
	Format string
	// Speed is ElevenLabs's voice speed; zero: its default.
	Speed float64
	// Language is Soniox's language for the text; empty: en (OmniRoute's default).
	Language string
	Provider string
}

// Formats are the audio formats speech is made in, OpenAI's.
func Formats() []string { return []string{"mp3", "wav", "pcm", "opus"} }

// Speech is spoken audio, and who made it.
type Speech struct {
	Audio      []byte
	Format     string
	MIME       string
	Characters int
	Provider   string
	Model      string
	Errors     []Failure
}

// mimeOf is the content type of a format, as OmniRoute's speech handler
// answers it (audioSpeech.ts:353) and OpenAI's PCM, raw samples.
func mimeOf(format string) string {
	switch format {
	case "wav":
		return "audio/wav"
	case "opus":
		return "audio/opus"
	case "pcm":
		return "audio/pcm"
	default:
		return "audio/mpeg"
	}
}

// Transcribe asks the providers that are on, in the owner's order for
// speech-to-text, until one answers.
func (s *Service) Transcribe(ctx context.Context, req TranscribeRequest) (Transcript, error) {
	if req.Audio.Open == nil || req.Audio.Size <= 0 {
		return Transcript{}, fmt.Errorf("%w: a transcription needs a recording", ErrInvalid)
	}

	if req.Audio.Size > MaxAudioBytes {
		return Transcript{}, fmt.Errorf("%w: the recording is larger than %d MiB", ErrInvalid, MaxAudioBytes>>20)
	}

	ctx, cancel := context.WithTimeout(ctx, transcriptionBudget)
	defer cancel()

	var out Transcript

	err := s.each(ctx, KindSTT, req.Provider, func(ctx context.Context, r route) error {
		var (
			t   Transcript
			err error
		)

		if r.key.Provider == ProviderSoniox {
			t, err = s.sonioxTranscribe(ctx, r.key, req)
		} else {
			t, err = s.elevenLabsTranscribe(ctx, r.key, req)
		}

		out = t

		return err
	}, func(provider, model string, failures []Failure) {
		out.Provider, out.Model, out.Errors = provider, model, failures
	})

	return out, err
}

// Speak asks the providers that are on, in the owner's order for
// text-to-speech, until one answers.
func (s *Service) Speak(ctx context.Context, req SpeakRequest) (Speech, error) {
	req.Format = strings.ToLower(strings.TrimSpace(req.Format))
	if req.Format == "" {
		req.Format = "mp3"
	}

	characters := len([]rune(req.Input))

	switch {
	case strings.TrimSpace(req.Input) == "":
		return Speech{}, fmt.Errorf("%w: speech needs an input", ErrInvalid)
	case characters > MaxInputCharacters:
		return Speech{}, fmt.Errorf("%w: the input is longer than %d characters", ErrInvalid, MaxInputCharacters)
	case !slices.Contains(Formats(), req.Format):
		return Speech{}, fmt.Errorf("%w: the format is one of %s", ErrInvalid, strings.Join(Formats(), ", "))
	case req.Speed < 0:
		return Speech{}, fmt.Errorf("%w: the speed is positive", ErrInvalid)
	}

	var out Speech

	err := s.each(ctx, KindTTS, req.Provider, func(ctx context.Context, r route) error {
		ctx, cancel := context.WithTimeout(ctx, speechTimeout)
		defer cancel()

		var (
			audio []byte
			err   error
		)

		if r.key.Provider == ProviderSoniox {
			audio, err = s.sonioxSpeak(ctx, r.key, req)
		} else {
			audio, err = s.elevenLabsSpeak(ctx, r.key, req)
		}

		if err == nil && len(audio) == 0 {
			err = errors.New("it sent no audio")
		}

		out = Speech{Audio: audio, Format: req.Format, MIME: mimeOf(req.Format), Characters: characters}

		return err
	}, func(provider, model string, failures []Failure) {
		out.Provider, out.Model, out.Errors = provider, model, failures
	})

	return out, err
}

// settings reads what the owner set.
func (s *Service) settings(ctx context.Context) (Settings, error) {
	if s.cfg.Settings == nil {
		return Settings{}, nil
	}

	set, err := s.cfg.Settings(ctx)
	if err != nil {
		return Settings{}, fmt.Errorf("reading the audio settings: %w", err)
	}

	return set, nil
}

// route is one of a provider's keys, as a call asks it.
type route struct {
	id  string
	key Key
}

func keyRoute(k Key) route { return route{id: "key:" + k.Name, key: k} }

// each asks the routes of a kind in turn, each provider's keys least recently
// used first, until one answers: a key's own refusal (a limit, no credits, a
// refused key) rests the key and asks the provider's next key; any other
// failure goes to the next provider, as web search does (websearch.go,
// search). answered is told who answered after whose failures.
func (s *Service) each(ctx context.Context, kind, provider string,
	ask func(ctx context.Context, r route) error, answered func(provider, model string, failures []Failure),
) error {
	provider = strings.ToLower(strings.TrimSpace(provider))

	set, err := s.settings(ctx)
	if err != nil {
		return err
	}

	switch {
	case provider != "" && !slices.Contains(Providers(), provider):
		return fmt.Errorf("%w: the provider is one of %s", ErrInvalid, strings.Join(Providers(), ", "))
	case provider != "" && slices.Contains(set.Off, provider):
		return fmt.Errorf("%w: %s is turned off on this control plane", ErrInvalid, provider)
	}

	keys, err := s.keys(ctx)
	if err != nil {
		return err
	}

	var (
		failures  []Failure
		tried     bool
		firstFree time.Time
		keyed     bool
	)

	for _, p := range set.Order(kind) {
		if slices.Contains(set.Off, p) || (provider != "" && p != provider) {
			continue
		}

		model := Model(p, kind)

		for _, k := range s.leastRecentlyUsed(keys[p]) {
			keyed = true
			r := keyRoute(k)

			if until, why, ok := s.restingUntil(r.id); ok {
				failures = append(failures, Failure{
					Provider: p, Model: model,
					Reason: "key " + k.Name + ": " + why + " until " + until.UTC().Format(time.RFC3339),
				})

				if firstFree.IsZero() || until.Before(firstFree) {
					firstFree = until
				}

				continue
			}

			tried = true

			s.use(r.id)

			err := ask(ctx, r)
			if err == nil {
				s.served(r.id)
				answered(p, model, failures)

				return nil
			}

			if ctx.Err() != nil {
				return ctx.Err()
			}

			failures = append(failures, Failure{Provider: p, Model: model, Reason: "key " + k.Name + ": " + reasonOf(err)})

			s.cfg.Logger.WarnContext(ctx, "a speech provider failed",
				slog.String("event", "speech.provider_failed"), slog.String("provider", p),
				slog.String("kind", kind), slog.String("key", k.Name), slog.String("error", err.Error()))

			if !s.failed(r, err) {
				// Not the key's: another key of the provider would meet it too.
				break
			}
		}
	}

	switch {
	case !keyed:
		return fmt.Errorf("%w: no provider that is on has a key; add one on Settings → Gateway → Audio",
			ErrUnavailable)
	case !tried && !firstFree.IsZero():
		return &RestingError{Until: firstFree}
	}

	answered("", "", failures)

	return &FailedError{Failures: failures}
}
