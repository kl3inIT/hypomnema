package agentapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// fakeAudio stands for the gateway's speech: it records whose call asked
// what, never keeping the words, and answers heard or fails with err.
type fakeAudio struct {
	asked []string
	heard string
	err   error
}

func (f *fakeAudio) Transcribe(_ context.Context, who agentapi.Who, req speech.TranscribeRequest,
) (speech.Transcript, error) {
	r, err := req.Audio.Open()
	if err != nil {
		return speech.Transcript{}, err
	}

	data, _ := io.ReadAll(r)
	_ = r.Close()

	f.asked = append(f.asked, fmt.Sprintf("%s transcribe %s %s %d bytes language=%s speakers=%t terms=%v",
		whose(who), req.Audio.Name, req.Audio.MIME, len(data), req.Language, req.Speakers, req.Terms))

	if f.err != nil {
		return speech.Transcript{}, f.err
	}

	return speech.Transcript{
		Text: f.heard, Provider: "soniox", Model: "stt-async-v5", Duration: 90 * time.Second,
		Segments: []speech.Segment{{Start: 0, Text: "Xin chào.", Speaker: "1"}, {Start: 65 * time.Second,
			Text: "Chào anh.", Speaker: "2"}},
	}, nil
}

func (f *fakeAudio) Speak(_ context.Context, who agentapi.Who, req speech.SpeakRequest) (speech.Speech, error) {
	f.asked = append(f.asked, fmt.Sprintf("%s speak %d characters voice=%s format=%s", whose(who),
		len([]rune(req.Input)), req.Voice, req.Format))

	if f.err != nil {
		return speech.Speech{}, f.err
	}

	return speech.Speech{
		Audio: []byte("ID3 speech"), Format: req.Format, MIME: "audio/mpeg", Characters: len([]rune(req.Input)),
		Provider: "elevenlabs", Model: "eleven_multilingual_v2",
		Errors: []speech.Failure{{Provider: "soniox", Reason: "key soniox-1: rate limited"}},
	}, nil
}

// hearingWorld is newWorld with the gateway's speech, web and images, and
// workers that run Claude Code.
func hearingWorld(t *testing.T) (*world, *fakeAudio) {
	t.Helper()

	audio := &fakeAudio{heard: "Gặp nhau lúc 3 giờ chiều."}
	w := newWorldWith(t, 0, func(api *agentapi.Server) {
		api.WithAudio(audio).WithWeb(&fakeWeb{}).WithImages(&fakeImages{}).
			WithRuntimes(func(context.Context, string) (string, error) { return "claude", nil })
	})

	return w, audio
}

// botPi is the token of a Bot's pi in a new chat, with the owner's message in it, so that it is his turn.
func (w *world) botPi(bot orchestration.Bot) (string, string) {
	w.t.Helper()

	ctx := w.t.Context()

	if bot.ID == "" {
		made, err := w.svc.PutBot(ctx, bot)
		require.NoError(w.t, err)

		bot = made
	}

	chat, err := w.svc.CreateChat(ctx, bot.ID, "")
	require.NoError(w.t, err)
	_, _, err = w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(w.t, err)
	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, bot.ID)
	require.NoError(w.t, err)
	_, err = w.svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "listen to this")
	require.NoError(w.t, err)

	return pi, chat.ID
}

type toolResult struct {
	Content []struct{ Type, Text, Data, MimeType string } `json:"content"`
	IsError bool                                          `json:"isError"`
}

func (w *world) result(key, tool string, arguments any) toolResult {
	w.t.Helper()

	raw, err := json.Marshal(arguments)
	require.NoError(w.t, err)

	status, body := w.call(http.MethodPost, agenttool.PathToolsCall, key,
		agenttool.BrowserCall{Tool: tool, Arguments: raw})
	require.Equal(w.t, http.StatusOK, status, string(body))

	var r toolResult
	require.NoError(w.t, json.Unmarshal(body, &r))

	return r
}

