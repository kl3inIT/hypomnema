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
	assert.Equal(t, "stt-async-v5", listed.JSON200.Stt[0].Model)
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
