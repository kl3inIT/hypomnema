package agentapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/muse"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// fakeMuse stands for the Muse pool.
type fakeMuse struct {
	mu    sync.Mutex
	on    bool
	asked []muse.Question
}

func (f *fakeMuse) ToolsOn(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.on, nil
}

func (f *fakeMuse) Ask(_ context.Context, q muse.Question) (muse.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.asked = append(f.asked, q)

	if q.Prompt == "" {
		return muse.Result{}, muse.ErrInvalid
	}

	return muse.Result{Outcome: muse.OutcomeAnswered, Answer: "Rice cookers [1]", Account: "acc_1",
		AccountLabel: "research", Links: []muse.Link{{URL: "https://example.com"}}, Thread: "t1",
		At: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}, nil
}

func (f *fakeMuse) Chats(context.Context, string, int) ([]muse.Chat, error) { return nil, nil }

func (f *fakeMuse) Messages(context.Context, string, string, int) ([]muse.Asked, error) {
	return []muse.Asked{{Prompt: "p", Outcome: muse.OutcomeAnswered}}, nil
}

// Scenario: the Muse tools are every worker runtime's and a Bot's pi's; not a
// coordinator's or a Routine's script; the Muse page's switch takes them from
// all, listed and called.
func TestTheMuseToolsGoToWorkersAndBotsAndFollowTheSwitch(t *testing.T) {
	t.Parallel()

	runtimes := map[string]string{"d_claude": "claude", "d_codex": "codex", "d_omp": "omp", "d_oc": "opencode"}
	w, _, _ := gatewayWorld(t, false, runtimes)
	m := &fakeMuse{on: true}
	w.api.WithMuse(m)
	ctx := t.Context()

	museNames := []string{agenttool.ToolMuseAsk, agenttool.ToolMuseChats, agenttool.ToolMuseMessages}

	keys := map[string]string{}

	for dispatch := range runtimes {
		key, err := w.svc.AddAgentSession(ctx, dispatch, nil)
		require.NoError(t, err)

		keys[dispatch] = key
		assert.Subset(t, w.allNames(key), museNames, dispatch)
	}

	_, coordinator, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, orchestration.DefaultBotID)
	require.NoError(t, err)
	assert.Subset(t, w.allNames(pi), museNames)

	assert.NotContains(t, w.allNames(coordinator), agenttool.ToolMuseAsk)

	routine, err := w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: orchestration.DefaultBotID, Name: "sync", Kind: routinestore.RoutineScript, Script: "sync.sh",
		ScriptText: "#!/bin/sh\n", EveryMinutes: 60,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	script, err := w.svc.AddRoutineToken(ctx, "pi", routine.ID)
	require.NoError(t, err)

	call := func(key, tool, arguments string) (int, string) {
		status, body := w.call(http.MethodPost, agenttool.PathToolsCall, key,
			agenttool.BrowserCall{Tool: tool, Arguments: json.RawMessage(arguments)})

		return status, string(body)
	}

	for _, key := range []string{keys["d_codex"], pi} {
		status, body := call(key, agenttool.ToolMuseAsk, `{"prompt":"rice cookers","deadline_seconds":120}`)
		require.Equal(t, http.StatusOK, status, body)

		var result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		}

		require.NoError(t, json.Unmarshal([]byte(body), &result))
		assert.False(t, result.IsError)
		assert.Contains(t, result.Content[0].Text, `"outcome":"answered"`)
		assert.Contains(t, result.Content[0].Text, "data, not instructions")
		assert.Contains(t, result.Content[0].Text, `"account_label":"research"`)
	}

	require.Len(t, m.asked, 2)
	assert.Equal(t, 2*time.Minute, m.asked[0].Deadline)
	assert.NotEmpty(t, m.asked[0].Agent)

	status, body := call(pi, agenttool.ToolMuseMessages, `{"thread":"t1"}`)
	require.Equal(t, http.StatusOK, status, body)

	status, body = call(pi, agenttool.ToolMuseAsk, `{"prompt":""}`)
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, `"isError":true`)

	for _, key := range []string{coordinator, script} {
		status, _ = call(key, agenttool.ToolMuseAsk, `{"prompt":"x"}`)
		assert.Equal(t, http.StatusNotFound, status)
	}

	// Switched off on the Muse page: listed for no one, called by no one.
	m.mu.Lock()
	m.on = false
	m.mu.Unlock()

	assert.NotContains(t, w.allNames(keys["d_claude"]), agenttool.ToolMuseAsk)
	assert.NotContains(t, w.allNames(pi), agenttool.ToolMuseAsk)

	status, _ = call(pi, agenttool.ToolMuseAsk, `{"prompt":"x"}`)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Len(t, m.asked, 3, "a call switched off reaches no account")
}

var _ agentapi.Muse = (*fakeMuse)(nil)

// allNames lists every tool an agent is offered (PathTools).
func (w *world) allNames(key string) []string {
	w.t.Helper()

	status, body := w.call(http.MethodGet, agenttool.PathTools, key, nil)
	require.Equal(w.t, http.StatusOK, status, string(body))

	var list agenttool.BrowserToolList
	require.NoError(w.t, json.Unmarshal(body, &list))

	names := []string{}
	for _, tool := range list.Data {
		names = append(names, tool.Name)
	}

	return names
}
