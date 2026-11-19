import { expect, test, type Page } from "@playwright/test"

import { openMenu } from "./menu.js"
import { shot } from "./shot.js"

// A Bot's main chat in the web (issue #471), built, against a fake client API: the first message to a Bot with no
// main chat goes to the one the CLI made meanwhile, and /new in a chat starts a new main chat. An older chat is made
// the main chat from the Bot's menu, and another chat leads back to the main one (issue #923).

const ago = (seconds: number) => new Date(Date.now() - seconds * 1000).toISOString()

const chat = (id: string, title: string, bot: string, m: object = {}) => ({
  id,
  title,
  bot,
  working: false,
  coordinator: true,
  routine: false,
  channel: false,
  created_at: ago(600),
  updated_at: ago(60),
  ...m,
})

const bot = (id: string, position: number, mainChat?: string) => ({
  id,
  main: position === 0,
  name: id,
  position,
  pinned: false,
  ...(mainChat && { main_chat: mainChat }),
  persona: "",
  memory: "",
  memory_limit: 2200,
  fallbacks: [],
  skills: [],
  skills_off: [],
  tools: [],
  tools_off: [],
  connections: { limited: false, names: [] },
  workers: { limited: false, names: [] },
  scopes: {},
  tool_display: "on",
  guaranteed_dispatches: 4,
  created_at: ago(86400),
  updated_at: ago(86400),
})

type Call = { call: string; body: unknown }

// fakeServer answers as the control plane: coordinator's main chat is cht_1, beside an older chat of it (cht_old) and
// a Routine's run (cht_run); writer has none until something makes it. POST /chats/main answers writer's main chat,
// the CLI's (cht_cli) when cliFirst says the CLI wrote first.
async function fakeServer(page: Page, { cliFirst = false } = {}) {
  const calls: Call[] = []
  const chats = [
    chat("cht_1", "Fix the header", "coordinator"),
    chat("cht_old", "The release plan", "coordinator", { updated_at: ago(3600) }),
    chat("cht_run", "Daily digest", "coordinator", { routine: true, updated_at: ago(1800) }),
  ]
  const bots = [bot("coordinator", 0, "cht_1"), bot("writer", 1)]

  await page.addInitScript({ content: "localStorage.setItem('hypomnema.token', 'secret')" })
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })
    const body = request.method() === "GET" ? null : request.postDataJSON()
    if (request.method() !== "GET") calls.push({ call, body })

    if (call === "POST /chats/main") {
      const asked = body as { bot: string; id: string; title?: string }
      const made = cliFirst
        ? chat("cht_cli", "from the terminal", asked.bot)
        : chat(asked.id, asked.title ?? "Chat", asked.bot)
      if (!chats.some((c) => c.id === made.id)) chats.push(made)
      bots.find((b) => b.id === asked.bot)!.main_chat = made.id

      return json(made, cliFirst ? 200 : 201)
    }
    if (request.method() === "PATCH" && path.startsWith("/bots/")) {
      const changed = bots.find((b) => path === `/bots/${b.id}`)!
      changed.main_chat = (body as { main_chat: string }).main_chat

      return json(changed)
    }
    if (call === "POST /chats") {
      const asked = body as { bot: string }
      const made = chat("cht_new", "Chat", asked.bot)
      chats.push(made)
      bots.find((b) => b.id === asked.bot)!.main_chat = made.id

      return json(made, 201)
    }
    const open = chats.find((c) => path.startsWith(`/chats/${c.id}`))
    if (open && call === `POST /chats/${open.id}/messages`) {
      const text = (body as { text: string }).text

      return json(
        { seq: 9, chat_id: open.id, role: "owner", text, created_at: new Date().toISOString() },
        201,
      )
    }
    if (open && path.endsWith("/stream")) {
      return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
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
        return json({ data: bots })
      case "/events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "/tasks":
        return json({ data: [], runs: [], after: 0 })
      default:
        return json({ data: [] })
    }
  })

  return calls
}

