package agentapi_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Scenario (issues #1157, #1188): the main Bot reads the owner's projects in inbox_list, the sidebar's departments in
// its order, adds an item with its priority, the reason and the project by its name, changes the priority with
// inbox_update and lists the project's items. A project it names that is not there is refused, with the names it may
// use.
func TestTheMainBotSetsThePriorityAndTheProjectOfAnItem(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	main, err := w.svc.GetBot(ctx, orchestration.DefaultBotID)
	require.NoError(t, err)

	pi, _ := w.inboxPi(main)

	vmap, err := w.svc.CreateDepartment(ctx, "VMAP", nil)
	require.NoError(t, err)
	_, err = w.svc.CreateDepartment(ctx, "BeyondPilot", nil)
	require.NoError(t, err)

	type view struct {
		ID             string `json:"id"`
		Priority       string `json:"priority"`
		PriorityReason string `json:"priority_reason"`
		Project        string `json:"project"`
	}
	call := func(tool string, args map[string]any) (string, bool) {
		t.Helper()

		raw, err := json.Marshal(args)
		require.NoError(t, err)

		status, text, isError := w.use(pi, tool, string(raw))
		require.Equal(t, http.StatusOK, status, text)

		return text, isError
	}
	read := func(tool string, args map[string]any) view {
		t.Helper()

		text, isError := call(tool, args)
		require.False(t, isError, text)

		var v view
		require.NoError(t, json.Unmarshal([]byte(text), &v), text)

		return v
	}

	text, isError := call(agenttool.ToolInboxList, map[string]any{})
	require.False(t, isError, text)

	var page struct {
		Projects []string `json:"projects"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &page), text)
	assert.Equal(t, []string{"VMAP", "BeyondPilot"}, page.Projects)

	added := read(agenttool.ToolInboxAdd, map[string]any{
		"title": "Trả lời cô Laura", "priority": 1, "priority_reason": "Her deadline is today", "project": "vmap",
	})
	assert.Equal(t, "urgent", added.Priority)
	assert.Equal(t, "Her deadline is today", added.PriorityReason)
	assert.Equal(t, "VMAP", added.Project)

	td, err := w.svc.Inbox().Get(ctx, added.ID)
	require.NoError(t, err)
	assert.Equal(t, vmap.ID, td.Project)

	changed := read(agenttool.ToolInboxUpdate, map[string]any{
		"id": added.ID, "priority": 3, "priority_reason": "She moved it to Friday",
	})
	assert.Equal(t, "medium", changed.Priority)
	assert.Equal(t, "VMAP", changed.Project)

	assert.Equal(t, []string{added.ID}, w.inboxList(pi, map[string]any{"project": "VMAP"}))
	assert.Empty(t, w.inboxList(pi, map[string]any{"project": "none"}))

	text, isError = call(agenttool.ToolInboxAdd, map[string]any{"title": "x", "project": "SecondBite"})
	assert.True(t, isError, text)
	assert.Contains(t, text, "VMAP", "it names the projects there are")

	out := read(agenttool.ToolInboxUpdate, map[string]any{"id": added.ID, "project": "none"})
	assert.Empty(t, out.Project)
}
