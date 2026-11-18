package agentapi_test

import (
	"context"
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

// guideWorld is a world whose agents have the web, images, audio, the browser and the phone, and whose workers
// run Claude Code; api is its agent API, for WorkerTools and BotTools.
func guideWorld(t *testing.T) (*world, *agentapi.Server) {
	t.Helper()

	var api *agentapi.Server

	w := newWorldWith(t, 0, func(s *agentapi.Server) {
		api = s.WithAudio(&fakeAudio{}).WithWeb(&fakeWeb{}).WithImages(&fakeImages{}).WithDevice(&sharedPhone{}).
			WithRuntimes(func(context.Context, string) (string, error) { return "claude", nil })
	})

	return w, api
}

// connect saves a Connection the owner allows for chats.
func (w *world) connect(c orchestration.Connection) {
	w.t.Helper()

	_, err := w.svc.SaveServiceConnection(w.t.Context(), c)
	require.NoError(w.t, err)
	_, err = w.svc.SetConnectionChats(w.t.Context(), c.Integration, c.Label, true)
	require.NoError(w.t, err)
}

// Scenario (issue #723, decisions 2.2 and 2.3; issue #789, decision 8): the "Tools" block is built from the tools
// each agent gets. A Claude Code worker and a Bot's pi with the same families get the same block, a line for
// each: the memory, the artifacts, the web, images, audio, the browser and the phone. A Bot's own Routines add
// their line, and a Bot that switches the speech off gets a block without it.
func TestAWorkerAndABotWithTheSameToolsGetTheSameBlock(t *testing.T) {
	t.Parallel()

	w, api := guideWorld(t)
	ctx := t.Context()

	key := w.workerUnder("", "laptop", "")
	session, err := w.svc.AgentSessionOf(ctx, key)
	require.NoError(t, err)

	workerTools := api.WorkerTools(ctx, session.DispatchID, key)
	assert.Subset(t, workerTools, []string{agenttool.ToolWebSearch, agenttool.ToolGenerateImage,
		agenttool.ToolAudioSpeak, agenttool.ToolMemory, agenttool.ToolShowArtifact, "agent_browser_open",
		devicetool.Screen})
	assert.Equal(t, toolNames(w.tools(key)), workerTools[:len(toolNames(w.tools(key)))],
		"what the block is built from starts with what the worker's tools server lists")

	same, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "same", ToolsOff: []string{agenttool.ToolRoutines}})
	require.NoError(t, err)

	block := agenttool.ToolGuide(workerTools)
	assert.Equal(t, block, agenttool.ToolGuide(api.BotTools(ctx, same)))

	for _, family := range []string{"Memory", "Artifacts", "Web", "Images", "Audio", "Browser", "Phone"} {
		assert.Contains(t, block, "\n- "+family+": ")
	}

	assert.NotContains(t, block, "- Routines: ", "a worker has no Routines")
	assert.Contains(t, block, "never in the owner's tab \"My browser\"", "the browser's rules reach a worker")
	assert.Contains(t, block, "Never ask for, accept, use or type a password")

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "own"})
	require.NoError(t, err)
	assert.Contains(t, agenttool.ToolGuide(api.BotTools(ctx, bot)), "\n- Routines: ")

	quiet, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "quiet", ToolsOff: []string{"audio_*"}})
	require.NoError(t, err)

	botTools := api.BotTools(ctx, quiet)
	assert.NotContains(t, botTools, agenttool.ToolAudioSpeak)
	assert.NotContains(t, agenttool.ToolGuide(botTools), "- Audio: ")
	assert.Contains(t, agenttool.ToolGuide(botTools), "- Web: ")
}

