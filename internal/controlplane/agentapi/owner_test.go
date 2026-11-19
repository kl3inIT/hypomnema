package agentapi_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

func TestTheOwnerUsesAllConnectedToolsButASendStillWaits(t *testing.T) {
	t.Parallel()

	w := newSupersetWorld(t, "reports", "private")
	ctx := t.Context()
	messages := &ownWhatsApp{}
	w.agents.WithMessaging(messages).WithJudge(w.proxy).WithWeb(&fakeWeb{})

	_, err := w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "whatsapp", Label: "me", Secret: "conn-whatsapp-me",
	})
	require.NoError(t, err)
	for _, label := range []string{"reports", "private"} {
		_, err = w.svc.SetConnectionChats(ctx, "superset", label, true)
		require.NoError(t, err)
	}

	_, owner, err := w.svc.AddToken(ctx, "local", bearertoken.Owner)
	require.NoError(t, err)
	_, _, err = w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)
	bot, err := w.svc.PutBot(ctx, orchestration.Bot{
		Name: "reporter", Connections: orchestration.AllowList{Limited: true, Names: []string{"superset:reports"}},
	})
	require.NoError(t, err)
	chat, err := w.svc.CreateChat(ctx, bot.ID, "")
	require.NoError(t, err)
	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, bot.ID)
	require.NoError(t, err)

	assert.Subset(t, toolNames(w.tools(owner)), []string{
		agenttool.ToolWhatsAppMessages, agenttool.ToolWhatsAppSend, agenttool.ToolWebSearch, "superset_dashboards",
	})
	assert.NotContains(t, toolNames(w.tools(pi)), agenttool.ToolWhatsAppMessages)
	assert.Contains(t, toolNames(w.tools(pi)), "superset_dashboards")

	status, text, isError := w.use(owner, agenttool.ToolWhatsAppMessages, `{"chat":"120363@g.us"}`)
	require.Equal(t, http.StatusOK, status, text)
	require.False(t, isError, text)
	assert.Contains(t, text, `"ID": "m1"`)
	assert.Equal(t, []string{"whatsapp:me:120363@g.us"}, messages.asked)

	status, text, isError = w.use(owner, "superset_dashboards", `{"account":"private","search":"usage"}`)
	require.Equal(t, http.StatusOK, status, text)
	require.False(t, isError, text)
	assert.Contains(t, text, `"title":"Product usage"`)

	_, text, isError = w.use(pi, "superset_dashboards", `{"account":"private"}`)
	assert.True(t, isError)
	assert.Contains(t, text, "private")
	status, _, _ = w.use(pi, agenttool.ToolWhatsAppMessages, `{"chat":"120363@g.us"}`)
	assert.Equal(t, http.StatusNotFound, status)

	status, text, isError = w.use(owner, agenttool.ToolWhatsAppSend, `{"chat":"120363@g.us","text":"Hello"}`)
	require.Equal(t, http.StatusOK, status, text)
	assert.True(t, isError)
	assert.Contains(t, text, "Waiting for the owner's approval")
	assert.Empty(t, messages.sent)
	approvals, err := w.svc.ListApprovals(ctx, orchestration.ApprovalFilter{Status: orchestration.ApprovalPending})
	require.NoError(t, err)
	require.Len(t, approvals, 1)
	assert.Equal(t, "whatsapp.send", approvals[0].Action)
	assert.Equal(t, "owner", approvals[0].AskedBy)
	assert.Empty(t, approvals[0].ChatID)
}

func TestTheOwnerMemoryToolHasOnlySharedUserMemory(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	_, owner, err := w.svc.AddToken(t.Context(), "local", bearertoken.Owner)
	require.NoError(t, err)
	assert.Contains(t, toolNames(w.tools(owner)), agenttool.ToolMemory)

	_, text, isError := w.use(owner, agenttool.ToolMemory, `{"command":"view","path":"/memories"}`)
	require.False(t, isError, text)
	assert.Contains(t, text, "USER.md")
	assert.NotContains(t, text, "MEMORY.md")

	_, text, isError = w.use(owner, agenttool.ToolMemory,
		`{"command":"insert","path":"/memories/USER.md","insert_line":0,"insert_text":"Prefers short replies."}`)
	require.False(t, isError, text)
	memory, err := w.svc.Memories().OwnerMemory(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "Prefers short replies.", memory.Text)

	_, _, isError = w.use(owner, agenttool.ToolMemory, `{"command":"view","path":"/memories/MEMORY.md"}`)
	assert.True(t, isError)
}
