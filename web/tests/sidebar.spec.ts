import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The sidebar's Bots (issue #361), built, against a fake client API: a row a Bot that opens to its chats, as
// OpenMausBot's sidebar. A phone lists them on the start page, under the places.

const ago = (seconds: number) => new Date(Date.now() - seconds * 1000).toISOString()

const bot = (name: string) => ({
  name,
  persona: "",
  memory: "",
  memory_limit: 2200,
  skills: [],
  skills_off: [],
  connections: { limited: false, names: [] },
  workers: { limited: false, names: [] },
  created_at: ago(86400),
  updated_at: ago(86400),
})

const chat = (id: string, name: string, title: string, updated: number, m: object = {}) => ({
  id,
  title,
  bot: name,
  working: false,
  coordinator: true,
  created_at: ago(updated + 600),
  updated_at: ago(updated),
  ...m,
})

const chats = [
  chat("cht_1", "coordinator", "Add a /health page", 30, { working: true }),
  chat("cht_2", "coordinator", "Fix the header layout", 7200),
  chat("cht_3", "coordinator", "Old release notes", 3 * 86400),
  chat("cht_4", "researcher", "Send the invoice reply", 600),
  chat("cht_5", "researcher", "Compare the gateways", 86400),
  chat("cht_6", "reporter", "Weekly usage summary", 5 * 3600),
]

// What researcher asked to send waits for the owner.
const held = {
  id: "apr_1",
  integration: "google",
  label: "work",
  action: "gmail.send",
  action_name: "Send mail",
  asked_by: "researcher",
  chat_id: "cht_4",
  title: "Send mail: minh@example.com",
  payload: [],
  status: "pending",
  used: false,
  created_at: ago(600),
}

async function fakeServer(page: Page) {
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    const open = chats.find((c) => path.startsWith(`/chats/${c.id}`))
    if (open && path.endsWith("/stream")) {
      const said = {
        seq: 1,
        chat_id: open.id,
        role: "owner",
        text: `about ${open.title}`,
        created_at: open.created_at,
      }

      return route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: `id: 1\nevent: message\ndata: ${JSON.stringify(said)}\n\n`,
      })
    }
    if (open && path === `/chats/${open.id}`) return json(open)

    switch (path) {
      case "/me":
        return json({ id: "usr_1", name: "admin", kind: "owner" })
      case "/chats":
        return json({ data: chats })
      case "/bots":
        return json({ data: ["coordinator", "researcher", "reporter"].map(bot) })
      case "/approvals":
        return json({ data: [held] })
      case "/events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "/tasks":
        return json({ data: [], runs: [], after: 0 })
      default:
        return json({ data: [] })
    }
  })
}

const bots = (page: Page) => page.getByRole("navigation", { name: "Bots" })
const botRow = (page: Page, name: string) =>
  bots(page).getByRole("link", { name: new RegExp(`^${name}`) })

// The list is on screen for the picture: a phone shows it under the places, below the composer.
async function showList(page: Page) {
  await bots(page).scrollIntoViewIfNeeded()
}

// Scenario: the owner sees his Bots, the one whose chat moved last first, each with its latest chat and what it
// is doing; he opens coordinator to its chats, newest first, and it stays open after a reload; clicking a Bot's
// row opens its latest chat.
test("the sidebar lists the Bots, each opening to its chats", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/")

  await expect(botRow(page, "coordinator")).toContainText("Add a /health page")
  await expect(botRow(page, "coordinator")).toContainText("Working")
  await expect(botRow(page, "researcher")).toContainText("Send the invoice reply")
  await expect(botRow(page, "researcher")).toContainText("Needs you")
  await expect(botRow(page, "reporter")).toContainText("Weekly usage summary")
  await expect(
    bots(page).getByRole("link", { name: /^(coordinator|researcher|reporter) / }),
  ).toHaveText([/coordinator/, /researcher/, /reporter/])
  // A Bot with one chat has nothing to open.
  await expect(bots(page).getByRole("button", { name: "Show the chats of reporter" })).toHaveCount(
    0,
  )
  expect(await page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")).toBe(
    true,
  )
  await showList(page)
  await shot(page, "sidebar-bots")

  await bots(page).getByRole("button", { name: "Show the chats of coordinator" }).click()
  const coordinator = bots(page).getByRole("list", { name: "Chats of coordinator" })
  await expect(coordinator.getByRole("link")).toHaveText([
    /Add a \/health page.*Working/,
    /Fix the header layout/,
    /Old release notes/,
  ])
  await showList(page)
  await shot(page, "sidebar-bot-open")

  await page.reload()
  await expect(coordinator.getByRole("link")).toHaveCount(3)
  await bots(page).getByRole("button", { name: "Hide the chats of coordinator" }).click()
  await expect(coordinator).toHaveCount(0)

  await bots(page).getByRole("button", { name: "Show the chats of researcher" }).click()
  await expect(
    bots(page).getByRole("list", { name: "Chats of researcher" }).getByRole("link").first(),
  ).toContainText("Needs you")
  await showList(page)
  await shot(page, "sidebar-needs-you")

  await botRow(page, "reporter").click()
  await expect(page).toHaveURL(/\/chats\/cht_6$/)
})

// Scenario: pointing at a Bot's row offers a new chat with it; the start page opens with that Bot picked. A phone
// starts a chat from New chat, with its own picker.
test("the + on a Bot's row starts a chat with that Bot", async ({ page, isMobile }) => {
  test.skip(isMobile, "a phone has no pointer to rest on a row")

  await fakeServer(page)
  await page.goto("/chats/cht_1")

  await botRow(page, "reporter").hover()
  const start = bots(page).getByRole("button", { name: "New chat with reporter" })
  await expect(start).toBeVisible()
  await start.hover()
  await expect(page.getByText("New chat with reporter", { exact: true }).last()).toBeVisible()
  await shot(page, "sidebar-new-chat")

  await start.click()
  await expect(page).toHaveURL(/\/\?bot=reporter$/)
  await expect(page.getByRole("combobox", { name: "Bot" })).toContainText("reporter")
})
