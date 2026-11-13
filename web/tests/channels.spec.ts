import { expect, test, type Page } from "@playwright/test"

import { choose } from "./choose.js"

// The Channels page, built, against a fake client API, on a phone and a desktop.

type Person = { open_id: string; email: string; name: string; created_at: string }

type Group = {
  chat_id: string
  name: string
  enabled: boolean
  asked_at?: string
  created_at: string
}

type Channel = {
  name: string
  bot: string
  app_id: string
  mode: string
  owner_open_id: string
  enabled: boolean
  connected: boolean
  last_error?: string
  people: Person[]
  groups: Group[]
  created_at: string
  updated_at: string
}

const now = new Date().toISOString()

const bot = (name: string, persona: string) => ({
  name,
  persona,
  memory: "",
  memory_limit: 2200,
  skills: [],
  skills_off: [],
  created_at: now,
  updated_at: now,
})

const bots = [
  bot("coordinator", "Plan the work, give it to workers and report what they did."),
  bot("vmap", "You answer about the maps app."),
]

const main: Channel = {
  name: "main",
  bot: "coordinator",
  app_id: "cli_a1b2c3",
  mode: "owner",
  owner_open_id: "ou_owner",
  enabled: true,
  connected: true,
  people: [],
  groups: [],
  created_at: now,
  updated_at: now,
}

// shot keeps a picture for the owner to look at when UI_CHECK names a folder; a normal run takes none.
async function shot(page: Page, name: string) {
  const dir = process.env.UI_CHECK
  if (!dir) return

  const size = page.viewportSize()
  const device = size && size.width < 600 ? "phone" : "desktop"
  await page.screenshot({ path: `${dir}/${name}-${device}.png` })
}

// The fake keeps the Channels as the control plane does: each set up by a PUT with its app, changed by later
// PUTs, and connected as soon as it is on. An app that another Channel has is refused, and so is an owner Lark
// does not know.
async function fakeServer(page: Page, start: Channel[] = []) {
  let channels = start
  const calls: { call: string; body: unknown }[] = []

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = decodeURIComponent(new URL(request.url()).pathname.replace("/api/v1", ""))
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (request.method() !== "GET") calls.push({ call, body: request.postDataJSON() })

    const one = /^\/channels\/lark\/([^/]+)$/.exec(path)
    const people = /^\/channels\/lark\/([^/]+)\/people(?:\/([^/]+))?$/.exec(path)
    const group = /^\/channels\/lark\/([^/]+)\/groups\/([^/]+)$/.exec(path)
    const found = channels.find((c) => c.name === (one ?? people ?? group)?.[1])

    if (group && found && request.method() === "PUT") {
      const { enabled } = request.postDataJSON() as { enabled: boolean }
      const changed = found.groups.find((g) => g.chat_id === group[2])
      if (!changed) return json({ code: "not_found", message: "no such group" }, 404)

      const stored = { ...changed, enabled }
      channels = channels.map((c) =>
        c === found
          ? { ...c, groups: c.groups.map((g) => (g.chat_id === stored.chat_id ? stored : g)) }
          : c,
      )

      return json(stored)
    }

    if (one && request.method() === "PUT") {
      const set = request.postDataJSON() as {
        bot?: string
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

      const other = channels.find((c) => c.app_id === set.app_id && c.name !== one[1])
      if (other) {
        return json(
          {
            code: "conflict",
            message: `the Lark app ${other.app_id} is the app of channel ${other.name}; one app is one channel`,
          },
          409,
        )
      }

      const stored: Channel = {
        name: one[1]!,
        bot: set.bot ?? found?.bot ?? "coordinator",
        app_id: set.app_id ?? found?.app_id ?? "",
        mode: set.mode ?? found?.mode ?? "owner",
        owner_open_id: found?.owner_open_id ?? "ou_owner",
        enabled: set.enabled ?? found?.enabled ?? false,
        connected: set.enabled ?? found?.connected ?? false,
        people: found?.people ?? [],
        groups: found?.groups ?? [],
        created_at: found?.created_at ?? now,
        updated_at: now,
      }
      channels = found
        ? channels.map((c) => (c.name === stored.name ? stored : c))
        : [...channels, stored]

      return json(stored)
    }

    if (one && request.method() === "DELETE") {
      channels = channels.filter((c) => c.name !== one[1])

      return route.fulfill({ status: 204 })
    }

    if (people && found && request.method() === "POST") {
      const added = {
        open_id: "ou_linh",
        email: (request.postDataJSON() as { who: string }).who,
        name: "Linh",
        created_at: now,
      }
      channels = channels.map((c) => (c === found ? { ...c, people: [...c.people, added] } : c))

      return json(added)
    }

    if (people && found && request.method() === "DELETE") {
      channels = channels.map((c) =>
        c === found ? { ...c, people: c.people.filter((p) => p.open_id !== people[2]) } : c,
      )

      return route.fulfill({ status: 204 })
    }

    switch (call) {
      case "GET /me":
        return json({ id: "usr_1", name: "admin", kind: "owner" })
      case "GET /channels/lark":
        return json({ data: channels })
      case "GET /bots":
        return json({ data: bots })
      case "GET /bots/vmap":
        return json(bots[1])
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "GET /chats":
      case "GET /questions":
      case "GET /profiles":
      case "GET /models":
        return json({ data: [] })
      default:
        return json({ code: "not_found", message: "not found" }, 404)
    }
  })

  return calls
}

