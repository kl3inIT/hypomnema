package controlplane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// Speech-to-text and text-to-speech (issue #694): one speech.Service, which
// the model gateway serves on /v1/audio/*, recording each call on the key
// that asked, and the client API gives a Bot and the owner through
// `hypomnema audio`, recorded on the key of the chat's running pi as an
// image is (images.go botImages). Another token's call, which belongs to no
// chat, is not recorded.

// speechKeys reads the audio providers' keys from the vault: every secret
// named as a key (speech.KeyName), as searchKeys reads web search's (web.go).
func speechKeys(secrets *vault.Service, logger *slog.Logger) func(ctx context.Context) ([]speech.Key, error) {
	return func(ctx context.Context) ([]speech.Key, error) {
		all, err := secrets.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing the vault's secrets: %w", err)
		}

		var out []speech.Key

		for _, sec := range all {
			provider, _, ok := speech.ParseKeyName(sec.Name)
			if !ok {
				continue
			}

			opened, err := secrets.Open(ctx, sec.Name)

			switch {
			case errors.Is(err, vault.ErrNotFound), errors.Is(err, vault.ErrNotConfigured):
				continue
			case err != nil:
				logger.ErrorContext(ctx, "opening an audio key",
					slog.String("event", "controlplane.speech_key_failed"), slog.String("secret", sec.Name),
					slog.String("error", err.Error()))

				continue
			}

			out = append(out, speech.Key{Provider: provider, Name: sec.Name, Value: opened.Payload[vault.KeyValue]})
		}

		return out, nil
	}
}

// botAudio is the audio as the client API gives it to a token: recorded on
// the key of the chat's running pi when a Bot's pi asks.
type botAudio struct {
	speech  *speech.Service
	gateway *modelgateway.Service
	chats   chatKeyHolder
	logger  *slog.Logger
}

func (b botAudio) Transcribe(ctx context.Context, chatID string, req speech.TranscribeRequest,
) (speech.Transcript, error) {
	start := time.Now()
	t, err := b.speech.Transcribe(ctx, req)
	b.record(ctx, chatID, modelgateway.AudioCall{
		Kind: speech.KindSTT, Provider: t.Provider, Model: t.Model, Failures: t.Errors, Err: err, Audio: t.Duration,
		Duration: time.Since(start),
	})

	return t, err
}

func (b botAudio) Speak(ctx context.Context, chatID string, req speech.SpeakRequest) (speech.Speech, error) {
	start := time.Now()
	out, err := b.speech.Speak(ctx, req)
	b.record(ctx, chatID, modelgateway.AudioCall{
		Kind: speech.KindTTS, Provider: out.Provider, Model: out.Model, Failures: out.Errors, Err: err,
		Characters: int64(out.Characters), Duration: time.Since(start),
	})

	return out, err
}

func (b botAudio) record(ctx context.Context, chatID string, c modelgateway.AudioCall) {
	ctx = context.WithoutCancel(ctx)

	keyID, ok := chatKeyID(ctx, b.gateway, b.chats, chatID)
	if !ok {
		return
	}

	c.KeyID = keyID
	if err := b.gateway.RecordAudioCall(ctx, c); err != nil {
		b.logger.ErrorContext(ctx, "recording an audio call",
			slog.String("event", "controlplane.audio_usage_failed"), slog.String("error", err.Error()))
	}
}

// audioSettings is what the Audio page shows and changes: the keys and rests
// the speech service keeps, and the gateway's switches, order and counts.
type audioSettings struct {
	*modelgateway.Service

	speech *speech.Service
}

func (a audioSettings) Chain(ctx context.Context) ([]speech.Link, speech.Settings, error) {
	return a.speech.Chain(ctx)
}

// workerAudio is the speech as the agent API gives it to a Dispatch's worker
// (issue #723): recorded on the Dispatch's gateway key, when it has one, as
// workerImages records its images.
type workerAudio struct {
	speech  *speech.Service
	gateway *modelgateway.Service
	logger  *slog.Logger
}

