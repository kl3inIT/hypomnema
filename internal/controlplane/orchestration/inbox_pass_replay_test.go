package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Replay the actual wakes with a real model, as the triage eval does. The model
// reports its intended tool calls as JSON so it cannot act on any real item.
func TestReplayTheMainBotRoutesAnItemByName(t *testing.T) {
	t.Parallel()

	claude := os.Getenv("HYPOMNEMA_TEST_CLAUDE")
	if claude == "" {
		t.Skip("set HYPOMNEMA_TEST_CLAUDE=claude to replay the Inbox wakes with a real model")
	}

	w := newPassWorld(t)
	_, err := w.s.Inbox().Comment(t.Context(), inboxOwner, w.item.ID, "cho VMAP")
	require.NoError(t, err)

	wakes := w.wakes(w.item.Chat)
	require.Len(t, wakes, 2)

	t.Run("a single match", func(t *testing.T) {
		calls := replayInboxWake(t, claude, wakes[0])
		require.NotEmpty(t, calls)
		assert.Equal(t, "inbox_update", calls[0].Tool)
		assert.Equal(t, map[string]any{"id": w.item.ID, "pass_to": w.beyond.ID}, calls[0].Arguments)
	})

	t.Run("two matches", func(t *testing.T) {
		calls := replayInboxWake(t, claude, wakes[1])
		require.Len(t, calls, 2)
		assert.Equal(t, "inbox_comment", calls[0].Tool)
		assert.Equal(t, w.item.ID, calls[0].Arguments["id"])
		assert.Contains(t, calls[0].Arguments["text"], w.vmap.Name)
		assert.Contains(t, calls[0].Arguments["text"], w.vmapData.Name)
		assert.Equal(t, "inbox_update", calls[1].Tool)
		assert.Equal(t, map[string]any{"id": w.item.ID, "hand_back": true}, calls[1].Arguments)
	})
}

type inboxReplayCall struct {
	Tool      string
	Arguments map[string]any
}

func replayInboxWake(t *testing.T, claude, wake string) []inboxReplayCall {
	t.Helper()

	const schema = `{"type":"object","required":["calls"],"properties":{"calls":{"type":"array","items":` +
		`{"type":"object","required":["tool","arguments"],"properties":{"tool":{"type":"string"},"arguments":{"type":"object"}}}}}}`

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, claude, "-p", "--tools", "", "--strict-mcp-config", "--no-session-persistence",
		"--output-format", "json", "--json-schema", schema)
	cmd.Dir = t.TempDir()
	cmd.Stdin = strings.NewReader(wake + "\nReport the tool calls you would make on this item, in order.")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)

	var response struct {
		StructuredOutput struct{ Calls []inboxReplayCall } `json:"structured_output"`
	}
	require.NoError(t, json.Unmarshal(out, &response), "%s", out)

	return response.StructuredOutput.Calls
}
