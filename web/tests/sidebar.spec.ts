import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The sidebar's Bots (issue #361), built, against a fake client API: a row a Bot that opens to its chats, as
// OpenMausBot's sidebar, in the one order the owner sets by dragging, with the pinned Bots under the main one (issue
// #470). A phone lists them on the start page, under the places, and shows the order without dragging.

const ago = (seconds: number) => new Date(Date.now() - seconds * 1000).toISOString()

const bot = (name: string, position: number) => ({
  id: name,
  main: name === "coordinator",
  name,
  position,
  pinned: false,
  persona: "",
  memory: "",
  memory_limit: 2200,
  skills: [],
  skills_off: [],
  connections: { limited: false, names: [] },
  workers: { limited: false, names: [] },
  scopes: {},
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

// fakeServer answers as the control plane; the Bots' order and pins are kept across reloads, and every order the
// page sends is returned.
async function fakeServer(page: Page) {
  const listed = ["coordinator", "researcher", "reporter", "writer"].map(bot)
  const orders: string[][] = []

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    // The server's reorder (orchestration/bots.go, ReorderBots): the Bots named take the places they held together.
    if (path === "/bots/order" && request.method() === "PUT") {
      const ids: string[] = request.postDataJSON().bot_ids
      orders.push(ids)
      const byPlace = [...listed].sort((a, b) => a.position - b.position)
      const slots = byPlace.flatMap((b, i) => (ids.includes(b.id) ? [i] : []))
      slots.forEach((slot, i) => (byPlace[slot] = listed.find((b) => b.id === ids[i])!))
      byPlace.forEach((b, i) => (b.position = i))

      return json({ bot_ids: byPlace.map((b) => b.id) })
    }
    const patched = listed.find((b) => path === `/bots/${b.id}`)
    if (patched && request.method() === "PATCH") {
      patched.pinned = request.postDataJSON().pinned

      return json(patched)
    }

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
    if (open && path.endsWith("/messages"))
      return json({ data: [], next_after: 0, has_more: false })
    if (open && path === `/chats/${open.id}`) return json(open)

    switch (path) {
      case "/me":
        return json({ id: "usr_1", name: "admin", kind: "owner" })
      case "/chats":
        return json({ data: chats })
      case "/bots":
        return json({ data: listed })
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

  return orders
}

const bots = (page: Page) => page.getByRole("navigation", { name: "Bots" })
const botRow = (page: Page, name: string) =>
  bots(page).getByRole("link", { name: new RegExp(`^${name}`) })
const botNames = (page: Page) =>
  bots(page).getByRole("link", { name: /^(coordinator|researcher|reporter|writer) / })
const pinnedNames = (page: Page) =>
  bots(page)
    .getByRole("region", { name: "Pinned" })
    .getByRole("link", { name: /^(researcher|reporter|writer) / })

// The list is on screen for the picture: a phone shows it under the places, below the composer.
async function showList(page: Page) {
  await bots(page).scrollIntoViewIfNeeded()
}

// Scenario: the owner sees every Bot in his order, the main one first, each with its latest chat and what it is
// doing, and a Bot without chats too; he opens coordinator to its chats, newest first, and it stays open after a
// reload; clicking a Bot's row opens its latest chat.
test("the sidebar lists the Bots, each opening to its chats", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/")

  await expect(botRow(page, "coordinator")).toContainText("Add a /health page")
  await expect(botRow(page, "coordinator")).toContainText("Working")
  await expect(botRow(page, "researcher")).toContainText("Send the invoice reply")
  await expect(botRow(page, "researcher")).toContainText("Needs you")
  await expect(botRow(page, "reporter")).toContainText("Weekly usage summary")
  await expect(botRow(page, "writer")).toContainText("No chats yet")
  await expect(botNames(page)).toHaveText([/coordinator/, /researcher/, /reporter/, /writer/])
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

// Scenario: the owner pins reporter from its row's menu (issue #470): it moves to the Pinned section under the main
// Bot, which has no Pin, and stays there after a reload; Unpin puts it back among the others in its place. A phone
// pins from the same menu, always shown there.
test("a Bot is pinned and unpinned from its row's menu", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/")

  await botRow(page, "reporter").hover()
  await bots(page).getByRole("button", { name: "Actions for reporter" }).click()
  await expect(page.getByRole("menuitem", { name: "Delete chat" })).toBeVisible()
  await shot(page, "sidebar-bot-menu")
  await page.getByRole("menuitem", { name: "Pin" }).click()

  await expect(pinnedNames(page)).toHaveText([/reporter/])
  await expect(botNames(page)).toHaveText([/coordinator/, /reporter/, /researcher/, /writer/])
  await showList(page)
  // The picture shows the rows at rest, not one the pointer was left on after the menu closed.
  await page.mouse.move(0, 0)
  await shot(page, "sidebar-pinned")

  await page.reload()
  await expect(pinnedNames(page)).toHaveText([/reporter/])
  await botRow(page, "coordinator").hover()
  await expect(bots(page).getByRole("button", { name: "Actions for coordinator" })).toHaveCount(0)

  await botRow(page, "reporter").hover()
  await bots(page).getByRole("button", { name: "Actions for reporter" }).click()
  await page.getByRole("menuitem", { name: "Unpin" }).click()
  await expect(bots(page).getByRole("region", { name: "Pinned" })).toHaveCount(0)
  await expect(botNames(page)).toHaveText([/coordinator/, /researcher/, /reporter/, /writer/])
})

// Scenario: the owner drags writer by its avatar above researcher, then moves reporter down with the keyboard; the
// page sends only the Bots it moves, and the order is the same after a reload. The main Bot stays first and has no
// handle. A phone shows the order without a handle.
test("Bots are reordered by their avatar, with the mouse or the keyboard", async ({
  page,
  isMobile,
}) => {
  const orders = await fakeServer(page)
  await page.goto("/")
  await expect(botNames(page)).toHaveCount(4)
  await expect(bots(page).getByRole("button", { name: "Move coordinator" })).toHaveCount(0)

  if (isMobile) {
    await expect(bots(page).getByRole("button", { name: /^Move / })).toHaveCount(0)
    return
  }

  const handle = bots(page).getByRole("button", { name: "Move writer" })
  const target = (await botRow(page, "researcher").boundingBox())!
  await handle.hover()
  await shot(page, "sidebar-grabber")
  await page.mouse.down()
  await page.mouse.move(target.x + 40, target.y + 4, { steps: 12 })
  await shot(page, "sidebar-dragging")
  await page.mouse.up()

  await expect(botNames(page)).toHaveText([/coordinator/, /writer/, /researcher/, /reporter/])
  expect(orders).toEqual([["writer", "researcher", "reporter"]])

  const researcher = bots(page).getByRole("button", { name: "Move researcher" })
  await researcher.focus()
  await page.keyboard.press("Space")
  await expect(researcher).toHaveAttribute("aria-pressed", "true")
  // dnd-kit listens for the arrows a moment after the pick-up, and says each step in its live region, as a screen
  // reader hears it: the arrow is pressed until it says the move.
  const movedOver = page.getByText(
    "Draggable item researcher was moved over droppable area reporter.",
  )
  await expect(async () => {
    if ((await movedOver.count()) === 0) await page.keyboard.press("ArrowDown")
    await expect(movedOver).toBeAttached({ timeout: 500 })
  }).toPass()
  await page.keyboard.press("Space")
  await expect(botNames(page)).toHaveText([/coordinator/, /writer/, /reporter/, /researcher/])
  expect(orders.at(-1)).toEqual(["writer", "reporter", "researcher"])

  await page.reload()
  await expect(botNames(page)).toHaveText([/coordinator/, /writer/, /reporter/, /researcher/])
})

// Scenario: a Bot's row still carries its latest chat to the grid when dragged by its name (issue #235): only its
// avatar moves it in the order.
test("a Bot's row is still dragged to the grid", async ({ page, isMobile }) => {
  test.skip(isMobile, "a phone shows one chat: there is no grid")

  const orders = await fakeServer(page)
  await page.goto("/chats/cht_1")
  await expect(page.getByText("about Add a /health page")).toBeVisible()

  const main = (await page.getByRole("main").boundingBox())!
  await botRow(page, "reporter").getByText("reporter", { exact: true }).hover()
  await page.mouse.down()
  await page.mouse.move(main.x + main.width * 0.97, main.y + main.height * 0.5, { steps: 12 })
  await expect(page.getByTestId("grid-ghost")).toBeVisible()
  await page.mouse.up()

  await expect(page).toHaveURL(/\/grid$/)
  expect(orders).toEqual([])
})