// Scenario (issue #723, decisions 1.2 to 1.5): a worker and a Bot's pi are
// offered audio_transcribe and audio_speak; a Bot's audio_speak says how to
// send the speech into its chat. A recording is heard as the caller's, so it
// is recorded on its key, and answered with its words and who heard them;
// with speakers, a line a segment. Speech answers who spoke it and the audio.
// A recording past 48 MiB or empty is refused before any provider; what no
// provider could do is the tool's error. What was said is never logged.
func TestAgentsHearAndSpeakThroughTheirTools(t *testing.T) {
	t.Parallel()

	w, audio := hearingWorld(t)
	worker := w.workerUnder("", "laptop", "")
	pi, chatID := w.botPi(orchestration.Bot{Name: "voice"})

	assert.Subset(t, toolNames(w.tools(worker)), []string{agenttool.ToolAudioTranscribe, agenttool.ToolAudioSpeak})

	var speak agenttool.BrowserTool

	for _, tool := range w.tools(pi) {
		if tool.Name == agenttool.ToolAudioSpeak {
			speak = tool
		}
	}

	assert.Contains(t, speak.Description, "MEDIA line", "a Bot is told how to send the speech")
	assert.Contains(t, speak.Description, "never in place of a written reply")

	r := w.result(worker, agenttool.ToolAudioTranscribe, agenttool.TranscribeCall{
		Audio: []byte("OggS recording"), FileName: "call.ogg", MIME: "audio/ogg", Language: "vi",
		Terms: []string{"Hypomnema"},
	})
	require.False(t, r.IsError, r.Content)
	require.Len(t, r.Content, 2)
	assert.Equal(t, "Gặp nhau lúc 3 giờ chiều.", r.Content[0].Text)
	assert.Equal(t, "Heard by soniox with stt-async-v5.", r.Content[1].Text)

	r = w.result(pi, agenttool.ToolAudioTranscribe, agenttool.TranscribeCall{Audio: []byte("ID3"), Speakers: true})
	require.False(t, r.IsError)
	assert.Equal(t, "[0:00] speaker 1: Xin chào.\n[1:05] speaker 2: Chào anh.", r.Content[0].Text)

	r = w.result(pi, agenttool.ToolAudioSpeak, map[string]any{"text": "Xin chào", "voice": "alloy", "format": "mp3"})
	require.False(t, r.IsError)
	require.Len(t, r.Content, 2)
	assert.Equal(t, "Spoken by elevenlabs with eleven_multilingual_v2, after soniox failed (key soniox-1: rate limited).",
		r.Content[0].Text)
	assert.Equal(t, "audio", r.Content[1].Type)
	assert.Equal(t, "audio/mpeg", r.Content[1].MimeType)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("ID3 speech")), r.Content[1].Data)

	require.Len(t, audio.asked, 3)
	assert.Regexp(t, `^dsp_\w+ transcribe call.ogg audio/ogg 14 bytes language=vi speakers=false terms=\[Hypomnema\]$`,
		audio.asked[0])
	assert.Equal(t, "chat "+chatID+" transcribe recording audio/mpeg 3 bytes language= speakers=true "+
		"terms=[]", audio.asked[1])
	assert.Equal(t, "chat "+chatID+" speak 8 characters voice=alloy format=mp3", audio.asked[2])

	r = w.result(worker, agenttool.ToolAudioTranscribe, agenttool.TranscribeCall{})
	assert.True(t, r.IsError)
	assert.Equal(t, "the recording is empty", r.Content[0].Text)

	r = w.result(worker, agenttool.ToolAudioTranscribe,
		agenttool.TranscribeCall{Audio: make([]byte, agenttool.MaxTranscribeBytes+1)})
	assert.True(t, r.IsError)
	assert.Contains(t, r.Content[0].Text, "larger than 47 MiB")
	assert.Len(t, audio.asked, 3, "a recording refused reaches no provider")

	audio.err = &speech.FailedError{Failures: []speech.Failure{{Provider: "soniox", Reason: "key soniox-1: 401"}}}

	r = w.result(worker, agenttool.ToolAudioSpeak, map[string]any{"text": "x"})
	assert.True(t, r.IsError)
	assert.Equal(t, "no speech provider answered: soniox (key soniox-1: 401)", r.Content[0].Text)

	assert.Contains(t, w.logs.String(), "agentapi.audio_call")
	assert.NotContains(t, w.logs.String(), "Xin chào", "what was said is not logged")
	assert.NotContains(t, w.logs.String(), "Gặp nhau")
}

