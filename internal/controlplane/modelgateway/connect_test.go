package modelgateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/kl3inIT/hypomnema/api"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// newConnectGateway is newSubscriptionGateway with the vault, so that a key's value is stored, and OpenCode Go's
// kimi-k3 served on Responses. bare is the same gateway without the vault, whose keys have no stored value.
func newConnectGateway(t *testing.T) (g, bare *modelgateway.Service) {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	wire := func(_ context.Context, kind, upstream string) (string, bool) {
		if kind == "opencode_go" && upstream == "kimi-k3" {
			return "responses", true
		}

		return "", false
	}

	return subscriptionGatewayOn(db).WithServedWire(wire).WithKeyVault(testKeyVault(t, db)),
		subscriptionGatewayOn(db).WithServedWire(wire)
}

func serve(t *testing.T, g *modelgateway.Service, path, key string) (int, []byte) {
	t.Helper()

	mux := http.NewServeMux()
	modelgateway.NewServer(g, platform.NewLogger(t.Output(), platform.LogConfig{}, "test"),
		modelgateway.Config{}).Register(mux)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	return rec.Code, rec.Body.Bytes()
}

// catalogModel is the part of Codex's ModelInfo (codex-rs/protocol/src/openai_models.rs) the tests read.
type catalogModel struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Levels      []struct {
		Effort      string `json:"effort"`
		Description string `json:"description"`
	} `json:"supported_reasoning_levels"`
	ContextWindow    int64           `json:"context_window"`
	MaxContextWindow int64           `json:"max_context_window"`
	Visibility       string          `json:"visibility"`
	ShellType        string          `json:"shell_type"`
	BaseInstructions *string         `json:"base_instructions"`
	ModelMessages    json.RawMessage `json:"model_messages"`
	Upgrade          json.RawMessage `json:"upgrade"`
}

// Scenario: Codex asks the gateway's catalog with a key (issue #907). It gets the key's models that /v1/responses
// serves, under their gateway names, each with Codex's own entry for the model behind it, the gateway's window and
// effort levels; a model of the key Codex cannot call, and a model the key may not use, are not listed; a model
// Codex does not bundle has Codex's entry for an unknown model.
func TestTheCodexCatalogListsTheKeysModels(t *testing.T) {
	t.Parallel()

	g, _ := newConnectGateway(t)

	_, key, err := g.AddKey(t.Context(), "laptop", []string{
		"codex-subscription/gpt-6.1-sol", "claude-subscription/claude-opus-5-5", "opencode-go/kimi-k3",
		"opencode-go/glm-5.3",
	}, nil)
	require.NoError(t, err)

	status, body := serve(t, g, "/v1/codex/models?client_version=0.167.0", key)
	require.Equal(t, http.StatusOK, status, string(body))

	var catalog struct {
		Models []catalogModel `json:"models"`
	}

	require.NoError(t, json.Unmarshal(body, &catalog))
	require.Len(t, catalog.Models, 2, "only what the key may call on /v1/responses")

	sol, kimi := catalog.Models[0], catalog.Models[1]

	assert.Equal(t, "codex-subscription/gpt-6.1-sol", sol.Slug)
	assert.Equal(t, "codex-subscription/GPT-6.1-Sol", sol.DisplayName)
	assert.Equal(t, int64(500000), sol.ContextWindow, "the gateway's window")
	assert.GreaterOrEqual(t, sol.MaxContextWindow, sol.ContextWindow)
	assert.Equal(t, "list", sol.Visibility)

	efforts := make([]string, 0, len(sol.Levels))
	for _, l := range sol.Levels {
		efforts = append(efforts, l.Effort)
		assert.NotEmpty(t, l.Description)
	}

	assert.Equal(t, []string{"low", "medium", "high", "xhigh"}, efforts, "the gateway's levels, not max or ultra")

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &raw))
	assert.Contains(t, string(raw["models"]), `"default_reasoning_level":"low"`, "Codex's default, which it has")
	assert.Contains(t, string(sol.ModelMessages), "instructions_template", "Codex's own instructions for the model")
	assert.JSONEq(t, "null", string(sol.Upgrade))

	assert.Equal(t, "opencode-go/kimi-k3", kimi.Slug)
	require.NotNil(t, kimi.BaseInstructions)
	assert.Equal(t, "You are Codex, a coding agent. You and the user share one workspace.", *kimi.BaseInstructions,
		"CLIProxyAPI's short instructions for a model Codex does not bundle")
	assert.Equal(t, int64(272000), kimi.ContextWindow, "Codex's window for a model it does not know")
	assert.Equal(t, "unified_exec", kimi.ShellType)
	assert.Empty(t, kimi.Levels)

	status, _ = serve(t, g, "/v1/codex/models", "")
	assert.Equal(t, http.StatusUnauthorized, status)

	// The answer is what api/openapi.yaml says of it (GetCodexModelCatalog).
	spec, err := openapi3.NewLoader().LoadFromData(api.Spec)
	require.NoError(t, err)

	var decoded any
	require.NoError(t, json.Unmarshal(body, &decoded))
	require.NoError(t, spec.Components.Schemas["CodexModelCatalog"].Value.VisitJSON(decoded,
		openapi3.MultiErrors()))
}

