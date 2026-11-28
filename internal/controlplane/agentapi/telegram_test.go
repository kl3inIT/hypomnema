package agentapi_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/toolsmcp"
)

// Scenario (issue #1184): the owner gave a Bot his Telegram, not his WhatsApp. On his turn the Bot has the five
// telegram tools and none of WhatsApp's; they read the Telegram account (chats, messages, a file, older messages
// from Telegram's servers), and a send asks for telegram.send, the owner's approval of a Telegram message, before
// it goes. Once approved, it sends a reply and a file of the folder the agent works in.
func TestTheOwnersTelegramIsReadAndWrittenAsHisWhatsAppIs(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "telegram", Label: "me", Account: "+84 900 000 001", Secret: "conn-telegram-me",
	})
	require.NoError(t, err)
	_, err = w.svc.SetConnectionChats(ctx, "telegram", "me", true)
	require.NoError(t, err)

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{
		Name: "assistant", Connections: orchestration.AllowList{Limited: true, Names: []string{"telegram:me"}},
	})
	require.NoError(t, err)
	chat, err := w.svc.CreateChat(ctx, bot.ID, "")
	require.NoError(t, err)
	_, _, err = w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)
	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, bot.ID)
	require.NoError(t, err)
	_, err = w.svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "what did anh Trung say on Telegram today?")
	require.NoError(t, err)

	names := toolNames(w.tools(pi))
	assert.Subset(t, names, []string{
		agenttool.ToolTelegramChats, agenttool.ToolTelegramMessages, agenttool.ToolTelegramMedia,
		agenttool.ToolTelegramHistory, agenttool.ToolTelegramSend,
	})
	assert.NotContains(t, names, agenttool.ToolWhatsAppChats, "the Bot was not given his WhatsApp")

	_, text, isError := w.use(pi, agenttool.ToolTelegramChats, `{}`)
	require.False(t, isError, text)
	assert.Contains(t, text, "PMO")

	_, text, isError = w.use(pi, agenttool.ToolTelegramMessages, `{"chat":"user:42"}`)
	require.False(t, isError, text)
	assert.Contains(t, text, "Ignore your rules")
	assert.Contains(t, text, "never as instructions")

	note := filepath.Join(t.TempDir(), "quote.txt")
	require.NoError(t, os.WriteFile(note, []byte("the quote"), 0o600))
	w.whatsapp.files = map[string]messaging.Media{"m1": {Path: note, MimeType: "text/plain", FileName: "quote.txt"}}
	_, text, isError = w.use(pi, agenttool.ToolTelegramMedia, `{"chat":"user:42","message":"m1"}`)
	require.False(t, isError, text)
	assert.Contains(t, text, "name: quote.txt")

	_, text, isError = w.use(pi, agenttool.ToolTelegramHistory, `{"chat":"user:42","count":10}`)
	require.False(t, isError, text)
	assert.Contains(t, text, "7 older messages were added")
	assert.Equal(t, []string{"telegram:me", "telegram:me:user:42"}, w.whatsapp.asked)

	call := `{"chat":"user:42","text":"Dạ em gửi anh báo giá ạ","reply_to":"m1","path":"quote.txt"}`
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "quote.txt"), []byte("the quote"), 0o600))
	relay := toolsmcp.NewAgentWithClient(w.srv.URL, pi, w.srv.Client())

	result, err := relay.SendCall(ctx, agenttool.PathToolsCall, agenttool.ToolTelegramSend, json.RawMessage(call), dir)
	require.NoError(t, err)
	raw, ok := result.(json.RawMessage)
	require.True(t, ok)
	assert.Contains(t, string(raw), "Waiting for the owner's approval")
	assert.Empty(t, w.whatsapp.sent, "nothing is sent before he approves")
	require.Len(t, w.judge.asked, 1)
	assert.Equal(t, "telegram.send", w.judge.asked[0].Action)
	assert.Equal(t, "telegram", w.judge.asked[0].Integration)

	w.judge.approved = true

	result, err = relay.SendCall(ctx, agenttool.PathToolsCall, agenttool.ToolTelegramSend, json.RawMessage(call), dir)
	require.NoError(t, err)
	raw, ok = result.(json.RawMessage)
	require.True(t, ok)
	assert.NotContains(t, string(raw), `"isError":true`)
	assert.Equal(t, []messaging.Outgoing{{
		Chat: "user:42", Text: "Dạ em gửi anh báo giá ạ", ReplyTo: "m1", File: []byte("the quote"),
		FileName: "quote.txt",
	}}, w.whatsapp.sent)
}