// Scenario (issue #789, decisions 2 and 3): a Bot whose list has the owner's WhatsApp, a Google account and the
// github.com account gets their lines: WhatsApp's, though its tools run on his turn only, since pi lists them
// between his turns (#748); `gws` and `gh`, which point to their skills. A worker whose Run was given the same
// Google and GitHub accounts gets `gws`, and not `gh` nor WhatsApp: it keeps its own gh, and never has the
// owner's own accounts. A worker given none gets no command's line.
func TestEachAgentIsToldOfTheAccountsItReachesAndTheirSkills(t *testing.T) {
	t.Parallel()

	w, api := guideWorld(t)
	ctx := t.Context()

	w.connect(orchestration.Connection{Integration: "whatsapp", Label: "me", Account: "+84 900 000 001",
		Secret: "conn-whatsapp-me"})
	w.connect(orchestration.Connection{Integration: "google", Label: "work", Account: "work@example.com",
		Secret: "conn-google-work"})
	//nolint:gosec // G101: the name of a vault secret, not a credential
	w.connect(orchestration.Connection{Integration: "github.com", Label: "default", Account: "kl3in",
		Secret: "conn-github-default"})

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "assistant", Connections: orchestration.AllowList{
		Limited: true, Names: []string{"whatsapp:me", "google:work", "github.com:default"},
	}})
	require.NoError(t, err)

	botTools := api.BotTools(ctx, bot)
	assert.Subset(t, botTools, []string{agenttool.ToolWhatsAppChats, agenttool.ToolWhatsAppSend, "gws", "gh"})
	assert.NotContains(t, botTools, "lark-cli", "no Lark account is on its list")

	block := agenttool.ToolGuide(botTools)
	assert.Contains(t, block, "\n- WhatsApp: Read the owner's own WhatsApp on his turn only")
	assert.Contains(t, block, "\n- gws: ")
	assert.Contains(t, block, "`google-workspace` skill")
	assert.Contains(t, block, "\n- gh: ")
	assert.Contains(t, block, "`github` skill")
	assert.Greater(t, strings.Index(block, "- gws: "), strings.Index(block, "- Phone: "),
		"the commands come after the families")

	key, err := w.svc.AddAgentSession(ctx, "dsp_given", []string{"google:work", "github.com:default"})
	require.NoError(t, err)

	workerTools := api.WorkerTools(ctx, "dsp_given", key)
	assert.Contains(t, workerTools, "gws")
	assert.NotContains(t, workerTools, "gh", "a worker keeps its own gh")
	assert.NotContains(t, workerTools, agenttool.ToolWhatsAppChats)

	bare, err := w.svc.AddAgentSession(ctx, "dsp_bare", nil)
	require.NoError(t, err)
	assert.NotContains(t, api.WorkerTools(ctx, "dsp_bare", bare), "gws")
}

// Scenario: a worker whose key cannot be read gets no tools named, rather than tools it may not have.
func TestAWorkerWithAnUnknownKeyIsNamedNoTools(t *testing.T) {
	t.Parallel()

	w, api := guideWorld(t)

	assert.Empty(t, api.WorkerTools(t.Context(), "dsp_gone", "hyp_session_v1_unknown"))
	assert.Contains(t, w.logs.String(), "agentapi.session_unknown")
}

// Scenario (issue #789, decision 4): every tool of the control plane's own a Bot is offered (its memory, artifacts,
// Routines, the web, images, audio, Muse, the owner's WhatsApp, the nodes) has a description in one template: what
// it does, then when to use it ("Use it"). The browser's and the phone's are their engines' own and are left out.
func TestEveryToolSaysWhenToUseIt(t *testing.T) {
	t.Parallel()

	w, api := guideWorld(t)
	api.WithMuse(&fakeMuse{on: true}).WithNodeExec(&machines{}, &machines{},
		func(context.Context, orchestration.Node) (string, error) { return "", nil })

	ctx := t.Context()

	w.connect(orchestration.Connection{Integration: "whatsapp", Label: "me", Account: "+84 900 000 001",
		Secret: "conn-whatsapp-me"})

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "assistant", Connections: orchestration.AllowList{
		Limited: true, Names: []string{"whatsapp:me"},
	}})
	require.NoError(t, err)
	chat, err := w.svc.CreateChat(ctx, bot.ID, "")
	require.NoError(t, err)
	_, _, err = w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)
	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, bot.ID)
	require.NoError(t, err)

	tools := w.tools(pi)
	assert.Subset(t, toolNames(tools), []string{agenttool.ToolMemory, agenttool.ToolShowArtifact,
		agenttool.ToolRoutines, agenttool.ToolWebSearch, agenttool.ToolWebFetch, agenttool.ToolGenerateImage,
		agenttool.ToolAudioTranscribe, agenttool.ToolAudioSpeak, agenttool.ToolMuseAsk, agenttool.ToolMuseChats,
		agenttool.ToolMuseMessages, agenttool.ToolWhatsAppChats, agenttool.ToolWhatsAppMessages,
		agenttool.ToolWhatsAppMedia, agenttool.ToolWhatsAppHistory, agenttool.ToolWhatsAppSend,
		agenttool.ToolNodeExec, agenttool.ToolNodeProcess, agenttool.ToolNodeProcessKill})

	for _, tool := range tools {
		if strings.HasPrefix(tool.Name, "agent_browser_") {
			continue
		}

		assert.Contains(t, tool.Description, "Use it", tool.Name)
	}
}
