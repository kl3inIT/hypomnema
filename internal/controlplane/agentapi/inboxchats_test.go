package agentapi_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// inboxChatsAnswer is what inbox_chats answers.
type inboxChatsAnswer struct {
	Defaults struct {
		Direct, Group, Bot bool
		BotPatterns        []string `json:"bot_patterns"`
	} `json:"defaults"`
	Chats []struct {
		Account, Chat string
		Reads         bool
	} `json:"chats"`
}

// Scenario: issue #1270. The owner switched a Zalo chat off for the secretary and a bot's chat on. The check of the
// Thư ký Bot's read Routine reads his choices with its own token (inbox_chats), of the accounts the Bot may read
// only, with the default of every other chat; the chat switched off is still read when he asks his Bot in a chat:
// nothing walls it off. A worker has no inbox_chats.
func TestTheCheckReadsWhichChatsTheOwnerSwitchedOffAndABotStillReadsThem(t *testing.T) {
	t.Parallel()

	sw := newSecretaryWorld(t)
	ctx := t.Context()

	_, err := sw.svc.Inbox().SetChatRead(ctx, "zalo", "me", "g_pmo", false)
	require.NoError(t, err)
	_, err = sw.svc.Inbox().SetChatRead(ctx, "lark", "me", "oc_bot", true)
	require.NoError(t, err)
	_, err = sw.svc.Inbox().SetChatRead(ctx, "whatsapp", "other", "8490000002@s.whatsapp.net", false)
	require.NoError(t, err)

	script, _ := sw.check()
	assert.Contains(t, toolNames(sw.tools(script)), agenttool.ToolInboxChats)

	_, text, failed := sw.use(script, agenttool.ToolInboxChats, `{}`)
	require.False(t, failed, text)

	var got inboxChatsAnswer
	require.NoError(t, json.Unmarshal([]byte(text), &got), text)
	assert.True(t, got.Defaults.Direct)
	assert.True(t, got.Defaults.Group)
	assert.False(t, got.Defaults.Bot)
	assert.Contains(t, got.Defaults.BotPatterns, `\bOA\b`)
	assert.ElementsMatch(t, []struct {
		Account, Chat string
		Reads         bool
	}{
		{Account: "zalo:me", Chat: "g_pmo", Reads: false},
		{Account: "lark:me", Chat: "oc_bot", Reads: true},
	}, got.Chats, "not whatsapp:other, which the Bot may not read")

	// The owner asks his Bot in a chat: the chat switched off is read as any other.
	chat, err := sw.svc.CreateChat(ctx, sw.bot.ID, "")
	require.NoError(t, err)
	pi, err := sw.svc.AddBotToken(ctx, "pi", chat.ID, sw.bot.ID)
	require.NoError(t, err)
	_, err = sw.svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "what did the PMO group say?")
	require.NoError(t, err)

	_, text, failed = sw.use(pi, agenttool.ToolZaloMessages, `{"chat":"g_pmo"}`)
	require.False(t, failed, text)
	assert.Contains(t, sw.whatsapp.asked, "zalo:me:g_pmo")

	worker, err := sw.svc.AddAgentSession(ctx, "dsp_1", []string{"zalo:me"})
	require.NoError(t, err)
	assert.NotContains(t, toolNames(sw.tools(worker)), agenttool.ToolInboxChats)
}
