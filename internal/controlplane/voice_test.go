package controlplane

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// Scenario (issue #723, incoming voice; review of #792): the first message of
// a new Lark chat is a voice message, heard before the chat's pi ever started,
// so the chat has no gateway key yet. Its transcription is still a usage row,
// on a key made for the chat and its Bot and never usable, so it counts under
// the Bot. When the pi starts, its own key works, and the next voice message
// is recorded on it.
func TestTheFirstVoiceMessageOfANewChatIsAUsageRow(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	bot, err := svc.PutBot(ctx, orchestration.Bot{Name: "voice"})
	require.NoError(t, err)
	chat, err := svc.CreateChat(ctx, bot.ID, "")
	require.NoError(t, err)

	gateway := modelgateway.New(db, platform.SystemClock{}, platform.NewID, nil, nil)

	// ElevenLabs's Scribe, which hears "hello" in a recording of 90 seconds.
	scribe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"hello","words":[{"text":"hello","start":0,"end":90,"type":"word"}]}`)
	}))
	t.Cleanup(scribe.Close)

	voice := channelVoice{
		speech: speech.New(speech.Config{ElevenLabsURL: scribe.URL, Keys: func(context.Context) ([]speech.Key, error) {
			return []speech.Key{{Provider: speech.ProviderElevenLabs, Name: "audio-elevenlabs", Value: "k"}}, nil
		}}),
		gateway: gateway, chats: svc, logger: slog.New(slog.DiscardHandler),
	}

	assert.Equal(t, "\"hello\"\n[voice message, transcribed]", voice.Hear(ctx, chat.ID, []byte("OggS voice"), nil))

	rows, err := gateway.ListUsage(ctx, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1, "the first voice message of a new chat is recorded")
	assert.Equal(t, "audio/"+speech.ElevenLabsSTTModel, rows[0].Model)

	keys, err := gateway.ListKeys(ctx)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, chat.ID, keys[0].ChatID)
	assert.NotNil(t, keys[0].RevokedAt, "the key made for the record is never usable")

	report, err := gateway.Usage(ctx, "24h", 0)
	require.NoError(t, err)
	require.Len(t, report.ByKey, 1)
	assert.Equal(t, bot.ID, report.ByKey[0].ID, "it counts under the chat's Bot")

	live, secret, err := gateway.AddChatKey(ctx, chat.ID, bot.ID, "")
	require.NoError(t, err)

	_ = voice.Hear(ctx, chat.ID, []byte("OggS voice"), nil)

	rows, err = gateway.ListUsage(ctx, 10)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, live.ID, rows[0].KeyID, "once the pi runs, its key")

	_, err = gateway.Authenticate(ctx, secret)
	require.NoError(t, err, "the pi's key still works")
}
