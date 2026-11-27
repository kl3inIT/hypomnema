package modelgateway

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
)

// What the owner sets on Settings → Gateway → Audio (issue #694), kept as the
// image providers' switches and order are (image_settings.go): one switch for
// each audio provider, all on until turned off, and the order speech-to-text
// and text-to-speech each ask them in, by default Soniox then ElevenLabs
// (owner, 2026-11-17). Read at each request, so a change counts from the next.

// AudioSettings reads what the owner set, for each request.
func (s *Service) AudioSettings(ctx context.Context) (speech.Settings, error) {
	rows, err := store.New(s.db).ListGatewayAudioProviders(ctx)
	if err != nil {
		return speech.Settings{}, fmt.Errorf("reading the audio providers' settings: %w", err)
	}

	var out speech.Settings

	for _, r := range rows {
		if r.Off != 0 {
			out.Off = append(out.Off, r.Provider)
		}

		if r.Voice != "" {
			if out.Voices == nil {
				out.Voices = map[string]string{}
			}

			out.Voices[r.Provider] = r.Voice
		}

		if r.TtsModel != "" {
			if out.Models == nil {
				out.Models = map[string]string{}
			}

			out.Models[r.Provider] = r.TtsModel
		}
	}

	placed := func(position func(store.GatewayAudioProvider) sql.NullInt64) []string {
		var with []store.GatewayAudioProvider

		for _, r := range rows {
			if position(r).Valid {
				with = append(with, r)
			}
		}

		slices.SortFunc(with, func(a, b store.GatewayAudioProvider) int {
			return cmp.Or(cmp.Compare(position(a).Int64, position(b).Int64), cmp.Compare(a.Provider, b.Provider))
		})

		order := make([]string, 0, len(with))
		for _, r := range with {
			order = append(order, r.Provider)
		}

		return order
	}

	out.STT = placed(func(r store.GatewayAudioProvider) sql.NullInt64 { return r.SttPosition })
	out.TTS = placed(func(r store.GatewayAudioProvider) sql.NullInt64 { return r.TtsPosition })

	return out, nil
}

// SetAudioProvider switches an audio provider on or off, for both kinds.
func (s *Service) SetAudioProvider(ctx context.Context, provider string, on bool) error {
	if !slices.Contains(speech.Providers(), provider) {
		return fmt.Errorf("%w: no audio provider %q", ErrNotFound, provider)
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.SetGatewayAudioOff(ctx, store.SetGatewayAudioOffParams{
			Provider: provider, Off: flag(!on), Now: now,
		}); err != nil {
			return fmt.Errorf("switching audio provider %s: %w", provider, err)
		}

		return nil
	})
}

// MaxVoice bounds a provider's voice: a name or a provider's voice id.
const MaxVoice = 100

// SetAudioVoice keeps the voice a provider speaks a video with when none is asked (issue #818, row 5); empty: the
// provider's own default.
func (s *Service) SetAudioVoice(ctx context.Context, provider, voice string) error {
	voice = strings.TrimSpace(voice)

	switch {
	case !slices.Contains(speech.Providers(), provider):
		return fmt.Errorf("%w: no audio provider %q", ErrNotFound, provider)
	case len(voice) > MaxVoice || strings.ContainsFunc(voice, func(r rune) bool { return r < ' ' }):
		return fmt.Errorf("%w: a voice is a name of at most %d characters", ErrInvalid, MaxVoice)
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.SetGatewayAudioVoice(ctx, store.SetGatewayAudioVoiceParams{
			Provider: provider, Voice: voice, Now: now,
		}); err != nil {
			return fmt.Errorf("setting audio provider %s's voice: %w", provider, err)
		}

		return nil
	})
}

// MaxModel bounds a provider's model id.
const MaxModel = 100

