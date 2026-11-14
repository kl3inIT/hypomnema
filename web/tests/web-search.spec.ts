import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The Web search page of Settings → Gateway (issues #354, #393), built, against a fake client API, on a phone and
// a desktop.

type Tally = { answered: number; failed: number }
type Usage = { today: Tally; month: Tally; month_dollars?: number }
type Key = {
  name: string
  state: "ready" | "resting" | "spent"
  rest?: string
  resting_until?: string
  last_used_at?: string
  balance?: { used: number; limit: number; remaining: number; checked_at: string }
  usage: Usage
}
type Link = {
  provider: string
  source: "key" | "public" | "subscription"
  on: boolean
  state: "ready" | "resting" | "not_set_up" | "off"
  rest?: string
  resting_until?: string
  keys?: Key[]
  usage: Usage
}

const rest = new Date(Date.now() + 2 * 60_000).toISOString()
const used = new Date(Date.now() - 5 * 60_000).toISOString()
const none: Usage = { today: { answered: 0, failed: 0 }, month: { answered: 0, failed: 0 } }
const some = (today: number, month: number, failed = 0): Usage => ({
  today: { answered: today, failed: 0 },
  month: { answered: month, failed },
})

// The chain with every kind of provider: Exa with two keys, one resting out of credits; Firecrawl with a key whose
// balance it tells; Parallel with none; Tavily with a key whose balance is spent; TinyFish with none. Without a
// key: Parallel resting after a 429, You.com at its daily limit. A Codex account that searches, Claude switched off,
// and no Antigravity account.
function start(): Link[] {
  const keyed = (provider: string, keys: Key[]): Link => ({
    provider,
    source: "key",
    on: true,
    state: keys.length ? "ready" : "not_set_up",
    keys,
    usage: none,
  })
  const keyless = (provider: string, more: Partial<Link> = {}): Link => ({
    provider,
    source: "public",
    on: true,
    state: "ready",
    usage: some(1, 12),
    ...more,
  })

  return [
    keyed("exa", [
      {
        name: "web-search-exa-2",
        state: "ready",
        usage: { ...some(3, 41, 2), month_dollars: 0.287 },
        last_used_at: used,
      },
      {
        name: "web-search-exa",
        state: "resting",
        rest: "no_credits",
        resting_until: rest,
        usage: { ...some(0, 120), month_dollars: 10 },
        last_used_at: used,
      },
    ]),
    keyed("firecrawl", [
      {
        name: "web-search-firecrawl",
        state: "ready",
        balance: { used: 120, limit: 1000, remaining: 880, checked_at: used },
        usage: some(2, 60),
      },
    ]),
    keyed("parallel", []),
    keyed("tavily", [
      {
        name: "web-search-tavily",
        state: "spent",
        balance: { used: 1000, limit: 1000, remaining: 0, checked_at: used },
        usage: some(0, 1000),
      },
    ]),
    keyed("tinyfish", []),
    keyless("exa"),
    keyless("parallel", { state: "resting", rest: "rate_limit", resting_until: rest }),
    keyless("firecrawl"),
    keyless("tavily"),
    keyless("you", {
      state: "resting",
      rest: "daily_limit",
      resting_until: rest,
      usage: some(100, 900),
    }),
    { provider: "codex", source: "subscription", on: true, state: "ready", usage: some(1, 3) },
    { provider: "claude", source: "subscription", on: false, state: "off", usage: none },
    { provider: "antigravity", source: "subscription", on: true, state: "not_set_up", usage: none },
  ]
}

async function fakeServer(page: Page) {
  const world = { chain: start(), keys: {} as Record<string, string> }
  const calls: string[] = []
  const answer = () => ({ chain: world.chain })

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })
    const keys = path.match(/^\/gateway\/web-search\/providers\/(\w+)\/keys(?:\/(.+))?$/)

    if (keys && request.method() === "POST") {
      const provider = keys[1]
      const { key } = request.postDataJSON() as { key: string }
      const link = world.chain.find((l) => l.source === "key" && l.provider === provider)!
      const name = `web-search-${provider}-${(link.keys?.length ?? 0) + 1}`
      world.keys[name] = key
      calls.push(`${call} ${key.length} characters`)
      link.keys = [...(link.keys ?? []), { name, state: "ready", usage: none }]
      link.state = "ready"

      return json({ name, web_search: answer() }, 201)
    }

    if (keys && request.method() === "DELETE") {
      calls.push(call)
      const link = world.chain.find((l) => l.source === "key" && l.provider === keys[1])!
      link.keys = (link.keys ?? []).filter((k) => k.name !== keys[2])

      return json(answer())
    }

    if (request.method() === "PUT" && path.startsWith("/gateway/web-search/providers/")) {
      const provider = path.split("/").pop() ?? ""
      const { on } = request.postDataJSON() as { on: boolean }
      calls.push(`${call} ${on}`)
      world.chain = world.chain.map((link) =>
        link.provider === provider
          ? { ...link, on, state: on ? (link.state === "off" ? "ready" : link.state) : "off" }
          : link,
      )

      return json(answer())
    }

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /gateway/web-search":
        return json(answer())
      case "PUT /gateway/web-search/order": {
        const { providers } = request.postDataJSON() as { providers: string[] }
        calls.push(`${call} ${providers.join(",")}`)
        const keyed = world.chain.filter((l) => l.source === "key")
        world.chain = [
          ...providers.map((p) => keyed.find((l) => l.provider === p)!),
          ...world.chain.filter((l) => l.source !== "key"),
        ]

        return json(answer())
      }
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      default:
        return json({ data: [] })
    }
  })

  return { calls, world }
}

