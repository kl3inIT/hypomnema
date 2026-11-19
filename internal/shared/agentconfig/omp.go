package agentconfig

import (
	"fmt"
)

// OmpModels is omp's models.yml naming the gateway as the provider ProviderID with one model (omp docs/models.md):
// the model's own wire when it is a subscription's (issue #549), as pi is given it: Messages at the gateway's root,
// Responses or chat under /v1. omp reads apiKey as an environment variable's name, and as the key itself when no
// such variable is set (docs/models.md, "models.yml apiKey behavior").
func OmpModels(g Gateway) string {
	api, base := "openai-completions", g.URL

	switch g.API {
	case WireMessages:
		api, base = g.API, g.Root()
	case WireResponses:
		api = g.API
	}

	return fmt.Sprintf(`providers:
  %s:
    baseUrl: %s
    apiKey: %s
    api: %s
    models:
      - id: %s
        name: %s
        input: [text]
        reasoning: %t
        contextWindow: %d
        maxTokens: %d
`, ProviderID, quoteYAML(base), quoteYAML(g.Key), api, quoteYAML(g.Model), quoteYAML(g.Model), g.Reasoning,
		g.window(), g.output())
}

// OmpModel is how omp names the model of OmpModels on its command line and in its /model picker.
func OmpModel(g Gateway) string { return ProviderID + "/" + g.Model }
