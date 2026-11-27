package modelgateway

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
)

// Speech-to-text and text-to-speech (issue #694): one speech.Service, which the gateway serves on /v1/audio/*,
// recording each call on the key that asked, and the client API gives a Bot and the owner through `hypomnema audio`,
// recorded on the key of the chat's running pi as an image is (BotImages). Another token's call, which belongs to no
// chat, is not recorded.

// BotAudio is the audio as the client API gives it to a token: recorded on the key of the chat's running pi when a
// Bot's pi asks.
type BotAudio struct {
	Speech  *speech.Service
	Gateway *Service
	Chats   ChatKeys
	Logger  *slog.Logger
}

func (b BotAudio) Transcribe(ctx context.Context, chatID string, req speech.TranscribeRequest,
) (speech.Transcript, error) {
	t, call := transcribeAudio(ctx, b.Speech, req)
	b.record(ctx, chatID, call)

	return t, call.Err
}

func (b BotAudio) Speak(ctx context.Context, chatID string, req speech.SpeakRequest) (speech.Speech, error) {
	out, call := speakAudio(ctx, b.Speech, req)
	b.record(ctx, chatID, call)

	return out, call.Err
}

func (b BotAudio) record(ctx context.Context, chatID string, c AudioCall) {
	ctx = context.WithoutCancel(ctx)

	keyID, ok := chatKeyID(ctx, b.Gateway, b.Chats, chatID)
	if !ok {
		return
	}

	c.KeyID = keyID
	if err := b.Gateway.RecordAudioCall(ctx, c); err != nil {
		b.Logger.ErrorContext(ctx, "recording an audio call",
			slog.String("event", "controlplane.audio_usage_failed"), slog.String("error", err.Error()))
	}
}

// WorkerAudio is the speech as the agent API gives it to a Dispatch's worker (issue #723): recorded on the
// Dispatch's gateway key, when it has one, as WorkerImages records its images.
type WorkerAudio struct {
	Speech  *speech.Service
	Gateway *Service
	Logger  *slog.Logger
}

func (w WorkerAudio) Transcribe(ctx context.Context, dispatchID string, req speech.TranscribeRequest,
) (speech.Transcript, error) {
	t, call := transcribeAudio(ctx, w.Speech, req)
	w.record(ctx, dispatchID, call)

	return t, call.Err
}

func (w WorkerAudio) Speak(ctx context.Context, dispatchID string, req speech.SpeakRequest) (speech.Speech, error) {
	out, call := speakAudio(ctx, w.Speech, req)
	w.record(ctx, dispatchID, call)

	return out, call.Err
}

func (w WorkerAudio) record(ctx context.Context, dispatchID string, c AudioCall) {
	ctx = context.WithoutCancel(ctx)

	keyID, ok := dispatchKeyID(ctx, w.Gateway, w.Logger, dispatchID)
	if !ok {
		return
	}

	c.KeyID = keyID
	if err := w.Gateway.RecordAudioCall(ctx, c); err != nil {
		w.Logger.ErrorContext(ctx, "recording an audio call",
			slog.String("event", "controlplane.audio_usage_failed"), slog.String("error", err.Error()))
	}
}

// ChannelVoice hears the voice messages a Channel takes (issue #723, incoming voice): transcribed with the same
// speech service as audio_transcribe, and recorded on the newest key of the chat, the one its pi has or last had, or
// on one made for a chat whose pi never ran, since a message may come before the pi starts (ChatUsageKey). Lark
// sends its voice messages as Opus.
type ChannelVoice struct {
	Speech  *speech.Service
	Gateway *Service
	// BotOf names a chat's Bot, which a key made for the chat counts under; nil names none.
	BotOf  ChatBot
	Logger *slog.Logger
}

// ChatBot names a chat's Bot: "" and no error for a chat that is not there.
type ChatBot func(ctx context.Context, chatID string) (string, error)

func transcribeAudio(ctx context.Context, voice *speech.Service, req speech.TranscribeRequest,
) (speech.Transcript, AudioCall) {
	start := time.Now()
	t, err := voice.Transcribe(ctx, req)

	return t, AudioCall{
		Kind: speech.KindSTT, Provider: t.Provider, Model: t.Model, Failures: t.Errors, Err: err, Audio: t.Duration,
		Duration: time.Since(start),
	}
}

