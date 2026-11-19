import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"
import type { GatewayKey as Key, GatewayToolConfig } from "../src/api/generated/types.gen.js"

// Settings → Connect a tool (issue #907), built, against a fake client API, on a phone and a desktop.

const now = new Date().toISOString()
const secret = "hyp_gateway_v1_Zx81Qa34ef56gh78ij90klmnopqrstuv_Pq4x"

const keys: Key[] = [
  {
    id: "gwk_1",
    name: "memoryos-he",
    prefix: "hyp_gateway_v1_Zx81Qa",
    masked_value: "hyp_ga•••Pq4x",
    models: ["*"],
    endpoints: [],
    created_at: now,
  },
  {
    id: "gwk_2",
    name: "omp-desktop",
    prefix: "hyp_gateway_v1_Lm40Pr",
    models: ["claude-subscription/claude-opus-5-5"],
    endpoints: [],
    created_at: now,
  },
]

const codexModels = ["codex-subscription/gpt-6.1-sol", "codex-subscription/gpt-6-sol"]
const allModels = ["claude-subscription/claude-opus-5-5", ...codexModels]

// file is what the server writes for a tool, enough of it for the page to show: the path and the key and model.
function file(tool: string, key: string, model: string): GatewayToolConfig["files"][number] {
  switch (tool) {
    case "codex":
      return {
        path: "~/.codex/config.toml",
        format: "toml",
        content: `model = "${model}"\nmodel_provider = "hypomnema"\n\n[model_providers.hypomnema]\nexperimental_bearer_token = "${key}"\n`,
      }
    case "claude_code":
      return {
        path: "~/.claude/settings.json",
        format: "json",
        content: JSON.stringify(
          { env: { ANTHROPIC_AUTH_TOKEN: key, ANTHROPIC_DEFAULT_OPUS_MODEL: model } },
          null,
          2,
        ),
      }
    case "omp":
      return {
        path: "~/.omp/agent/models.yml",
        format: "yaml",
        content: `providers:\n  hypomnema:\n    apiKey: "${key}"\n    models:\n      - id: "${model}"\n`,
      }
    default:
      return {
        path: "~/.config/opencode/opencode.json",
        format: "json",
        content: JSON.stringify({ model: `hypomnema/${model}`, apiKey: key }, null, 2),
      }
  }
}

async function fakeServer(page: Page) {
  const calls: string[] = []

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    const connect = path.match(/^\/gateway\/keys\/([^/]+)\/connect\/([^/]+)$/)
    if (connect) {
      const [, id, tool] = connect
      const asked = url.searchParams.get("model") ?? ""
      calls.push(`${id} ${tool} ${asked} ${url.searchParams.get("base_url")}`)
      const key = keys.find((k) => k.id === id)!
      const models = (tool === "codex" ? codexModels : allModels).filter(
        (m) => key.models.includes("*") || key.models.includes(m),
      )
      const model = models.includes(asked) ? asked : (models[0] ?? "")
      const stored = id === "gwk_1"

      return json({
        tool: tool as GatewayToolConfig["tool"],
        models,
        model,
        key_stored: stored,
        files: [file(tool!, stored ? secret : "<your gateway key>", model || "<model>")],
      } satisfies GatewayToolConfig)
    }

    switch (`${request.method()} ${path}`) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /gateway/keys":
        return json({ data: keys, live_worker_keys: 0 })
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      default:
        return json({ data: [] })
    }
  })

  return calls
}

const fits = (page: Page) =>
  page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")

// Scenario: the owner opens Connect a tool. Each of the four tools has a card with a key whose value is stored and
// a model of that key, and the file with its path and what it turns on; changing the model writes the file again.
test("the owner copies each tool's file", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/connect")

  for (const [name, path] of [
    ["Claude Code", "~/.claude/settings.json"],
    ["Codex", "~/.codex/config.toml"],
    ["omp", "~/.omp/agent/models.yml"],
    ["OpenCode", "~/.config/opencode/opencode.json"],
  ] as const) {
    const card = page.getByRole("region", { name, exact: true })
    await expect(card).toContainText(path)
    await expect(card).toContainText(secret)
    await expect(card.getByLabel("Key")).toContainText("memoryos-he")
    await expect(card.getByRole("button", { name: `Copy ${path} for ${name}` })).toBeVisible()
  }

  const codex = page.getByRole("region", { name: "Codex", exact: true })
  await expect(codex.getByLabel("Model")).toContainText("GPT-6.1-Sol")
  await expect(codex).toContainText("model_catalog_url gives Codex the gateway's catalog")
  await expect(page.getByRole("region", { name: "Claude Code" })).toContainText(
    "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY",
  )
  expect(calls).toContain(`gwk_1 codex  ${new URL(page.url()).origin}`)
  // A key without a stored value is not offered.
  await expect(page.getByText("omp-desktop")).toHaveCount(0)
  expect(await fits(page)).toBe(true)
  await shot(page, "connect")

  await codex.getByLabel("Model").click()
  await page.getByRole("option", { name: "GPT-6-Sol" }).click()
  await expect(codex).toContainText('model = "codex-subscription/gpt-6-sol"')
  expect(calls).toContain(`gwk_1 codex codex-subscription/gpt-6-sol ${new URL(page.url()).origin}`)
})

// Scenario: the owner presses Connect on a key's row of Gateway keys. The page opens with that key; one whose value
// is not stored gives a placeholder and says so, and a tool it can call no model from says that too.
test("a key opened from Gateway keys is chosen, and a key without a value gives a placeholder", async ({
  page,
}) => {
  await fakeServer(page)
  await page.goto("/settings/gateway-keys")
  await page
    .getByRole("row", { name: "omp-desktop" })
    .getByRole("link", { name: "Connect a tool with omp-desktop" })
    .click()

  await expect(page).toHaveURL(/\/settings\/connect\?key=gwk_2$/)
  const omp = page.getByRole("region", { name: "omp", exact: true })
  await expect(omp.getByLabel("Key")).toContainText("omp-desktop")
  await expect(omp).toContainText("This key's value is not stored")
  await expect(omp).toContainText('apiKey: "<your gateway key>"')
  await expect(page.getByRole("region", { name: "Codex", exact: true })).toContainText(
    "This key may use no model Codex can call",
  )
  expect(await fits(page)).toBe(true)
  await shot(page, "connect-placeholder")
})
