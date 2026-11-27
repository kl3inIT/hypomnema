package agentapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// fullBrowser is the shared browser with its whole list, agenttool.BrowserTools, as the engine offers it
// (browser.offered; browser TestCatalogNeedsEveryOfferedTool), recording each call by the tab it runs in.
type fullBrowser struct{ sharedBrowser }

func (b *fullBrowser) Tools(context.Context) ([]browser.Tool, error) {
	out := make([]browser.Tool, 0, len(agenttool.BrowserTools))
	for _, name := range agenttool.BrowserTools {
		out = append(out, browser.Tool{Name: name, Description: "The browser's " + name + ".",
			InputSchema: json.RawMessage(`{"type":"object"}`)})
	}

	return out, nil
}

// The tools each agent kind is offered with every engine wired, in the order each is listed: the control plane's
// own, then the phone's on its own route. A tool missing from a list, or one listed that is not here, fails.
var (
	expectedPiTools = slices.Concat([]string{
		"memory", "show_artifact", "routines", "chat_search", "inbox_list", "inbox_get", "inbox_add", "inbox_update",
		"inbox_start", "inbox_release", "inbox_done", "inbox_comment", "inbox_drop", "meeting_list", "meeting_get",
		"web_search", "web_fetch", "generate_image", "audio_transcribe", "audio_speak", "muse_ask", "muse_chats",
		"muse_messages", "whatsapp_chats", "whatsapp_messages", "whatsapp_media", "whatsapp_history", "whatsapp_send",
		"zalo_chats", "zalo_messages", "zalo_media", "zalo_history", "zalo_send", "node_exec", "node_process",
		"node_process_kill", "browser_video_start", "browser_video_say", "browser_video_stop",
	}, agenttool.BrowserTools)
	expectedWorkerTools = slices.Concat([]string{
		"memory", "show_artifact", "web_search", "web_fetch", "generate_image", "audio_transcribe", "audio_speak",
		"muse_ask", "muse_chats", "muse_messages", "node_exec", "node_process", "node_process_kill",
		"browser_video_start", "browser_video_say", "browser_video_stop",
	}, agenttool.BrowserTools)
	// The coordinator token and a Routine's script have no MCP and no other tool of the list, but each keeps a
	// tab of its own in the shared browser, as the browser's own routes gave them before issue #1220.
	expectedTabTools = agenttool.BrowserTools
)