func speakAudio(ctx context.Context, voice *speech.Service, req speech.SpeakRequest,
) (speech.Speech, AudioCall) {
	start := time.Now()
	out, err := voice.Speak(ctx, req)

	return out, AudioCall{
		Kind: speech.KindTTS, Provider: out.Provider, Model: out.Model, Failures: out.Errors, Err: err,
		Characters: int64(out.Characters), Duration: time.Since(start),
	}
}

// VideoAudio is the speech a video's revision is voiced and captioned with (videos.Speech): each call recorded on
// the key of whoever asked for the revision, a Bot's chat, a Dispatch or the owner.
type VideoAudio struct {
	Speech  *speech.Service
	Gateway *Service
	BotOf   ChatBot
	Logger  *slog.Logger
}

func (v VideoAudio) Transcribe(ctx context.Context, source videos.AudioSource, req speech.TranscribeRequest,
) (speech.Transcript, error) {
	t, call := transcribeAudio(ctx, v.Speech, req)
	v.record(ctx, source, call)

	return t, call.Err
}

func (v VideoAudio) Speak(ctx context.Context, source videos.AudioSource, req speech.SpeakRequest,
) (speech.Speech, error) {
	out, call := speakAudio(ctx, v.Speech, req)
	v.record(ctx, source, call)

	return out, call.Err
}

func (v VideoAudio) record(ctx context.Context, source videos.AudioSource, call AudioCall) {
	ctx = context.WithoutCancel(ctx)
	call.VideoID, call.VideoRev = source.Video, source.Rev

	key, err := v.key(ctx, source.By)
	if err == nil {
		call.KeyID = key
		err = v.Gateway.RecordAudioCall(ctx, call)
	}

	if err != nil {
		v.Logger.ErrorContext(ctx, "recording video audio usage", slog.String("event", "controlplane.audio_usage_failed"),
			slog.String("video", source.Video), slog.Int("rev", source.Rev), slog.String("error", err.Error()))
	}
}

func (v VideoAudio) key(ctx context.Context, by string) (string, error) {
	kind, id, _ := strings.Cut(by, "-")

	switch kind {
	case "b":
		bot, err := v.BotOf(ctx, id)
		if err != nil {
			return "", err
		}

		k, err := v.Gateway.ChatUsageKey(ctx, id, bot)

		return k.ID, err
	case "d":
		k, err := v.Gateway.DispatchUsageKey(ctx, id)

		return k.ID, err
	case videos.Owner, "o":
		return v.Gateway.VideosKey(ctx)
	default:
		return "", fmt.Errorf("unknown video requester %q", by)
	}
}

func (c ChannelVoice) Hear(ctx context.Context, chatID string, recording []byte, err error) string {
	if err != nil || len(recording) == 0 {
		return speech.VoiceUntranscribed
	}

	start := time.Now()
	t, err := c.Speech.Transcribe(ctx, speech.TranscribeRequest{Audio: speech.RecordingOf(recording, "voice.opus", "")})

	// That a voice message was heard, and never what it said.
	c.Logger.InfoContext(ctx, "a voice message was transcribed", slog.String("event", "controlplane.voice_heard"),
		slog.String("chat_id", chatID), slog.Bool("heard", err == nil))

	ctx = context.WithoutCancel(ctx)

	bot := ""
	if c.BotOf != nil {
		if b, cerr := c.BotOf(ctx, chatID); cerr == nil {
			bot = b
		}
	}

	// Each transcription is a usage row (incoming voice's design): on the chat's newest key, or on one made for a
	// chat whose pi never ran, since the first message may come before it (review of #792).
	k, kerr := c.Gateway.ChatUsageKey(ctx, chatID, bot)

	switch {
	case kerr != nil:
		c.Logger.ErrorContext(ctx, "reading a chat's gateway key", slog.String("event", "controlplane.voice_key_failed"),
			slog.String("chat_id", chatID), slog.String("error", kerr.Error()))
	default:
		if rerr := c.Gateway.RecordAudioCall(ctx, AudioCall{
			KeyID: k.ID, Kind: speech.KindSTT, Provider: t.Provider, Model: t.Model, Failures: t.Errors, Err: err,
			Audio: t.Duration, Duration: time.Since(start),
		}); rerr != nil {
			c.Logger.ErrorContext(ctx, "recording an audio call",
				slog.String("event", "controlplane.audio_usage_failed"), slog.String("error", rerr.Error()))
		}
	}

	return speech.VoiceNote(t, err)
}
