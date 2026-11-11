import { expect, test, type Page } from "@playwright/test"

// The Channels page, built, against a fake client API, on a phone and a desktop.

type Person = { open_id: string; email: string; name: string; created_at: string }

type Bot = {
  app_id: string
  mode: string
  owner_open_id: string
  enabled: boolean
  connected: boolean
  last_error?: string
  people: Person[]
  updated_at: string
}

const now = new Date().toISOString()

// shot keeps a picture for the owner to look at when UI_CHECK names a folder; a normal run takes none.
async function shot(page: Page, name: string) {
  const dir = process.env.UI_CHECK
  if (!dir) return

  const size = page.viewportSize()
  const device = size && size.width < 600 ? "phone" : "desktop"
  await page.screenshot({ path: `${dir}/${name}-${device}.png` })
}

// The fake keeps one bot as the control plane does: set up by a PUT with the app, changed by later PUTs, and
// connected as soon as it is on.
async function fakeServer(page: Page, start: Bot | null = null) {
  let bot = start
  const calls: { call: string; body: unknown }[] = []

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = decodeURIComponent(new URL(request.url()).pathname.replace("/api/v1", ""))
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (request.method() !== "GET") calls.push({ call, body: request.postDataJSON() })

    switch (call) {
      case "GET /me":
        return json({ id: "usr_1", name: "admin", kind: "owner" })
      case "GET /channels/lark":
        return bot ? json(bot) : json({ code: "not_found", message: "no Lark bot is set up" }, 404)
      case "PUT /channels/lark": {
        const set = request.postDataJSON() as {
          app_id?: string
          owner?: string
          mode?: string
          enabled?: boolean
        }

        if (set.owner === "nobody@example.com") {
          return json(
            { code: "invalid", message: "no member of the app's Lark organization has that email" },
            422,
          )
        }

        bot = {
          app_id: set.app_id ?? bot?.app_id ?? "",
          mode: set.mode ?? bot?.mode ?? "owner",
          owner_open_id: bot?.owner_open_id ?? "ou_owner",
          enabled: set.enabled ?? bot?.enabled ?? false,
          connected: set.enabled ?? bot?.connected ?? false,
          people: bot?.people ?? [],
          updated_at: now,
        }

        return json(bot)
      }
      case "DELETE /channels/lark":
        bot = null

        return route.fulfill({ status: 204 })
      case "POST /channels/lark/people": {
        const added = {
          open_id: "ou_linh",
          email: (request.postDataJSON() as { who: string }).who,
          name: "Linh",
          created_at: now,
        }
        if (bot) bot = { ...bot, people: [...bot.people, added] }

        return json(added)
      }
      case "DELETE /channels/lark/people/ou_linh":
        if (bot) bot = { ...bot, people: [] }

        return route.fulfill({ status: 204 })
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "GET /chats":
      case "GET /questions":
      case "GET /profiles":
        return json({ data: [] })
      default:
        return json({ code: "not_found", message: "not found" }, 404)
    }
  })

  return calls
}

const fits = (page: Page) =>
  page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")

// Scenario: there is no Lark bot. The owner sets one up with his app and his email at Lark; an email Lark does
// not know is refused in the dialog, and the secret is typed into a password field. The bot starts off and for
// him only. He switches it on, lets the people he adds in, adds Linh, and takes her off again.
test("the owner sets the Lark bot up, switches it on and lets a person in", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/channels")

  const card = page.getByTestId("channel-card")
  await expect(card).toContainText("Not set up")
  expect(await fits(page)).toBe(true)
  await shot(page, "channels-unset")

  await card.getByRole("button", { name: "Set up" }).click()
  const dialog = page.getByRole("dialog")
  await dialog.getByLabel("App ID").fill("cli_a1b2c3")
  await dialog.getByLabel("App Secret").fill("never-shown")
  await expect(dialog.getByLabel("App Secret")).toHaveAttribute("type", "password")
  await dialog.getByLabel("Your email at Lark").fill("nobody@example.com")
  await dialog.getByRole("button", { name: "Save" }).click()
  await expect(dialog.getByRole("alert")).toContainText("no member of the app's Lark organization")
  await shot(page, "channels-setup-refused")

  await dialog.getByLabel("Your email at Lark").fill("owner@example.com")
  await dialog.getByRole("button", { name: "Save" }).click()
  await expect(dialog).toBeHidden()

  await expect(card).toContainText("Off")
  await expect(card).toContainText("cli_a1b2c3")
  await expect(card.getByLabel("Who may talk to it")).toHaveValue("owner")
  await expect(page.getByText("never-shown")).toHaveCount(0)

  await card.getByRole("switch", { name: "On" }).click()
  await expect(card).toContainText("Connected")

  await card.getByLabel("Who may talk to it").selectOption("allowlist")
  await card.getByRole("button", { name: "Add person" }).click()
  await page.getByRole("dialog").getByLabel("Email at Lark").fill("linh@example.com")
  await page.getByRole("dialog").getByRole("button", { name: "Add" }).click()
  await expect(card.getByLabel("Person Linh")).toContainText("linh@example.com")
  await expect(page.getByRole("dialog", { name: "Add a person" })).toHaveCount(0)
  expect(await fits(page)).toBe(true)
  await shot(page, "channels-connected")

  await card.getByRole("button", { name: "Remove Linh" }).click()
  await shot(page, "channels-remove-person")
  await page.getByRole("alertdialog").getByRole("button", { name: "Remove" }).click()
  await expect(card.getByLabel("Person Linh")).toHaveCount(0)

  expect(calls).toEqual([
    {
      call: "PUT /channels/lark",
      body: { app_id: "cli_a1b2c3", app_secret: "never-shown", owner: "nobody@example.com" },
    },
    {
      call: "PUT /channels/lark",
      body: { app_id: "cli_a1b2c3", app_secret: "never-shown", owner: "owner@example.com" },
    },
    { call: "PUT /channels/lark", body: { enabled: true } },
    { call: "PUT /channels/lark", body: { mode: "allowlist" } },
    { call: "POST /channels/lark/people", body: { who: "linh@example.com" } },
    { call: "DELETE /channels/lark/people/ou_linh", body: null },
  ])
})

// Scenario: the bot is on and Lark refused its app. The card says it is not connected and why, in Lark's words.
test("a bot that could not connect says why", async ({ page }) => {
  await fakeServer(page, {
    app_id: "cli_a1b2c3",
    mode: "owner",
    owner_open_id: "ou_owner",
    enabled: true,
    connected: false,
    last_error: "opening the persistent connection to Lark: app secret invalid",
    people: [],
    updated_at: now,
  })
  await page.goto("/settings/channels")

  const card = page.getByTestId("channel-card")
  await expect(card).toContainText("Not connected")
  await expect(card).toContainText("app secret invalid")
  expect(await fits(page)).toBe(true)
  await shot(page, "channels-not-connected")
})
