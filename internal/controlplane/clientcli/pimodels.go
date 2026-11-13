package clientcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// kindModelGateway is the kind of the provider that is the control plane's
// own model gateway (orchestration.ProviderModelGateway).
const kindModelGateway = "model_gateway"

// keyCommand is what pi's models.json gives as a provider's key: a command,
// which pi runs at each request, and no key.
const keyCommand = "!hypomnema model-key"

// errNoGatewayProvider means pi would start without a model it can use.
var errNoGatewayProvider = errors.New("no provider is the model gateway with a model: pi would have none to use")

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

type piModel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Reasoning bool   `json:"reasoning"`
	// Input is what the model reads. pi takes a custom model as text-only
	// when it is left out (src/core/model-config.ts), and drops a picture
	// before the gateway sees it; the gateway passes pictures on to every
	// vendor it serves (issue #368, as Omnigent declares a gateway model,
	// omnigent/harnesses/pi_native/credentials.py).
	Input []string `json:"input"`
}

// piModels prints the models.json pi starts with (issue #204): one provider
// of pi for each provider of the control plane that is the model gateway,
// with the models that provider lists, so that a Bot given another model
// needs nothing changed on the machine. The launcher writes it as pi starts
// for a chat, as Omnigent writes a models.json for each of its pi sessions
// (inner/pi_executor.py) and as a node writes a worker's configuration for
// each Dispatch (internal/node/acp/agents.go). The key is the command pi
// runs at each request.
//
// A provider is the gateway when its kind says so, or when it is an
// OpenAI-compatible provider whose address is the gateway's own: the
// provider pi was given before the gateway had a kind of its own.
func piModels(ctx context.Context, e *env, _ *flag.FlagSet) error {
	resp, err := e.api.ListProvidersWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("listing providers: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing providers", resp.StatusCode(), resp.JSONDefault)
	}

	gateway := strings.TrimRight(e.url, "/") + "/v1"
	file := piModelsFile{Providers: map[string]piProvider{}}

	for _, p := range resp.JSON200.Data {
		if !isGateway(p, gateway) || len(p.Models) == 0 {
			continue
		}

		models := make([]piModel, 0, len(p.Models))
		for _, m := range p.Models {
			models = append(models, piModel{ID: m, Name: m + " (gateway)", Reasoning: true, Input: []string{"text", "image"}})
		}

		file.Providers[p.Name] = piProvider{
			Name: "Hypomnema gateway (" + p.Name + ")", BaseURL: gateway, API: "openai-completions",
			APIKey: keyCommand, Models: models,
		}
	}

	if len(file.Providers) == 0 {
		return errNoGatewayProvider
	}

	out, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding pi's models: %w", err)
	}

	e.printf("%s\n", out)

	return nil
}

func isGateway(p openapi.Provider, gateway string) bool {
	if string(p.Kind) == kindModelGateway {
		return true
	}

	return p.BaseUrl != nil && strings.TrimRight(*p.BaseUrl, "/") == gateway
}
