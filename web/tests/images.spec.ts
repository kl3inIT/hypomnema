import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The Images page of Settings → Gateway (issue #431), built, against a fake client API, on a phone and a desktop.

type Tally = { answered: number; failed: number }
type Link = {
  provider: "codex" | "antigravity"
  on: boolean
  state: "ready" | "resting" | "not_set_up" | "off"
  resting_until?: string
  model: string
  default_model: string
  model_chosen: boolean
  suggested_models: string[]
  accounts: number
  usage: { today: Tally; month: Tally }
}

const rest = new Date(Date.now() + 20 * 60_000).toISOString()

// Codex with two paid accounts, ready, that made images; Antigravity with one account that rests after its limit.
function start(): Link[] {
  return [
    {
      provider: "codex",
      on: true,
      state: "ready",
      model: "gpt-image-2",
      default_model: "gpt-image-2",
      model_chosen: false,
      suggested_models: ["gpt-image-2"],
      accounts: 2,
      usage: { today: { answered: 3, failed: 0 }, month: { answered: 41, failed: 2 } },
    },
    {
      provider: "antigravity",
      on: true,
      state: "resting",
      resting_until: rest,
      model: "gemini-3.1-flash-image",
      default_model: "gemini-3.1-flash-image",
      model_chosen: false,
      suggested_models: ["gemini-3.1-flash-image", "gemini-3-pro-image"],
      accounts: 1,
      usage: { today: { answered: 0, failed: 1 }, month: { answered: 9, failed: 1 } },
    },
  ]
}

async function fakeServer(page: Page) {
  const world = { providers: start() }
  const calls: string[] = []
  const answer = () => ({ providers: world.providers })

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (request.method() === "PUT" && path.startsWith("/gateway/images/providers/")) {
      const provider = path.split("/").pop() ?? ""
      const change = request.postDataJSON() as { on?: boolean; model?: string }
      calls.push(`${call} ${JSON.stringify(change)}`)
      world.providers = world.providers.map((link) => {
        if (link.provider !== provider) return link

        const next = { ...link }
        if (change.on !== undefined) {
          next.on = change.on
          next.state = change.on ? "ready" : "off"
        }

        if (change.model !== undefined) {
          next.model = change.model || link.default_model
          next.model_chosen = change.model !== ""
        }

        return next
      })

      return json(answer())
    }

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /gateway/images":
        return json(answer())
      case "PUT /gateway/images/order": {
        const { providers } = request.postDataJSON() as { providers: string[] }
        calls.push(`${call} ${providers.join(",")}`)
        world.providers = providers.map((p) => world.providers.find((l) => l.provider === p)!)

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

// Scenario: the owner opens Images from Settings. Codex comes first, ready, with its default model, its two
// accounts and what it made; Antigravity rests after its limit, until the time its vendor named. The page fits a
// phone, and reads on the light theme as on the dark.
test("the owner sees the image providers in their order", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/settings/models")
  await page.getByRole("link", { name: "Images" }).first().click()

  await expect(page.getByRole("heading", { name: "Images", level: 1 })).toBeVisible()

  const codex = row(page, "Codex")
  await expect(codex).toContainText("#1")
  await expect(codex).toContainText("Ready")
  await expect(codex).toContainText("gpt-image-2 (default)")
  await expect(codex).toContainText("2 accounts")
  await expect(codex).toContainText("Today 3 · this month 41 (+2 failed)")

  const antigravity = row(page, "Antigravity")
  await expect(antigravity).toContainText("#2")
  await expect(antigravity).toContainText("Rate limited until")
  await expect(antigravity).toContainText("1 account")

  expect(await fits(page)).toBe(true)
  await shot(page, "images-providers")

  await page.evaluate(`localStorage.setItem("theme", "light")`)
  await page.reload()
  await expect(page.locator("html")).not.toHaveClass(/dark/)
  await expect(row(page, "Codex")).toBeVisible()
  await shot(page, "images-providers-light")
})

// Scenario: the owner sets Antigravity's model to a newer one, by typing its name; a name with a space is refused
// before it is sent. Then he picks a known name, and goes back to the default.
test("the owner sets a provider's model to try a newer one", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/settings/images")

  const antigravity = row(page, "Antigravity")
  await antigravity.getByRole("button", { name: "Change Antigravity's image model" }).click()

  const dialog = page.getByRole("dialog", { name: "Antigravity's image model" })
  const field = dialog.getByRole("textbox", { name: "Model" })
  await expect(field).toHaveAttribute("placeholder", "gemini-3.1-flash-image")

  await field.fill("gemini nano")
  await dialog.getByRole("button", { name: "Save model" }).click()
  await expect(dialog).toContainText("Letters, digits and . _ : - only.")

  await field.fill("gemini-nano-banana-2.1")
  await shot(page, "images-model-dialog")
  await dialog.getByRole("button", { name: "Save model" }).click()

  await expect(dialog).toBeHidden()
  await expect(page.getByText("Antigravity makes images with gemini-nano-banana-2.1")).toBeVisible()
  await expect(antigravity).toContainText("gemini-nano-banana-2.1 (yours)")
  expect(calls).toContain(
    `PUT /gateway/images/providers/antigravity {"model":"gemini-nano-banana-2.1"}`,
  )

  await antigravity.getByRole("button", { name: "Change Antigravity's image model" }).click()
  await dialog.getByRole("button", { name: "gemini-3-pro-image" }).click()
  await expect(field).toHaveValue("gemini-3-pro-image")
  await dialog.getByRole("button", { name: "Use the default model" }).click()
  await expect(field).toHaveValue("")
  await dialog.getByRole("button", { name: "Save model" }).click()

  await expect(antigravity).toContainText("gemini-3.1-flash-image (default)")
  expect(calls).toContain(`PUT /gateway/images/providers/antigravity {"model":""}`)
  expect(await fits(page)).toBe(true)
})

// Scenario: the owner switches Codex off: the server is asked, the row says Off, and a toast says so.
test("the owner switches a provider off", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/settings/images")

  const codex = row(page, "Codex")
  await codex.getByRole("switch", { name: "In use" }).click()

  await expect(codex).toContainText("Off")
  await expect(page.getByText("Codex is off")).toBeVisible()
  expect(calls).toContain(`PUT /gateway/images/providers/codex {"on":false}`)
  await shot(page, "images-codex-off")
})

// Scenario: on a desktop the owner drags Antigravity by its handle onto Codex: the new order is sent, Antigravity
// is #1 and a toast says so. A phone has no drag; the order is set there with `hypomnema image order`.
test("the owner drags a provider to the top", async ({ page, isMobile }) => {
  test.skip(isMobile, "a touch screen does not drag")

  const { calls } = await fakeServer(page)
  await page.goto("/settings/images")

  await page.getByLabel("Drag Antigravity to change the order").dragTo(row(page, "Codex"))

  await expect(row(page, "Antigravity")).toContainText("#1")
  await expect(row(page, "Codex")).toContainText("#2")
  await expect(page.getByText("Antigravity is asked first")).toBeVisible()
  expect(calls).toContain("PUT /gateway/images/order antigravity,codex")
  await shot(page, "images-reordered")
})