// Scenario: the page is open on writer, which has no main chat, when the CLI writes to writer first and makes its
// main chat. The page's first message asks for writer's main chat with the id it chose, gets the CLI's, and goes
// there: no second chat is made.
test("the first message to a Bot with no main chat goes to the one made meanwhile", async ({
  page,
}) => {
  const calls = await fakeServer(page, { cliFirst: true })
  await page.goto("/?bot=writer")

  await page.getByRole("textbox", { name: "Message" }).fill("draft the release notes")
  await page.keyboard.press("Enter")

  await expect(page).toHaveURL(/\/chats\/cht_cli$/)
  expect(calls.map((c) => c.call)).toEqual(["POST /chats/main", "POST /chats/cht_cli/messages"])
  expect(calls[0]!.body).toMatchObject({ bot: "writer", title: "draft the release notes" })
})

// Scenario: a Bot with no main chat and nothing made meanwhile: the first message makes it under the page's id.
test("the first message to a Bot with no main chat makes it under the page's id", async ({
  page,
}) => {
  const calls = await fakeServer(page)
  await page.goto("/?bot=writer")

  await page.getByRole("textbox", { name: "Message" }).fill("draft the release notes")
  await page.keyboard.press("Enter")

  await expect(page).toHaveURL(/\/chats\/cht_\w+$/)
  const made = (calls[0]!.body as { id: string }).id
  expect(calls.map((c) => c.call)).toEqual(["POST /chats/main", `POST /chats/${made}/messages`])
  await expect(page).toHaveURL(new RegExp(`/chats/${made}$`))
})

// Scenario: the owner types /new in coordinator's main chat (issue #471): a new chat with coordinator is made, which
// becomes its main chat, and opens; /new is not sent as a message, and the former chat stays.
test("/new in a chat starts a new main chat with its Bot", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/chats/cht_1")

  const box = page.getByRole("textbox", { name: "Message" })
  await box.fill("/new")
  await shot(page, "main-chat-new-command")
  await page.keyboard.press("Enter")

  await expect(page).toHaveURL(/\/chats\/cht_new$/)
  expect(calls).toEqual([{ call: "POST /chats", body: { bot: "coordinator" } }])
})

// Scenario: the owner reads an older chat of coordinator, opened from a link (issue #923). The page leads back to the
// main chat; the Bot's menu in the sidebar makes this chat the main one, after which the way back is gone and the
// menu says it is already the main chat. A Routine's chat has no Set as main chat, and leads back to the new main
// chat.
test("an older chat is set as the main chat, and another chat leads back to it", async ({
  page,
}) => {
  const calls = await fakeServer(page)
  await page.goto("/chats/cht_old")

  const back = page.getByRole("button", { name: "Back to main chat" })
  await expect(back).toBeVisible()
  await shot(page, "main-chat-back")

  const actions = async () => {
    await openMenu(page)
    const nav = page.getByRole("navigation", { name: "Bots" })
    await nav.getByRole("link", { name: /coordinator/ }).hover()
    await nav.getByRole("button", { name: "Actions for coordinator" }).click()
  }

  // A phone's drawer is closed again, so that the page under it is read.
  const closeMenu = async () => {
    const drawer = page.getByRole("dialog", { name: "Sidebar" })
    if (await drawer.isVisible()) {
      await page.keyboard.press("Escape")
      await expect(drawer).toBeHidden()
    }
  }

  await actions()
  const setMain = page.getByRole("menuitem", { name: /Set as main chat/ })
  await expect(setMain).toBeEnabled()
  await shot(page, "main-chat-set-menu")
  await setMain.click()

  expect(calls).toEqual([{ call: "PATCH /bots/coordinator", body: { main_chat: "cht_old" } }])
  await closeMenu()
  await expect(page.getByRole("textbox", { name: "Message" })).toBeVisible()
  await expect(back).toHaveCount(0)

  await actions()
  await expect(setMain).toBeDisabled()
  await expect(setMain).toContainText("This is already the main chat")
  await page.keyboard.press("Escape")
  await closeMenu()

  await page.goto("/chats/cht_run")
  await actions()
  await expect(page.getByRole("menuitem", { name: /Delete/ })).toBeVisible()
  await expect(setMain).toHaveCount(0)
  await page.keyboard.press("Escape")
  await closeMenu()

  await back.click()
  await expect(page).toHaveURL(/\/chats\/cht_old$/)
})

// actionsOf opens coordinator's menu in the sidebar, behind ≡ on a phone.
async function actionsOf(page: Page) {
  await openMenu(page)
  const nav = page.getByRole("navigation", { name: "Bots" })
  await nav.getByRole("link", { name: /coordinator/ }).hover()
  await nav.getByRole("button", { name: "Actions for coordinator" }).click()
}

