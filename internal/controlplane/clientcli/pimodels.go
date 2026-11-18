package clientcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// piProviderID is the default provider of pi's models.json (orchestration.PiProvider): the supervisor names it with
// each model's gateway name.
const piProviderID = "hypomnema"

// piWires is the wire pi speaks to the gateway for a model of a subscription of the kind (issue #549), as
// orchestration.GatewayWires gives omp: the
// subscription's own, as Claude Code and Codex speak to it; the gateway's chat for the rest. Measured on zm on
// 2026-11-15, a Claude turn is a fifth smaller on Messages with the same cache, and Codex's cache is the same on
// Responses.
var piWires = map[string]string{
	"claude_subscription": "anthropic-messages",
	"codex_subscription":  "openai-responses",
}

// keyCommand is what pi's models.json gives as a provider's key: a command,
// which pi runs at each request, and no key.
const keyCommand = "!hypomnema model-key"

// errNoGatewayProvider means pi would start without a model it can use.
var errNoGatewayProvider = errors.New("the model gateway serves no model: pi would have none to use")

// piModelsFile is pi's models.json (pi's docs/models.md).
type piModelsFile struct {
	Providers map[string]piProvider `json:"providers"`
}

type piProvider struct {
	Name    string    `json:"name"`
	BaseURL string    `json:"baseUrl"`
	API     string    `json:"api"`
	APIKey  string    `json:"apiKey"`
	Models  []piModel `json:"models"`
}

// piModel is a model of pi's models.json (pi model-config.ts). What the
// gateway does not know is left out, so pi keeps its default for it: a
// window of 128000, 16384 out, no reasoning, no price; but for its input.
type piModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// API and BaseURL are the model's own wire, over the provider's (pi model-config.ts).
	API              string             `json:"api,omitempty"`
	BaseURL          string             `json:"baseUrl,omitempty"`
	Reasoning        bool               `json:"reasoning,omitempty"`
	ThinkingLevelMap map[string]*string `json:"thinkingLevelMap,omitempty"`
	Input            []string           `json:"input,omitempty"`
	Cost             *piCost            `json:"cost,omitempty"`
	ContextWindow    int64              `json:"contextWindow,omitempty"`
	MaxTokens        int64              `json:"maxTokens,omitempty"`
}

// piCost is a model's price in dollars per million tokens, every rate given
// (pi model-schema.ts ModelCostSchema).
type piCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// piThinkingLevels are pi's thinking levels (pi model-schema.ts
// MODEL_THINKING_LEVELS); off is the gateway's none.
var piThinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// piModels prints the models.json pi starts with (issue #204): the gateway, with every
// model it serves (issue #549: each subscription's models, under "<provider>/<model>", and the names made in its
// table), so that a Bot given another model needs nothing changed on the machine. The launcher writes it as pi
// starts for a chat, as Omnigent writes a models.json for each of its pi sessions (inner/pi_executor.py) and as a
// node writes a worker's configuration for each Dispatch (internal/node/acp/agents.go). The key is the command pi
// runs at each request. Each model has the attributes the gateway gives it (issue #367): its window, largest
// output, inputs, reasoning, effort levels and price; and the wire of its subscription (piWires).
func piModels(ctx context.Context, e *env, _ *flag.FlagSet) error {
	resp, err := e.api.ListProvidersWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("listing providers: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing providers", resp.StatusCode(), resp.JSONDefault)
	}

	served, err := e.api.ListGatewayModelsWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("listing the gateway's models: %w", err)
	}

	if served.JSON200 == nil {
		return apiError("listing the gateway's models", served.StatusCode(), served.JSONDefault)
	}

	if len(served.JSON200.Data) == 0 {
		return errNoGatewayProvider
	}

	kinds := make(map[string]string, len(resp.JSON200.Data))
	for _, p := range resp.JSON200.Data {
		kinds[p.Name] = string(p.Kind)
	}

	root := strings.TrimRight(e.url, "/")
	models := make([]piModel, 0, len(served.JSON200.Data))
	fallbackModels := make([]piModel, 0, len(served.JSON200.Data))

	for _, m := range served.JSON200.Data {
		pm := piModelOf(m.Name, m.Attributes)
		fallbackModels = append(fallbackModels, pm)

		// The wire is chosen as a worker's offer chooses it (orchestration modelAPI): a subscription's own for a
		// name "<subscription>/<model>"; chat, which the gateway turns into each target's, for a name of its table.
		provider, _, _ := strings.Cut(m.Name, "/")
		wire := piWires[kinds[provider]]

		switch wire {
		case "anthropic-messages":
			pm.API, pm.BaseURL = wire, root
		case "openai-responses":
			pm.API = wire
		}

		models = append(models, pm)
	}

	file := piModelsFile{Providers: map[string]piProvider{
		piProviderID: {
			Name: "Hypomnema gateway", BaseURL: root + "/v1", API: "openai-completions", APIKey: keyCommand,
			Models: models,
		},
		orchestration.PiFallbackProvider: {
			Name: "Hypomnema gateway with Bot fallbacks", BaseURL: root + "/v1", API: "openai-completions",
			APIKey: keyCommand, Models: fallbackModels,
		},
	}}

	out, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding pi's models: %w", err)
	}

	e.printf("%s\n", out)

	return nil
}

// piModelOf is a model as pi is given it: each attribute the gateway knows.
func piModelOf(name string, a openapi.GatewayModelAttributes) piModel {
	m := piModel{
		ID: name, Name: name + " (gateway)", Reasoning: a.Reasoning.Value != nil && *a.Reasoning.Value,
		ContextWindow: val(a.ContextWindow.Value), MaxTokens: val(a.MaxOutput.Value),
	}

	for _, in := range val(a.Input.Value) {
		m.Input = append(m.Input, string(in))
	}

	// A model whose input is not known reads pictures: pi takes a custom
	// model without one as text-only and drops a picture before the gateway
	// sees it, and the gateway passes pictures on to every vendor it serves
	// (issue #368, as Omnigent declares a gateway model,
	// omnigent/harnesses/pi_native/credentials.py).
	if a.Input.Value == nil {
		m.Input = []string{"text", "image"}
	}

	// Each of pi's levels is sent as the model's level of that name; one it
	// does not have is null, which pi does not offer. Off sends no effort
	// unless the model has none.
	if efforts := val(a.Efforts.Value); m.Reasoning && len(efforts) > 0 {
		m.ThinkingLevelMap = map[string]*string{}

		for _, level := range piThinkingLevels {
			effort := level
			if level == "off" {
				effort = string(openapi.GatewayModelEffortNone)
			}

			switch {
			case slices.Contains(efforts, openapi.GatewayModelEffort(effort)):
				m.ThinkingLevelMap[level] = &effort
			case level != "off":
				m.ThinkingLevelMap[level] = nil
			}
		}
	}

	// A cache price the vendor does not give is the input's, as the gateway
	// counts it (prices.Cost).
	if p := a.Price; p != nil {
		m.Cost = &piCost{
			Input: p.Input, Output: p.Output, CacheRead: valOr(p.CacheRead, p.Input),
			CacheWrite: valOr(p.CacheWrite, p.Input),
		}
	}

	return m
}

func valOr[T any](p *T, otherwise T) T {
	if p == nil {
		return otherwise
	}

	return *p
}