// Scenario (issue #1220, owner 2026-11-27; review round 1 of PR #1236): the old per-family routes are gone, and no
// tool went with them. With every engine wired, a Bot's main pi, a worker, the coordinator token and a Routine's
// script list their tools on the routes the current tools servers use (GET /agent/v1/tools, and the phone's on
// its own), each list exactly the one expected; every tool listed is called there and answered by its own handler:
// a tool's result (MCP content) or the handler's refusal of the empty arguments, never a 404 or a server error. A
// browser or phone tool reaches its engine in the caller's own tab or as the caller, and no other tool falls
// through to the browser.
func TestEveryListedToolIsReachedOnTheCurrentRoutes(t *testing.T) {
	t.Parallel()

	engine := &fullBrowser{}
	phone := &sharedPhone{}
	w := newWorldWith(t, 0, func(s *agentapi.Server) {
		s.WithBrowser(engine).WithAudio(&fakeAudio{}).WithWeb(&fakeWeb{}).WithImages(&fakeImages{}).
			WithDevice(phone).WithMuse(&fakeMuse{on: true}).WithBrowserTakes(&filmingBrowser{}).
			WithMeetings(recordedMeetings{}).
			WithNodeExec(&machines{}, &machines{}, func(context.Context, orchestration.Node) (string, error) {
				return "", nil
			}).
			WithRuntimes(func(context.Context, string) (string, error) { return "claude", nil })
	})
	ctx := t.Context()

	for _, integration := range []string{"whatsapp", "zalo"} {
		w.connect(orchestration.Connection{Integration: integration, Label: "me", Account: "+84 900 000 001",
			Secret: "conn-" + integration + "-me"})
	}

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)
	token, coordinator, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)
	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, orchestration.DefaultBotID)
	require.NoError(t, err)

	routine, err := w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: orchestration.DefaultBotID, Name: "sync", Kind: routinestore.RoutineScript, Script: "sync.sh",
		ScriptText: "#!/bin/sh\n", EveryMinutes: 60,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)
	_, err = w.svc.RunRoutineNow(ctx, routine.ID) // a script's token is its live run's (#1023)
	require.NoError(t, err)
	script, err := w.svc.AddRoutineToken(ctx, "pi", routine.ID)
	require.NoError(t, err)

	worker := w.workerUnder("tools: [phone]\n", "laptop", chat.ID)

	// The owner's turn in the pi's chat, and what his accounts hold, so that the reads of WhatsApp and Zalo get past
	// the owner's-turn gate and reach their handlers with what they need (review round 2 of PR #1236).
	_, err = w.svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "what did the PMO group say?")
	require.NoError(t, err)

	note := filepath.Join(t.TempDir(), "quote.txt")
	require.NoError(t, os.WriteFile(note, []byte("the quote"), 0o600))
	w.whatsapp.files = map[string]messaging.Media{"m1": {Path: note, MimeType: "text/plain", FileName: "quote.txt"}}

	reads := map[string]string{}
	for _, service := range []string{"whatsapp", "zalo"} {
		reads[service+"_chats"] = `{}`
		reads[service+"_messages"] = `{"chat":"120363@g.us"}`
		reads[service+"_media"] = `{"chat":"120363@g.us","message":"m1"}`
		reads[service+"_history"] = `{"chat":"120363@g.us","count":10}`
	}

	var allPhone []string
	for _, d := range devicetool.Tools() {
		allPhone = append(allPhone, d.Name)
	}

	for _, agent := range []struct {
		name, key, tab string
		tools, phone   []string
	}{
		// A Bot's phone is off unless its Bot turns it on: its pi keeps the list of its own takes (#851).
		{"pi", pi, "b-" + chat.ID, expectedPiTools, []string{devicetool.VideoStatus, devicetool.VideoList}},
		{"worker", worker, "d-", expectedWorkerTools, allPhone},
		// The phone's own route serves the coordinator token and a Routine's script as it did before (device.go).
		{"coordinator", coordinator, "c-" + token.ID, expectedTabTools, allPhone},
		{"script", script, "r-" + routine.ID, expectedTabTools, allPhone},
	} {
		assert.Equal(t, agent.tools, toolNames(w.tools(agent.key)), "%s's one list", agent.name)

		onPhone := w.listedOn(agenttool.PathDeviceTools, agent.key)
		assert.ElementsMatch(t, agent.phone, onPhone, "%s's phone list", agent.name)

		for _, tool := range slices.Concat(agent.tools, onPhone) {
			path := agenttool.PathToolsCall
			if devicetool.Routed(tool) {
				path = agenttool.PathDeviceCall
			}

			arguments, read := reads[tool]
			if !read {
				arguments = `{}`
			}

			tabs, phones := len(engine.calls), len(phone.calls)
			status, body := w.call(http.MethodPost, path, agent.key,
				agenttool.BrowserCall{Tool: tool, Arguments: json.RawMessage(arguments)})

			answered(t, agent.name+"'s "+tool, status, body)

			if read {
				var result struct {
					IsError bool `json:"isError"`
				}
				require.NoError(t, json.Unmarshal(body, &result), "%s: %s", tool, body)
				assert.False(t, result.IsError, "%s reads on the owner's turn, past the gate: %s", tool, body)
			}

			switch {
			case strings.HasPrefix(tool, "agent_browser_"):
				require.Len(t, engine.calls, tabs+1, "%s reaches the browser", tool)
				assert.True(t, strings.HasPrefix(engine.calls[tabs], agent.tab), "%s runs in the caller's tab: %s",
					tool, engine.calls[tabs])
			case path == agenttool.PathDeviceCall && strings.HasPrefix(tool, devicetool.Prefix):
				require.Len(t, phone.calls, phones+1, "%s reaches the phone", tool)
				assert.Len(t, engine.calls, tabs)
			default:
				assert.Len(t, engine.calls, tabs, "%s is its own handler's, not the browser's", tool)
			}
		}
	}
}

// answered checks that a tool's own handler answered a call with empty arguments: its result, MCP content, or its
// refusal of the arguments in words. A route or a tool that is not there answers 404, which fails here.
func answered(t *testing.T, tool string, status int, body []byte) {
	t.Helper()

	switch status {
	case http.StatusOK:
		var result struct {
			Content []json.RawMessage `json:"content"`
		}
		require.NoError(t, json.Unmarshal(body, &result), "%s answers a tool's result: %s", tool, body)
		assert.NotEmpty(t, result.Content, "%s answers a tool's result: %s", tool, body)
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		assert.NotEmpty(t, strings.TrimSpace(string(body)), "%s says what is wrong with its arguments", tool)
	default:
		assert.Failf(t, "the tool's handler did not answer", "%s answered %d: %s", tool, status, body)
	}

	assert.NotContains(t, string(body), "has no tool", tool)
}
