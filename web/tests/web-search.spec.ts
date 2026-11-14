import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The Web search page of Settings → Gateway (issue #354), built, against a fake client API, on a phone and a
// desktop.

type Link = {
  provider: string
  source: "key" | "self_hosted" | "public" | "subscription"
  on: boolean
  state: "ready" | "resting" | "not_set_up" | "off"
  resting_until?: string
}

const rest = new Date(Date.now() + 2 * 60_000).toISOString()

// The chain with every kind of provider: Exa with no key yet, the owner's SearXNG, Exa's and Parallel's public
// MCPs (Parallel resting after a 429), a Codex account that searches, Claude switched off, and no Antigravity
// account.
function start(): { chain: Link[]; searxng?: string; exaKey?: string } {
  return {
    searxng: "http://10.0.0.5:8888",
    chain: [
      { provider: "exa", source: "key", on: true, state: "not_set_up" },
      { provider: "searxng", source: "self_hosted", on: true, state: "ready" },
      { provider: "exa", source: "public", on: true, state: "ready" },
      { provider: "parallel", source: "public", on: true, state: "resting", resting_until: rest },
      { provider: "codex", source: "subscription", on: true, state: "ready" },
      { provider: "claude", source: "subscription", on: false, state: "off" },
      { provider: "antigravity", source: "subscription", on: true, state: "not_set_up" },
    ],
  }
}

async function fakeServer(page: Page) {
  const world = start()
  const calls: string[] = []
  const answer = () => ({
    chain: world.chain,
    exa_secret: "web-search-exa",
    ...(world.searxng ? { searxng_url: world.searxng } : {}),
  })

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

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
      case "PUT /gateway/web-search/exa-key": {
        const { key } = request.postDataJSON() as { key: string }
        world.exaKey = key
        calls.push(`${call} ${key.length} characters`)
        world.chain = world.chain.map((link) =>
          link.source === "key" ? { ...link, state: "ready" } : link,
        )

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

// Scenario: the owner opens Web search from Settings. The chain is listed in the order a search tries it, each
// provider with where its search comes from and its state: Exa needs a key, SearXNG is at its address, Parallel
// rests after a 429 until its time, Codex searches from the owner's account, Claude is off, and Antigravity has no
// account, with a way to the Models page to add one.
test("the owner sees the search chain in order", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/settings/models")
  await page.getByRole("link", { name: "Web search" }).first().click()

  await expect(page.getByRole("heading", { name: "Web search", level: 1 })).toBeVisible()

  const exa = row(page, "Exa, With your API key")
  await expect(exa).toContainText("#1")
  await expect(exa).toContainText("Needs a key")
  await expect(exa.getByRole("button", { name: "Add key" })).toBeVisible()

  const searxng = row(page, "SearXNG, Self-hosted")
  await expect(searxng).toContainText("#2")
  await expect(searxng).toContainText("http://10.0.0.5:8888")
  await expect(searxng).toContainText("Ready")

  await expect(row(page, "Exa, Public, no key")).toContainText("Ready")
  await expect(row(page, "Parallel, Public, no key")).toContainText("Resting until")
  await expect(row(page, "Codex, From your Codex account")).toContainText("Ready")

  const claude = row(page, "Claude, From your Claude account")
  await expect(claude).toContainText("Off")
  await expect(claude.getByRole("switch", { name: "In use" })).not.toBeChecked()

  const antigravity = row(page, "Antigravity, From your Antigravity account")
  await expect(antigravity).toContainText("#7")
  await expect(antigravity).toContainText("No account")
  await expect(antigravity.getByRole("link", { name: "Add an account" })).toHaveAttribute(
    "href",
    "/settings/models",
  )

  expect(await fits(page)).toBe(true)
  await shot(page, "web-search-chain")
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
  await shot(page, "web-search-switched-off")
})

// Scenario: the owner enters Exa's key. It is typed in a password field, sent once, and never shown: the row then
// says Ready and where the key is kept, and offers to replace or remove it.
test("the owner enters Exa's key", async ({ page }) => {
  const { calls, world } = await fakeServer(page)
  await page.goto("/settings/web-search")

  const exa = row(page, "Exa, With your API key")
  await exa.getByRole("button", { name: "Add key" }).click()

  const dialog = page.getByRole("dialog", { name: "Exa API key" })
  const field = dialog.getByLabel("API key")
  await expect(field).toHaveAttribute("type", "password")

  // An empty key is not sent.
  await dialog.getByRole("button", { name: "Save key" }).click()
  await expect(dialog).toContainText("Paste the key.")

  await field.fill("exa-0123456789abcdef")
  await shot(page, "web-search-exa-key")
  await dialog.getByRole("button", { name: "Save key" }).click()

  await expect(dialog).toBeHidden()
  await expect(exa).toContainText("Ready")
  await expect(exa).toContainText("In the vault as web-search-exa")
  await expect(exa.getByRole("button", { name: "Replace key" })).toBeVisible()
  await expect(exa.getByRole("button", { name: "Remove Exa's key" })).toBeVisible()
  await expect(page.getByText("exa-0123456789abcdef")).toHaveCount(0)
  expect(world.exaKey).toBe("exa-0123456789abcdef")
  expect(calls).toContain("PUT /gateway/web-search/exa-key 20 characters")

  expect(await fits(page)).toBe(true)
  await shot(page, "web-search-exa-key-saved")
})
