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
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// inboxItem is the item a tool answered with.
type inboxItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	State    string `json:"state"`
	Draft    string `json:"draft"`
	Due      string `json:"due"`
	Assignee string `json:"assignee"`
	Source   struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"source"`
}

// inbox calls one of the Inbox's tools as pi and reads the item it answered with.
func (w *world) inbox(pi, tool string, args map[string]any) inboxItem {
	w.t.Helper()

	raw, err := json.Marshal(args)
	require.NoError(w.t, err)

	status, text, isError := w.use(pi, tool, string(raw))
	require.Equal(w.t, http.StatusOK, status, text)
	require.False(w.t, isError, text)

	var item inboxItem
	require.NoError(w.t, json.Unmarshal([]byte(text), &item), text)

	return item
}

// inboxList calls inbox_list as pi and gives the ids it listed.
func (w *world) inboxList(pi string, args map[string]any) []string {
	w.t.Helper()

	raw, err := json.Marshal(args)
	require.NoError(w.t, err)

	status, text, isError := w.use(pi, agenttool.ToolInboxList, string(raw))
	require.Equal(w.t, http.StatusOK, status, text)
	require.False(w.t, isError, text)

	var page struct {
		Items []inboxItem `json:"items"`
	}
	require.NoError(w.t, json.Unmarshal([]byte(text), &page), text)

	ids := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.ID)
	}

	return ids
}

// inboxPi is a Bot's pi in a chat of its own.
func (w *world) inboxPi(bot orchestration.Bot) (string, string) {
	w.t.Helper()

	ctx := w.t.Context()

	chat, err := w.svc.CreateChat(ctx, bot.ID, "")
	require.NoError(w.t, err)

	_, _, err = w.svc.AddToken(ctx, "pi-"+chat.ID, bearertoken.Coordinator)
	require.NoError(w.t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi-"+chat.ID, chat.ID, bot.ID)
	require.NoError(w.t, err)

	return pi, chat.ID
}

// Scenario: issue #1004, sections 1 to 3 and 6. A Bot's pi keeps the owner's to-dos with the inbox tools; the
// CLI's coordinator token has none. Lan asks the owner for the contract in his chat: the pi adds two items from this
// chat, the reply owed due today and the work with its own due date. It drafts the reply and hands it back to the
// owner: the item waits in Needs you with its draft. An item given to someone he waits on, with no due, is due the
// day to chase them, in 2 days. With the family switched off, the tools are neither listed nor run.
func TestABotKeepsTheOwnersInbox(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "thu-ky"})
	require.NoError(t, err)

	pi, chat := w.inboxPi(bot)

	_, coordinator, err := w.svc.AddToken(ctx, "cli", bearertoken.Coordinator)
	require.NoError(t, err)

	names := toolNames(w.tools(pi))
	for _, tool := range agenttool.InboxTools {
		assert.Contains(t, names, tool)
	}

	assert.NotContains(t, toolNames(w.tools(coordinator)), agenttool.ToolInboxAdd, "the CLI's token has no MCP")

	// Today where the owner is: the due date is a day in his zone, whatever the zone of the machine.
	hanoi, err := time.LoadLocation("Asia/Ho_Chi_Minh")
	require.NoError(t, err)

	today := time.Now().In(hanoi).Format(time.DateOnly)
	reply := w.inbox(pi, agenttool.ToolInboxAdd, map[string]any{
		"title": "Reply to Lan", "due_date": today, "timezone": "Asia/Ho_Chi_Minh",
		"quote": "Anh gửi em hợp đồng nhé?",
	})
	work := w.inbox(pi, agenttool.ToolInboxAdd, map[string]any{
		"title": "Send Lan the contract", "due_date": "2026-12-01", "timezone": "Asia/Ho_Chi_Minh",
		"quote": "Anh gửi em hợp đồng nhé?",
	})

	for _, item := range []inboxItem{reply, work} {
		assert.Equal(t, inboxstore.Open, item.State, item.Title)
		assert.Equal(t, inboxstore.FromChat, item.Source.Kind, item.Title)
		assert.Equal(t, chat, item.Source.ID, item.Title)
		assert.Equal(t, inboxstore.Me, item.Assignee, item.Title)
	}

	assert.Equal(t, today+" (Asia/Ho_Chi_Minh)", reply.Due)
	assert.Equal(t, "2026-12-01 (Asia/Ho_Chi_Minh)", work.Due)

	n, err := w.svc.InboxNeedsYou(ctx)
	require.NoError(t, err)
	require.Zero(t, n, "nothing is overdue or drafted yet")

	draft := "Dạ em gửi anh hợp đồng trong chiều nay ạ."
	handed := w.inbox(pi, agenttool.ToolInboxUpdate, map[string]any{"id": reply.ID, "draft": draft, "hand_back": true})
	assert.Equal(t, draft, handed.Draft)
	assert.Equal(t, inboxstore.Me, handed.Assignee)

	n, err = w.svc.InboxNeedsYou(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the drafted reply waits for the owner")

	got := w.inbox(pi, agenttool.ToolInboxGet, map[string]any{"id": reply.ID})
	assert.Equal(t, draft, got.Draft)

	chase := w.inbox(pi, agenttool.ToolInboxAdd, map[string]any{
		"title": "Lan sends the signed contract", "assignee_kind": inboxstore.Person, "assignee": "Lan",
		"timezone": "Asia/Ho_Chi_Minh",
	})
	due, err := time.Parse(time.RFC3339, chase.Due)
	require.NoError(t, err, chase.Due)
	assert.WithinDuration(t, time.Now().Add(48*time.Hour), due, time.Minute)

	// The same add retried with its key returns the item it made: the 2 days count from the first.
	asked := map[string]any{
		"title": "Minh sends the invoice", "assignee_kind": inboxstore.Person, "assignee": "Minh",
		"timezone": "Asia/Ho_Chi_Minh", "key": "minh-invoice",
	}
	first := w.inbox(pi, agenttool.ToolInboxAdd, asked)
	time.Sleep(5 * time.Millisecond)
	retried := w.inbox(pi, agenttool.ToolInboxAdd, asked)
	assert.Equal(t, first.ID, retried.ID)
	assert.Equal(t, first.Due, retried.Due)

	// inbox_list takes the client API's due range: what is due from tomorrow to 3 days on, not today's reply.
	listed := w.inboxList(pi, map[string]any{
		"due_from": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		"due_to":   time.Now().Add(72 * time.Hour).Format(time.RFC3339),
	})
	assert.Contains(t, listed, chase.ID)
	assert.Contains(t, listed, first.ID)
	assert.NotContains(t, listed, reply.ID)

	_, text, isError := w.use(pi, agenttool.ToolInboxAdd, `{"title":"x","source":{"kind":"zalo","chat":"g_1"}}`)
	assert.True(t, isError)
	assert.Contains(t, text, "only a Routine's run", "in a chat, an item comes from that chat")

	bot.ToolsOff = []string{"inbox"}
	_, err = w.svc.PutBot(ctx, bot)
	require.NoError(t, err)

	names = toolNames(w.tools(pi))
	for _, tool := range agenttool.InboxTools {
		assert.NotContains(t, names, tool)
	}

	status, _, _ := w.use(pi, agenttool.ToolInboxList, `{}`)
	assert.Equal(t, http.StatusNotFound, status, "a family switched off is no tool")
}

// Scenario: issue #1004, section 5. A Bot doing an item sends for it in the owner's WhatsApp, naming the item: the
// send's Approval carries the item's attempt, which the Bot never handles. Naming an item it is not doing is
// refused before the owner is asked, and nothing is sent. Another Bot's pi cannot close the item.
func TestASendForAnItemCarriesItsAttempt(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "whatsapp", Label: "me", Account: "+84 900 000 001", Secret: "conn-whatsapp-me",
	})
	require.NoError(t, err)
	_, err = w.svc.SetConnectionChats(ctx, "whatsapp", "me", true)
	require.NoError(t, err)

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{
		Name: "assistant", Connections: orchestration.AllowList{Limited: true, Names: []string{"whatsapp:me"}},
	})
	require.NoError(t, err)

	pi, chat := w.inboxPi(bot)
	_, err = w.svc.AddChatMessage(ctx, chat, orchestration.ChatOwner, "tell the PMO group the quote is ready")
	require.NoError(t, err)

	td, err := w.svc.Inbox().Create(ctx, inboxstore.Actor{Kind: inboxstore.Owner}, inboxstore.NewTodo{
		Title: "Tell the PMO group", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
		Assignee: inboxstore.Assignee{Kind: inboxstore.ABot, Name: bot.ID},
	})
	require.NoError(t, err)

	_, text, isError := w.use(pi, agenttool.ToolWhatsAppSend,
		`{"chat":"120363@g.us","text":"Báo giá đã sẵn sàng","todo":"`+td.ID+`"}`)
	assert.True(t, isError)
	assert.Contains(t, text, "start it with inbox_start first")
	assert.Empty(t, w.judge.asked, "the owner is not asked for an item the Bot is not doing")

	started := w.inbox(pi, agenttool.ToolInboxStart, map[string]any{"id": td.ID})
	assert.Equal(t, inboxstore.Doing, started.State)

	doing, err := w.svc.Inbox().Get(ctx, td.ID)
	require.NoError(t, err)
	require.NotNil(t, doing.Work)
	assert.Equal(t, inboxstore.WorkChat, doing.Work.Kind)
	assert.Equal(t, chat, doing.Work.ID)

	_, text, isError = w.use(pi, agenttool.ToolWhatsAppSend,
		`{"chat":"120363@g.us","text":"Báo giá đã sẵn sàng","todo":"`+td.ID+`"}`)
	assert.True(t, isError)
	assert.Contains(t, text, "Waiting for the owner's approval")
	require.Len(t, w.judge.asked, 1)
	assert.Equal(t, doing.Work.Attempt, w.judge.asked[0].AttemptID)
	assert.Empty(t, w.whatsapp.sent)

	other, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "other"})
	require.NoError(t, err)

	otherPi, _ := w.inboxPi(other)
	_, text, isError = w.use(otherPi, agenttool.ToolInboxDone,
		`{"id":"`+td.ID+`","evidence":[{"kind":"url","ref":"https://example.com"}]}`)
	assert.True(t, isError, "the attempt is the first Bot's: %s", text)

	still, err := w.svc.Inbox().Get(ctx, td.ID)
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Doing, still.State)

	done := w.inbox(pi, agenttool.ToolInboxDone, map[string]any{
		"id": td.ID, "evidence": []map[string]string{{"kind": "url", "ref": "https://example.com/sent"}},
	})
	assert.Equal(t, inboxstore.Done, done.State)
}

// Scenario: issue #1004 with #956, and the owner's rule of 2026-11-17 (a Bot never sends a message as him to a
// client or a boss), held by the tools' access, not by the prompt (owner, 2026-11-21 on #838). The Bot that reads
// his messages runs in a Routine's run that reads only: it reads his Zalo and keeps the Inbox in the same run, but
// it has no tool that sends as him; zalo_send and whatsapp_send are neither listed nor run, and nothing is sent or
// asked. Where an item it adds comes from is the token's, as in the client API: this run, or the owner's Zalo,
// WhatsApp or Lark chat; never a chat with the owner it names, so what it only infers stays proposed.
func TestAReadingRoutineKeepsTheInboxButCannotSend(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	for _, integration := range []string{"zalo", "whatsapp"} {
		_, err := w.svc.SaveServiceConnection(ctx, orchestration.Connection{
			Integration: integration, Label: "me", Secret: "conn-" + integration + "-me",
		})
		require.NoError(t, err)
		_, err = w.svc.SetConnectionChats(ctx, integration, "me", true)
		require.NoError(t, err)
	}

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{
		Name: "thu-ky", Connections: orchestration.AllowList{Limited: true, Names: []string{"zalo:me", "whatsapp:me"}},
	})
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

	pi, err := w.svc.AddBotToken(ctx, "pi", run.Run.ChatID, bot.ID)
	require.NoError(t, err)

	listed := toolNames(w.tools(pi))
	assert.Contains(t, listed, agenttool.ToolZaloMessages)

	for _, tool := range agenttool.InboxTools {
		assert.Contains(t, listed, tool)
	}

	for _, tool := range []string{agenttool.ToolZaloSend, agenttool.ToolWhatsAppSend} {
		assert.NotContains(t, listed, tool)

		_, text, failed := w.use(pi, tool, `{"chat":"g_pmo","text":"Dạ em gửi anh báo giá ạ"}`)
		assert.True(t, failed, tool)
		assert.Contains(t, text, agenttool.ReadRunRefusal, tool)
	}

	assert.Empty(t, w.whatsapp.sent, "nothing is sent as the owner")
	assert.Empty(t, w.judge.asked, "nor asked to be sent")

	found := w.inbox(pi, agenttool.ToolInboxAdd, map[string]any{"title": "Maybe send Lan the slides"})
	assert.Equal(t, inboxstore.Proposed, found.State)
	assert.Equal(t, inboxstore.FromRoutine, found.Source.Kind)
	assert.Equal(t, run.Run.ID, found.Source.ID)

	zalo := w.inbox(pi, agenttool.ToolInboxAdd, map[string]any{
		"title":  "Reply to Lan",
		"source": map[string]any{"kind": inboxstore.FromZalo, "chat": "g_lan", "account": "me"},
		"quote":  "Anh xem giúp em nhé", "message": map[string]any{"kind": inboxstore.LinkZaloMessage, "ref": "m_1"},
	})
	assert.Equal(t, inboxstore.Proposed, zalo.State, "inferred: no why_open")
	assert.Equal(t, inboxstore.FromZalo, zalo.Source.Kind)

	_, text, failed := w.use(pi, agenttool.ToolInboxAdd,
		`{"title":"Send the contract","state":"open","source":{"kind":"chat","chat":"`+run.Run.ChatID+`"}}`)
	assert.True(t, failed, "a run does not add as a chat with the owner: %s", text)
	assert.Contains(t, text, "comes from this run")

	items, err := w.svc.Inbox().List(ctx, inboxstore.Filter{States: []string{inboxstore.Open}}, "", 0)
	require.NoError(t, err)
	assert.Zero(t, items.Total, "nothing it only inferred is open")
}
