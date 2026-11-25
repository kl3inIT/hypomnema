package agentapi_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Scenario: issue #1142. The Bot is offered waiting_on and why_open waiting. Việt answers in the team's Zalo chat the
// ask the Bot listed for the owner, "em nhận": inbox_update with waiting_on alone moves it to Waiting on others,
// given to Việt and due 48 hours on, the day to chase him. Beside any other field it is refused. inbox_add with
// waiting gives the item to the person waited on, due the same; without waiting_on it is refused.
func TestABotMovesAnItemToWaitingOnOthers(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	c := orchestration.Connection{Integration: "zalo", Label: "main", Account: "Hồng Đạt"}
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

	type property struct {
		Enum       []string       `json:"enum"`
		Properties map[string]any `json:"properties"`
	}

	var update, add map[string]property

	for _, tool := range w.tools(pi) {
		var schema struct {
			Properties map[string]property `json:"properties"`
		}
		require.NoError(t, json.Unmarshal(tool.InputSchema, &schema))

		switch tool.Name {
		case agenttool.ToolInboxUpdate:
			update = schema.Properties
		case agenttool.ToolInboxAdd:
			add = schema.Properties
		}
	}

	require.Contains(t, update, "waiting_on")
	require.Contains(t, add, "waiting_on")
	assert.Contains(t, update["waiting_on"].Properties, "due_date", "a move takes its due")
	assert.Contains(t, add["why_open"].Enum, inboxstore.Waiting)

	zalo := map[string]any{"kind": inboxstore.FromZalo, "chat": "g_team", "account": "main"}
	asked := w.inbox(pi, agenttool.ToolInboxAdd, map[string]any{
		"title": "Sửa slide cho Laura", "source": zalo, "why_open": inboxstore.AskedDirectly,
		"quote": "Anh Đạt sửa giúp em slide?", "message": map[string]any{"kind": "zalo_message", "ref": "m_1"},
		"draft": "Dạ để anh xem.",
	})

	viet := map[string]any{"name": "Việt", "quote": "em nhận",
		"message": map[string]any{"kind": "zalo_message", "ref": "m_2"}, "timezone": "Asia/Ho_Chi_Minh"}

	_, text, failed := w.use(pi, agenttool.ToolInboxUpdate, mustJSON(t, map[string]any{
		"id": asked.ID, "waiting_on": viet, "title": "Việt sửa slide",
	}))
	assert.True(t, failed, text)
	assert.Contains(t, text, "title")

	moved := w.inbox(pi, agenttool.ToolInboxUpdate, map[string]any{"id": asked.ID, "waiting_on": viet})
	assert.Equal(t, "person Việt", moved.Assignee)
	assert.Equal(t, inboxstore.Open, moved.State)
	assert.Equal(t, inboxstore.Waiting, moved.WhyOpen)
	assert.Empty(t, moved.Draft)

	due, err := time.Parse(time.RFC3339, moved.Due)
	require.NoError(t, err, moved.Due)
	assert.WithinDuration(t, time.Now().Add(48*time.Hour), due, time.Minute)
	assert.True(t, strings.HasSuffix(moved.Due, "+07:00"), "in the owner's zone: %s", moved.Due)

	_, text, failed = w.use(pi, agenttool.ToolInboxAdd, mustJSON(t, map[string]any{
		"title": "Lan gửi deck", "source": zalo, "why_open": inboxstore.Waiting, "quote": "em gửi anh deck",
		"message": map[string]any{"kind": "zalo_message", "ref": "m_3"},
	}))
	assert.True(t, failed, "waiting without waiting_on: %s", text)

	promised := w.inbox(pi, agenttool.ToolInboxAdd, map[string]any{
		"title": "Lan gửi deck", "source": zalo, "why_open": inboxstore.Waiting, "quote": "em gửi anh deck",
		"message": map[string]any{"kind": "zalo_message", "ref": "m_3"}, "timezone": "Asia/Ho_Chi_Minh",
		"waiting_on": map[string]any{"name": "Lan", "quote": "em gửi anh deck",
			"message": map[string]any{"kind": "zalo_message", "ref": "m_3"}},
	})
	assert.Equal(t, "person Lan", promised.Assignee)
	assert.Equal(t, inboxstore.Open, promised.State)
	assert.Equal(t, inboxstore.Waiting, promised.WhyOpen)

	due, err = time.Parse(time.RFC3339, promised.Due)
	require.NoError(t, err, promised.Due)
	assert.WithinDuration(t, time.Now().Add(48*time.Hour), due, time.Minute)
}
