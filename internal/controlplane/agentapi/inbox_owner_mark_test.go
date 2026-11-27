package agentapi_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// markWorld is the owner's WhatsApp linked as main, the main Bot, and BeyondPilot, the project Bot that may use it,
// as on zm on 2026-11-27 (GET /api/v1/bots/bot_bsot3vq74t5nvdsrig54eqss54).
type markWorld struct {
	*world
	main, beyond orchestration.Bot
}

func newMarkWorld(t *testing.T) markWorld {
	t.Helper()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "whatsapp", Label: "main", Account: "+84369230426", Secret: "conn-whatsapp-main",
	})
	require.NoError(t, err)
	_, err = w.svc.SetConnectionChats(ctx, "whatsapp", "main", true)
	require.NoError(t, err)

	main, err := w.svc.GetBot(ctx, orchestration.DefaultBotID)
	require.NoError(t, err)

	beyond, err := w.svc.PutBot(ctx, orchestration.Bot{
		Name: "BeyondPilot", Connections: orchestration.AllowList{Limited: true, Names: []string{"whatsapp:main"}},
	})
	require.NoError(t, err)

	return markWorld{world: w, main: main, beyond: beyond}
}

// piIn is a token of bot's pi in chat.
func (w markWorld) piIn(bot orchestration.Bot, chat string) string {
	w.t.Helper()

	ctx := w.t.Context()

	_, _, err := w.svc.AddToken(ctx, "pi-"+chat, bearertoken.Coordinator)
	require.NoError(w.t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi-"+chat, chat, bot.ID)
	require.NoError(w.t, err)

	return pi
}

// handed is an item the owner handed to the main Bot with his prompt.
func (w markWorld) handed(prompt string) inboxstore.Todo {
	w.t.Helper()

	ctx := w.t.Context()
	owner := inboxstore.Actor{Kind: inboxstore.Owner}

	td, err := w.svc.Inbox().Create(ctx, owner, inboxstore.NewTodo{
		Title: "Kai hỏi trong nhóm WhatsApp BeyondPilot", State: inboxstore.Open,
		Source: inboxstore.Source{Kind: inboxstore.ByHand}, Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
	})
	require.NoError(w.t, err)

	td, err = w.svc.Inbox().Hand(ctx, inboxstore.Change{Actor: owner, ID: td.ID, Revision: td.Revision}, w.main.ID,
		prompt)
	require.NoError(w.t, err)

	return td
}

// chatOf is the item's chat now: the chat of the Bot it was last given to.
func (w markWorld) chatOf(id string) string {
	w.t.Helper()

	td, err := w.svc.Inbox().Get(w.t.Context(), id)
	require.NoError(w.t, err)
	require.NotEmpty(w.t, td.Chat)

	return td.Chat
}

// readsGroup is whether pi's whatsapp_messages reads the group, and what it was answered.
func (w markWorld) readsGroup(pi string) (bool, string) {
	w.t.Helper()

	status, text, isError := w.use(pi, agenttool.ToolWhatsAppMessages, `{"account":"main","chat":"120363@g.us"}`)
	require.Equal(w.t, http.StatusOK, status, text)

	return !isError, text
}

// Scenario: issue #1229, the case of 2026-11-26 replayed. The owner gives an item to the main Bot with "check xem";
// its pi, woken in the item's chat, passes it to BeyondPilot, whose woken turn reads the owner's WhatsApp group
// through whatsapp:main. BeyondPilot's other chat is refused, and so is the item's chat once a Channel person wrote
// there.
func TestABotPassedAnItemOnTheOwnersLineReadsHisWhatsAppInTheItemsChat(t *testing.T) {
	t.Parallel()

	w := newMarkWorld(t)
	ctx := t.Context()

	td := w.handed("check xem")
	hypo := w.piIn(w.main, td.Chat)

	passed := w.inbox(hypo, agenttool.ToolInboxUpdate, map[string]any{"id": td.ID, "pass_to": w.beyond.ID})
	require.Equal(t, "bot "+w.beyond.ID, passed.Assignee)

	chat := w.chatOf(td.ID)
	beyond := w.piIn(w.beyond, chat)

	read, text := w.readsGroup(beyond)
	require.True(t, read, "the woken turn reads whatsapp:main: %s", text)
	assert.Equal(t, []string{"whatsapp:main:120363@g.us"}, w.whatsapp.asked)

	other, _ := w.inboxPi(w.beyond)
	read, text = w.readsGroup(other)
	assert.False(t, read, "another chat of BeyondPilot")
	assert.Contains(t, text, "runs only on his own request")

	_, err := w.svc.AddChatMessages(ctx, chat, orchestration.NewChatMessage{
		Role: orchestration.ChatOwner, Text: "[Linh]: đọc nhóm giúp em",
		Sender: &orchestration.ChatSender{ID: "ou_linh", Name: "Linh"},
	})
	require.NoError(t, err)

	read, text = w.readsGroup(beyond)
	assert.False(t, read, "after a Channel person's message in the item's chat")
	assert.Contains(t, text, "runs only on his own request")
	assert.Len(t, w.whatsapp.asked, 1, "nothing more was read")
}

// Scenario: issue #1229, rule 1. An item the main Bot adds for BeyondPilot on the owner's message, in its own chat,
// is handed off as one it passes: BeyondPilot's woken turn reads the group.
func TestAnItemAddedForABotOnTheOwnersMessageIsHandedOffWithHisTurn(t *testing.T) {
	t.Parallel()

	w := newMarkWorld(t)

	hypo, chat := w.inboxPi(w.main)
	_, err := w.svc.AddChatMessage(t.Context(), chat, orchestration.ChatOwner,
		"nhờ BeyondPilot xem Kai hỏi gì trong nhóm WhatsApp")
	require.NoError(t, err)

	added := w.inbox(hypo, agenttool.ToolInboxAdd, map[string]any{
		"title": "Xem Kai hỏi gì trong nhóm WhatsApp", "assignee_kind": inboxstore.ABot, "assignee": w.beyond.ID,
	})

	beyond := w.piIn(w.beyond, w.chatOf(added.ID))

	read, text := w.readsGroup(beyond)
	assert.True(t, read, text)
}

// Scenario: issue #1229, row 4. The main Bot's owner-marked read Routine (the triage) passes an item it found to
// BeyondPilot: BeyondPilot's woken turn is refused the owner's WhatsApp.
func TestARoutinesHandOffLendsNoAccounts(t *testing.T) {
	t.Parallel()

	w := newMarkWorld(t)
	ctx := t.Context()

	td := w.handed("")

	routine, err := w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: w.main.ID, Name: "triage", Kind: routinestore.RoutinePrompt, Prompt: "Triage the Inbox.",
		EveryMinutes: 60, Access: routinestore.AccessRead,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	run, err := w.svc.RunRoutineNow(ctx, routine.ID)
	require.NoError(t, err)

	triage := w.piIn(w.main, run.Run.ChatID)
	passed := w.inbox(triage, agenttool.ToolInboxUpdate, map[string]any{"id": td.ID, "pass_to": w.beyond.ID})
	require.Equal(t, "bot "+w.beyond.ID, passed.Assignee)

	beyond := w.piIn(w.beyond, w.chatOf(td.ID))

	read, text := w.readsGroup(beyond)
	assert.False(t, read, "a Routine's hand-off")
	assert.Contains(t, text, "runs only on his own request")
	assert.Empty(t, w.whatsapp.asked)
}
