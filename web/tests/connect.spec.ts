import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"
import type { GatewayKey as Key, GatewayToolConfig } from "../src/api/generated/types.gen.js"

// Settings → Connect a tool (issue #907), built, against a fake client API, on a phone and a desktop.

const now = new Date().toISOString()
// Fake keys: the files are those the control plane writes (internal/shared/agentconfig), with these in them.
const fullSecret = "hyp_gateway_v1_Zx81Qa34ef56gh78ij90klmnopqrstuv_Pq4x"
const limitedSecret = "hyp_gateway_v1_Lm40Pr34ef56gh78ij90klmnopqrstuv_Tt7w"
const placeholder = "<your gateway key>"

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
    prefix: "hyp_gateway_v1_Nn12Pr",
    models: ["claude-subscription/claude-opus-5-5"],
    endpoints: [],
    created_at: now,
  },
  {
    id: "gwk_3",
    name: "work-laptop",
    prefix: "hyp_gateway_v1_Lm40Pr",
    masked_value: "hyp_ga•••Tt7w",
    models: ["codex-subscription/gpt-6-sol"],
    endpoints: [],
    created_at: now,
  },
]
const secrets: Record<string, string> = { gwk_1: fullSecret, gwk_3: limitedSecret }

// The gateway's models with their windows and largest outputs, as Resolve gives them.
const served: Record<string, [number, number]> = {
  "claude-subscription/claude-opus-5-5": [1000000, 128000],
  "codex-subscription/gpt-6.1-sol": [500000, 128000],
  "codex-subscription/gpt-6-sol": [272000, 128000],
}
const codexModels = ["codex-subscription/gpt-6.1-sol", "codex-subscription/gpt-6-sol"]

const json = (value: unknown) => JSON.stringify(value, null, 2) + "\n"

// files are what the control plane writes for a tool (agentconfig: CodexConfig, ClaudeSettings, OmpDiscovery,
// OmpSettings, OpencodeFile), byte for byte, its JSON keys in Go's order.
function files(tool: string, origin: string, key: string, model: string, models: string[]) {
  const [window] = served[model] ?? [0, 0]
  switch (tool) {
    case "claude_code":
      return [
        {
          path: "~/.claude/settings.json",
          format: "json",
          content: json({
            env: {
              ANTHROPIC_AUTH_TOKEN: key,
              ANTHROPIC_BASE_URL: origin,
              ANTHROPIC_DEFAULT_HAIKU_MODEL: model,
              ANTHROPIC_DEFAULT_OPUS_MODEL: model,
              ANTHROPIC_DEFAULT_SONNET_MODEL: model,
              ...(window ? { CLAUDE_CODE_AUTO_COMPACT_WINDOW: String(window) } : {}),
              CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY: "1",
            },
            hasCompletedOnboarding: true,
          }),
        },
      ]
    case "codex":
      return [
        {
          path: "~/.codex/config.toml",
          format: "toml",
          content:
            `# Hypomnema's model gateway for the Codex CLI\nmodel = "${model}"\nmodel_provider = "hypomnema"\n\n` +
            `[model_providers.hypomnema]\nname = "Hypomnema"\nbase_url = "${origin}/v1"\nwire_api = "responses"\n` +
            `model_catalog_url = "${origin}/v1/codex/models"\nexperimental_bearer_token = "${key}"\n` +
            `supports_websockets = false\n`,
        },
      ]
    case "omp":
      return [
        {
          path: "~/.omp/agent/models.yml",
          format: "yaml",
          content:
            `providers:\n  hypomnema:\n    baseUrl: "${origin}/v1"\n    apiKey: "${key}"\n    authHeader: true\n` +
            `    discovery:\n      type: proxy\n`,
        },
        {
          path: "~/.omp/agent/config.yml",
          format: "yaml",
          content: `modelRoles:\n  default: "hypomnema/${model}"\n`,
        },
      ]
    default:
      return [
        {
          path: "~/.config/opencode/opencode.json",
          format: "json",
          content: json({
            $schema: "https://opencode.ai/config.json",
            model: `hypomnema/${model}`,
            providers: {
              hypomnema: {
                models: Object.fromEntries(
                  models.map((m) => [
                    m,
                    {
                      capabilities: { input: ["text"], output: ["text"], tools: true },
                      limit: { context: served[m]![0], output: served[m]![1] },
                    },
                  ]),
                ),
                package: "@opencode/ai/providers/openai-compatible",
                settings: { apiKey: key, baseURL: `${origin}/v1` },
              },
            },
          }),
        },
      ]
  }
}

