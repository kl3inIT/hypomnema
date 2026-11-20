import type { GatewayKey, GatewayTool } from "@/api/generated/types.gen"

// connectKeys are the keys Connect a tool offers (issue #907): the owner's keys whose value is stored (issue #887),
// which a file can hold, by name; and the key the page was opened for from its row on Gateway keys, even without
// one, so that its file shows with a placeholder.
export function connectKeys(keys: GatewayKey[], chosen?: string) {
  return keys
    .filter((k) => k.masked_value || k.id === chosen)
    .toSorted((a, b) => a.name.localeCompare(b.name))
}

// toolNote is the line under a tool's file on what it turns on: for Claude Code and Codex, that /model lists the
// key's models (Claude Code's gateway model discovery; Codex's model_catalog_url), and for omp what each of its two
// files does, and for OpenCode that its provider lists every model.
export function toolNote(tool: GatewayTool, model: string): string {
  switch (tool) {
    case "claude_code":
      return (
        "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY turns on model discovery: /model lists the key's models " +
        "from the gateway. Merge the env block into a settings.json you already have."
      )
    case "codex":
      return (
        "model_catalog_url gives Codex the gateway's catalog: /model lists the key's models with their " +
        "reasoning levels. Keep a config.toml you already have and add these lines."
      )
    case "omp":
      return (
        "models.yml reads every model of the key from the gateway (proxy discovery); config.yml makes " +
        `hypomnema/${model || "<model>"} the default. Merge them into the files you already have.`
      )
    case "opencode":
      return (
        "The provider lists every model of the key, and model is the default. Merge it into an " +
        "opencode.json you already have."
      )
  }
}
