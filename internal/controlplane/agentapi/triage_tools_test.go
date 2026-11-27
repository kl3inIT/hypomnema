package agentapi_test

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

var updateTriageTools = flag.Bool("update", false,
	"write deploy/thu-ky/triage-tools.json, the tools and Tools block the triage eval gives the model")

// triageTools is the file the triage eval (deploy/thu-ky/triage.eval.mjs) reads the Bot's tools from.
const triageTools = "../../../deploy/thu-ky/triage-tools.json"

// triageToolsFile is that file: the tools a read run of the triage lists, as this server describes them, and the
// "Tools" block of the Bot's prompt.
type triageToolsFile struct {
	About string                  `json:"about"`
	Guide string                  `json:"guide"`
	Tools []agenttool.BrowserTool `json:"tools"`
}

// Scenario: issue #1206, W6. The triage eval runs triage.md with a real model against a fake of the owner's world;
// the model sees the tools as this server lists them to a read run of the triage (their descriptions and schemas)
// and the Tools block of the Bot's prompt, not a hand-written copy that drifts from them. This writes them into
// deploy/thu-ky/triage-tools.json with -update, and fails while the file is not what the server gives:
//
//	go test ./internal/controlplane/agentapi -run TestTheTriageEvalHasTheBotsTools -update
func TestTheTriageEvalHasTheBotsTools(t *testing.T) {
	t.Parallel()

	sw := newSecretaryWorldOf(t, "lark:me", "google:me", "zalo:main", "whatsapp:main")
	pi := sw.wake()

	var names []string
	for _, tool := range sw.tools(pi) {
		names = append(names, tool.Name)
	}

	got := triageToolsFile{
		About: "The tools a read run of the triage Routine lists and the Tools block of the Bot's prompt, as the " +
			"control plane gives them to a Bot with lark:me, google:me, zalo:main, whatsapp:main and the meetings. " +
			"Written by go test ./internal/controlplane/agentapi -run TestTheTriageEvalHasTheBotsTools -update; " +
			"triage.eval.mjs gives them to the model.",
		Guide: agenttool.ToolGuide(sw.api.BotTools(t.Context(), sw.bot)),
		Tools: sw.tools(pi),
	}
	for _, want := range []string{agenttool.ToolInboxUpdate, agenttool.ToolInboxDrop, agenttool.ToolMeetingGet,
		agenttool.ToolAccountRead, agenttool.ToolZaloMessages, agenttool.ToolWhatsAppMessages, agenttool.ToolMemory} {
		require.Contains(t, names, want)
	}
	require.Contains(t, got.Guide, "- Inbox: ")

	raw, err := json.MarshalIndent(got, "", " ")
	require.NoError(t, err)
	raw = append(raw, '\n')

	if *updateTriageTools {
		require.NoError(t, os.WriteFile(triageTools, raw, 0o644)) //nolint:gosec // a file of the repository

		return
	}

	kept, err := os.ReadFile(triageTools)
	require.NoError(t, err, "write it with -update")
	assert.Equal(t, string(raw), string(kept), "the triage eval's tools are not the server's: run this test with -update")
}