// fakeServer answers the client API; slow names the keys whose files come late.
async function fakeServer(page: Page, slow = new Set<string>()) {
  const calls: string[] = []

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname.replace("/api/v1", "")
    const answer = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    const connect = path.match(/^\/gateway\/keys\/([^/]+)\/connect\/([^/]+)$/)
    if (connect) {
      const [, id, tool] = connect as unknown as [string, string, string]
      const asked = url.searchParams.get("model") ?? ""
      const origin = url.searchParams.get("base_url") ?? ""
      calls.push(`${id} ${tool} ${asked} ${origin}`)
      const key = keys.find((k) => k.id === id)!
      const models = (tool === "codex" ? codexModels : Object.keys(served)).filter(
        (m) => key.models.includes("*") || key.models.includes(m),
      )
      const model = models.includes(asked) ? asked : (models[0] ?? "")
      if (slow.has(id)) await new Promise((done) => setTimeout(done, 1500))

      return answer({
        tool: tool as GatewayToolConfig["tool"],
        models,
        model,
        key_stored: id in secrets,
        files: files(tool, origin, secrets[id] ?? placeholder, model || "<model>", models),
      } satisfies GatewayToolConfig)
    }

    switch (`${request.method()} ${path}`) {
      case "GET /me":
        return answer({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /gateway/keys":
        return answer({ data: keys, live_worker_keys: 0 })
      case "GET /tasks":
        return answer({ data: [], runs: [], after: 0 })
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      default:
        return answer({ data: [] })
    }
  })

  return calls
}

const fits = (page: Page) =>
  page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")

// Scenario: the owner opens Connect a tool. Each of the four tools has a card with a key whose value is stored and
// a model of that key, and its files with their paths and what they turn on; changing the model writes them again.
test("the owner copies each tool's file", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/connect")
  const origin = new URL(page.url()).origin

  for (const [name, paths] of [
    ["Claude Code", ["~/.claude/settings.json"]],
    ["Codex", ["~/.codex/config.toml"]],
    ["omp", ["~/.omp/agent/models.yml", "~/.omp/agent/config.yml"]],
    ["OpenCode", ["~/.config/opencode/opencode.json"]],
  ] as const) {
    const card = page.getByRole("region", { name, exact: true })
    await expect(card.getByLabel("Key")).toContainText("memoryos-he")
    for (const path of paths) {
      await expect(card).toContainText(path)
      await expect(card.getByRole("button", { name: `Copy ${path} for ${name}` })).toBeVisible()
    }
    await expect(card).toContainText(fullSecret)
  }

  const codex = page.getByRole("region", { name: "Codex", exact: true })
  await expect(codex.getByLabel("Model")).toContainText("GPT-6.1-Sol")
  await expect(codex).toContainText(`model_catalog_url = "${origin}/v1/codex/models"`)
  await expect(codex).toContainText("model_catalog_url gives Codex the gateway's catalog")
  await expect(page.getByRole("region", { name: "omp", exact: true })).toContainText("type: proxy")
  await expect(page.getByRole("region", { name: "OpenCode", exact: true })).toContainText(
    '"claude-subscription/claude-opus-5-5": {',
  )
  expect(calls).toContain(`gwk_1 codex  ${origin}`)
  // A key without a stored value is not offered.
  await expect(page.getByText("omp-desktop")).toHaveCount(0)
  expect(await fits(page)).toBe(true)
  await shot(page, "connect")
  for (const name of ["Codex", "omp", "OpenCode"]) {
    await page.getByRole("region", { name, exact: true }).evaluate((card) => card.scrollIntoView())
    await shot(page, `connect-${name.toLowerCase()}`)
  }

  await codex.getByLabel("Model").click()
  await page.getByRole("option", { name: "GPT-6-Sol" }).click()
  await expect(codex).toContainText('model = "codex-subscription/gpt-6-sol"')
  expect(calls).toContain(`gwk_1 codex codex-subscription/gpt-6-sol ${origin}`)
})

