package agentapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// Scenario (issue #1220, owner 2026-11-27): the old per-family routes are gone, and no tool went with them. With
// every engine wired, a Bot's main pi and a worker list their tools on the routes the current tools servers use
// (GET /agent/v1/tools, and the phone's on its own), and every tool listed is called there and reaches its own
// handler: none answers "this agent has no tool", and none but the browser's falls through to the browser. The web's,
// image, audio, Muse, node, phone, take, memory, artifact, Routine, chat, Inbox and WhatsApp tools are among them.
func TestEveryListedToolIsReachedOnTheCurrentRoutes(t *testing.T) {
	t.Parallel()

	w := newWorldWith(t, 0, func(s *agentapi.Server) {
		s.WithAudio(&fakeAudio{}).WithWeb(&fakeWeb{}).WithImages(&fakeImages{}).WithDevice(&sharedPhone{}).
			WithMuse(&fakeMuse{on: true}).WithBrowserTakes(&filmingBrowser{}).WithMeetings(recordedMeetings{}).
			WithNodeExec(&machines{}, &machines{}, func(context.Context, orchestration.Node) (string, error) {
				return "", nil
			}).
			WithRuntimes(func(context.Context, string) (string, error) { return "claude", nil })
	})
	ctx := t.Context()

	w.connect(orchestration.Connection{Integration: "whatsapp", Label: "me", Account: "+84 900 000 001",
		Secret: "conn-whatsapp-me"})

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)
	_, _, err = w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)
	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, orchestration.DefaultBotID)
	require.NoError(t, err)

	worker := w.workerUnder("tools: [phone]\n", "laptop", chat.ID)

	reached := map[string]bool{}

	for _, agent := range []struct{ name, key string }{{"pi", pi}, {"worker", worker}} {
		listed := toolNames(w.tools(agent.key))
		require.NotEmpty(t, listed, agent.name)

		phone := w.listedOn(agenttool.PathDeviceTools, agent.key)

		for _, tool := range append(slices.Clone(listed), phone...) {
			path := agenttool.PathToolsCall
			if devicetool.Routed(tool) {
				path = agenttool.PathDeviceCall
			}

			before := len(w.browser.calls)
			status, body := w.call(http.MethodPost, path, agent.key,
				agenttool.BrowserCall{Tool: tool, Arguments: json.RawMessage(`{}`)})

			assert.NotContains(t, string(body), "has no tool", "%s's %s is reached on %s (%d)", agent.name, tool,
				path, status)

			if !strings.HasPrefix(tool, "agent_browser_") {
				assert.Len(t, w.browser.calls, before, "%s's %s is its own handler's, not the browser's", agent.name,
					tool)
			}

			reached[tool] = true
		}
	}

	for _, tool := range []string{
		agenttool.ToolMemory, agenttool.ToolShowArtifact, agenttool.ToolRoutines, agenttool.ToolChatSearch,
		agenttool.ToolWebSearch, agenttool.ToolWebFetch, agenttool.ToolGenerateImage, agenttool.ToolAudioTranscribe,
		agenttool.ToolAudioSpeak, agenttool.ToolMuseAsk, agenttool.ToolMuseChats, agenttool.ToolMuseMessages,
		agenttool.ToolNodeExec, agenttool.ToolNodeProcess, agenttool.ToolNodeProcessKill, agenttool.ToolInboxList,
		agenttool.ToolWhatsAppChats, agenttool.ToolWhatsAppSend, "agent_browser_open", devicetool.BrowserVideoStart,
		devicetool.BrowserVideoSay, devicetool.BrowserVideoStop, devicetool.Screen, devicetool.Tap,
		devicetool.VideoStart, devicetool.VideoStop,
	} {
		assert.True(t, reached[tool], "%s is listed and called", tool)
	}
}
