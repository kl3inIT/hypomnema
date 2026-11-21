package agentapi_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

func requestArgs(t *testing.T, use string) string {
	t.Helper()

	raw, err := json.Marshal(agenttool.RequestConnectionCall{Use: use, Reason: "to read the team's calendar"})
	require.NoError(t, err)

	return string(raw)
}

// Scenario (issue #471, part D): only a Bot's pi in its Bot's main chat sees request_connection, with exactly the
// Connections it may ask for: on for chats, not the owner's own account, outside its list. Its other chats, a worker
// of a Run from the main chat, the coordinator token and its Routine's script neither see it nor may call it. An ask
// is answered at once as pending, the same request when repeated; what it may not ask for is refused and makes no
// card. Granted, the account is the pi's at its next call and the tool goes, there being nothing left to ask for;
// taken back on the Bot's page, the next call is refused.
func TestOnlyTheMainChatsPiAsksForAConnection(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, coordinator, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	for _, c := range []orchestration.Connection{
		{Integration: "notion", Label: "work", Secret: "s-work"},
		{Integration: "notion", Label: "team", Secret: "s-team"},
		{Integration: "notion", Label: "off", Secret: "s-off"},
		{Integration: "whatsapp", Label: "me", Secret: "s-me"},
	} {
		_, err := w.svc.SaveServiceConnection(ctx, c)
		require.NoError(t, err)

		if c.Label != "off" {
			_, err = w.svc.SetConnectionChats(ctx, c.Integration, c.Label, true)
			require.NoError(t, err)
		}
	}

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "hr",
		Connections: orchestration.AllowList{Limited: true, Names: []string{"notion:work"}}})
	require.NoError(t, err)

	main, _, err := w.svc.OpenMainChat(ctx, bot.ID, "", "", orchestration.ChatModel{})
	require.NoError(t, err)

	other, err := w.svc.CreateChatWithID(ctx, "", bot.ID, "another", orchestration.ChatModel{})
	require.NoError(t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi", main.ID, bot.ID)
	require.NoError(t, err)

	elsewhere, err := w.svc.AddBotToken(ctx, "pi", other.ID, bot.ID)
	require.NoError(t, err)

	routine, err := w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: bot.ID, Name: "sync", Kind: routinestore.RoutineScript, Script: "sync.sh", ScriptText: "#!/bin/sh\n",
		EveryMinutes: 60,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	_, err = w.svc.RunRoutineNow(ctx, routine.ID) // a script's token is its live run's (#1023)
	require.NoError(t, err)
	script, err := w.svc.AddRoutineToken(ctx, "pi", routine.ID)
	require.NoError(t, err)

	worker := w.workerOf("node_a", main.ID)

	tools := w.tools(pi)
	at := slices.IndexFunc(tools, func(t agenttool.BrowserTool) bool { return t.Name == agenttool.ToolRequestConnection })
	require.GreaterOrEqual(t, at, 0, "the main chat's pi sees it")

	var schema struct {
		Properties struct {
			Use struct {
				Enum []string `json:"enum"`
			} `json:"use"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(tools[at].InputSchema, &schema))
	assert.Equal(t, []string{"notion:team"}, schema.Properties.Use.Enum)
	assert.Contains(t, tools[at].Description, "notion:team")
	assert.NotContains(t, tools[at].Description, "whatsapp:me", "a user Connection is never named")

	for name, key := range map[string]string{"another chat": elsewhere, "a worker": worker} {
		assert.NotContains(t, toolNames(w.tools(key)), agenttool.ToolRequestConnection, name)

		status, _, _ := w.use(key, agenttool.ToolRequestConnection, requestArgs(t, "notion:team"))
		assert.Equal(t, http.StatusNotFound, status, name)
	}

	for name, key := range map[string]string{"the coordinator token": coordinator, "a Routine's script": script} {
		status, _, _ := w.use(key, agenttool.ToolRequestConnection, requestArgs(t, "notion:team"))
		assert.Equal(t, http.StatusNotFound, status, name)
	}

	for _, use := range []string{"notion:off", "whatsapp:me", "notion:none"} {
		_, text, isError := w.use(pi, agenttool.ToolRequestConnection, requestArgs(t, use))
		assert.True(t, isError, use)
		assert.Contains(t, text, "none of the accounts", use)
	}

	_, text, isError := w.use(pi, agenttool.ToolRequestConnection, requestArgs(t, "notion:work"))
	assert.False(t, isError, text)
	assert.Contains(t, text, "already", "what it was given needs no card")

	_, text, isError = w.use(pi, agenttool.ToolRequestConnection, requestArgs(t, "notion:team"))
	require.False(t, isError, text)
	assert.True(t, strings.HasPrefix(text, "pending: "), text)
	assert.Contains(t, text, "End your turn")

	_, again, _ := w.use(pi, agenttool.ToolRequestConnection, requestArgs(t, "notion:team"))
	assert.Equal(t, text, again, "the same request")

	pending, err := w.svc.ListConnectionRequests(ctx, orchestration.ConnectionRequestFilter{})
	require.NoError(t, err)
	require.Len(t, pending, 1)

	_, labels := w.get(pi, "notion")
	assert.Equal(t, []string{"work"}, labels)

	_, err = w.svc.DecideConnectionRequest(ctx, pending[0].ID, true)
	require.NoError(t, err)

	_, labels = w.get(pi, "notion")
	assert.ElementsMatch(t, []string{"work", "team"}, labels, "granted at the next call")
	assert.NotContains(t, toolNames(w.tools(pi)), agenttool.ToolRequestConnection, "nothing left to ask for")

	bot, err = w.svc.GetBot(ctx, bot.ID)
	require.NoError(t, err)

	bot.Connections = orchestration.AllowList{Limited: true, Names: []string{"notion:work"}}
	_, err = w.svc.PutBot(ctx, bot)
	require.NoError(t, err)

	_, labels = w.get(pi, "notion")
	assert.Equal(t, []string{"work"}, labels, "taken back on the Bot's page")

	status, _ := w.token(pi, "team")
	assert.Equal(t, http.StatusForbidden, status)
}

// Scenario: the "Tools" block `hypomnema bots prompt` writes names request_connection for a Bot that has a
// Connection to ask for, as its main chat's tools/list does, and not for a Bot whose list is not limited.
func TestTheGuideNamesRequestConnectionWhenThereIsOneToAskFor(t *testing.T) {
	t.Parallel()

	w, _, _ := gatewayWorld(t, false, map[string]string{})
	ctx := t.Context()

	for _, label := range []string{"work", "team"} {
		_, err := w.svc.SaveServiceConnection(ctx, orchestration.Connection{
			Integration: "notion", Label: label, Secret: "s-" + label,
		})
		require.NoError(t, err)
		_, err = w.svc.SetConnectionChats(ctx, "notion", label, true)
		require.NoError(t, err)
	}

	limited, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "hr",
		Connections: orchestration.AllowList{Limited: true, Names: []string{"notion:work"}}})
	require.NoError(t, err)

	every, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "every"})
	require.NoError(t, err)

	assert.Contains(t, w.api.BotTools(ctx, limited), agenttool.ToolRequestConnection)
	assert.NotContains(t, w.api.BotTools(ctx, every), agenttool.ToolRequestConnection)
	assert.Contains(t, agenttool.ToolGuide(w.api.BotTools(ctx, limited)), "request_connection, in your main chat only")
}
