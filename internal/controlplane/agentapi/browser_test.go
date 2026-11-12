package agentapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// sharedBrowser stands for the browser: it records who called what and
// answers with err when it is set.
type sharedBrowser struct {
	calls []string
	err   error
}

func (b *sharedBrowser) Tools(context.Context) ([]browser.Tool, error) {
	return []browser.Tool{{
		Name: "agent_browser_open", Description: "Open a page.", InputSchema: json.RawMessage(`{"type":"object"}`),
	}}, b.err
}

func (b *sharedBrowser) Call(
	_ context.Context, agent, tool string, arguments json.RawMessage,
) (json.RawMessage, error) {
	b.calls = append(b.calls, fmt.Sprintf("%s %s %s", agent, tool, arguments))

	return json.RawMessage(`{"content":[{"type":"text","text":"Example Domain"}],"isError":false}`), b.err
}

func (w *world) browse(key, tool, arguments string) (int, string) {
	w.t.Helper()

	status, body := w.call(http.MethodPost, agenttool.PathBrowserCall, key,
		agenttool.BrowserCall{Tool: tool, Arguments: json.RawMessage(arguments)})

	return status, string(body)
}

// Scenario: two workers and the coordinator use the browser. Each call
// reaches the browser under a name of the caller's own, so each has its own
// tab: a worker's is its Dispatch's. The tool's answer is passed on as it
// is. Which agent used which tool is logged, and never what it typed.
// Without a credential nothing is listed or run.
func TestEachAgentUsesTheBrowserAsItself(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	first, err := w.svc.AddAgentSession(ctx, "dsp_1", []string{"notion:work"})
	require.NoError(t, err)
	second, err := w.svc.AddAgentSession(ctx, "dsp_2", []string{"notion:work"})
	require.NoError(t, err)
	pi, piKey, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	status, body := w.call(http.MethodGet, agenttool.PathBrowserTools, first, nil)
	require.Equal(t, http.StatusOK, status)

	var tools agenttool.BrowserToolList
	require.NoError(t, json.Unmarshal(body, &tools))
	require.Len(t, tools.Data, 1)
	assert.Equal(t, "agent_browser_open", tools.Data[0].Name)
	assert.JSONEq(t, `{"type":"object"}`, string(tools.Data[0].InputSchema))

	status, answer := w.browse(first, "agent_browser_fill", `{"selector":"#password","text":"hunter2"}`)
	require.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `{"content":[{"type":"text","text":"Example Domain"}],"isError":false}`, answer)

	status, _ = w.browse(second, "agent_browser_get_url", `{}`)
	require.Equal(t, http.StatusOK, status)

	status, _ = w.browse(piKey, "agent_browser_get_url", `{}`)
	require.Equal(t, http.StatusOK, status)

	assert.Equal(t, []string{
		`d-dsp_1 agent_browser_fill {"selector":"#password","text":"hunter2"}`,
		`d-dsp_2 agent_browser_get_url {}`,
		`c-` + pi.ID + ` agent_browser_get_url {}`,
	}, w.browser.calls)

	assert.Contains(t, w.logs.String(), "dsp_1")
	assert.Contains(t, w.logs.String(), "agent_browser_fill")
	assert.NotContains(t, w.logs.String(), "hunter2")

	status, _ = w.call(http.MethodGet, agenttool.PathBrowserTools, "", nil)
	assert.Equal(t, http.StatusUnauthorized, status)

	status, _ = w.browse("", "agent_browser_get_url", `{}`)
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Len(t, w.browser.calls, 3, "a call without a credential does not reach the browser")

	require.NoError(t, w.svc.RevokeAgentSessions(ctx, "dsp_1"))

	status, _ = w.browse(first, "agent_browser_get_url", `{}`)
	assert.Equal(t, http.StatusUnauthorized, status, "a Dispatch that is over has no browser")
}

// An agent is told what it did wrong, and why the browser cannot be used
// when it cannot; a failure that is neither is not told.
func TestABrowserCallThatFailsSaysWhy(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	key, err := w.svc.AddAgentSession(t.Context(), "dsp_1", []string{"notion:work"})
	require.NoError(t, err)

	tests := []struct {
		err    error
		status int
		says   string
	}{
		{fmt.Errorf("%w: agent_browser_close", browser.ErrNoSuchTool), http.StatusNotFound, "agent_browser_close"},
		{fmt.Errorf("%w: open takes an http(s) address", browser.ErrBadArguments), http.StatusBadRequest, "http(s)"},
		{fmt.Errorf("%w: this machine blocks the sandbox", browser.ErrUnavailable), http.StatusServiceUnavailable, "sandbox"},
		{errors.New("disk /home/dat is full"), http.StatusInternalServerError, "try again"},
	}

	for _, tt := range tests {
		w.browser.err = tt.err

		status, body := w.browse(key, "agent_browser_open", `{"url":"example.com"}`)
		assert.Equal(t, tt.status, status, tt.err)
		assert.Contains(t, body, tt.says, tt.err)
	}

	assert.NotContains(t, w.logs.String(), "an agent used the browser", "a call that failed is not logged as made")

	status, body := w.call(http.MethodPost, agenttool.PathBrowserCall, key, map[string]string{"arguments": "x"})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, string(body), "name the tool")
}
