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

// OmpDiscovery is omp's models.yml for a key that may use several models: the gateway as the provider ProviderID
// with proxy discovery, which reads the key's models from GET /v1/models and gives each its wire from
// supported_endpoint_types, Messages for "anthropic" and chat for "openai", with its window and effort levels from
// the row (omp docs/models.md "Proxy discovery" and "Allowed auth/discovery values";
// packages/coding-agent/src/config/model-discovery.ts discoverProxyModels). authHeader sends the key as a bearer
// to the chat models, as the docs' example does; no provider api is named, which proxy discovery does not need.
func OmpDiscovery(g Gateway) string {
	return fmt.Sprintf(`providers:
  %s:
    baseUrl: %s
    apiKey: %s
    authHeader: true
    discovery:
      type: proxy
`, ProviderID, quoteYAML(g.URL), quoteYAML(g.Key))
}

// OmpSettings is omp's config.yml choosing g's model as the default role (omp docs/models.md: modelRoles.default,
// "Assigning default ... switches the active model").
func OmpSettings(g Gateway) string {
	return fmt.Sprintf("modelRoles:\n  default: %s\n", quoteYAML(OmpModel(g)))
}
