package clientapi

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
)

// audioOver wires the Audio page as the control plane does: the keys the
// vault's, the rests the speech service's, the switches, order and counts
// the gateway's.
type audioOver struct {
	*modelgateway.Service

	speech *speech.Service
}

func (a audioOver) Chain(ctx context.Context) ([]speech.Link, speech.Settings, error) {
	return a.speech.Chain(ctx)
}

func (a audioOver) Models(ctx context.Context, provider, kind string) ([]speech.ModelChoice, error) {
	return a.speech.Models(ctx, provider, kind)
}

func (a audioOver) Voices(ctx context.Context) ([]speech.Voice, []speech.Failure, error) {
	return a.speech.Voices(ctx)
}

// heard stands for the speech service: it keeps what it was asked.
type heard struct {
	asked  speech.TranscribeRequest
	audio  string
	spoken speech.SpeakRequest
	chat   string
}

func (h *heard) Transcribe(_ context.Context, chatID string, req speech.TranscribeRequest,
) (speech.Transcript, error) {
	h.asked, h.chat = req, chatID

	src, err := req.Audio.Open()
	if err != nil {
		return speech.Transcript{}, err
	}

	data, _ := io.ReadAll(src)
	h.audio = string(data)

	return speech.Transcript{
		Text: "Hello. Hi.", Language: "en", Duration: 2 * time.Second, Provider: "elevenlabs", Model: "scribe_v2",
		Segments: []speech.Segment{
			{Start: 0, End: time.Second, Text: "Hello.", Speaker: "speaker_0"},
			{Start: time.Second, End: 2 * time.Second, Text: "Hi.", Speaker: "speaker_1"},
		},
		Errors: []speech.Failure{{Provider: "soniox", Model: "stt-async-v5", Reason: "key speech-soniox: answered 503"}},
	}, nil
}

func (h *heard) Speak(_ context.Context, chatID string, req speech.SpeakRequest) (speech.Speech, error) {
	h.spoken, h.chat = req, chatID
	if req.Input == "limited" {
		return speech.Speech{}, &speech.RestingError{Until: time.Now().Add(time.Minute)}
	}

	return speech.Speech{
		Audio: []byte("ID3"), Format: "mp3", MIME: "audio/mpeg", Characters: 5, Provider: "soniox", Model: "tts-rt-v2",
	}, nil
}