// Scenario: Claude Code lists the key's models with gateway model discovery (issue #907): each has a display name,
// a Codex model Codex's description, and a limit gives the first ones and says there are more.
func TestTheModelListHasDisplayNamesAndALimit(t *testing.T) {
	t.Parallel()

	g, _ := newConnectGateway(t)

	_, key, err := g.AddKey(t.Context(), "laptop", []string{
		"claude-subscription/claude-opus-5-5", "codex-subscription/gpt-6.1-sol", "opencode-go/glm-5.3",
	}, nil)
	require.NoError(t, err)

	type list struct {
		Data []struct {
			ID            string   `json:"id"`
			Name          string   `json:"name"`
			DisplayName   string   `json:"display_name"`
			Description   string   `json:"description"`
			EndpointTypes []string `json:"supported_endpoint_types"`
			ContextLength *int64   `json:"context_length"`
			Reasoning     *bool    `json:"reasoning"`
			Levels        []string `json:"supported_reasoning_levels"`
		} `json:"data"`
		HasMore bool `json:"has_more"`
	}

	status, body := serve(t, g, "/v1/models?limit=1000", key)
	require.Equal(t, http.StatusOK, status)

	var all list
	require.NoError(t, json.Unmarshal(body, &all))
	require.Len(t, all.Data, 3)
	assert.False(t, all.HasMore)
	assert.Equal(t, "claude-subscription/Claude Opus 5.5", all.Data[0].DisplayName)
	assert.Empty(t, all.Data[0].Description)
	assert.Equal(t, "codex-subscription/GPT-6.1-Sol", all.Data[1].DisplayName)
	assert.NotEmpty(t, all.Data[1].Description, "Codex's words for its model")
	assert.Equal(t, "opencode-go/glm-5.3", all.Data[2].DisplayName, "a name of no known family stays as it is")

	// What omp's proxy discovery reads: a Claude subscription's model on Messages first, the rest on chat; the
	// window and the levels when known.
	opus, sol, glm := all.Data[0], all.Data[1], all.Data[2]
	assert.Equal(t, opus.DisplayName, opus.Name)
	assert.Equal(t, []string{"anthropic", "openai"}, opus.EndpointTypes)
	assert.Equal(t, []string{"openai"}, sol.EndpointTypes)
	require.NotNil(t, sol.ContextLength)
	assert.Equal(t, int64(500000), *sol.ContextLength)
	require.NotNil(t, sol.Reasoning)
	assert.True(t, *sol.Reasoning)
	assert.Equal(t, []string{"low", "medium", "high", "xhigh"}, sol.Levels)
	assert.Equal(t, []string{"openai"}, glm.EndpointTypes)
	assert.Nil(t, glm.ContextLength, "unknown: left out")

	status, body = serve(t, g, "/v1/models?limit=2", key)
	require.Equal(t, http.StatusOK, status)

	var first list
	require.NoError(t, json.Unmarshal(body, &first))
	assert.Len(t, first.Data, 2)
	assert.True(t, first.HasMore)
}

