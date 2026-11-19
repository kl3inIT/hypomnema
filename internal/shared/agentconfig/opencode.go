package agentconfig

// OpencodeConfig is opencode's configuration naming the gateway as the provider ProviderID with one model, chosen
// as the default (opencode services/site/src/docs/content/providers.mdx, "Custom": providers.<id> with package,
// settings.baseURL and models; config.mdx: model). opencode reads settings.apiKey as the key, or "{env:NAME}" from
// the environment. A caller adds its own keys (a worker's permissions and skills) before it encodes the map.
func OpencodeConfig(g Gateway) map[string]any {
	model := map[string]any{
		"capabilities": map[string]any{"tools": true, "input": []string{"text"}, "output": []string{"text"}},
		"limit":        map[string]any{"context": g.window(), "output": g.output()},
	}

	return map[string]any{
		"model": ProviderID + "/" + g.Model,
		"providers": map[string]any{ProviderID: map[string]any{
			"package":  "@opencode/ai/providers/openai-compatible",
			"settings": map[string]any{"baseURL": g.URL, "apiKey": g.Key},
			"models":   map[string]any{g.Model: model},
		}},
	}
}
