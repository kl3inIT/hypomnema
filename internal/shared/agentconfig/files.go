package agentconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// CodexCatalogPath is where the gateway serves its models in Codex's own catalog format (issue #907), the address a
// Codex provider's model_catalog_url names.
const CodexCatalogPath = "/v1/codex/models"

// CodexConfig is Codex's ~/.codex/config.toml naming the gateway, with g's model as the default, as 9router's Codex
// card writes it (CodexToolCard.js getManualConfigs: model, model_provider, model_providers.<id> with name, base_url
// and wire_api "responses") and the node's Codex runner passes it (internal/node/codex/runner.go argv: no
// WebSocket), with two fields more:
//   - model_catalog_url, so Codex's /model lists the gateway's models with their windows and effort levels
//     (codex-rs/model-provider-info/src/lib.rs model_catalog_url; model-provider/src/models_endpoint.rs);
//   - the key as experimental_bearer_token rather than 9router's Authorization header: Codex asks a catalog only of
//     a provider whose key is its own, env_key or experimental_bearer_token (models_endpoint.rs
//     has_provider_api_key and uses_api_key_auth; models-manager/src/manager.rs supports_api_key_discovery), and a
//     file to copy carries its key, as 9router's does.
func CodexConfig(g Gateway) string {
	return fmt.Sprintf(`# Hypomnema's model gateway for the Codex CLI
model = %s
model_provider = %s

[model_providers.%s]
name = "Hypomnema"
base_url = %s
wire_api = "responses"
model_catalog_url = %s
experimental_bearer_token = %s
supports_websockets = false
`, tomlString(g.Model), tomlString(ProviderID), ProviderID, tomlString(g.URL),
		tomlString(g.Root()+CodexCatalogPath), tomlString(g.Key))
}

// tomlString writes a TOML basic string: a backslash, a quote and a control character escaped (TOML v1.0.0,
// "Basic strings").
func tomlString(s string) string {
	var b strings.Builder

	b.WriteByte('"')

	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\u%04X", r)
		default:
			b.WriteRune(r)
		}
	}

	b.WriteByte('"')

	return b.String()
}

// ClaudeSettings is Claude Code's ~/.claude/settings.json for the gateway, as 9router's Claude card writes it
// (ClaudeToolCard.js getManualConfigs: hasCompletedOnboarding, and in env ANTHROPIC_BASE_URL at the gateway's root,
// ANTHROPIC_AUTH_TOKEN, a model for each family and CLAUDE_CODE_AUTO_COMPACT_WINDOW) and as Claude Code's docs place
// a gateway in the env block of the settings file (llm-gateway-connect, "Set in a settings file"). The model is
// pinned for Opus, Sonnet and Haiku as the node's Claude Code runner pins it (internal/node/claude/runner.go
// credentials), so that Claude Code's default is it. Not for Fable as 9router does (shared/constants/cliTools.js):
// in Claude Code 2.1.322 each pin takes a row of /model's nine, and a Fable pin hid the gateway's Fable model. Nor
// ANTHROPIC_MODEL, which 9router does not set either: it overrides the model /model saves ("new sessions use that
// while it is set"). CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY makes /model list the models the key may use, from
// the gateway's GET /v1/models (llm-gateway-protocol, "Model discovery"). Claude Code cannot be told a model's window
// by the gateway: the chosen model's, when known, is CLAUDE_CODE_AUTO_COMPACT_WINDOW.
func ClaudeSettings(g Gateway) (string, error) {
	env := map[string]string{
		"ANTHROPIC_BASE_URL":                         g.Root(),
		"ANTHROPIC_AUTH_TOKEN":                       g.Key,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":               g.Model,
		"ANTHROPIC_DEFAULT_SONNET_MODEL":             g.Model,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":              g.Model,
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
	}

	if g.ContextWindow > 0 {
		env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] = strconv.FormatInt(g.ContextWindow, 10)
	}

	out, err := encodeJSON(map[string]any{"hasCompletedOnboarding": true, "env": env})
	if err != nil {
		return "", fmt.Errorf("encoding Claude Code's settings: %w", err)
	}

	return out, nil
}

// OpencodeFile is OpencodeConfig as the file opencode reads.
func OpencodeFile(g Gateway) (string, error) {
	config := OpencodeConfig(g)
	config["$schema"] = "https://opencode.ai/config.json"

	out, err := encodeJSON(config)
	if err != nil {
		return "", fmt.Errorf("encoding opencode's configuration: %w", err)
	}

	return out, nil
}

// encodeJSON writes a file's JSON indented, with <, > and & as they are: a placeholder such as
// "<your gateway key>" reads as written.
func encodeJSON(v any) (string, error) {
	var out bytes.Buffer

	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")

	if err := enc.Encode(v); err != nil {
		return "", err
	}

	return out.String(), nil
}