const fits = (page: Page) =>
  page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")

// Scenario: there is no Lark channel. The owner sets the first one up with his app and his email at Lark; it is
// named main and talks to the first Bot unless he says otherwise. An email Lark does not know is refused in the
// dialog, and the secret is typed into a password field. The channel starts off and for him only. He switches
// it on, lets the people he adds in, adds Linh, and takes her off again.
test("the owner sets the first Lark channel up, switches it on and lets a person in", async ({
  page,
}) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/channels")

  await expect(page.getByTestId("channel-empty")).toContainText("Not set up")
  expect(await fits(page)).toBe(true)
  await shot(page, "channels-unset")

  await page.getByTestId("channel-empty").getByRole("button", { name: "Set up" }).click()
  const dialog = page.getByRole("dialog")
  await expect(dialog.getByLabel("Name")).toHaveValue("main")
  await expect(dialog.getByLabel("Bot")).toContainText("coordinator")
  await dialog.getByLabel("App ID").fill("cli_a1b2c3")
  await dialog.getByLabel("App Secret").fill("never-shown")
  await expect(dialog.getByLabel("App Secret")).toHaveAttribute("type", "password")
  await dialog.getByLabel("Your email at Lark").fill("nobody@example.com")
  await dialog.getByRole("button", { name: "Save" }).click()
  await expect(dialog.getByRole("alert")).toContainText("no member of the app's Lark organization")
  await shot(page, "channels-setup-refused")
  // Taller than a phone, the dialog keeps its title and its buttons in view; only the fields scroll (issue #242).
  await expect(dialog.getByText("Add a Lark channel")).toBeInViewport()
  await expect(dialog.getByRole("button", { name: "Save" })).toBeInViewport()

  await dialog.getByLabel("Your email at Lark").fill("owner@example.com")
  await dialog.getByRole("button", { name: "Save" }).click()
  await expect(dialog).toBeHidden()

  const card = page.getByTestId("channel-card")
  await expect(card).toContainText("main")
  await expect(card).toContainText("Off")
  await expect(card).toContainText("cli_a1b2c3")
  await expect(card.getByLabel("Bot")).toContainText("coordinator")
  await expect(card.getByLabel("Who may talk to it")).toContainText("Only you")
  await expect(page.getByText("never-shown")).toHaveCount(0)

  await card.getByRole("switch", { name: "On" }).click()
  await expect(card).toContainText("Connected")

  // The list opens in the app's own popup, not the system's.
  await card.getByLabel("Who may talk to it").click()
  await expect(page.getByRole("option", { name: "Only you" })).toBeVisible()
  await shot(page, "channels-select-open")
  await page.getByRole("option", { name: "You and the people you add" }).click()
  await expect(card).toContainText("Connected")
  expect(await fits(page)).toBe(true)
  await shot(page, "channels-connected")

  // The people are a line under the card that opens their list in a sheet, as an account's actions do.
  await card.getByRole("button", { name: "People you added to main" }).click()
  const sheet = page.getByRole("dialog", { name: "main · People you added" })
  await expect(sheet).toContainText("Nobody yet.")
  await sheet.getByRole("button", { name: "Add person" }).click()
  const adding = page.getByRole("dialog", { name: "Add a person" })
  await adding.getByLabel("Email at Lark").fill("linh@example.com")
  await adding.getByRole("button", { name: "Add" }).click()
  await expect(sheet.getByLabel("Person Linh")).toContainText("linh@example.com")
  await expect(adding).toHaveCount(0)
  expect(await fits(page)).toBe(true)
  await shot(page, "channels-people")

  await sheet.getByRole("button", { name: "Remove Linh" }).click()
  await shot(page, "channels-remove-person")
  await page.getByRole("alertdialog").getByRole("button", { name: "Remove" }).click()
  await expect(sheet.getByLabel("Person Linh")).toHaveCount(0)
  await sheet.getByRole("button", { name: "Close" }).click()
  await expect(sheet).toHaveCount(0)
  await expect(card.getByRole("button", { name: "People you added to main" })).toContainText(
    "none",
  )

  expect(calls).toEqual([
    {
      call: "PUT /channels/lark/main",
      body: {
        app_id: "cli_a1b2c3",
        app_secret: "never-shown",
        owner: "nobody@example.com",
        bot: "coordinator",
      },
    },
    {
      call: "PUT /channels/lark/main",
      body: {
        app_id: "cli_a1b2c3",
        app_secret: "never-shown",
        owner: "owner@example.com",
        bot: "coordinator",
      },
    },
    { call: "PUT /channels/lark/main", body: { enabled: true } },
    { call: "PUT /channels/lark/main", body: { mode: "allowlist" } },
    { call: "POST /channels/lark/main/people", body: { who: "linh@example.com" } },
    { call: "DELETE /channels/lark/main/people/ou_linh", body: null },
  ])
})

