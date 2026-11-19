// Package agentconfig writes the configuration that points a coding agent's CLI at Hypomnema's model gateway: one
// home for both the files a node writes for its workers (internal/node/acp) and the files the owner copies from
// Settings → Connect a tool (issue #907), so the two never drift. Each file is read in the CLI's own source or
// documentation (docs/research/2026-11-10-acp-omp-opencode.md; omp docs/models.md; opencode
// services/site/src/docs/content/providers.mdx; Codex codex-rs/model-provider-info/src/lib.rs; Claude Code's
// llm-gateway-connect page), as 9router's CLI tools page writes them (src/app/(dashboard)/dashboard/cli-tools).
package agentconfig

import (
	"strings"
)

// ProviderID names the gateway in a CLI's configuration.
const ProviderID = "hypomnema"

// The size of a model when it is not known: the values omp's and opencode's own documentation use in their
// examples of a custom model (omp docs/models.md, opencode models.mdx).
const (
	DefaultContextWindow int64 = 128000
	DefaultMaxOutput     int64 = 16384
)

// The wires a subscription's model is spoken to on (issue #549), as orchestration.GatewayWires names them; empty
// is OpenAI's chat.
const (
	WireMessages  = "anthropic-messages"
	WireResponses = "openai-responses"
)

// Gateway is what a CLI needs to reach the gateway for one model.
type Gateway struct {
	// URL is the gateway's base URL, ending in /v1.
	URL string
	// Key is what the CLI is given as its key: the key itself, or how the CLI reads it from the environment
	// (omp: the variable's name; opencode: "{env:NAME}").
	Key   string
	Model string
	// API is the model's wire (WireMessages, WireResponses); empty is chat.
	API string
	// Reasoning says the model reasons.
	Reasoning bool
	// ContextWindow and MaxOutput are the model's, when known; zero is DefaultContextWindow and DefaultMaxOutput.
	ContextWindow int64
	MaxOutput     int64
	// Others are the other models the key may use, which a file for the owner names beside Model (Connect a tool,
	// issue #907); a worker's key is good for its model alone, so its files name that one.
	Others []Model
}

// Model is one more model a file names: its name and size, as Gateway has them for its own.
type Model struct {
	ID            string
	ContextWindow int64
	MaxOutput     int64
}

// models are the file's models, Gateway's own first, each with its size or the defaults.
func (g Gateway) models() []Model {
	out := []Model{{ID: g.Model, ContextWindow: g.window(), MaxOutput: g.output()}}

	for _, m := range g.Others {
		if m.ID == g.Model {
			continue
		}

		if m.ContextWindow <= 0 {
			m.ContextWindow = DefaultContextWindow
		}

		if m.MaxOutput <= 0 {
			m.MaxOutput = DefaultMaxOutput
		}

		out = append(out, m)
	}

	return out
}

// Root is the gateway's origin: its URL without /v1, where Anthropic's Messages are served.
func (g Gateway) Root() string { return strings.TrimSuffix(strings.TrimRight(g.URL, "/"), "/v1") }

func (g Gateway) window() int64 {
	if g.ContextWindow > 0 {
		return g.ContextWindow
	}

	return DefaultContextWindow
}

func (g Gateway) output() int64 {
	if g.MaxOutput > 0 {
		return g.MaxOutput
	}

	return DefaultMaxOutput
}

// quoteYAML writes a string as a double-quoted YAML scalar.
func quoteYAML(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Wires is the wire a CLI that can choose one speaks to the gateway for a model of a subscription of the kind (issue
// #549): the subscription's own, as Claude Code and Codex speak to it; chat for the rest. Measured on zm on
// 2026-11-15 with pi, a Claude turn is a fifth smaller on Messages with the same cache, and Codex's cache is the same
// on Responses.
var Wires = map[string]string{
	"claude_subscription": WireMessages,
	"codex_subscription":  WireResponses,
}
