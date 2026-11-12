import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The Models page of Settings (issue #199), built, against a fake client API, on a phone and a desktop.

const now = Date.now()
const iso = (ms: number) => new Date(ms).toISOString()

type Account = {
  id: string
  runtime: string
  label: string
  status: "active" | "disabled"
  position: number
  cooling_until?: string
  cooling_reason?: string
  email?: string
  plan?: string
  sign_in?: "active" | "login_required" | "no_tokens"
}

const provider = (name: string, kind: string, models: string[], more: object = {}) => ({
  name,
  kind,
  models,
  source: "",
  updated_at: iso(now),
  ...more,
})

const providers = [
  provider("claude-subscription", "claude_subscription", [
    "claude-fable-5-2",
    "claude-opus-5-5",
    "claude-sonnet-5-5",
    "claude-haiku-5-5",
  ]),
  provider("codex-subscription", "codex_subscription", ["gpt-6.1-sol", "gpt-6-luna"]),
  provider("antigravity-subscription", "antigravity_subscription", [
    "claude-sonnet-4-6",
    "gemini-3-flash",
  ]),
  provider("openrouter", "openai_compatible", ["deepseek-v4"], {
    base_url: "https://openrouter.ai/api/v1",
    api_key_ref: "vault:openrouter-key",
  }),
  provider("model-gateway", "model_gateway", ["gpt-6.1-sol", "claude-sonnet-5-5"]),
]

const models = [
  {
    name: "gpt-6.1-sol",
    targets: [{ provider: "codex-subscription", upstream_model: "gpt-6.1-sol" }],
    updated_at: iso(now),
  },
  {
    name: "claude-sonnet-5-5",
    targets: [
      { provider: "claude-subscription", upstream_model: "claude-sonnet-5-5" },
      { provider: "openrouter", upstream_model: "deepseek-v4" },
    ],
    updated_at: iso(now),
  },
  {
    name: "claude-sonnet",
    targets: [{ provider: "antigravity-subscription", upstream_model: "claude-sonnet-4-6" }],
    updated_at: iso(now),
  },
]

async function fakeServer(page: Page, start?: Account[]) {
  let accounts: Account[] = start ?? [
    {
      id: "acc_work",
      runtime: "claude",
      label: "work",
      status: "active",
      position: 1,
      email: "owner@example.com",
      plan: "max",
      sign_in: "active",
    },
    {
      id: "acc_spare",
      runtime: "claude",
      label: "spare",
      status: "active",
      position: 2,
      email: "second@example.com",
      plan: "pro",
      sign_in: "active",
      cooling_until: iso(now + 2 * 3600_000),
      cooling_reason: "rate limit",
    },
    {
      id: "acc_main",
      runtime: "codex",
      label: "main",
      status: "active",
      position: 1,
      email: "owner@example.com",
      plan: "pro",
      sign_in: "active",
    },
    {
      id: "acc_old",
      runtime: "codex",
      label: "old",
      status: "active",
      position: 2,
      email: "a-rather-long-address-of-an-old-account@example.com",
      plan: "plus",
      sign_in: "login_required",
    },
    { id: "acc_node", runtime: "codex", label: "kdlstc-dev", status: "active", position: 3 },
    {
      id: "acc_trial",
      runtime: "antigravity",
      label: "trial",
      status: "disabled",
      position: 1,
      email: "owner@example.com",
      sign_in: "active",
    },
  ]
  const calls: string[] = []

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = decodeURIComponent(new URL(request.url()).pathname.replace("/api/v1", ""))
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    const [, kind, id, action] = path.split("/")

    if (kind === "accounts" && id) {
      calls.push(call)

      const account = accounts.find((a) => a.id === id)
      if (!account) return json({ code: "not_found", message: `not found: account ${id}` }, 404)

      if (request.method() === "DELETE") {
        if (!account.sign_in) {
          return json(
            { code: "conflict", message: "the account was not logged in on the control plane" },
            409,
          )
        }

        accounts = accounts.filter((a) => a.id !== id)

        return route.fulfill({ status: 204 })
      }

      account.status = action === "enable" ? "active" : "disabled"

      return json(account)
    }

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /accounts":
        return json({ data: accounts })
      case "GET /providers":
        return json({ data: providers })
      case "GET /gateway/models":
        return json({ data: models })
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

const card = (page: Page, name: string) =>
  page.getByTestId("subscription-card").filter({ has: page.getByText(name, { exact: true }) })

// Scenario: the owner opens Models from Settings. Each subscription shows whether it serves now and its accounts
// in the order the gateway tries them, each with the one thing wrong with it; the gateway's models show where
// each is served from, in order, and which an agent may name.
test("the owner sees where models come from", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/settings/integrations")
  await page.getByRole("link", { name: "Models" }).first().click()

  await expect(page.getByRole("heading", { name: "Models", level: 1 })).toBeVisible()

  const claude = card(page, "Claude")
  await expect(claude).toContainText("Claude subscription · 2 accounts")
  await expect(claude).toContainText("Serving")
  await expect(claude.getByLabel("Claude account spare")).toContainText("Resting until")
  await expect(claude).toContainText("Claude Opus 5.5")

  const codex = card(page, "Codex")
  await expect(codex.getByLabel("Codex account old")).toContainText("Needs sign-in")
  await expect(codex.getByLabel("Codex account kdlstc-dev")).toContainText("Signed in on a machine")
  // An account that was signed in on a machine is not removed from here.
  await expect(codex.getByRole("button", { name: "Remove kdlstc-dev" })).toHaveCount(0)

  await expect(card(page, "Antigravity")).toContainText("Off")
  await expect(page.getByTestId("api-provider-card")).toContainText("vault:openrouter-key")

  expect(await fits(page)).toBe(true)
  await shot(page, "models")

  const routes = page.getByRole("region", { name: "Models" }).last()
  await routes.scrollIntoViewIfNeeded()
  await expect(routes).toContainText("then")
  await expect(routes).toContainText("Offered")
  await shot(page, "models-routes")
})

// Scenario: the owner takes an account out of use and puts it back, turns a whole subscription off, and
// removes an account after being told what follows from it.
test("the owner turns accounts off and removes one", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/models")

  const codex = card(page, "Codex")
  const main = codex.getByLabel("Codex account main")

  await main.getByRole("switch", { name: "In use" }).click()
  await expect(main).toContainText("Off")
  await expect(page.getByText("main is out of use")).toBeVisible()
  await shot(page, "models-account-off")

  await main.getByRole("switch", { name: "In use" }).click()
  await expect(main).not.toContainText("Off")

  // The card's switch turns every account of the subscription off at once.
  await codex.getByRole("switch", { name: "In use" }).first().click()
  await expect(codex.getByText("Off", { exact: true })).toHaveCount(4)
  expect(calls.filter((c) => c.endsWith("/disable"))).toHaveLength(4)
  await shot(page, "models-subscription-off")

  await codex.getByRole("button", { name: "Remove old" }).click()
  const dialog = page.getByRole("alertdialog")
  await expect(dialog).toContainText("later work uses the next Codex account")
  await shot(page, "models-remove-confirm")

  await dialog.getByRole("button", { name: "Remove" }).click()
  await expect(codex.getByLabel("Codex account old")).toHaveCount(0)
  expect(calls).toContain("DELETE /accounts/acc_old")
})

// With nothing set up the page says how to begin.
test("an installation with no subscription says how to add one", async ({ page }) => {
  await fakeServer(page, [])
  await page.route("**/api/v1/providers", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: '{"data":[]}' }),
  )
  await page.route("**/api/v1/gateway/models", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: '{"data":[]}' }),
  )
  await page.goto("/settings/models")

  await expect(page.getByText("hypomnema accounts login claude")).toBeVisible()
  await expect(page.getByText("The gateway serves no model yet.")).toBeVisible()
  await shot(page, "models-empty")
})