const fits = (page: Page) =>
  page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")

const row = (page: Page, label: string) => page.getByLabel(label, { exact: true })

// Scenario: the owner opens Web search from Settings. The keyed providers come first in their order, each with its
// keys: Exa's two, one resting out of credits, with what each used and Exa's spend; Firecrawl's with its balance as
// a meter; Tavily's spent; Parallel and TinyFish with no key, TinyFish with the note on its terms. Then the
// keyless ones, Parallel rate limited and You.com at its daily limit, each with its calls and "allowance unknown";
// then the subscriptions, Antigravity with a way to the Models page.
test("the owner sees the search chain, its keys and what each used", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/settings/models")
  await page.getByRole("link", { name: "Web search" }).first().click()

  await expect(page.getByRole("heading", { name: "Web search", level: 1 })).toBeVisible()

  const exa = row(page, "Exa, With your API keys")
  await expect(exa).toContainText("#1")
  await expect(exa).toContainText("2 keys")

  const exaKeys = page.getByRole("list", { name: "Exa's keys" })
  const second = exaKeys.getByLabel("web-search-exa-2", { exact: true })
  await expect(second).toContainText("Ready")
  await expect(second).toContainText("Today 3 · this month 41 (+2 failed)")
  await expect(second).toContainText("$0.287 this month")
  await expect(second).toContainText("Allowance unknown")
  await expect(exaKeys.getByLabel("web-search-exa", { exact: true })).toContainText(
    "Out of credits until",
  )

  const firecrawl = page.getByLabel("web-search-firecrawl", { exact: true })
  await expect(firecrawl).toContainText("880 of 1,000 credits left")
  await expect(firecrawl.getByRole("meter")).toHaveAttribute("aria-valuenow", "12")

  await expect(page.getByLabel("web-search-tavily", { exact: true })).toContainText("Spent")
  await expect(row(page, "Parallel, With your API keys")).toContainText("No key")
  await expect(row(page, "TinyFish, With your API keys")).toContainText("No key")
  await expect(
    page.getByText("TinyFish's terms let it use your queries to train its models."),
  ).toBeVisible()

  await expect(row(page, "Parallel, Public, no key")).toContainText("Rate limited until")
  const you = row(page, "You.com, Public, no key")
  await expect(you).toContainText("Daily limit until")
  await expect(you).toContainText("Today 100 · this month 900")
  await expect(you).toContainText("Allowance unknown")

  await expect(row(page, "Codex, From your Codex account")).toContainText("Ready")

  const claude = row(page, "Claude, From your Claude account")
  await expect(claude).toContainText("Off")
  await expect(claude.getByRole("switch", { name: "In use" })).not.toBeChecked()

  const antigravity = row(page, "Antigravity, From your Antigravity account")
  await expect(antigravity).toContainText("No account")
  await expect(antigravity.getByRole("link", { name: "Add an account" })).toHaveAttribute(
    "href",
    "/settings/models",
  )

  expect(await fits(page)).toBe(true)
  await shot(page, "web-search-chain")
  await page.getByRole("heading", { name: "Without a key" }).scrollIntoViewIfNeeded()
  await shot(page, "web-search-keyless")
})

