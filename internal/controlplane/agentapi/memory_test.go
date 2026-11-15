package agentapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Scenario: a worker keeps a fact about the owner with its Dispatch's key,
// and the answer says how full that memory is. A change over the limit, a
// target it does not know, and the memory of a Dispatch that does not exist
// are refused in words the worker can act on. Only a worker writes here: the
// owner's own token is refused. The log names the worker, never the fact
// (issue #21).
func TestAWorkerChangesAMemoryWithItsDispatchsKey(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	key, err := w.svc.AddAgentSession(ctx, "dsp_1", nil)
	require.NoError(t, err)

	status, body := w.call(http.MethodPost, agenttool.PathMemory, key,
		agenttool.MemoryChange{Target: "owner", Action: "add", Text: "The owner writes in Vietnamese."})
	require.Equal(t, http.StatusOK, status, string(body))

	var answer agenttool.MemoryAnswer
	require.NoError(t, json.Unmarshal(body, &answer))
	assert.Equal(t, agenttool.MemoryAnswer{Memory: "The owner writes in Vietnamese.", Used: 31, Limit: 1375}, answer)

	status, body = w.call(http.MethodPost, agenttool.PathMemory, key,
		agenttool.MemoryChange{Target: "owner", Action: "add", Text: strings.Repeat("x", 1400)})
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Contains(t, string(body), "over its limit of 1375")

	status, body = w.call(http.MethodPost, agenttool.PathMemory, key,
		agenttool.MemoryChange{Target: "agent", Action: "add", Text: "x"})
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Contains(t, string(body), "dispatch dsp_1")

	status, _ = w.call(http.MethodPost, agenttool.PathMemory, key, agenttool.MemoryChange{Target: "team", Action: "add"})
	assert.Equal(t, http.StatusBadRequest, status)

	_, owner, err := w.svc.AddToken(ctx, "me", bearertoken.Owner)
	require.NoError(t, err)

	status, _ = w.call(http.MethodPost, agenttool.PathMemory, owner,
		agenttool.MemoryChange{Target: "owner", Action: "add", Text: "x"})
	assert.Equal(t, http.StatusForbidden, status)

	assert.Contains(t, w.logs.String(), "dispatch dsp_1")
	assert.NotContains(t, w.logs.String(), "Vietnamese")
}

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