// keep puts panes in this browser's storage before the page loads, as a drag would have left them.
async function keep(page: Page, panes: object, grid?: object) {
  await page.addInitScript({
    content: `localStorage.setItem('hypomnema.panes', ${JSON.stringify(JSON.stringify(panes))})
      ${grid ? `localStorage.setItem('hypomnema.grid', ${JSON.stringify(JSON.stringify(grid))})` : ""}`,
  })
}

// Scenario: on coordinator's main chat's page, the owner had dragged an older chat into the middle of the pane, so it
// is in front while the address still names the main chat (issue #923). Set as main chat acts on the chat in front,
// not on the address; back on the former main chat's tab, Back to main chat brings the new main chat in front there.
test("Set as main chat and Back to main chat follow the chat in front of the page's pane", async ({
  page,
}) => {
  const calls = await fakeServer(page)
  await keep(page, {
    "chat-cht_1": {
      tabs: [
        { kind: "chat", chat: "cht_1" },
        { kind: "chat", chat: "cht_old" },
      ],
      active: "chat:cht_old",
    },
  })
  await page.goto("/chats/cht_1")

  const back = page.getByRole("button", { name: "Back to main chat" })
  await expect(back).toBeVisible()

  await actionsOf(page)
  await page.getByRole("menuitem", { name: /Set as main chat/ }).click()
  expect(calls).toEqual([{ call: "PATCH /bots/coordinator", body: { main_chat: "cht_old" } }])
  const drawer = page.getByRole("dialog", { name: "Sidebar" })
  if (await drawer.isVisible()) {
    await page.keyboard.press("Escape")
    await expect(drawer).toBeHidden()
  }
  await expect(back).toHaveCount(0)

  await page.getByRole("tab", { name: "Fix the header" }).click()
  await expect(back).toBeVisible()
  await back.click()
  await expect(page.getByRole("tab", { name: "The release plan" })).toHaveAttribute(
    "aria-selected",
    "true",
  )
  await expect(page).toHaveURL(/\/chats\/cht_1$/)
  await expect(back).toHaveCount(0)
})

// Scenario: a grid of two panes, coordinator's older chat in one and its Routine's run in the other (issue #923). Each
// pane leads back to the main chat on its own; the sidebar's menu acts on the chat in front of the pane last used:
// none on the Routine's run, Set as main chat on the older chat. Back to main chat then brings it into the other pane.
test("in a grid, each pane leads back to the main chat and the sidebar acts on the pane used", async ({
  page,
}, info) => {
  test.skip(info.project.name === "phone", "a phone shows one chat: there is no grid")
  const calls = await fakeServer(page)
  await keep(
    page,
    {
      p1: { tabs: [{ kind: "chat", chat: "cht_old" }], active: "chat:cht_old" },
      p2: { tabs: [{ kind: "chat", chat: "cht_run" }], active: "chat:cht_run" },
    },
    { axis: "columns", children: ["p1", "p2"] },
  )
  await page.goto("/grid")

  const pane = (title: string) => page.getByRole("region", { name: title })
  const back = (title: string) => pane(title).getByRole("button", { name: "Back to main chat" })
  await expect(back("The release plan")).toBeVisible()
  await expect(back("Daily digest")).toBeVisible()

  await pane("Daily digest").getByRole("tab", { name: "Daily digest" }).click()
  await actionsOf(page)
  await expect(page.getByRole("menuitem", { name: /Delete/ })).toBeVisible()
  await expect(page.getByRole("menuitem", { name: /Set as main chat/ })).toHaveCount(0)
  await page.keyboard.press("Escape")

  await pane("The release plan").getByRole("tab", { name: "The release plan" }).click()
  await actionsOf(page)
  await page.getByRole("menuitem", { name: /Set as main chat/ }).click()
  expect(calls).toEqual([{ call: "PATCH /bots/coordinator", body: { main_chat: "cht_old" } }])
  await expect(back("The release plan")).toHaveCount(0)
  await shot(page, "main-chat-grid")

  await back("Daily digest").click()
  const second = page.getByRole("main", { name: "Grid of chats" }).getByRole("region").nth(1)
  await expect(second.getByRole("tab")).toHaveText(["Daily digest", "The release plan"])
  await expect(second).toHaveAccessibleName("The release plan")
})