// Scenario: the owner has the channel main and a second Bot, vmap. He adds a channel for vmap. A name that is
// taken is refused in the form, and the app of main is refused by the server: one app is one channel. With
// another app the channel is added, and the page lists both, each with its Bot. He then points main at vmap
// too, and vmap's page lists the channels that talk to it, each a link back to this page. Removing a channel
// takes its card away.
test("the owner adds a second channel for another Bot", async ({ page }) => {
  const calls = await fakeServer(page, [main])
  await page.goto("/settings/channels")

  await expect(page.getByTestId("channel-card")).toHaveCount(1)

  await page.getByRole("button", { name: "Add channel" }).click()
  const dialog = page.getByRole("dialog")
  await expect(dialog.getByLabel("Name")).toHaveValue("")
  await dialog.getByLabel("Name").fill("main")
  await dialog.getByLabel("App ID").fill("cli_a1b2c3")
  await dialog.getByLabel("App Secret").fill("never-shown")
  await dialog.getByLabel("Your email at Lark").fill("owner@example.com")
  await choose(page, dialog.getByLabel("Bot"), "vmap")
  await dialog.getByRole("button", { name: "Save" }).click()
  await expect(dialog).toContainText("A channel has this name already.")
  expect(calls).toEqual([])

  await dialog.getByLabel("Name").fill("maps")
  expect(await fits(page)).toBe(true)
  await shot(page, "channels-add")

  await dialog.getByRole("button", { name: "Save" }).click()
  await expect(dialog.getByRole("alert")).toContainText("one app is one channel")
  await shot(page, "channels-add-refused")

  await dialog.getByLabel("App ID").fill("cli_m4p5")
  await dialog.getByRole("button", { name: "Save" }).click()
  await expect(dialog).toBeHidden()

  const cards = page.getByTestId("channel-card")
  await expect(cards).toHaveCount(2)

  const mainCard = page.getByLabel("Channel main", { exact: true })
  const mapsCard = page.getByLabel("Channel maps", { exact: true })
  await expect(mainCard.getByLabel("Bot")).toContainText("coordinator")
  await expect(mapsCard.getByLabel("Bot")).toContainText("vmap")
  await expect(mapsCard.getByLabel("Bot")).toContainText("vmap")
  await expect(mapsCard).toContainText("Off")
  expect(await fits(page)).toBe(true)
  await shot(page, "channels-list")

  await choose(page, mainCard.getByLabel("Bot"), "vmap")
  await expect(mainCard.getByLabel("Bot")).toContainText("vmap")

  await page.goto("/bots/vmap")
  const doors = page.getByRole("region", { name: "Channels" })
  await expect(doors.getByRole("listitem")).toHaveText([/main.*Connected/, /maps.*Off/])
  expect(await fits(page)).toBe(true)
  await shot(page, "bot-channels")

  await doors.getByRole("link", { name: "maps", exact: true }).click()
  await expect(page).toHaveURL(/\/settings\/channels$/)

  await page
    .getByLabel("Channel maps", { exact: true })
    .getByRole("button", { name: "Remove", exact: true })
    .click()
  await page.getByRole("alertdialog").getByRole("button", { name: "Remove" }).click()
  await expect(page.getByTestId("channel-card")).toHaveCount(1)

  expect(calls).toEqual([
    {
      call: "PUT /channels/lark/maps",
      body: {
        app_id: "cli_a1b2c3",
        app_secret: "never-shown",
        owner: "owner@example.com",
        bot: "vmap",
      },
    },
    {
      call: "PUT /channels/lark/maps",
      body: {
        app_id: "cli_m4p5",
        app_secret: "never-shown",
        owner: "owner@example.com",
        bot: "vmap",
      },
    },
    { call: "PUT /channels/lark/main", body: { bot: "vmap" } },
    { call: "DELETE /channels/lark/maps", body: null },
  ])
})

