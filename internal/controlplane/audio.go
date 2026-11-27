package controlplane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// Speech-to-text and text-to-speech (issue #694): the keys from the vault, and
// the agent API's, the Audio page's and the Channels' and videos' view of the
// gateway's metered speech (modelgateway/metered_audio.go).

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

// audioSettings is what the Audio page shows and changes: the keys and rests
// the speech service keeps, and the gateway's switches, order and counts.
type audioSettings struct {
	*modelgateway.Service

	speech *speech.Service
}

func (a audioSettings) Chain(ctx context.Context) ([]speech.Link, speech.Settings, error) {
	return a.speech.Chain(ctx)
}

func (a audioSettings) Models(ctx context.Context, provider, kind string) ([]speech.ModelChoice, error) {
	return a.speech.Models(ctx, provider, kind)
}

func (a audioSettings) Voices(ctx context.Context) ([]speech.Voice, []speech.Failure, error) {
	return a.speech.Voices(ctx)
}

// agentAudio is the speech as the agent API gives it: a worker's recorded as
// modelgateway.WorkerAudio records it, a Bot's pi's as modelgateway.BotAudio
// records its `hypomnema audio`, on the key of the chat's running pi.
type agentAudio struct {
	worker modelgateway.WorkerAudio
	bot    modelgateway.BotAudio
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

// chatBot names a chat's Bot for the gateway's metered speech: "" for a chat
// that is not there.
func chatBot(chats chatReader) modelgateway.ChatBot {
	return func(ctx context.Context, chatID string) (string, error) {
		chat, err := chats.GetChat(ctx, chatID)
		if errors.Is(err, orchestration.ErrNotFound) {
			return "", nil
		}

		return chat.Bot, err
	}
}

// chatReader reads a chat (orchestration.Service).
type chatReader interface {
	GetChat(ctx context.Context, id string) (orchestration.Chat, error)
}