// Scenario: the owner switches Codex's search off: the server is asked, the row says Off, and a toast says so.
test("the owner switches a provider off", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/settings/web-search")

  const codex = row(page, "Codex, From your Codex account")
  await codex.getByRole("switch", { name: "In use" }).click()

  await expect(codex).toContainText("Off")
  await expect(page.getByText("Codex is off")).toBeVisible()
  expect(calls).toContain("PUT /gateway/web-search/providers/codex false")
})

// Scenario: the owner adds a second Tavily key. It is typed in a password field, sent once, and never shown: the
// toast names the secret it is kept under and the key is listed by that name. Then he removes Exa's resting key,
// after saying yes.
test("the owner adds a key and removes one", async ({ page }) => {
  const { calls, world } = await fakeServer(page)
  await page.goto("/settings/web-search")

  const tavily = row(page, "Tavily, With your API keys")
  await tavily.getByRole("button", { name: "Add a Tavily key" }).click()

  const dialog = page.getByRole("dialog", { name: "Tavily API key" })
  const field = dialog.getByLabel("API key")
  await expect(field).toHaveAttribute("type", "password")

  // An empty key is not sent.
  await dialog.getByRole("button", { name: "Save key" }).click()
  await expect(dialog).toContainText("Paste the key.")

  await field.fill("tvly-0123456789abcdef")
  await shot(page, "web-search-add-key")
  await dialog.getByRole("button", { name: "Save key" }).click()

  await expect(dialog).toBeHidden()
  await expect(
    page.getByText("Tavily's key is kept in the vault as web-search-tavily-2"),
  ).toBeVisible()
  await expect(page.getByLabel("web-search-tavily-2", { exact: true })).toContainText("Ready")
  await expect(page.getByText("tvly-0123456789abcdef")).toHaveCount(0)
  expect(world.keys["web-search-tavily-2"]).toBe("tvly-0123456789abcdef")
  expect(calls).toContain("POST /gateway/web-search/providers/tavily/keys 21 characters")

  await page.getByRole("button", { name: "Remove web-search-exa", exact: true }).click()
  await page.getByRole("alertdialog").getByRole("button", { name: "Remove" }).click()
  await expect(page.getByLabel("web-search-exa", { exact: true })).toHaveCount(0)
  expect(calls).toContain("DELETE /gateway/web-search/providers/exa/keys/web-search-exa")

  expect(await fits(page)).toBe(true)
})

// Scenario: on a desktop the owner drags Tavily by its handle onto Exa: the new order is sent, Tavily is #1 and a
// toast says so. A phone has no drag; the order is set there with `hypomnema web order`.
test("the owner drags a keyed provider to the top", async ({ page, isMobile }) => {
  test.skip(isMobile, "a touch screen does not drag")

  const { calls } = await fakeServer(page)
  await page.goto("/settings/web-search")

  await page
    .getByLabel("Drag Tavily to change the order")
    .dragTo(row(page, "Exa, With your API keys"))

  await expect(row(page, "Tavily, With your API keys")).toContainText("#1")
  await expect(row(page, "Exa, With your API keys")).toContainText("#2")
  await expect(page.getByText("Tavily is asked first")).toBeVisible()
  expect(calls).toContain("PUT /gateway/web-search/order tavily,exa,firecrawl,parallel,tinyfish")
  await shot(page, "web-search-reordered")
})