// Scenario: a channel is on and Lark refused its app. Its card says it is not connected and why, in Lark's
// words.
test("a channel that could not connect says why", async ({ page }) => {
  await fakeServer(page, [
    {
      ...main,
      connected: false,
      last_error: "opening the persistent connection to Lark: app secret invalid",
    },
  ])
  await page.goto("/settings/channels")

  const card = page.getByTestId("channel-card")
  await expect(card).toContainText("Not connected")
  await expect(card).toContainText("app secret invalid")
  expect(await fits(page)).toBe(true)
  await shot(page, "channels-not-connected")
})

// Scenario: the channel main's bot was added to two Lark groups; nobody has asked it in one, and the other was
// asked an hour ago. Both are off until the owner switches them on. He switches the maps team on, and it is saved
// as he does; he switches it off again. A channel whose bot is in no group says so. The Bot of a channel is shown
// with its face (its gradient marks it), in the closed picker and in its list.
test("the owner switches a Lark group of a channel on and off", async ({ page }) => {
  const hourAgo = new Date(Date.now() - 60 * 60 * 1000).toISOString()
  const calls = await fakeServer(page, [
    {
      ...main,
      mode: "everyone",
      groups: [
        {
          chat_id: "oc_maps",
          name: "Maps team",
          enabled: false,
          asked_at: hourAgo,
          created_at: now,
        },
        { chat_id: "oc_unnamed", name: "", enabled: false, created_at: now },
      ],
    },
    { ...main, name: "maps", bot: "vmap", app_id: "cli_m4p5", enabled: false, connected: false },
  ])
  await page.goto("/settings/channels")

  const mainCard = page.getByLabel("Channel main", { exact: true })
  const maps = mainCard.getByLabel("Group Maps team")
  await expect(maps).toContainText(/asked 1\s?h(r|our)? ago/)
  await expect(mainCard.getByLabel("Group oc_unnamed")).toContainText("not asked yet")
  await expect(maps.getByRole("switch", { name: "Answers here" })).not.toBeChecked()
  await expect(page.getByLabel("Channel maps", { exact: true }).getByRole("status")).toContainText(
    "No group chats yet",
  )
  await expect(mainCard.getByLabel("Bot").locator("svg linearGradient")).toHaveCount(1)
  expect(await fits(page)).toBe(true)
  await shot(page, "channels-groups")

  await maps.getByRole("switch", { name: "Answers here" }).click()
  await expect(maps.getByRole("switch", { name: "Answers here" })).toBeChecked()
  await shot(page, "channels-group-on")

  await mainCard.getByLabel("Bot").click()
  await expect(
    page.getByRole("option", { name: "vmap" }).locator("svg linearGradient"),
  ).toHaveCount(1)
  await shot(page, "channels-bot-picker")
  await page.keyboard.press("Escape")

  await maps.getByRole("switch", { name: "Answers here" }).click()
  await expect(maps.getByRole("switch", { name: "Answers here" })).not.toBeChecked()

  expect(calls).toEqual([
    { call: "PUT /channels/lark/main/groups/oc_maps", body: { enabled: true } },
    { call: "PUT /channels/lark/main/groups/oc_maps", body: { enabled: false } },
  ])
})