// Scenario: a profile switches the speech family off by audio_* (decision
// 1.1). Its worker is offered neither tool, and a call of one is refused as a
// tool it never had.
func TestTheAudioFamilyIsSwitchedOffAsOne(t *testing.T) {
	t.Parallel()

	w, audio := hearingWorld(t)
	key := w.workerUnder("tools_off: [audio_*]\n", "laptop", "")

	names := toolNames(w.tools(key))
	assert.NotContains(t, names, agenttool.ToolAudioTranscribe)
	assert.NotContains(t, names, agenttool.ToolAudioSpeak)
	assert.Contains(t, names, agenttool.ToolWebFetch, "what is not turned off stays")

	status, _, _ := w.use(key, agenttool.ToolAudioSpeak, `{"text":"x"}`)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Empty(t, audio.asked)
}

// Scenario (issue #723, incoming voice): a voice note of the owner's
// WhatsApp, and one of his Zalo, opened by a Bot comes with its transcript
// after its path, heard as the Bot's chat so that it is recorded on its key;
// one no provider could hear says so in one neutral line. An image is still
// an image.
func TestAVoiceNoteIsHeardBeforeTheBotReadsIt(t *testing.T) {
	t.Parallel()

	w, audio := hearingWorld(t)
	ctx := t.Context()

	dir := t.TempDir()
	note := filepath.Join(dir, "note.ogg")
	require.NoError(t, os.WriteFile(note, []byte("OggS voice"), 0o600))

	w.whatsapp.files = map[string]messaging.Media{"v1": {Path: note, MimeType: "audio/ogg; codecs=opus"}}

	for _, service := range []string{"whatsapp", "zalo"} {
		_, err := w.svc.SaveServiceConnection(ctx, orchestration.Connection{
			Integration: service, Label: "me", Account: "Owner", Secret: "conn-" + service + "-me",
		})
		require.NoError(t, err)
		_, err = w.svc.SetConnectionChats(ctx, service, "me", true)
		require.NoError(t, err)
	}

	pi, chatID := w.botPi(orchestration.Bot{
		Name: "listener", Connections: orchestration.AllowList{Limited: true, Names: []string{"whatsapp:me", "zalo:me"}},
	})

	for _, tool := range []string{agenttool.ToolWhatsAppMedia, agenttool.ToolZaloMedia} {
		r := w.result(pi, tool, map[string]string{"chat": "120363@g.us", "message": "v1"})
		require.False(t, r.IsError, tool)
		require.Len(t, r.Content, 1)
		assert.Contains(t, r.Content[0].Text, "path: "+note, tool)
		assert.True(t, strings.HasSuffix(r.Content[0].Text,
			"transcript:\n\"Gặp nhau lúc 3 giờ chiều.\"\n[voice message, transcribed]"), r.Content[0].Text)
	}

	assert.Equal(t, []string{
		"chat " + chatID + " transcribe note.ogg audio/ogg; codecs=opus 10 bytes language= speakers=false terms=[]",
		"chat " + chatID + " transcribe note.ogg audio/ogg; codecs=opus 10 bytes language= speakers=false terms=[]",
	}, audio.asked)

	audio.err = fmt.Errorf("%w: no provider that is on has a key", speech.ErrUnavailable)

	r := w.result(pi, agenttool.ToolWhatsAppMedia, map[string]string{"chat": "120363@g.us", "message": "v1"})
	require.False(t, r.IsError)
	assert.True(t, strings.HasSuffix(r.Content[0].Text, "transcript:\n[voice message could not be transcribed]"))

	assert.Contains(t, w.logs.String(), "agentapi.voice_heard")
	assert.NotContains(t, w.logs.String(), "Gặp nhau", "what a voice note says is not logged")
}
