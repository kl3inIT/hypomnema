package acp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: omp is given the wire of its model's subscription (issue #549):
// Anthropic Messages at the gateway's root, where omp adds /v1/messages;
// OpenAI Responses under /v1; chat under /v1 for any other model.
func TestOmpSpeaksTheSubscriptionsOwnWire(t *testing.T) {
	t.Parallel()

	for api, want := range map[string][2]string{
		"anthropic-messages": {"anthropic-messages", `"https://hypomnema.example"`},
		"openai-responses":   {"openai-responses", `"https://hypomnema.example/v1"`},
		"":                   {"openai-completions", `"https://hypomnema.example/v1"`},
	} {
		dir := t.TempDir()

		_, _, err := prepareOmp(launch{
			Dir: dir, URL: "https://hypomnema.example/v1", Model: "claude-subscription/claude-opus-5-5", API: api,
		})
		require.NoError(t, err, api)

		models, err := os.ReadFile(filepath.Join(dir, "agent", "models.yml")) //nolint:gosec // the test's own
		require.NoError(t, err, api)
		assert.Contains(t, string(models), "    api: "+want[0]+"\n", api)
		assert.Contains(t, string(models), "    baseUrl: "+want[1]+"\n", api)
	}
}
