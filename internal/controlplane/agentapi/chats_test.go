package agentapi_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// chatSearchAnswer is what chat_search answered: a search's results or a scroll's messages.
type chatSearchAnswer struct {
	Results []struct {
		Chat    string `json:"chat"`
		Where   string `json:"where"`
		Message int64  `json:"message"`
		At      string `json:"at"`
		Speaker string `json:"speaker"`
		Excerpt string `json:"excerpt"`
	} `json:"results"`
	Chat     string `json:"chat"`
	Messages []struct {
		Message int64  `json:"message"`
		Speaker string `json:"speaker"`
		Text    string `json:"text"`
	} `json:"messages"`
}

func (w *world) chatSearch(pi string, args map[string]any) chatSearchAnswer {
	w.t.Helper()

	raw, err := json.Marshal(args)
	require.NoError(w.t, err)

	status, text, isError := w.use(pi, agenttool.ToolChatSearch, string(raw))
	require.Equal(w.t, http.StatusOK, status, text)
	require.False(w.t, isError, text)

	var got chatSearchAnswer
	require.NoError(w.t, json.Unmarshal([]byte(text), &got), text)

	return got
}

// Scenario: issue #805, rows 1, 3 and 6. Bot vmap agreed with the owner in its main chat what its revenue dashboard
// counts. From another chat its pi finds that with chat_search: the main chat's id, the time, who said it and the
// words, and the main Bot's chat about the same dashboard is not among them. Around the hit it reads the chat in
// order. The main Bot finds nothing of vmap's chats, nor reads around them; a worker has no chat_search.
func TestABotSearchesItsOwnEarlierChats(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	vmap, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "vmap", Persona: "You answer about VMAP."})
	require.NoError(t, err)

	main, err := w.svc.CreateChat(ctx, vmap.ID, "")
	require.NoError(t, err)
	_, err = w.svc.SetMainChat(ctx, vmap.ID, main.ID)
	require.NoError(t, err)

	asked, err := w.svc.AddChatMessage(ctx, main.ID, orchestration.ChatOwner, "What should the revenue dashboard count?")
	require.NoError(t, err)
	agreed, err := w.svc.AddChatMessage(ctx, main.ID, orchestration.ChatOwner,
		"The revenue dashboard counts only paid orders, as Lan agreed.")
	require.NoError(t, err)
	noted, err := w.svc.AddChatMessage(ctx, main.ID, orchestration.ChatCoordinator, "Noted: paid orders only.")
	require.NoError(t, err)

	hypo, err := w.svc.CreateChat(ctx, orchestration.DefaultBotID, "")
	require.NoError(t, err)
	_, err = w.svc.AddChatMessage(ctx, hypo.ID, orchestration.ChatOwner, "Hypo, the revenue dashboard counts paid orders.")
	require.NoError(t, err)

	pi, _ := w.inboxPi(vmap)
	hypoPi, _ := w.inboxPi(orchestration.Bot{ID: orchestration.DefaultBotID})

	assert.Contains(t, toolNames(w.tools(pi)), agenttool.ToolChatSearch)
	assert.NotContains(t, toolNames(w.tools(w.workerOf("n1", ""))), agenttool.ToolChatSearch, "a worker has none")

	got := w.chatSearch(pi, map[string]any{"query": "paid orders revenue dashboard"})
	require.Len(t, got.Results, 1, "the other Bot's chat is not searched")
	hit := got.Results[0]
	assert.Equal(t, main.ID, hit.Chat)
	assert.Equal(t, "your main chat", hit.Where)
	assert.Equal(t, agreed.Seq, hit.Message)
	assert.Equal(t, agreed.CreatedAt.UTC().Format(time.RFC3339), hit.At)
	assert.Equal(t, "the owner", hit.Speaker)
	assert.Equal(t, "The revenue dashboard counts only paid orders, as Lan agreed.", hit.Excerpt)

	around := w.chatSearch(pi, map[string]any{"chat": main.ID, "around": hit.Message, "window": 1})
	assert.Equal(t, main.ID, around.Chat)

	var order []int64
	for _, m := range around.Messages {
		order = append(order, m.Message)
	}

	assert.Equal(t, []int64{asked.Seq, agreed.Seq, noted.Seq}, order, "one message on each side, in order")
	assert.Equal(t, "you", around.Messages[2].Speaker)

	assert.Empty(t, w.chatSearch(hypoPi, map[string]any{"query": "Lan agreed"}).Results, "vmap's chats are its own")

	_, text, isError := w.use(hypoPi, agenttool.ToolChatSearch, `{"chat":"`+main.ID+`","around":1}`)
	assert.True(t, isError, "another Bot's chat is not read around: %s", text)
}

// Scenario: issue #1218, row 4 (ARCHITECTURE.md's claims). A Routine's run that reads only is not given chat_search,
// and a call of it is refused as the read run refuses every tool it lacks. A Bot whose chats family is switched off
// neither lists nor runs it; switched back on, it does.
func TestChatSearchIsAbsentInAReadRunAndWhenSwitchedOff(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "thu-ky"})
	require.NoError(t, err)

	_, _, err = w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	routine, err := w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: bot.ID, Name: "read-messages", Kind: routinestore.RoutinePrompt, Prompt: "Read new messages.",
		EveryMinutes: 60, Access: routinestore.AccessRead,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	run, err := w.svc.RunRoutineNow(ctx, routine.ID)
	require.NoError(t, err)

	reader, err := w.svc.AddBotToken(ctx, "pi", run.Run.ChatID, bot.ID)
	require.NoError(t, err)

	assert.NotContains(t, toolNames(w.tools(reader)), agenttool.ToolChatSearch, "a read run")

	_, text, failed := w.use(reader, agenttool.ToolChatSearch, `{"query":"revenue dashboard"}`)
	assert.True(t, failed, text)
	assert.Contains(t, text, agenttool.ReadRunRefusal)

	pi, _ := w.inboxPi(bot)
	require.Contains(t, toolNames(w.tools(pi)), agenttool.ToolChatSearch)

	bot.ToolsOff = []string{"chats"}
	_, err = w.svc.PutBot(ctx, bot)
	require.NoError(t, err)

	assert.NotContains(t, toolNames(w.tools(pi)), agenttool.ToolChatSearch, "switched off")

	status, _, _ := w.use(pi, agenttool.ToolChatSearch, `{"query":"revenue dashboard"}`)
	assert.Equal(t, http.StatusNotFound, status, "a family switched off is no tool")

	bot.ToolsOff = nil
	_, err = w.svc.PutBot(ctx, bot)
	require.NoError(t, err)

	assert.Empty(t, w.chatSearch(pi, map[string]any{"query": "revenue dashboard"}).Results, "switched on, it runs")
}
