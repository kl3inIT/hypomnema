import type { ToolAccess, ToolFamily } from "../../src/api/generated/types.gen.js"

// Hypomnema's tools by family, as GET /tools answers (issue #536; internal/shared/agenttool/switches.go), shortened
// to a few names per family.
export const toolFamilies: ToolFamily[] = [
  {
    name: "memory",
    description: "What the agent keeps across chats or Runs",
    pattern: "memory",
    tools: ["memory"],
  },
  {
    name: "artifacts",
    description: "Show a page or a deck in the chat",
    pattern: "show_artifact",
    tools: ["show_artifact"],
  },
  {
    name: "web",
    description: "Search the web and read pages",
    pattern: "web_*",
    tools: ["web_search", "web_fetch"],
  },
  {
    name: "superset",
    description: "Superset's dashboards, charts and data, with a Superset Connection",
    pattern: "superset_*",
    tools: ["superset_dashboards", "superset_api"],
  },
  {
    name: "whatsapp",
    description: "The owner's WhatsApp, with a WhatsApp Connection",
    pattern: "whatsapp_*",
    tools: ["whatsapp_chats", "whatsapp_messages"],
  },
  {
    name: "browser",
    description: "The browser every agent shares, in a tab of its own",
    pattern: "agent_browser_*",
    tools: ["agent_browser_open", "agent_browser_click"],
  },
  {
    name: "phone",
    description: "The phone a node holds",
    pattern: "device_*",
    tools: ["device_screen", "device_tap"],
  },
]

export const fullToolAccess: ToolAccess = {
  families: toolFamilies.map((family) => {
    const state =
      family.name === "phone" ? "off" : family.name === "whatsapp" ? "unavailable" : "available"
    const reason =
      state === "off"
        ? "Not included in the preset"
        : state === "unavailable"
          ? "No WhatsApp Connection"
          : "Included in the preset"
    const source = state === "unavailable" ? "connection" : "preset"

    return {
      ...family,
      state,
      reason,
      source,
      tools: family.tools.map((name) => ({ name, state, reason, source })),
    }
  }),
  skills: [{ name: "android-device", missing: true, reason: "Phone tools are off" }],
}
