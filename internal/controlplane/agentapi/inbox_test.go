package agentapi_test

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
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
	PassedBy string `json:"passed_by"`
	WhyOpen  string `json:"why_open"`
	Source   struct {
		Kind    string           `json:"kind"`
		ID      string           `json:"id"`
		Account string           `json:"account"`
		Message *inboxstore.Link `json:"message"`
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

// heldClock is a clock that stands still until the test moves it; the server's goroutines read it.
type heldClock struct{ at atomic.Int64 }

func newHeldClock(at time.Time) *heldClock {
	c := &heldClock{}
	c.at.Store(at.UnixNano())

	return c
}

func (c *heldClock) Now() time.Time { return time.Unix(0, c.at.Load()).UTC() }

func (c *heldClock) Add(d time.Duration) { c.at.Add(int64(d)) }

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

	// The control plane's clock, held: 17:59 UTC is 00:59 of the next day where the owner is (Asia/Ho_Chi_Minh), the
	// moment a check of today in UTC failed (issue #1194), and the work's due date below stays ahead of it.
	clock := newHeldClock(time.Date(2026, 11, 20, 17, 59, 0, 0, time.UTC))
	w := newWorldAt(t, clock)
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

	today := clock.Now().In(hanoi).Format(time.DateOnly)
	require.Equal(t, "2026-11-21", today)
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
	assert.Equal(t, clock.Now().Add(48*time.Hour).Unix(), due.Unix())

	// The same add retried with its key returns the item it made: the 2 days count from the first.
	asked := map[string]any{
		"title": "Minh sends the invoice", "assignee_kind": inboxstore.Person, "assignee": "Minh",
		"timezone": "Asia/Ho_Chi_Minh", "key": "minh-invoice",
	}
	first := w.inbox(pi, agenttool.ToolInboxAdd, asked)
	clock.Add(time.Hour)
	retried := w.inbox(pi, agenttool.ToolInboxAdd, asked)
	assert.Equal(t, first.ID, retried.ID)
	assert.Equal(t, first.Due, retried.Due)

	// inbox_list takes the client API's due range: what is due from tomorrow to 3 days on, not today's reply.
	listed := w.inboxList(pi, map[string]any{
		"due_from": clock.Now().Add(24 * time.Hour).Format(time.RFC3339),
		"due_to":   clock.Now().Add(72 * time.Hour).Format(time.RFC3339),
	})
	assert.Contains(t, listed, chase.ID)
	assert.Contains(t, listed, first.ID)
	assert.NotContains(t, listed, reply.ID)

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

	// A link is no work of the Bot's; the message its send recorded on the item is (issue #1206).
	_, text, isError = w.use(pi, agenttool.ToolInboxDone,
		`{"id":"`+td.ID+`","evidence":[{"kind":"url","ref":"https://example.com/sent"}]}`)
	assert.True(t, isError, text)

	w.judge.approved = true
	_, text, isError = w.use(pi, agenttool.ToolWhatsAppSend,
		`{"chat":"120363@g.us","text":"Báo giá đã sẵn sàng","todo":"`+td.ID+`"}`)
	require.False(t, isError, text)

	done := w.inbox(pi, agenttool.ToolInboxDone, map[string]any{
		"id": td.ID, "evidence": []map[string]string{{"kind": "whatsapp_message", "ref": "sent-1", "account": "me",
			"chat": "120363@g.us"}},
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

// Scenario: issue #1111, section 2. The owner hands an item to a Bot; its pi posts its progress in the item's thread
// with inbox_comment, which the owner reads with the item's history. Another Bot's pi cannot post on it.
func TestABotPostsItsProgressInTheItemsThread(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "thu-ky"})
	require.NoError(t, err)

	other, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "other"})
	require.NoError(t, err)

	pi, _ := w.inboxPi(bot)
	otherPi, _ := w.inboxPi(other)

	owner := inboxstore.Actor{Kind: inboxstore.Owner}
	td, err := w.svc.Inbox().Create(ctx, owner, inboxstore.NewTodo{
		Title: "Fix the login page", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
	})
	require.NoError(t, err)

	_, err = w.svc.Inbox().Hand(ctx, inboxstore.Change{Actor: owner, ID: td.ID, Revision: td.Revision}, bot.ID,
		"Give it to a worker.")
	require.NoError(t, err)

	w.inbox(pi, agenttool.ToolInboxComment, map[string]any{"id": td.ID, "text": "Started: Run run_1 fixes it."})

	got, err := w.svc.Inbox().Get(ctx, td.ID)
	require.NoError(t, err)

	last := got.Events[len(got.Events)-1]
	assert.Equal(t, inboxstore.EventCommented, last.Kind)
	assert.Equal(t, inboxstore.Actor{Kind: inboxstore.Bot, ID: bot.ID}, last.Actor)
	assert.JSONEq(t, `{"text":"Started: Run run_1 fixes it."}`, string(last.After))

	status, text, isError := w.use(otherPi, agenttool.ToolInboxComment,
		`{"id":"`+td.ID+`","text":"Mine now."}`)
	assert.Equal(t, http.StatusOK, status, text)
	assert.True(t, isError, "another Bot's item: %s", text)
}