// Scenario: the owner moves a card from a key that may use every model to a limited one whose file comes late
// (review of PR #922). Until it comes, the card shows neither the other key's file nor a button that would copy it.
test("a file is never shown or copied for a key other than the one chosen", async ({ page }) => {
  await fakeServer(page, new Set(["gwk_3"]))
  await page.goto("/settings/connect")

  const codex = page.getByRole("region", { name: "Codex", exact: true })
  await expect(codex).toContainText(fullSecret)
  await codex.getByLabel("Key").click()
  await page.getByRole("option", { name: "work-laptop" }).click()

  await expect(codex.getByLabel("Key")).toContainText("work-laptop")
  await expect(codex).toContainText("Writing the file…")
  await expect(codex).not.toContainText(fullSecret)
  await expect(codex.getByRole("button", { name: /^Copy / })).toHaveCount(0)
  await expect(codex.getByLabel("Model")).toBeDisabled()
  await shot(page, "connect-writing")

  await expect(codex).toContainText(limitedSecret)
  await expect(codex).toContainText('model = "codex-subscription/gpt-6-sol"')
  await expect(
    codex.getByRole("button", { name: "Copy ~/.codex/config.toml for Codex" }),
  ).toBeVisible()
  await expect(codex).not.toContainText(fullSecret)
})

test("Connect a tool opens from Gateway keys with no key chosen", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings")
  const menu = page.getByRole("navigation", { name: "Settings", exact: true })
  const gatewayKeys = menu.getByRole("link", { name: "Gateway keys", exact: true })
  await expect(gatewayKeys).toBeVisible()
  await expect(menu.getByRole("link", { name: "Connect a tool", exact: true })).toHaveCount(0)
  await gatewayKeys.click()
  await page.getByRole("link", { name: "Connect a tool", exact: true }).click()
  await expect(page).toHaveURL(/\/settings\/connect$/)
  await expect(page.getByRole("heading", { name: "Connect a tool" })).toBeVisible()
  for (const name of ["Claude Code", "Codex", "omp", "OpenCode"]) {
    const card = page.getByRole("region", { name, exact: true })
    await expect(card.getByLabel("Key")).toHaveText("Choose a key")
    await expect(card.getByRole("button", { name: /^Copy / })).toHaveCount(0)
  }
  expect(calls).toEqual([])
})

// Scenario: the owner presses Connect on a key's row of Gateway keys. The page opens with that key; one whose value
// is not stored gives a placeholder and says so, and a tool it can call no model from says that too.
test("a key opened from Gateway keys is chosen, and a key without a value gives a placeholder", async ({
  page,
}) => {
  await fakeServer(page)
  await page.goto("/settings/gateway-keys")
  const row = page.getByRole("row", { name: "omp-desktop" })
  const connect = row.getByRole("link", { name: "Connect a tool with omp-desktop" })
  // The row's actions are at the table's end: it is scrolled there, as the owner would, to show Connect.
  await page
    .locator("[data-slot=table-container]")
    .evaluate((table) => (table.scrollLeft = table.scrollWidth))
  await expect(connect).toBeInViewport()
  await shot(page, "gateway-keys-connect")
  await connect.click()

  await expect(page).toHaveURL(/\/settings\/connect\?key=gwk_2$/)
  const omp = page.getByRole("region", { name: "omp", exact: true })
  await expect(omp.getByLabel("Key")).toContainText("omp-desktop")
  await expect(omp).toContainText("This key's value is not stored")
  await expect(omp).toContainText(`apiKey: "${placeholder}"`)
  await expect(page.getByRole("region", { name: "Codex", exact: true })).toContainText(
    "This key may use no model Codex can call",
  )
  expect(await fits(page)).toBe(true)
  await shot(page, "connect-placeholder")
})
