package agentapi_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// folder stands for the Bots' folders: the files there, by Bot and path.
type folder map[string]bool

func (f folder) Ask(_ context.Context, bot string, req workspacefs.Request) (workspacefs.Result, error) {
	if req.Op != workspacefs.OpRead || !f[bot+"/"+req.Path] {
		return workspacefs.Result{Error: &workspacefs.Error{Code: workspacefs.CodeNotFound, Message: "no file"}}, nil
	}

	return workspacefs.Result{File: &workspacefs.File{Path: req.Path, Bytes: 1}}, nil
}

// Scenario: review round 3 of PR #1243, through the agent API. The owner handed a Bot an ask and commented that he
// sent the report himself; the Bot comments with a path of a file it never made, and closes the item as its own on
// that comment: refused, as is the same on a control plane that cannot read the Bot's folder. A file the Bot made,
// which its folder holds, is its work.
func TestRound3MissingFileIsNotWorkThroughTheAgentAPI(t *testing.T) {
	t.Parallel()

	for _, reads := range []bool{false, true} {
		files := folder{}
		w := newWorldWith(t, 0, func(api *agentapi.Server) {
			if reads {
				api.WithBotFiles(files)
			}
		})
		ctx := t.Context()

		bot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "assistant"})
		require.NoError(t, err)

		files[bot.ID+"/exports/report.xlsx"] = true
		pi, _ := w.inboxPi(bot)
		owner := inboxstore.Actor{Kind: inboxstore.Owner}

		item, err := w.svc.Inbox().Create(ctx, owner, inboxstore.NewTodo{
			Title: "Gửi báo cáo tuần cho Minh", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
			Assignee: inboxstore.Assignee{Kind: inboxstore.ABot, Name: bot.ID},
		})
		require.NoError(t, err)
		w.inbox(pi, agenttool.ToolInboxStart, map[string]any{"id": item.ID})
		_, err = w.svc.Inbox().Comment(ctx, owner, item.ID, "Anh gửi báo cáo rồi.")
		require.NoError(t, err)

		closeOn := func(path string) (string, bool) {
			_, text, failed := w.use(pi, agenttool.ToolInboxComment,
				`{"id":"`+item.ID+`","text":"Báo cáo đây ạ.","files":["`+path+`"]}`)
			require.False(t, failed, text)

			var c struct {
				Revision int `json:"revision"`
			}
			require.NoError(t, json.Unmarshal([]byte(text), &c), text)

			_, text, failed = w.use(pi, agenttool.ToolInboxDone, `{"id":"`+item.ID+`","evidence":[{"kind":"comment",`+
				`"ref":"`+strconv.Itoa(c.Revision)+`"}]}`)

			return text, failed
		}

		text, failed := closeOn("exports/never-made.xlsx")
		assert.True(t, failed, "reads %v: a file never made is no work: %s", reads, text)

		text, failed = closeOn("exports/report.xlsx")
		assert.Equal(t, !reads, failed, "reads %v: the file it made: %s", reads, text)
	}
}