// Scenario: issue #1207. The owner hands an item to a Bot; its pi exports a sheet and shows an Artifact, and posts
// them on the item with inbox_comment: the comment keeps the file's path in the Bot's folder, read through the
// item's own chat, and the Artifact's id. An Artifact another Bot showed, and a file outside the folder, are refused.
func TestABotPostsTheWorksFilesAndArtifactsOnTheItem(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "thu-ky"})
	require.NoError(t, err)

	other, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "other"})
	require.NoError(t, err)

	otherPi, otherChat := w.inboxPi(other)

	owner := inboxstore.Actor{Kind: inboxstore.Owner}
	td, err := w.svc.Inbox().Create(ctx, owner, inboxstore.NewTodo{
		Title: "Export the October sales", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
	})
	require.NoError(t, err)

	td, err = w.svc.Inbox().Hand(ctx, inboxstore.Change{Actor: owner, ID: td.ID, Revision: td.Revision}, bot.ID, "")
	require.NoError(t, err)
	require.NotEmpty(t, td.Chat)

	// The Bot's pi in the item's own chat.
	_, _, err = w.svc.AddToken(ctx, "pi-"+td.Chat, bearertoken.Coordinator)
	require.NoError(t, err)
	pi, err := w.svc.AddBotToken(ctx, "pi-"+td.Chat, td.Chat, bot.ID)
	require.NoError(t, err)

	show := func(pi string) string {
		_, text, isError := w.use(pi, agenttool.ToolShowArtifact,
			arguments(t, agenttool.ArtifactCall{Title: "October sales", HTML: "<h1>Sales</h1>"}))
		require.False(t, isError, text)

		return shownID(t, text)
	}
	mine, theirs := show(pi), show(otherPi)

	w.inbox(pi, agenttool.ToolInboxComment, map[string]any{
		"id": td.ID, "text": "Done: the export and its chart.",
		"files": []string{"./exports/sales.xlsx"}, "artifacts": []string{mine},
	})

	got, err := w.svc.Inbox().Get(ctx, td.ID)
	require.NoError(t, err)

	last := got.Events[len(got.Events)-1]
	assert.Equal(t, inboxstore.EventCommented, last.Kind)
	assert.JSONEq(t, `{"text":"Done: the export and its chart.","files":["exports/sales.xlsx"],"artifacts":["`+
		mine+`"],"chat":"`+td.Chat+`"}`, string(last.After))

	for name, args := range map[string]map[string]any{
		"another Bot's Artifact": {"id": td.ID, "text": "Theirs.", "artifacts": []string{theirs}},
		"no such Artifact":       {"id": td.ID, "text": "None.", "artifacts": []string{"art_none"}},
		"a file outside":         {"id": td.ID, "text": "Outside.", "files": []string{"../other/secret.txt"}},
		"an absolute path":       {"id": td.ID, "text": "Absolute.", "files": []string{"/etc/passwd"}},
	} {
		raw, err := json.Marshal(args)
		require.NoError(t, err)

		status, text, isError := w.use(pi, agenttool.ToolInboxComment, string(raw))
		assert.Equal(t, http.StatusOK, status, text)
		assert.True(t, isError, "%s: %s", name, text)
	}

	after, err := w.svc.Inbox().Get(ctx, td.ID)
	require.NoError(t, err)
	assert.Len(t, after.Events, len(got.Events), "nothing refused is posted")

	// An item the other Bot added in its chat has no chat of its own: its files are read through the Bot's chat.
	added := w.inbox(otherPi, agenttool.ToolInboxAdd, map[string]any{"title": "Send the deck", "state": "open"})
	w.inbox(otherPi, agenttool.ToolInboxComment, map[string]any{"id": added.ID, "text": "The deck.",
		"files": []string{"deck.pdf"}})

	theirItem, err := w.svc.Inbox().Get(ctx, added.ID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"text":"The deck.","files":["deck.pdf"],"chat":"`+otherChat+`"}`,
		string(theirItem.Events[len(theirItem.Events)-1].After))

	// Review of PR #1210: the owner deletes the item's chat. The files are still in the Bot's folder, which any of
	// its chats reads: the comment's files are read through the chat of that Bot still there.
	kept, err := w.svc.CreateChat(ctx, bot.ID, "")
	require.NoError(t, err)
	_, err = w.svc.DeleteChat(ctx, td.Chat)
	require.NoError(t, err)

	gone, err := w.svc.Inbox().Get(ctx, td.ID)
	require.NoError(t, err)

	var read struct {
		Chat  string   `json:"chat"`
		Files []string `json:"files"`
	}
	require.NoError(t, json.Unmarshal(gone.Events[len(got.Events)-1].After, &read))
	assert.Equal(t, kept.ID, read.Chat, "read through the Bot's chat still there")
	assert.Equal(t, []string{"exports/sales.xlsx"}, read.Files)
}

// Scenario: issue #1118. The main Bot's pi passes an item the owner handed it to a project Bot with inbox_update
// pass_to, and reads through whom that Bot has it; the project Bot's pi passes it to no other project Bot, and gives
// it back to the main Bot the same way.
func TestTheMainBotPassesAnItemWithTheUpdateTool(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	main, err := w.svc.GetBot(ctx, orchestration.DefaultBotID)
	require.NoError(t, err)

	beyond, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "BeyondPilot"})
	require.NoError(t, err)

	vmap, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "VMAP"})
	require.NoError(t, err)

	mainPi, _ := w.inboxPi(main)
	beyondPi, _ := w.inboxPi(beyond)

	owner := inboxstore.Actor{Kind: inboxstore.Owner}
	td, err := w.svc.Inbox().Create(ctx, owner, inboxstore.NewTodo{
		Title: "Send Kai the pilot's report", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
	})
	require.NoError(t, err)

	_, err = w.svc.Inbox().Hand(ctx, inboxstore.Change{Actor: owner, ID: td.ID, Revision: td.Revision}, main.ID, "")
	require.NoError(t, err)

	passed := w.inbox(mainPi, agenttool.ToolInboxUpdate, map[string]any{"id": td.ID, "pass_to": beyond.ID})
	assert.Equal(t, "bot "+beyond.ID, passed.Assignee)
	assert.Equal(t, "bot "+main.ID, passed.PassedBy)

	status, text, isError := w.use(beyondPi, agenttool.ToolInboxUpdate,
		`{"id":"`+td.ID+`","pass_to":"`+vmap.ID+`"}`)
	assert.Equal(t, http.StatusOK, status, text)
	assert.True(t, isError, "a project Bot passes to no other project Bot: %s", text)

	back := w.inbox(beyondPi, agenttool.ToolInboxUpdate, map[string]any{"id": td.ID, "pass_to": main.ID})
	assert.Equal(t, "bot "+main.ID, back.Assignee)
	assert.Equal(t, "bot "+beyond.ID, back.PassedBy)

	status, text, isError = w.use(mainPi, agenttool.ToolInboxUpdate,
		`{"id":"`+td.ID+`","pass_to":"`+vmap.ID+`","hand_back":true}`)
	assert.Equal(t, http.StatusOK, status, text)
	assert.True(t, isError, "an item goes to one place: %s", text)
}

// Scenario: issue #1124, section 1, as seen on zm on 2026-11-23. The owner's Zalo and WhatsApp are linked as "main":
// the Connections record his Zalo name and his phone number, and the sessions held know his Zalo id and his
// WhatsApp number's JID. The Bot names the account as it saw it: its label, or the id of the account, the Zalo id
// or the phone number, with or without its +. Each is accepted and the item keeps the label, which the read tools
// name in their answers, so the Bot copies the right one. An id of an account the Bot was not given is refused.
func TestASourceNamedByTheAccountsIDKeepsItsLabel(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	w.whatsapp.ids = map[string]string{
		"zalo:main": "2223515701293677881", "whatsapp:main": "84369230426:12@s.whatsapp.net",
		"zalo:work": "1111111111111111111",
	}

	for _, c := range []orchestration.Connection{
		{Integration: "zalo", Label: "main", Account: "Hồng Đạt"},
		{Integration: "whatsapp", Label: "main", Account: "+84369230426"},
		{Integration: "zalo", Label: "work", Account: "Đạt VTII"},
	} {
		c.Secret = "conn-" + c.Integration + "-" + c.Label
		_, err := w.svc.SaveServiceConnection(ctx, c)
		require.NoError(t, err)
		_, err = w.svc.SetConnectionChats(ctx, c.Integration, c.Label, true)
		require.NoError(t, err)
	}

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{
		Name: "hypo", Connections: orchestration.AllowList{Limited: true, Names: []string{"zalo:main", "whatsapp:main"}},
	})
	require.NoError(t, err)

	pi, chat := w.inboxPi(bot)

	for name, src := range map[string]struct{ kind, chat, account string }{
		"zalo by its id":               {inboxstore.FromZalo, "g_laura", "2223515701293677881"},
		"zalo by its label":            {inboxstore.FromZalo, "g_laura", "main"},
		"whatsapp by its label":        {inboxstore.FromWhatsApp, "8491@s.whatsapp.net", "main"},
		"whatsapp by its number":       {inboxstore.FromWhatsApp, "8491@s.whatsapp.net", "84369230426"},
		"whatsapp by its number and +": {inboxstore.FromWhatsApp, "8491@s.whatsapp.net", "+84369230426"},
	} {
		item := w.inbox(pi, agenttool.ToolInboxAdd, map[string]any{
			"title":   "Reply: " + name,
			"source":  map[string]any{"kind": src.kind, "chat": src.chat, "account": src.account},
			"quote":   "Anh xem giúp em nhé",
			"message": map[string]any{"kind": src.kind + "_message", "ref": "m_1"},
		})
		assert.Equal(t, src.kind, item.Source.Kind, name)
		assert.Equal(t, "main", item.Source.Account, "%s: the item keeps the label", name)
	}

	_, text, failed := w.use(pi, agenttool.ToolInboxAdd, mustJSON(t, map[string]any{
		"title":   "Reply to the team",
		"source":  map[string]any{"kind": inboxstore.FromZalo, "chat": "g_team", "account": "1111111111111111111"},
		"message": map[string]any{"kind": inboxstore.LinkZaloMessage, "ref": "m_2"},
	}))
	assert.True(t, failed, "the id of an account it was not given: %s", text)

	// With no matching account or session ID, the phone number is not a label.
	_, err = w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "whatsapp", Label: "main", Account: "+84911111111", Secret: "conn-whatsapp-main",
	})
	require.NoError(t, err)
	delete(w.whatsapp.ids, "whatsapp:main")

	_, text, failed = w.use(pi, agenttool.ToolInboxAdd, `{"title":"Reply to Kai",
		"source":{"kind":"whatsapp","chat":"8491@s.whatsapp.net","account":"84369230426"},
		"message":{"kind":"whatsapp_message","ref":"m_3"}}`)
	assert.True(t, failed, text)
	assert.Contains(t, text, "84369230426")
	assert.Contains(t, text, "main")

	_, err = w.svc.AddChatMessage(ctx, chat, orchestration.ChatOwner, "what is new on Zalo?")
	require.NoError(t, err)

	for _, tool := range []string{agenttool.ToolZaloChats, agenttool.ToolWhatsAppMessages} {
		_, text, failed := w.use(pi, tool, `{"chat":"g_laura"}`)
		require.False(t, failed, text)

		var answer struct {
			Account string `json:"account"`
		}
		require.NoError(t, json.Unmarshal([]byte(text), &answer), text)
		assert.Equal(t, "main", answer.Account, "%s names the account by its label", tool)
	}
}
