import { expect, test, type Page } from "@playwright/test"

import { openMenu } from "./menu.js"
import { shot } from "./shot.js"

// The sidebar's Bots (issue #361), built, against a fake client API: a row a Bot that opens its main chat (issue
// #471), as OpenMausBot's sidebar with its threads hidden, in the one order the owner sets by dragging, with the pinned Bots under the main one (issue
// #470). A phone shows them in the drawer behind ≡ (issue #604), in the order, without dragging.

const ago = (seconds: number) => new Date(Date.now() - seconds * 1000).toISOString()

// Each Bot's main chat (issue #471): coordinator's is not its newest, researcher's is not the one that waits on the
// owner, and writer has none.
const mainChats: Record<string, string> = {
  coordinator: "cht_2",
  researcher: "cht_5",
  reporter: "cht_6",
}

const bot = (name: string, position: number) => ({
  id: name,
  ...(mainChats[name] && { main_chat: mainChats[name] }),
  main: name === "coordinator",
  name,
  position,
  pinned: false,
  persona: "",
  memory: "",
  memory_limit: 2200,
  skills: [],
  skills_off: [],
  tools: [],
  tools_off: [],
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
  chat("cht_2", "coordinator", "Fix the header layout", 7200, {
    last_line: "Moved the menu under the logo",
  }),
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
async function fakeServer(page: Page, list = chats, approvals = [held]) {
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

    const open = list.find((c) => path.startsWith(`/chats/${c.id}`))
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
        return json({ data: list })
      case "/bots":
        return json({ data: listed })
      case "/approvals":
        return json({ data: approvals })
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

// The list is on screen for the picture: a phone shows it in the drawer, below the places.
async function showList(page: Page) {
  await bots(page).scrollIntoViewIfNeeded()
}

// Scenario: the owner sees every Bot in his order, the main one first, each one row with no list of chats under it
// (issue #471): its main chat's last line, what it is doing, and a Bot without a main chat says so. A Bot's row opens
// its main chat, not its newest; the row of a Bot one of whose other chats waits on him says Needs you and opens
// that chat; a Bot with no main chat opens New chat with it picked.
test("the sidebar lists the Bots, each opening its main chat", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/")
  await openMenu(page)

  // The places are the daily ones; the agents and the machines are in Settings (issue #857).
  await expect(page.getByRole("navigation", { name: "Places" }).getByRole("link")).toHaveText([
    "New chat",
    "Team",
    "Routines",
    "Meetings",
    "Studio",
    "Bots",
  ])

  await expect(botRow(page, "coordinator")).toContainText("Moved the menu under the logo")
  await expect(botRow(page, "coordinator")).toContainText("Working")
  await expect(botRow(page, "researcher")).toContainText("Compare the gateways")
  await expect(botRow(page, "researcher")).toContainText("Needs you")
  await expect(bots(page).getByTestId("needs-you-dot")).toHaveCount(1)
  await expect(botRow(page, "reporter")).toContainText("Weekly usage summary")
  await expect(botRow(page, "writer")).toContainText("No chat yet")
  await expect(botNames(page)).toHaveText([/coordinator/, /researcher/, /reporter/, /writer/])
  // No chevron, no list of chats, no Show all and no New chat on a row.
  await expect(bots(page).getByRole("button", { name: /chats of|New chat with/ })).toHaveCount(0)
  await expect(bots(page).getByRole("list")).toHaveCount(0)
  expect(await page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")).toBe(
    true,
  )
  await showList(page)
  await shot(page, "sidebar-bots")

  await botRow(page, "coordinator").click()
  await expect(page).toHaveURL(/\/chats\/cht_2$/)
  await expect(page.getByText("about Fix the header layout")).toBeVisible()

  await openMenu(page)
  await botRow(page, "researcher").click()
  await expect(page).toHaveURL(/\/chats\/cht_4$/)
  await expect(page.getByText("about Send the invoice reply")).toBeVisible()
  await openMenu(page)
  await showList(page)
  await shot(page, "sidebar-needs-you")

  await botRow(page, "writer").click()
  await expect(page).toHaveURL(/\/\?bot=writer$/)
})

// Scenario: the owner pins reporter from its row's menu (issue #470): it moves to the Pinned section under the main
// Bot, which has no Pin, and stays there after a reload; Unpin puts it back among the others in its place. A phone
// pins from the same menu, always shown there.
test("a Bot is pinned and unpinned from its row's menu", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/")
  await openMenu(page)

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
  await openMenu(page)
  await expect(pinnedNames(page)).toHaveText([/reporter/])
  // The main Bot's menu holds its main chat's Delete, and no Pin.
  await botRow(page, "coordinator").hover()
  await bots(page).getByRole("button", { name: "Actions for coordinator" }).click()
  await expect(page.getByRole("menuitem")).toHaveText(["Delete"])
  await page.keyboard.press("Escape")

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
  await openMenu(page)
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
  await openMenu(page)
  await expect(botNames(page)).toHaveText([/coordinator/, /writer/, /reporter/, /researcher/])
})

// Scenario: a Bot's row still carries its main chat to the grid when dragged by its name (issue #235, #471): only
// its avatar moves it in the order.
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

// Scenario: the owner has coordinator's main chat open when its Bot answers (issue #471): the row's last line
// follows the answer without a reload, as the chats are read again when a line comes.
test("a Bot's row shows its main chat's newest line as the Bot answers", async ({ page }) => {
  await fakeServer(page)
  let listed = 0
  let replied = false
  // Registered after fakeServer, so these answer first: the list, with the line the answer leaves; the chat's
  // stream, which answers once the list was read.
  await page.route("**/api/v1/chats", async (route) => {
    listed++
    const now = chats.map((c) =>
      c.id === "cht_2" && replied ? { ...c, last_line: "The menu is under the logo now" } : c,
    )
    await route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: now }) })
  })
  await page.route("**/api/v1/chats/cht_2/stream*", async (route) => {
    if (replied) {
      return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
    }
    await expect.poll(() => listed).toBeGreaterThan(0)
    replied = true
    const answer = {
      seq: 2,
      chat_id: "cht_2",
      role: "coordinator",
      text: "The menu is under the logo now",
      created_at: new Date().toISOString(),
    }
    await route.fulfill({
      status: 200,
      contentType: "text/event-stream",
      body: `id: 2\nevent: message\ndata: ${JSON.stringify(answer)}\n\n`,
    })
  })

  await page.goto("/chats/cht_2")
  await expect(page.getByText("The menu is under the logo now").first()).toBeVisible()
  await openMenu(page)
  await expect(botRow(page, "coordinator")).toContainText("The menu is under the logo now")
})
