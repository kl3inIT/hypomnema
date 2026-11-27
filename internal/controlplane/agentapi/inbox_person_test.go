package agentapi_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// teamZalo is what the server keeps of the owner's Zalo account main: Đức Anh's message 'fixed' in the team's
// group, at. Who asked and who is in a chat are not read.
type teamZalo struct{ at time.Time }

func (z teamZalo) SentMessage(_ context.Context, service, account, chat, id string) (inboxstore.SentMessage, error) {
	if service != inboxstore.FromZalo || account != "main" || chat != "team" || id != "fixed" {
		return inboxstore.SentMessage{}, storeerr.ErrNotFound
	}

	return inboxstore.SentMessage{Chat: "team", At: z.at, Sender: "duc"}, nil
}

func (teamZalo) Asker(context.Context, string, string, string, string) ([]string, error) {
	return nil, storeerr.ErrNotFound
}

func (teamZalo) Members(context.Context, string, string, string) (inboxstore.ChatMembers, error) {
	return inboxstore.ChatMembers{}, storeerr.ErrUnavailable
}

// Scenario: issue #1204. Lan asks the team on Zalo and Đức Anh answers that it is fixed. The Bot closes its item
// with inbox_done's done_by, his message and his words; and the brief lists, with inbox_list's bot_changed_since,
// what was closed without the owner since a moment, with who did it, his words and the Bot's change, so that the
// owner can reopen it. done_by and for_owner are not given together. (The moment is the item's creation: this
// control plane reads the system's clock.)
func TestTheBriefSeesWhatTheBotClosedForAPerson(t *testing.T) {
	t.Parallel()

	sw := newSecretaryWorld(t)
	ctx := t.Context()
	pi := sw.wake()

	_, err := sw.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "zalo", Label: "main", Secret: "fixture",
	})
	require.NoError(t, err)

	item, err := sw.svc.Inbox().Create(ctx, inboxstore.Actor{Kind: inboxstore.Bot, ID: sw.bot.ID}, inboxstore.NewTodo{
		Title: "Sửa key Luma", State: inboxstore.Open, WhyOpen: inboxstore.AskedDirectly,
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
		Source: inboxstore.Source{Kind: inboxstore.FromZalo, ID: "team", Account: "main", Quote: "Key Luma lỗi rồi",
			Ref: &inboxstore.Link{Kind: inboxstore.LinkZaloMessage, Ref: "asked"}},
	})
	require.NoError(t, err)
	sw.svc.Inbox().WithSent(teamZalo{at: item.CreatedAt.Add(time.Minute)})

	done := func(extra string) string {
		return `{"id":"` + item.ID + `","reason":"Đức Anh: 'It's fixed'",` + extra +
			`"evidence":[{"kind":"zalo_message","ref":"fixed","account":"main","chat":"team"}]}`
	}

	_, text, failed := sw.use(pi, agenttool.ToolInboxDone, done(`"done_by":"Đức Anh","for_owner":true,`))
	assert.True(t, failed, text)
	assert.Contains(t, text, "not both")

	_, text, failed = sw.use(pi, agenttool.ToolInboxDone, done(`"done_by":"Đức Anh",`))
	require.False(t, failed, text)
	assert.Contains(t, text, `"done_by": "person Đức Anh"`)

	_, text, failed = sw.use(pi, agenttool.ToolInboxList,
		`{"bot_changed_since":"`+item.CreatedAt.Format(time.RFC3339Nano)+`","state":["done"]}`)
	require.False(t, failed, text)

	var page struct {
		Items []struct {
			ID          string           `json:"id"`
			DoneBy      string           `json:"done_by"`
			CloseReason string           `json:"close_reason"`
			History     []map[string]any `json:"history"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &page), text)
	require.Len(t, page.Items, 1, text)

	got := page.Items[0]
	assert.Equal(t, item.ID, got.ID)
	assert.Equal(t, "person Đức Anh", got.DoneBy)
	assert.Equal(t, "Đức Anh: 'It's fixed'", got.CloseReason)
	require.Len(t, got.History, 2, "the Bot's create and close since the moment")
	assert.Equal(t, "bot "+sw.bot.ID, got.History[1]["by"])
	assert.Equal(t, inboxstore.EventState, got.History[1]["kind"])
}