// SetAudioModel keeps the text-to-speech model a provider speaks with (issue #1283, row 1), as the owner picked it
// from the provider's list; empty: its default. Only ElevenLabs has a choice: Soniox has one model.
func (s *Service) SetAudioModel(ctx context.Context, provider, model string) error {
	model = strings.TrimSpace(model)

	switch {
	case !slices.Contains(speech.Providers(), provider):
		return fmt.Errorf("%w: no audio provider %q", ErrNotFound, provider)
	case provider != speech.ProviderElevenLabs:
		return fmt.Errorf("%w: %s speaks with one model, %s", ErrInvalid, provider,
			speech.Model(provider, speech.KindTTS))
	case len(model) > MaxModel || strings.ContainsFunc(model, func(r rune) bool { return r <= ' ' }):
		return fmt.Errorf("%w: a model is an id of at most %d characters, without spaces", ErrInvalid, MaxModel)
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.SetGatewayAudioModel(ctx, store.SetGatewayAudioModelParams{
			Provider: provider, Model: model, Now: now,
		}); err != nil {
			return fmt.Errorf("setting audio provider %s's model: %w", provider, err)
		}

		return nil
	})
}

// SetAudioOrder keeps the order a kind, stt or tts, asks the providers in: each once.
func (s *Service) SetAudioOrder(ctx context.Context, kind string, providers []string) error {
	all := speech.Providers()

	switch {
	case !slices.Contains(speech.Kinds(), kind):
		return fmt.Errorf("%w: the kind is %s", ErrInvalid, strings.Join(speech.Kinds(), " or "))
	case len(providers) != len(all) || !sameSet(providers, all):
		return fmt.Errorf("%w: the order names each audio provider once: %v", ErrInvalid, all)
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		for i, p := range providers {
			position := sql.NullInt64{Int64: int64(i + 1), Valid: true}

			var err error
			if kind == speech.KindSTT {
				err = q.SetGatewayAudioSTTPosition(ctx, store.SetGatewayAudioSTTPositionParams{
					Provider: p, Position: position, Now: now,
				})
			} else {
				err = q.SetGatewayAudioTTSPosition(ctx, store.SetGatewayAudioTTSPositionParams{
					Provider: p, Position: position, Now: now,
				})
			}

			if err != nil {
				return fmt.Errorf("placing audio provider %s: %w", p, err)
			}
		}

		return nil
	})
}

// AudioTally is a provider's audio requests of one kind in a period: how
// many it answered and failed, and how much it heard or spoke.
type AudioTally struct {
	Answered, Failed int
	// Audio is the length of the recordings transcribed; Characters what was spoken.
	Audio      time.Duration
	Characters int64
}

// plus is both tallies together: a kind's requests made with each model the owner chose in the period.
func (t AudioTally) plus(o AudioTally) AudioTally {
	return AudioTally{
		Answered: t.Answered + o.Answered, Failed: t.Failed + o.Failed, Audio: t.Audio + o.Audio,
		Characters: t.Characters + o.Characters,
	}
}

// AudioCount is a provider's requests of one kind, of the UTC day and month.
type AudioCount struct {
	Today, Month AudioTally
}

// AudioCounts are each provider's requests of each kind (speech.KindSTT,
// speech.KindTTS) of the UTC day and month now is in, from the usage rows:
// a row's model tells its kind (speech.KindOf: a provider's one transcription
// model, else speech, whichever model the owner chose then); a request no
// provider was asked for is no provider's.
func (s *Service) AudioCounts(ctx context.Context, now time.Time) (map[string]map[string]AudioCount, error) {
	q := store.New(s.db)
	month := monthStart(now)
	day := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)

	out := map[string]map[string]AudioCount{}

	for _, period := range []struct {
		since time.Time
		add   func(c *AudioCount, t AudioTally)
	}{
		{month, func(c *AudioCount, t AudioTally) { c.Month = c.Month.plus(t) }},
		{day, func(c *AudioCount, t AudioTally) { c.Today = c.Today.plus(t) }},
	} {
		rows, err := q.CountGatewayAudioCalls(ctx, period.since.UnixMilli())
		if err != nil {
			return nil, fmt.Errorf("counting the audio calls: %w", err)
		}

		for _, r := range rows {
			model, ok := strings.CutPrefix(r.Model, ModelAudioPrefix)
			if !ok || r.Provider == "" || !slices.Contains(speech.Providers(), r.Provider) {
				continue
			}

			kind := speech.KindOf(r.Provider, model)

			if out[r.Provider] == nil {
				out[r.Provider] = map[string]AudioCount{}
			}

			c := out[r.Provider][kind]
			period.add(&c, AudioTally{
				Answered: int(r.Answered), Failed: int(r.Failed), Audio: time.Duration(r.AudioMs) * time.Millisecond,
				Characters: r.Characters,
			})
			out[r.Provider][kind] = c
		}
	}

	return out, nil
}
