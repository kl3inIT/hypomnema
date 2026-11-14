package clientcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: the launcher has written a chat's models.json and writes pi's
// settings from it (issue #395). A model with a window gets a reserve of 15%
// of it, at least 16,384; one without a window keeps pi's; grep, find and ls
// are on. No login is needed: it only reads the file.
func TestPiSettingsScaleCompactionToEachModelsWindow(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	models := filepath.Join(t.TempDir(), "models.json")
	require.NoError(t, os.WriteFile(models, []byte(`{"providers": {
		"model-gateway": {"models": [
			{"id": "claude-sonnet", "contextWindow": 200000},
			{"id": "gpt-sol", "contextWindow": 1000000},
			{"id": "small", "contextWindow": 32768},
			{"id": "unknown"}
		]},
		"gateway": {"models": [{"id": "gpt-sol", "contextWindow": 400000}]}
	}}`), 0o600))

	out, err := w.cmd(t, "pi-settings", models)
	require.NoError(t, err)

	var settings struct {
		DefaultTools []string `json:"defaultTools"`
		Compaction   struct {
			ModelOverrides map[string]struct {
				ReserveTokens int64 `json:"reserveTokens"`
			} `json:"modelOverrides"`
		} `json:"compaction"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &settings))

	assert.Equal(t, []string{"+grep", "+find", "+ls"}, settings.DefaultTools)

	reserve := map[string]int64{}
	for model, o := range settings.Compaction.ModelOverrides {
		reserve[model] = o.ReserveTokens
	}

	assert.Equal(t, map[string]int64{
		"model-gateway/claude-sonnet": 30000,
		"model-gateway/gpt-sol":       150000,
		"model-gateway/small":         16384,
		"gateway/gpt-sol":             60000,
	}, reserve, "the same model under two providers has the window each gives; one with no window keeps pi's")

	_, err = w.cmd(t, "pi-settings", filepath.Join(t.TempDir(), "missing.json"))
	require.Error(t, err, "without models pi would not know its windows")
}
