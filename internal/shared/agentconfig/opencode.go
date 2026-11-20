package agentconfig

// OpencodeConfig is opencode's configuration naming the gateway as the provider ProviderID with its models, Model
// chosen as the default (opencode services/site/src/docs/content/providers.mdx, "Custom": providers.<id> with
// package, settings.baseURL and models; config.mdx: model), as 9router's OpenCode card lists every model it is
// given under its provider with the active one as the default (OpenCodeToolCard.js:86-111, saveModels). opencode
// reads settings.apiKey as the key, or "{env:NAME}" from the environment. A caller adds its own keys (a worker's
// permissions and skills) before it encodes the map.
func OpencodeConfig(g Gateway) map[string]any {
	models := map[string]any{}

	for _, m := range g.models() {
		models[m.ID] = map[string]any{
			"capabilities": map[string]any{"tools": true, "input": []string{"text"}, "output": []string{"text"}},
			"limit":        map[string]any{"context": m.ContextWindow, "output": m.MaxOutput},
		}
	}

	return map[string]any{
		"model": ProviderID + "/" + g.Model,
		"providers": map[string]any{ProviderID: map[string]any{
			"package":  "@opencode/ai/providers/openai-compatible",
			"settings": map[string]any{"baseURL": g.URL, "apiKey": g.Key},
			"models":   models,
		}},
	}
}
