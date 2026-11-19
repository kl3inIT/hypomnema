package agentconfig_test

import (
	"encoding/json"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/kl3inIT/hypomnema/internal/shared/agentconfig"
)

// A value with what a format must escape: each file still parses and gives it back as written.
const awkward = "a \"quoted\" \\ name\twith a tab"

// Scenario: omp is given its model on the wire of its subscription: chat and Responses under /v1, Messages at the
// gateway's root; an unknown size is omp's example's.
func TestOmpModelsNamesTheWire(t *testing.T) {
	t.Parallel()

	type file struct {
		Providers map[string]struct {
			BaseURL string `yaml:"baseUrl"`
			APIKey  string `yaml:"apiKey"`
			API     string `yaml:"api"`
			Models  []struct {
				ID            string `yaml:"id"`
				ContextWindow int64  `yaml:"contextWindow"`
				MaxTokens     int64  `yaml:"maxTokens"`
			} `yaml:"models"`
		} `yaml:"providers"`
	}

	for wire, want := range map[string]struct{ api, base string }{
		"":                        {"openai-completions", "https://hyp.example/v1"},
		agentconfig.WireResponses: {"openai-responses", "https://hyp.example/v1"},
		agentconfig.WireMessages:  {"anthropic-messages", "https://hyp.example"},
	} {
		var got file
		require.NoError(t, yaml.Unmarshal([]byte(agentconfig.OmpModels(agentconfig.Gateway{
			URL: "https://hyp.example/v1", Key: awkward, Model: awkward, API: wire,
		})), &got), wire)

		p := got.Providers[agentconfig.ProviderID]
		assert.Equal(t, want.api, p.API, wire)
		assert.Equal(t, want.base, p.BaseURL, wire)
		assert.Equal(t, awkward, p.APIKey)
		require.Len(t, p.Models, 1)
		assert.Equal(t, awkward, p.Models[0].ID)
		assert.Equal(t, agentconfig.DefaultContextWindow, p.Models[0].ContextWindow)
		assert.Equal(t, agentconfig.DefaultMaxOutput, p.Models[0].MaxTokens)
	}
}

// Scenario: Codex's file is TOML naming the gateway's catalog, and Claude Code's and OpenCode's are JSON; each
// gives back the key and the model as written.
func TestEachFileParses(t *testing.T) {
	t.Parallel()

	g := agentconfig.Gateway{URL: "https://hyp.example/v1", Key: awkward, Model: awkward, ContextWindow: 500000}

	codex := agentconfig.CodexConfig(g)

	var config struct {
		Model          string `toml:"model"`
		ModelProviders map[string]struct {
			ModelCatalogURL string `toml:"model_catalog_url"`
			Token           string `toml:"experimental_bearer_token"`
			Websockets      bool   `toml:"supports_websockets"`
		} `toml:"model_providers"`
	}

	_, err := toml.Decode(codex, &config)
	require.NoError(t, err, codex)
	assert.Equal(t, awkward, config.Model)
	assert.Equal(t, "https://hyp.example/v1/codex/models", config.ModelProviders["hypomnema"].ModelCatalogURL)
	assert.Equal(t, awkward, config.ModelProviders["hypomnema"].Token)
	assert.False(t, config.ModelProviders["hypomnema"].Websockets)

	claude, err := agentconfig.ClaudeSettings(g)
	require.NoError(t, err)

	var settings struct {
		HasCompletedOnboarding bool              `json:"hasCompletedOnboarding"`
		Env                    map[string]string `json:"env"`
	}

	require.NoError(t, json.Unmarshal([]byte(claude), &settings))
	assert.True(t, settings.HasCompletedOnboarding)
	assert.Equal(t, awkward, settings.Env["ANTHROPIC_AUTH_TOKEN"])
	assert.Equal(t, "https://hyp.example", settings.Env["ANTHROPIC_BASE_URL"])
	assert.Equal(t, "500000", settings.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"])

	opencode, err := agentconfig.OpencodeFile(g)
	require.NoError(t, err)

	var oc map[string]any
	require.NoError(t, json.Unmarshal([]byte(opencode), &oc))
	assert.Equal(t, "https://opencode.ai/config.json", oc["$schema"])
	assert.Equal(t, "hypomnema/"+awkward, oc["model"])
}