// Scenario: the owner opens Connect a tool for a key whose value is stored (issue #907). Each tool's file parses
// in its format, holds the key, the gateway's address and the model chosen; Codex is offered only the key's models
// it can call; a model not offered gives the first.
func TestEachToolsFileHoldsTheKeyAndTheModel(t *testing.T) {
	t.Parallel()

	g, _ := newConnectGateway(t)
	ctx := t.Context()

	k, secret, err := g.AddKey(ctx, "laptop", []string{
		"claude-subscription/claude-opus-5-5", "codex-subscription/gpt-6.1-sol",
	}, nil)
	require.NoError(t, err)

	const base = "https://hyp.example/"

	claude, err := g.ToolConfig(ctx, k.ID, modelgateway.ToolClaudeCode, "claude-subscription/claude-opus-5-5", base)
	require.NoError(t, err)
	assert.True(t, claude.KeyStored)
	assert.Equal(t, []string{"claude-subscription/claude-opus-5-5", "codex-subscription/gpt-6.1-sol"}, claude.Models)
	require.Len(t, claude.Files, 1)
	assert.Equal(t, "~/.claude/settings.json", claude.Files[0].Path)

	var settings struct {
		Env map[string]string `json:"env"`
	}

	require.NoError(t, json.Unmarshal([]byte(claude.Files[0].Content), &settings))
	assert.Equal(t, "https://hyp.example", settings.Env["ANTHROPIC_BASE_URL"], "Messages at the gateway's root")
	assert.Equal(t, secret, settings.Env["ANTHROPIC_AUTH_TOKEN"])
	assert.Equal(t, "claude-subscription/claude-opus-5-5", settings.Env["ANTHROPIC_DEFAULT_OPUS_MODEL"])
	assert.NotContains(t, settings.Env, "ANTHROPIC_MODEL", "it would override what /model saves")
	assert.Equal(t, "1", settings.Env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"])
	assert.Equal(t, "1000000", settings.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"])

	codex, err := g.ToolConfig(ctx, k.ID, modelgateway.ToolCodex, "claude-subscription/claude-opus-5-5", base)
	require.NoError(t, err)
	assert.Equal(t, []string{"codex-subscription/gpt-6.1-sol"}, codex.Models, "Codex calls /v1/responses only")
	assert.Equal(t, "codex-subscription/gpt-6.1-sol", codex.Model, "a model not offered gives the first")
	assert.Equal(t, "~/.codex/config.toml", codex.Files[0].Path)

	var config struct {
		Model          string `toml:"model"`
		ModelProvider  string `toml:"model_provider"`
		ModelProviders map[string]struct {
			BaseURL         string `toml:"base_url"`
			WireAPI         string `toml:"wire_api"`
			ModelCatalogURL string `toml:"model_catalog_url"`
			Token           string `toml:"experimental_bearer_token"`
		} `toml:"model_providers"`
	}

	_, err = toml.Decode(codex.Files[0].Content, &config)
	require.NoError(t, err, codex.Files[0].Content)
	assert.Equal(t, "codex-subscription/gpt-6.1-sol", config.Model)

	provider := config.ModelProviders[config.ModelProvider]
	assert.Equal(t, "https://hyp.example/v1", provider.BaseURL)
	assert.Equal(t, "responses", provider.WireAPI)
	assert.Equal(t, "https://hyp.example/v1/codex/models", provider.ModelCatalogURL)
	assert.Equal(t, secret, provider.Token)

	omp, err := g.ToolConfig(ctx, k.ID, modelgateway.ToolOmp, "claude-subscription/claude-opus-5-5", base)
	require.NoError(t, err)
	require.Len(t, omp.Files, 2, "models.yml and config.yml")
	assert.Equal(t, "~/.omp/agent/models.yml", omp.Files[0].Path)
	assert.Equal(t, "~/.omp/agent/config.yml", omp.Files[1].Path)

	var models struct {
		Providers map[string]struct {
			BaseURL    string `yaml:"baseUrl"`
			APIKey     string `yaml:"apiKey"`
			AuthHeader bool   `yaml:"authHeader"`
			Discovery  struct {
				Type string `yaml:"type"`
			} `yaml:"discovery"`
			Models []any `yaml:"models"`
		} `yaml:"providers"`
	}

	require.NoError(t, yaml.Unmarshal([]byte(omp.Files[0].Content), &models))
	hyp := models.Providers["hypomnema"]
	assert.Equal(t, "https://hyp.example/v1", hyp.BaseURL)
	assert.Equal(t, secret, hyp.APIKey)
	assert.True(t, hyp.AuthHeader)
	assert.Equal(t, "proxy", hyp.Discovery.Type, "every model of the key, read from /v1/models")
	assert.Empty(t, hyp.Models, "no model is written down")

	var ompSettings struct {
		ModelRoles map[string]string `yaml:"modelRoles"`
	}

	require.NoError(t, yaml.Unmarshal([]byte(omp.Files[1].Content), &ompSettings))
	assert.Equal(t, "hypomnema/claude-subscription/claude-opus-5-5", ompSettings.ModelRoles["default"])

	opencode, err := g.ToolConfig(ctx, k.ID, modelgateway.ToolOpencode, "codex-subscription/gpt-6.1-sol", base)
	require.NoError(t, err)
	assert.Equal(t, "~/.config/opencode/opencode.json", opencode.Files[0].Path)

	var oc struct {
		Model     string `json:"model"`
		Providers map[string]struct {
			Settings struct {
				BaseURL string `json:"baseURL"`
				APIKey  string `json:"apiKey"`
			} `json:"settings"`
			Models map[string]json.RawMessage `json:"models"`
		} `json:"providers"`
	}

	require.NoError(t, json.Unmarshal([]byte(opencode.Files[0].Content), &oc))
	assert.Equal(t, "hypomnema/codex-subscription/gpt-6.1-sol", oc.Model)
	assert.Equal(t, "https://hyp.example/v1", oc.Providers["hypomnema"].Settings.BaseURL)
	assert.Equal(t, secret, oc.Providers["hypomnema"].Settings.APIKey)
	assert.Len(t, oc.Providers["hypomnema"].Models, 2, "every model of the key")
	assert.Contains(t, oc.Providers["hypomnema"].Models, "codex-subscription/gpt-6.1-sol")
	assert.JSONEq(t, `{"capabilities":{"tools":true,"input":["text"],"output":["text"]},`+
		`"limit":{"context":1000000,"output":128000}}`,
		string(oc.Providers["hypomnema"].Models["claude-subscription/claude-opus-5-5"]), "with its own size")
}

// Scenario: a key made without a stored value gives each file a placeholder in its place and says so; a key that
// may use no model the tool calls gives a placeholder model; an address that is not one, or a key that is not the
// owner's, is refused.
func TestAKeyWithoutAStoredValueGivesAPlaceholder(t *testing.T) {
	t.Parallel()

	g, bare := newConnectGateway(t)
	ctx := t.Context()

	k, _, err := bare.AddKey(ctx, "old", []string{"claude-subscription/claude-opus-5-5"}, nil)
	require.NoError(t, err)

	c, err := g.ToolConfig(ctx, k.ID, modelgateway.ToolClaudeCode, "", "http://hyp.example")
	require.NoError(t, err)
	assert.False(t, c.KeyStored)
	assert.Contains(t, c.Files[0].Content, modelgateway.KeyPlaceholder)

	codex, err := g.ToolConfig(ctx, k.ID, modelgateway.ToolCodex, "", "http://hyp.example")
	require.NoError(t, err)
	assert.Empty(t, codex.Models)
	assert.Empty(t, codex.Model)
	assert.Contains(t, codex.Files[0].Content, modelgateway.ModelPlaceholder)

	_, err = g.ToolConfig(ctx, k.ID, modelgateway.ToolOmp, "", "hyp.example")
	require.ErrorIs(t, err, modelgateway.ErrInvalid)

	_, err = g.ToolConfig(ctx, k.ID, modelgateway.Tool("cursor"), "", "http://hyp.example")
	require.ErrorIs(t, err, modelgateway.ErrInvalid)

	_, err = g.ToolConfig(ctx, "key_nobody", modelgateway.ToolOmp, "", "http://hyp.example")
	require.ErrorIs(t, err, modelgateway.ErrNotFound)
}

// Scenario: a name over a Claude model and then a Codex one (issue #907, review of PR #922). Codex is told the Codex
// model's window and levels, those of the target /v1/responses serves, not the Claude model's million tokens and
// max.
func TestTheCodexCatalogDescribesTheTargetItServes(t *testing.T) {
	t.Parallel()

	g, _ := newConnectGateway(t)

	_, err := g.ApplyModel(t.Context(), "name: opus-or-sol\ntargets:\n"+
		"  - {provider: claude-subscription, model: claude-opus-5-5}\n"+
		"  - {provider: codex-subscription, model: gpt-6.1-sol}\n")
	require.NoError(t, err)

	_, key, err := g.AddKey(t.Context(), "laptop", []string{"opus-or-sol"}, nil)
	require.NoError(t, err)

	status, body := serve(t, g, "/v1/codex/models", key)
	require.Equal(t, http.StatusOK, status, string(body))

	var catalog struct {
		Models []catalogModel `json:"models"`
	}

	require.NoError(t, json.Unmarshal(body, &catalog))
	require.Len(t, catalog.Models, 1)
	assert.Equal(t, "opus-or-sol", catalog.Models[0].Slug)
	assert.Equal(t, int64(500000), catalog.Models[0].ContextWindow, "GPT-6.1-Sol's, not Claude Opus's 1000000")

	efforts := []string{}
	for _, l := range catalog.Models[0].Levels {
		efforts = append(efforts, l.Effort)
	}

	assert.Equal(t, []string{"low", "medium", "high", "xhigh"}, efforts, "no max, which Codex is not served")
}

// Scenario: a key that may use every model, with 300 names over the Codex models (issue #907, the owner's
// addition 8). Codex ignores a catalog of 1 MiB or more and keeps its bundled one (CLIProxyAPI
// models_test.go:1710): the catalog stays under it. Only the first entry of each bundled model may keep Codex's own
// instructions, the others get the short ones, and past the cap the last that keep them give them up too.
func TestTheCodexCatalogStaysUnderOneMiB(t *testing.T) {
	t.Parallel()

	g, _ := newConnectGateway(t)

	upstreams := []string{"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-6-astra", "gpt-6-luna", "gpt-6-sol",
		"gpt-6.1-sol"}
	for i := range 300 {
		_, err := g.ApplyModel(t.Context(), fmt.Sprintf("name: alias-%03d\ntargets: [{provider: codex-subscription, "+
			"model: %s}]\n", i, upstreams[i%len(upstreams)]))
		require.NoError(t, err)
	}

	_, key, err := g.AddKey(t.Context(), "everything", nil, nil)
	require.NoError(t, err)

	status, body := serve(t, g, "/v1/codex/models", key)
	require.Equal(t, http.StatusOK, status)
	assert.Less(t, len(body), 1<<20)

	var catalog struct {
		Models []struct {
			Slug             string `json:"slug"`
			BaseInstructions string `json:"base_instructions"`
			ModelMessages    struct {
				Template string `json:"instructions_template"`
			} `json:"model_messages"`
		} `json:"models"`
	}

	require.NoError(t, json.Unmarshal(body, &catalog))
	require.Len(t, catalog.Models, 300+len(upstreams)+1, "the aliases, the Codex models and kimi-k3")

	full := 0

	for _, m := range catalog.Models {
		require.NotEmpty(t, m.ModelMessages.Template, m.Slug)

		if len(m.ModelMessages.Template) > 1000 {
			full++
		}
	}

	assert.Positive(t, full, "Codex's own instructions are kept where they fit")
	assert.Less(t, full, len(upstreams), "once a bundled model at most, and not all of them past the cap")
}

// Scenario: a model's name reads as the web client writes it (web/src/lib/model-name.ts).
func TestDisplayName(t *testing.T) {
	t.Parallel()

	for id, want := range map[string]string{
		"claude-opus-5-5":                     "Claude Opus 5.5",
		"claude-subscription/claude-opus-5-5": "claude-subscription/Claude Opus 5.5",
		"claude-sonnet-4-6-20260101":          "Claude Sonnet 4.6 20260101",
		"claude-fable-5":                      "Claude Fable 5",
		"gpt-6.1-sol":                         "GPT-6.1-Sol",
		"codex-subscription/gpt-5.6-luna":     "codex-subscription/GPT-5.6-Luna",
		"gemini-3.8-flash-high":               "Gemini 3.8 Flash High",
		"opencode-go/kimi-k3":                 "opencode-go/kimi-k3",
		"fast":                                "fast",
	} {
		assert.Equal(t, want, modelgateway.DisplayName(id), id)
	}
}