func (w workerAudio) Transcribe(ctx context.Context, dispatchID string, req speech.TranscribeRequest,
) (speech.Transcript, error) {
	start := time.Now()
	t, err := w.speech.Transcribe(ctx, req)
	w.record(ctx, dispatchID, modelgateway.AudioCall{
		Kind: speech.KindSTT, Provider: t.Provider, Model: t.Model, Failures: t.Errors, Err: err, Audio: t.Duration,
		Duration: time.Since(start),
	})

	return t, err
}

func (w workerAudio) Speak(ctx context.Context, dispatchID string, req speech.SpeakRequest) (speech.Speech, error) {
	start := time.Now()
	out, err := w.speech.Speak(ctx, req)
	w.record(ctx, dispatchID, modelgateway.AudioCall{
		Kind: speech.KindTTS, Provider: out.Provider, Model: out.Model, Failures: out.Errors, Err: err,
		Characters: int64(out.Characters), Duration: time.Since(start),
	})

	return out, err
}

func (w workerAudio) record(ctx context.Context, dispatchID string, c modelgateway.AudioCall) {
	ctx = context.WithoutCancel(ctx)

	keyID, ok := dispatchKeyID(ctx, w.gateway, w.logger, dispatchID)
	if !ok {
		return
	}

	c.KeyID = keyID
	if err := w.gateway.RecordAudioCall(ctx, c); err != nil {
		w.logger.ErrorContext(ctx, "recording an audio call",
			slog.String("event", "controlplane.audio_usage_failed"), slog.String("error", err.Error()))
	}
}

// agentAudio is the speech as the agent API gives it: a worker's recorded as
// workerAudio records it, a Bot's pi's as botAudio records its `hypomnema
// audio`, on the key of the chat's running pi.
type agentAudio struct {
	worker workerAudio
	bot    botAudio
}

func (a agentAudio) Transcribe(ctx context.Context, who agentapi.Who, req speech.TranscribeRequest,
) (speech.Transcript, error) {
	if who.DispatchID != "" {
		return a.worker.Transcribe(ctx, who.DispatchID, req)
	}

	return a.bot.Transcribe(ctx, who.ChatID, req)
}

func (a agentAudio) Speak(ctx context.Context, who agentapi.Who, req speech.SpeakRequest) (speech.Speech, error) {
	if who.DispatchID != "" {
		return a.worker.Speak(ctx, who.DispatchID, req)
	}

	return a.bot.Speak(ctx, who.ChatID, req)
}

// channelVoice hears the voice messages a Channel takes (issue #723,
// incoming voice): transcribed with the same speech service as
// audio_transcribe, and recorded on the newest key of the chat, the one its
// pi has or last had, since a message may come before the pi starts. Lark
// sends its voice messages as Opus.
type channelVoice struct {
	speech  *speech.Service
	gateway *modelgateway.Service
	logger  *slog.Logger
}

func (c channelVoice) Hear(ctx context.Context, chatID string, recording []byte, err error) string {
	if err != nil || len(recording) == 0 {
		return speech.VoiceUntranscribed
	}

	start := time.Now()
	t, err := c.speech.Transcribe(ctx, speech.TranscribeRequest{Audio: agentapi.RecordingOf(recording, "voice.opus", "")})

	// That a voice message was heard, and never what it said.
	c.logger.InfoContext(ctx, "a voice message was transcribed", slog.String("event", "controlplane.voice_heard"),
		slog.String("chat_id", chatID), slog.Bool("heard", err == nil))

	ctx = context.WithoutCancel(ctx)

	k, kerr := c.gateway.ChatKey(ctx, chatID)

	switch {
	case errors.Is(kerr, modelgateway.ErrNotFound):
	case kerr != nil:
		c.logger.ErrorContext(ctx, "reading a chat's gateway key", slog.String("event", "controlplane.voice_key_failed"),
			slog.String("chat_id", chatID), slog.String("error", kerr.Error()))
	default:
		if rerr := c.gateway.RecordAudioCall(ctx, modelgateway.AudioCall{
			KeyID: k.ID, Kind: speech.KindSTT, Provider: t.Provider, Model: t.Model, Failures: t.Errors, Err: err,
			Audio: t.Duration, Duration: time.Since(start),
		}); rerr != nil {
			c.logger.ErrorContext(ctx, "recording an audio call",
				slog.String("event", "controlplane.audio_usage_failed"), slog.String("error", rerr.Error()))
		}
	}

	return speech.VoiceNote(t, err)
}
