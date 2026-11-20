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

	today := time.Now().Format(time.DateOnly)
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
