import { efforts, maxFallbacks, runtimes } from "./profiles-state"

// The profile document, as the server reads it (orchestration/profiles.go, profileSource and ApplyProfile). The
// editor checks against it while the owner types; the server checks the rest when it saves.
export const profileSchemaUri = "https://hypomnema.local/schemas/profile.schema.json"
export const profileSchema = {
  $id: profileSchemaUri,
  title: "Hypomnema profile",
  type: "object",
  additionalProperties: false,
  required: ["name", "description"],
  properties: {
    name: {
      type: "string",
      pattern: "^[a-z][a-z0-9-]{0,62}$",
      description: "What a Task names the agent by: lowercase letters, digits and dashes.",
    },
    description: {
      type: "string",
      minLength: 1,
      maxLength: 500,
      description: "What the agent is for. The coordinator chooses an agent for each Task by this.",
    },
    runtime: {
      enum: [...runtimes],
      description: "The CLI a worker of this agent is.",
    },
    model: {
      type: "object",
      additionalProperties: false,
      description: "The model, or nothing for the runtime's default.",
      properties: {
        provider: { type: "string", description: "The provider that offers the model." },
        name: { type: "string", description: "The model's name at that provider." },
        effort: { enum: efforts.map((e) => e.value), description: "The reasoning effort." },
      },
    },
    instructions: {
      type: "string",
      description: "Added after the rules every worker gets, which it may not override.",
    },
    instruction: {
      type: "string",
      deprecationMessage: "Named instructions since 2026-11-28; this name is read for one release.",
      description: "Instructions under their name until 2026-11-28.",
    },
    preset: {
      enum: ["full", "report"],
      description: "Tool families to start from. Full (default) excludes the phone.",
    },
    tools: {
      type: "array",
      uniqueItems: true,
      items: { type: "string" },
      description: "Additional tool or family names on top of the preset.",
    },
    tools_off: {
      type: "array",
      uniqueItems: true,
      items: { type: "string" },
      description: "Tools or families to remove. These win over additions.",
    },
    fallbacks: {
      type: "array",
      maxItems: maxFallbacks,
      uniqueItems: true,
      items: { type: "string", pattern: "^[^/]+/.+$" },
      description:
        "Models, each provider/model, in order, a worker runs while its own model cannot be served.",
    },
    requests: {
      type: "object",
      additionalProperties: false,
      description:
        "What a worker of this agent asks of a machine; a part left out is the default for building work " +
        "(1 core, 3Gi).",
      properties: {
        cpu: {
          type: ["string", "number"],
          description: "Cores (0.5, 2) or thousandths of a core (500m), more than 0.",
        },
        memory: {
          type: ["string", "number"],
          description:
            "Bytes, or a number with Ki, Mi, Gi, Ti, k, M, G or T (512Mi, 3Gi), more than 0.",
        },
      },
    },
  },
}
