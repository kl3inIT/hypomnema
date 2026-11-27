package agentapi_test

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Scenario: issue #1206, W1 and W2. The triage listed Linh's ask to the owner, due today; he answered "dạ để em gửi
// tối nay". The same item becomes his promise with inbox_update: why_open promised, the due he gave, and what makes
// it done; a due he never gave is cleared with due_date none. Any other why_open is refused.
func TestTheTriageMakesTheAskItListedHisPromise(t *testing.T) {
	t.Parallel()

	sw := newSecretaryWorld(t)
	ctx := t.Context()
	pi := sw.wake()
	today := time.Now().In(time.FixedZone("ICT", 7*3600)).Format(time.DateOnly)

	item, err := sw.svc.Inbox().Create(ctx, inboxstore.Actor{Kind: inboxstore.Bot, ID: sw.bot.ID}, inboxstore.NewTodo{
		Title: "Trả lời Linh: gửi file số liệu community", State: inboxstore.Open, WhyOpen: inboxstore.AskedDirectly,
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
		Due:      inboxstore.Due{Kind: inboxstore.DueDate, Date: today, Zone: "Asia/Ho_Chi_Minh"},
		Source: inboxstore.Source{Kind: inboxstore.FromZalo, ID: "u_linh", Account: "me", Quote: "em gửi chị file nhé",
			Ref: &inboxstore.Link{Kind: inboxstore.LinkZaloMessage, Ref: "z_ask"}},
	})
	require.NoError(t, err)

	_, text, failed := sw.use(pi, agenttool.ToolInboxUpdate, `{"id":"`+item.ID+`","why_open":"waiting"}`)
	assert.True(t, failed, "only promised: %s", text)

	got := sw.inbox(pi, agenttool.ToolInboxUpdate, map[string]any{
		"id": item.ID, "why_open": inboxstore.Promised, "due_date": "none", "done_when": "Khi anh gửi file cho Linh",
	})
	assert.Equal(t, inboxstore.Promised, got.WhyOpen)
	assert.Equal(t, inboxstore.Open, got.State)
	assert.Empty(t, got.Due, "the due is cleared")

	stored, err := sw.svc.Inbox().Get(ctx, item.ID)
	require.NoError(t, err)
	assert.Equal(t, inboxstore.DueNone, stored.Due.Kind)
	assert.Equal(t, "Khi anh gửi file cho Linh", stored.DoneWhen)

	evening := sw.inbox(pi, agenttool.ToolInboxUpdate, map[string]any{
		"id": item.ID, "due_at": today + "T19:00:00+07:00", "timezone": "Asia/Ho_Chi_Minh",
	})
	assert.Equal(t, today+"T19:00:00+07:00", evening.Due)
}

// Scenario: issue #1206, the owner's comment of 2026-11-27. The owner handed a Bot the ask it had listed from his
// Lark thread, and then commented on the item that he had sent the answer himself. inbox_get shows his comment's
// revision; the Bot's own close of the item is refused, and it records it done by him with his comment as evidence.
func TestABotRecordsTheOwnersCommentThatHeAnsweredTheAsk(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "assistant"})
	require.NoError(t, err)

	pi, _ := w.inboxPi(bot)
	owner := inboxstore.Actor{Kind: inboxstore.Owner}

	item, err := w.svc.Inbox().Create(ctx, inboxstore.Actor{Kind: inboxstore.Bot, ID: bot.ID}, inboxstore.NewTodo{
		Title: "Trả lời Hà My: tổng hợp dashboard quý 3", State: inboxstore.Open, WhyOpen: inboxstore.AskedDirectly,
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
		Source: inboxstore.Source{Kind: inboxstore.FromLark, ID: "oc_guild", Quote: "anh tổng hợp giúp em các dashboard quý 3 nhé",
			Ref: &inboxstore.Link{Kind: inboxstore.LinkLarkMessage, Ref: "om_ask"}},
	})
	require.NoError(t, err)

	item, err = w.svc.Inbox().Hand(ctx, inboxstore.Change{Actor: owner, ID: item.ID, Revision: item.Revision},
		bot.ID, "")
	require.NoError(t, err)

	w.inbox(pi, agenttool.ToolInboxStart, map[string]any{"id": item.ID})

	his, err := w.svc.Inbox().Comment(ctx, owner, item.ID, "Anh gửi rồi nhé.")
	require.NoError(t, err)

	_, text, failed := w.use(pi, agenttool.ToolInboxGet, `{"id":"`+item.ID+`"}`)
	require.False(t, failed, text)

	var read struct {
		History []map[string]any `json:"history"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &read), text)
	require.NotEmpty(t, read.History)
	assert.InDelta(t, his.Revision, read.History[len(read.History)-1]["revision"], 0, "his comment by its revision")

	_, text, failed = w.use(pi, agenttool.ToolInboxDone,
		`{"id":"`+item.ID+`","evidence":[{"kind":"lark_message","ref":"om_ask"}]}`)
	assert.True(t, failed, "the owner's reply is not the Bot's work: %s", text)
	assert.Contains(t, text, "for_owner")

	done := w.inbox(pi, agenttool.ToolInboxDone, map[string]any{
		"id": item.ID, "for_owner": true, "reason": "Anh Đạt: 'Anh gửi rồi nhé.'",
		"evidence": []map[string]string{{"kind": inboxstore.LinkComment, "ref": strconv.Itoa(his.Revision)}},
	})
	assert.Equal(t, inboxstore.Done, done.State)

	closed, err := w.svc.Inbox().Get(ctx, item.ID)
	require.NoError(t, err)
	require.NotNil(t, closed.DoneBy)
	assert.Equal(t, inboxstore.Me, closed.DoneBy.Kind)
}

// Scenario: review of PR #1243 (W2). A Bot moves an item to Waiting on others with waiting_on's due_date none,
// which is not a day: it is a due left out, so the item is due 48 hours on, the day to chase them, as without one.
func TestReviewWaitingWithNone(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	c := orchestration.Connection{Integration: "zalo", Label: "main", Account: "Owner"}
	c.Secret = "conn-" + c.Integration + "-" + c.Label
	_, err := w.svc.SaveServiceConnection(ctx, c)
	require.NoError(t, err)
	_, err = w.svc.SetConnectionChats(ctx, "zalo", "main", true)
	require.NoError(t, err)

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{
		Name: "hypo", Connections: orchestration.AllowList{Limited: true, Names: []string{"zalo:main"}},
	})
	require.NoError(t, err)

	pi, _ := w.inboxPi(bot)

	item, err := w.svc.Inbox().Create(ctx, inboxstore.Actor{Kind: inboxstore.Bot, ID: bot.ID}, inboxstore.NewTodo{
		Title: "Trả lời Lan: sửa slide", State: inboxstore.Open, WhyOpen: inboxstore.AskedDirectly,
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
		Source: inboxstore.Source{Kind: inboxstore.FromZalo, ID: "team", Account: "main", Quote: "anh sửa giúp em slide",
			Ref: &inboxstore.Link{Kind: inboxstore.LinkZaloMessage, Ref: "z_ask"}},
	})
	require.NoError(t, err)

	got := w.inbox(pi, agenttool.ToolInboxUpdate, map[string]any{"id": item.ID, "waiting_on": map[string]any{
		"name": "Việt", "quote": "em nhận", "message": map[string]string{"kind": "zalo_message", "ref": "z_took"},
		"due_date": "none", "timezone": "Asia/Ho_Chi_Minh",
	}})
	assert.Equal(t, "person Việt", got.Assignee)
	assert.NotEmpty(t, got.Due, "due 48 hours on, the day to chase him")
}
