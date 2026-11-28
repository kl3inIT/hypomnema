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
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// listedOn is the names an agent's tools route lists.
func (w *world) listedOn(path, key string) []string {
	w.t.Helper()

	status, body := w.call(http.MethodGet, path, key, nil)
	require.Equal(w.t, http.StatusOK, status, string(body))

	var list agenttool.BrowserToolList
	require.NoError(w.t, json.Unmarshal(body, &list))

	return toolNames(list.Data)
}

// Scenario: a worker profile turns the browser off by its family and the
// memory by its name (issue #536, step 7). Its worker finds neither on the
// one list, and each call of them is refused as a tool it never had, before
// it reaches the browser; the refusal is logged. What the profile left on is still there.
func TestAWorkersProfileTurnsToolsOff(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	key := w.workerUnder("tools_off: [agent_browser_*, memory]\n", "laptop", "")

	names := toolNames(w.tools(key))
	assert.NotContains(t, names, "agent_browser_open")
	assert.NotContains(t, names, agenttool.ToolMemory)
	assert.Contains(t, names, agenttool.ToolShowArtifact, "what is not turned off stays")

	status, _, _ := w.use(key, "agent_browser_open", `{"url":"https://example.com"}`)
	assert.Equal(t, http.StatusNotFound, status)

	status, _, _ = w.use(key, agenttool.ToolMemory, `{"target":"owner","action":"add","text":"x"}`)
	assert.Equal(t, http.StatusNotFound, status)

	assert.Empty(t, w.browser.calls, "a tool turned off never reaches the browser")
	assert.Contains(t, w.logs.String(), "agentapi.tool_off")
}

// Scenario: a report Bot adds just the browser's open tool and is refused other browser tools. When the owner turns
// the open tool off instead, the pi's next list and call follow at once,
// without a new pi: the Bot is read at each request. Every tool a pi is
// listed is a name of the catalog its switches are checked against.
func TestABotsToolsAreReadAtEachCall(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, _, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{
		Name:      "vmap",
		AgentSpec: orchestration.AgentSpec{Preset: agenttool.PresetReport, Tools: []string{"agent_browser_open"}},
	})
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, bot.ID, "")
	require.NoError(t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, bot.ID)
	require.NoError(t, err)

	assert.Contains(t, toolNames(w.tools(pi)), "agent_browser_open")
	assert.NotContains(t, toolNames(w.tools(pi)), "agent_browser_click")

	status, _, _ := w.use(pi, "agent_browser_open", `{"url":"https://example.com"}`)
	require.Equal(t, http.StatusOK, status)

	status, _, _ = w.use(pi, "agent_browser_click", `{"selector":"button"}`)
	assert.Equal(t, http.StatusNotFound, status)

	bot.Tools, bot.ToolsOff = nil, []string{"agent_browser_open"}
	_, err = w.svc.PutBot(ctx, bot)
	require.NoError(t, err)

	names := toolNames(w.tools(pi))
	assert.NotContains(t, names, "agent_browser_open")
	assert.Contains(t, names, agenttool.ToolMemory)

	for _, name := range names {
		assert.Contains(t, agenttool.ToolNames(), name, "a listed tool can be switched by its name")
	}

	status, _, _ = w.use(pi, "agent_browser_open", `{"url":"https://example.com"}`)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Len(t, w.browser.calls, 1, "only the call made while the tool was on reached the browser")
}

// Scenario: a Bot turns the phone off by its family: its pi finds no phone
// tool on the phone's route and its call is refused before the phone.
func TestABotTurnsThePhoneOff(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	p := &sharedPhone{}
	withPhone(w, p)

	ctx := t.Context()

	_, _, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "vmap"})
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, bot.ID, "")
	require.NoError(t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, bot.ID)
	require.NoError(t, err)

	// The phone is off by default; only the tools that read a take, which the browser's family names too (issue
	// #1189), are listed on its route.
	assert.ElementsMatch(t, []string{devicetool.VideoStatus, devicetool.VideoList},
		w.listedOn(agenttool.PathDeviceTools, pi))
	bot.Tools = []string{"phone"}
	_, err = w.svc.PutBot(ctx, bot)
	require.NoError(t, err)
	assert.Contains(t, w.listedOn(agenttool.PathDeviceTools, pi), devicetool.Tap)
	bot.ToolsOff = []string{"phone"}
	_, err = w.svc.PutBot(ctx, bot)
	require.NoError(t, err)
	assert.Empty(t, w.listedOn(agenttool.PathDeviceTools, pi))

	status, _ := w.useDevice(pi, devicetool.Tap, `{"target":"#3"}`)
	assert.Equal(t, http.StatusNotFound, status)

	// Filming is under the phone's switch (issue #817).
	status, _ = w.useDevice(pi, devicetool.VideoStart, `{"title":"Sign in"}`)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Empty(t, p.calls)
}