// Scenario: the owner opens Settings → Gateway → Audio. Neither provider has
// a key; each kind lists Soniox first with its model. The owner adds a
// Soniox key, which goes into the vault as speech-soniox and never comes
// back; puts ElevenLabs first for text-to-speech only; turns ElevenLabs
// off; removes the key. A coordinator reads and changes nothing.
func TestTheOwnerSeesAndChangesTheAudioProviders(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	g := gatewayOver(t, w.svc)
	vault := &keptSecrets{values: map[string]string{}}
	voice := speech.New(speech.Config{
		Settings: g.AudioSettings,
		Keys: func(context.Context) ([]speech.Key, error) {
			vault.mu.Lock()
			defer vault.mu.Unlock()

			var out []speech.Key

			for name, value := range vault.values {
				if p, _, ok := speech.ParseKeyName(name); ok {
					out = append(out, speech.Key{Provider: p, Name: name, Value: value})
				}
			}

			return out, nil
		},
	})
	w.srv.WithSecrets(vault).WithAudio(&heard{}, audioOver{Service: g, speech: voice})

	owner, _ := w.token("web", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)

	listed, err := pi.GetAudioWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listed.StatusCode(), string(listed.Body))
	assert.Equal(t, openapi.AudioProviderLinkStateNotSetUp, listed.JSON200.Providers[0].State)
	assert.Equal(t, openapi.AudioProvider("soniox"), listed.JSON200.Stt[0].Provider)
	assert.Equal(t, "stt-async-v6", listed.JSON200.Stt[0].Model, "Soniox's newest by default")
	assert.Equal(t, "stt-async-v6", listed.JSON200.Stt[0].DefaultModel)
	assert.Equal(t, "eleven_multilingual_v2", listed.JSON200.Tts[1].Model)

	key := " sx-key \n"

	refused, err := pi.AddAudioKeyWithResponse(ctx, "soniox", openapi.AudioNewKey{Key: &key})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode())

	added, err := owner.AddAudioKeyWithResponse(ctx, "soniox", openapi.AudioNewKey{Key: &key})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, added.StatusCode(), string(added.Body))
	assert.Equal(t, "speech-soniox", added.JSON201.Name)
	assert.Equal(t, "sx-key", vault.value("speech-soniox"))
	assert.NotContains(t, string(added.Body), "sx-key", "the key is never answered")
	assert.Equal(t, openapi.AudioProviderLinkStateReady, added.JSON201.Audio.Providers[0].State)
	assert.Equal(t, []openapi.AudioKey{{Name: "speech-soniox", State: openapi.AudioKeyStateReady}},
		added.JSON201.Audio.Providers[0].Keys)

	ordered, err := owner.SetAudioOrderWithResponse(ctx, "tts", openapi.AudioOrder{
		Providers: []openapi.AudioProvider{"elevenlabs", "soniox"},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, ordered.StatusCode(), string(ordered.Body))
	assert.Equal(t, openapi.AudioProvider("elevenlabs"), ordered.JSON200.Tts[0].Provider)
	assert.Equal(t, openapi.AudioProvider("soniox"), ordered.JSON200.Stt[0].Provider)

	// The voice Soniox speaks videos with (issue #818, row 5): the owner's to set, empty for its own default.
	refusedVoice, err := pi.SetAudioVoiceWithResponse(ctx, "soniox", openapi.AudioVoice{Voice: "Linh"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refusedVoice.StatusCode())

	voiced, err := owner.SetAudioVoiceWithResponse(ctx, "soniox", openapi.AudioVoice{Voice: " Linh "})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, voiced.StatusCode(), string(voiced.Body))
	require.NotNil(t, voiced.JSON200.Providers[0].Voice)
	assert.Equal(t, "Linh", *voiced.JSON200.Providers[0].Voice)
	assert.Nil(t, voiced.JSON200.Providers[1].Voice, "ElevenLabs keeps its own default")

	set, err := g.AudioSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"soniox": "Linh"}, set.Voices)

	cleared, err := owner.SetAudioVoiceWithResponse(ctx, "soniox", openapi.AudioVoice{Voice: ""})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, cleared.StatusCode(), string(cleared.Body))
	assert.Nil(t, cleared.JSON200.Providers[0].Voice)

	bad, err := owner.SetAudioVoiceWithResponse(ctx, "soniox", openapi.AudioVoice{Voice: "a\nb"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, bad.StatusCode())

	// The model each kind asks a provider with (issue #1285, row 1): the owner's to choose, empty for the newest.
	refusedModel, err := pi.SetAudioModelWithResponse(ctx, "soniox", "tts", openapi.AudioModelChange{Model: "tts-rt-v1"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refusedModel.StatusCode())

	modeled, err := owner.SetAudioModelWithResponse(ctx, "soniox", "tts", openapi.AudioModelChange{Model: " tts-rt-v1 "})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, modeled.StatusCode(), string(modeled.Body))
	assert.Equal(t, "tts-rt-v1", modeled.JSON200.Tts[1].Model)
	assert.Equal(t, "tts-rt-v2", modeled.JSON200.Tts[1].DefaultModel)
	assert.Equal(t, "stt-async-v6", modeled.JSON200.Stt[0].Model, "speech-to-text keeps its own")

	set, err = g.AudioSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, "tts-rt-v1", set.Model("soniox", "tts"))

	newest, err := owner.SetAudioModelWithResponse(ctx, "soniox", "tts", openapi.AudioModelChange{Model: ""})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, newest.StatusCode(), string(newest.Body))
	assert.Equal(t, "tts-rt-v2", newest.JSON200.Tts[1].Model)

	spaced, err := owner.SetAudioModelWithResponse(ctx, "soniox", "stt", openapi.AudioModelChange{Model: "a b"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, spaced.StatusCode())

	switched, err := owner.SetAudioProviderWithResponse(ctx, "elevenlabs", openapi.AudioProviderChange{On: false})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, switched.StatusCode(), string(switched.Body))
	assert.Equal(t, openapi.AudioProviderLinkStateOff, switched.JSON200.Providers[1].State)
	assert.False(t, switched.JSON200.Tts[0].On)

	removed, err := owner.RemoveAudioKeyWithResponse(ctx, "soniox", "speech-soniox")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, removed.StatusCode(), string(removed.Body))
	assert.Empty(t, vault.value("speech-soniox"))

	other, err := owner.RemoveAudioKeyWithResponse(ctx, "elevenlabs", "web-search-exa")
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, other.StatusCode())
}

// Scenario: a token sends a recording, asking for speakers: the service gets
// the bytes, and the answer has the segments with their speakers and why
// Soniox did not answer. Speech comes back as base64 bytes; every provider
// resting is 429 with Retry-After.
func TestAudioIsHeardAndSpokenThroughTheClientAPI(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	h := &heard{}
	w.srv.WithAudio(h, audioOver{Service: gatewayOver(t, w.svc), speech: speech.New(speech.Config{})})

	owner, _ := w.token("web", bearertoken.Owner)

	speakers, name := true, "call.wav"
	resp, err := owner.TranscribeAudioWithResponse(ctx, openapi.TranscribeRequest{
		Audio: []byte("RIFF-call"), FileName: &name, Speakers: &speakers,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode(), string(resp.Body))
	assert.Equal(t, "RIFF-call", h.audio)
	assert.True(t, h.asked.Speakers)
	assert.Equal(t, "call.wav", h.asked.Audio.Name)
	assert.Empty(t, h.chat, "the owner's token belongs to no chat")

	speaker := "speaker_1"
	assert.Equal(t, openapi.TranscriptSegment{Start: 1, End: 2, Text: "Hi.", Speaker: &speaker}, resp.JSON200.Segments[1])
	assert.Equal(t, []openapi.WebSearchError{{Provider: "soniox", Message: "key speech-soniox: answered 503"}},
		val(resp.JSON200.Errors))

	spoken, err := owner.SpeakAudioWithResponse(ctx, openapi.SpeakRequest{Input: "Hello"})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, spoken.StatusCode(), string(spoken.Body))
	assert.Equal(t, "ID3", string(spoken.JSON200.Audio))
	assert.Equal(t, "audio/mpeg", spoken.JSON200.MimeType)

	limited, err := owner.SpeakAudioWithResponse(ctx, openapi.SpeakRequest{Input: "limited"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, limited.StatusCode())
	assert.NotEmpty(t, limited.HTTPResponse.Header.Get("Retry-After"))
}
