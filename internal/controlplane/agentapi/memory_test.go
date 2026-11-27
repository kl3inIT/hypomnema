package agentapi_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// tools lists an agent's tools on the control plane's one route.
func (w *world) tools(key string) []agenttool.BrowserTool {
	w.t.Helper()

	status, body := w.call(http.MethodGet, agenttool.PathTools, key, nil)
	require.Equal(w.t, http.StatusOK, status, string(body))

	var list agenttool.BrowserToolList
	require.NoError(w.t, json.Unmarshal(body, &list))

	return list.Data
}

// use runs one of an agent's tools on the control plane's one route and
// gives the tool's text and whether it is an error.
func (w *world) use(key, tool, arguments string) (int, string, bool) {
	w.t.Helper()

	status, body := w.call(http.MethodPost, agenttool.PathToolsCall, key,
		agenttool.BrowserCall{Tool: tool, Arguments: json.RawMessage(arguments)})
	if status != http.StatusOK {
		return status, string(body), false
	}

	var result struct {
		Content []struct{ Text string } `json:"content"`
		IsError bool                    `json:"isError"`
	}

	require.NoError(w.t, json.Unmarshal(body, &result))
	require.NotEmpty(w.t, result.Content)

	return status, result.Content[0].Text, result.IsError
}

func toolNames(tools []agenttool.BrowserTool) []string {
	out := []string{}
	for _, t := range tools {
		out = append(out, t.Name)
	}

	return out
}

// Scenario: a worker's tools server and a Bot's find the browser's tools on
// the control plane's one route, and a call of one reaches the browser in the
// agent's own tab: a worker's its Dispatch's, a Bot's pi's its chat's, so the
// pi that follows goes on in the same tab (issue #536, step 4). The
// coordinator token, which has no tools server, is offered no browser there.
func TestWorkersAndBotsUseTheBrowserOnTheOneRoute(t *testing.T) {
	t.Parallel()

	w, _, _ := gatewayWorld(t, true, map[string]string{"d_1": "codex"})
	ctx := t.Context()

	_, coordinator, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	worker, err := w.svc.AddAgentSession(ctx, "d_1", nil)
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, orchestration.DefaultBotID)
	require.NoError(t, err)

	status, text, _ := w.use(worker, "agent_browser_open", `{"url":"https://go.dev"}`)
	require.Equal(t, http.StatusOK, status, text)

	status, text, _ = w.use(pi, "agent_browser_open", `{"url":"https://go.dev"}`)
	require.Equal(t, http.StatusOK, status, text)

	assert.NotContains(t, toolNames(w.tools(coordinator)), "agent_browser_open")

	status, _, _ = w.use(coordinator, "agent_browser_open", `{"url":"https://go.dev"}`)
	assert.Equal(t, http.StatusNotFound, status)

	assert.Equal(t, []string{
		`d-d_1 agent_browser_open {"url":"https://go.dev"}`,
		`b-` + chat.ID + ` agent_browser_open {"url":"https://go.dev"}`,
	}, w.browser.calls)
	assert.Contains(t, w.logs.String(), "bot "+orchestration.DefaultBotID+" in chat "+chat.ID)
}