// Scenario: one memory tool for a Bot's pi and a worker (issue #536). Each
// finds it first on the control plane's one list, with the targets it may
// write: a worker its agent's memory and the one about the owner, a Bot's pi
// its own Bot's memory and the one about the owner, as their commands and
// their node's tool wrote before. A change lands where the CLI's would, and
// its result says how full the memory is; a target the agent does not write
// and a change over the limit are refused in words it can act on, and change
// nothing. Agents with no MCP (the coordinator token, a Routine's script) are
// not given the tool and cannot call it. The log names the agent, never the
// fact.
func TestBotsAndWorkersKeepMemoriesWithOneTool(t *testing.T) {
	t.Parallel()

	w, _, _ := gatewayWorld(t, true, map[string]string{"d_claude": "claude"})
	ctx := t.Context()

	_, coordinator, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, orchestration.DefaultBotID)
	require.NoError(t, err)

	worker, err := w.svc.AddAgentSession(ctx, "d_claude", nil)
	require.NoError(t, err)

	piTools := w.tools(pi)
	assert.Equal(t, []string{"memory", "show_artifact", "web_search", "web_fetch", "generate_image"}, toolNames(piTools),
		"a Bot's pi is offered none of the browser's yet")
	assert.Contains(t, string(piTools[0].InputSchema), `"enum":["bot","owner"]`)
	assert.Contains(t, piTools[0].Description, "your own memory, the same in every chat")

	workerTools := w.tools(worker)
	assert.Equal(t, []string{"memory", "show_artifact", "web_search", "web_fetch", "generate_image",
		"agent_browser_open"}, toolNames(workerTools))
	assert.Contains(t, string(workerTools[0].InputSchema), `"enum":["agent","owner"]`)
	assert.Contains(t, workerTools[0].Description, "the agent you run as")

	status, text, isError := w.use(pi, "memory", `{"target":"bot","action":"add","text":"Lan is the designer."}`)
	require.Equal(t, http.StatusOK, status, text)
	assert.False(t, isError)
	assert.Equal(t, "Saved. memory: 20 of 2,200 characters (0%)", text)

	bot, err := w.svc.GetBot(ctx, orchestration.DefaultBotID)
	require.NoError(t, err)
	assert.Equal(t, "Lan is the designer.", bot.Memory, "the Bot's own memory, as `hypomnema memory add` wrote it")

	_, text, isError = w.use(pi, "memory", `{"target":"owner","action":"add","text":"The owner writes in Vietnamese."}`)
	assert.False(t, isError, text)

	_, text, isError = w.use(worker, "memory", `{"target":"owner","action":"replace","match":"Vietnamese",`+
		`"text":"The owner writes in Vietnamese, short."}`)
	assert.False(t, isError, text)
	assert.Equal(t, "Saved. memory: 38 of 1,375 characters (2%)", text)

	owner, err := w.svc.Memories().OwnerMemory(ctx)
	require.NoError(t, err)
	assert.Equal(t, "The owner writes in Vietnamese, short.", owner.Text, "one memory about the owner for both kinds")

	_, text, isError = w.use(pi, "memory", `{"target":"agent","action":"add","text":"x"}`)
	assert.True(t, isError)
	assert.Equal(t, "say the target: bot or owner", text, "a Bot writes no agent's memory")

	_, text, isError = w.use(worker, "memory", `{"target":"bot","action":"add","text":"x"}`)
	assert.True(t, isError)
	assert.Equal(t, "say the target: agent or owner", text)

	_, text, isError = w.use(pi, "memory", `{"target":"bot","action":"add","text":"`+strings.Repeat("x", 2200)+`"}`)
	assert.True(t, isError)
	assert.Contains(t, text, "over its limit of 2200")

	bot, err = w.svc.GetBot(ctx, orchestration.DefaultBotID)
	require.NoError(t, err)
	assert.Equal(t, "Lan is the designer.", bot.Memory, "a refused change changes nothing")

	assert.Empty(t, w.tools(coordinator), "the coordinator token has no MCP")

	status, _, _ = w.use(coordinator, "memory", `{"target":"owner","action":"add","text":"x"}`)
	assert.Equal(t, http.StatusNotFound, status)

	status, _ = w.call(http.MethodGet, agenttool.PathTools, "", nil)
	assert.Equal(t, http.StatusUnauthorized, status)

	assert.Contains(t, w.logs.String(), "bot "+orchestration.DefaultBotID+" in chat "+chat.ID)
	assert.Contains(t, w.logs.String(), "dispatch d_claude")
	assert.NotContains(t, w.logs.String(), "Lan is the designer")
	assert.NotContains(t, w.logs.String(), "Vietnamese")
}

// Scenario: a worker's tools server of this release finds the browser's
// tools on the control plane's one route too, and a call of one reaches the
// browser in the worker's own tab; a Bot's pi calling one is refused, the
// browser not being its yet (issue #536, step 4).
func TestAWorkerUsesTheBrowserOnTheOneRoute(t *testing.T) {
	t.Parallel()

	w, _, _ := gatewayWorld(t, true, map[string]string{"d_1": "codex"})
	ctx := t.Context()

	_, _, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	worker, err := w.svc.AddAgentSession(ctx, "d_1", nil)
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, orchestration.DefaultBotID)
	require.NoError(t, err)

	status, text, _ := w.use(worker, "agent_browser_open", `{"url":"https://go.dev"}`)
	require.Equal(t, http.StatusOK, status, text)

	status, _, _ = w.use(pi, "agent_browser_open", `{"url":"https://go.dev"}`)
	assert.Equal(t, http.StatusNotFound, status)

	assert.Equal(t, []string{`d-d_1 agent_browser_open {"url":"https://go.dev"}`}, w.browser.calls)
}
