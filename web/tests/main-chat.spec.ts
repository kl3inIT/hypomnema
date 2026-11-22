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

// gate is a promise opened by hand: what waits on it goes on once the test opens it.
function gate() {
  let open = () => {}
  const opened = new Promise<void>((done) => (open = done))

  return { opened, open: () => open() }
}

// Network is how the fake network and the CLI behave; a test may change it while the page is open.
type Network = {
  // cliFirst: the CLI writes to writer once the server has read the Bots for the page, making its main chat.
  cliFirst?: boolean
  // botsAfter holds the Bots' answer, as a slow network does, and botsFail answers 503 (issue #1055).
  botsAfter?: Promise<void>
  botsFail?: boolean
  // postAfter is how long a chat's making takes to reach the server, in milliseconds.
  postAfter?: number
  // lost is a chat of writer's under this id, made by a send whose answer was lost: writer's main chat.
  lost?: string
}

// fakeServer answers as the control plane: coordinator's main chat is cht_1, beside an older chat of it (cht_old) and
// a Routine's run (cht_run); writer has none until something makes it. POST /chats/main answers writer's main chat,
// making it when there is none; the Bots are answered as the server read them when asked.
async function fakeServer(page: Page, net: Network = {}) {
  const calls: Call[] = []
  const chats = [
    chat("cht_1", "Fix the header", "coordinator"),
    chat("cht_old", "The release plan", "coordinator", { updated_at: ago(3600) }),
    chat("cht_run", "Daily digest", "coordinator", { routine: true, updated_at: ago(1800) }),
    ...(net.lost ? [chat(net.lost, "draft the release notes", "writer")] : []),
  ]
  const bots = [bot("coordinator", 0, "cht_1"), bot("writer", 1, net.lost)]
  // cli makes writer's main chat as the CLI would.
  const cli = () => {
    const made = chat("cht_cli", "from the terminal", "writer", { created_at: ago(0) })
    if (!chats.some((c) => c.id === made.id)) chats.push(made)
    bots.find((b) => b.id === "writer")!.main_chat = made.id

    return made
  }

  await page.addInitScript({ content: "localStorage.setItem('hypomnema.token', 'secret')" })
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })
    const body = request.method() === "GET" ? null : request.postDataJSON()
    if (request.method() !== "GET") calls.push({ call, body })

    if (call === "POST /chats/main" || call === "POST /chats") {
      await new Promise((done) => setTimeout(done, net.postAfter ?? 0))
    }
    if (call === "POST /chats/main") {
      // As the server: the Bot's main chat is answered, unless the page says how long ago it opened and the main
      // chat is older (issue #1055); then, and when the Bot has none, the chat asked for is made its main chat.
      const asked = body as { bot: string; id: string; title?: string; opened_ms_ago?: number }
      const of = bots.find((b) => b.id === asked.bot)!
      const main = chats.find((c) => c.id === of.main_chat)
      const opened = Date.now() - (asked.opened_ms_ago ?? Infinity)
      if (main && Date.parse(main.created_at) >= opened) return json(main)
      const made = chat(asked.id, asked.title ?? "Chat", asked.bot)
      chats.push(made)
      of.main_chat = made.id

      return json(made, 201)
    }
    if (request.method() === "PATCH" && path.startsWith("/bots/")) {
      const changed = bots.find((b) => path === `/bots/${b.id}`)!
      changed.main_chat = (body as { main_chat: string }).main_chat

      return json(changed)
    }
    if (call === "POST /chats") {
      const asked = body as { bot: string; id?: string; title?: string }
      if (chats.some((c) => c.id === asked.id)) {
        return json({ code: "conflict", message: `chat ${asked.id} exists` }, 409)
      }
      const made = chat(asked.id ?? "cht_new", asked.title ?? "Chat", asked.bot)
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
      case "/bots": {
        const read = structuredClone(bots)
        if (net.cliFirst) cli()
        await net.botsAfter
        if (net.botsFail) return json({ code: "unavailable", message: "unavailable" }, 503)

        return json({ data: read })
      }
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
// there: no second chat is made. The message is sent before the Bots arrive and its chat is slow to be made (issue
// #1055): the page waits for the Bots, which say writer has none, and the server's write decides the rest.
test("the first message to a Bot with no main chat goes to the one made meanwhile", async ({
  page,
}) => {
  const sent = gate()
  const calls = await fakeServer(page, { cliFirst: true, botsAfter: sent.opened, postAfter: 1000 })
  await page.goto("/?bot=writer")

  await page.getByRole("textbox", { name: "Message" }).fill("draft the release notes")
  await page.keyboard.press("Enter")
  sent.open()

  await expect(page).toHaveURL(/\/chats\/cht_cli$/)
  expect(calls.map((c) => c.call)).toEqual(["POST /chats/main", "POST /chats/cht_cli/messages"])
  expect(calls[0]!.body).toMatchObject({ bot: "writer", title: "draft the release notes" })
})

// Scenario: a Bot with no main chat and nothing made meanwhile: the first message, sent before the Bots are read,
// makes it under the page's id.
test("the first message to a Bot with no main chat makes it under the page's id", async ({
  page,
}) => {
  const sent = gate()
  const calls = await fakeServer(page, { botsAfter: sent.opened })
  await page.goto("/?bot=writer")

  await page.getByRole("textbox", { name: "Message" }).fill("draft the release notes")
  await page.keyboard.press("Enter")
  sent.open()

  await expect(page).toHaveURL(/\/chats\/cht_\w+$/)
  const made = (calls[0]!.body as { id: string }).id
  expect(calls.map((c) => c.call)).toEqual(["POST /chats/main", `POST /chats/${made}/messages`])
  await expect(page).toHaveURL(new RegExp(`/chats/${made}$`))
})

// Scenario: the Bots are not read yet, and the owner sends the first message and reloads the page (issue #1055).
// Nothing was asked of the server yet: the message is still the page's draft after the reload.
test("a first message waiting for the Bots stays in the draft through a reload", async ({
  page,
}) => {
  const calls = await fakeServer(page, { botsAfter: gate().opened })
  await page.goto("/?bot=writer")

  const box = page.getByRole("textbox", { name: "Message" })
  await box.fill("draft the release notes")
  await page.keyboard.press("Enter")
  await page.reload()

  await expect(box).toHaveValue("draft the release notes")
  expect(calls).toEqual([])
})

// Scenario: the Bots cannot be read (503, issue #1055): the first message is not sent, the page says so and the
// message stays in the box; once they can be, it is sent to writer's main chat.
test("a first message is kept and the page says so when the Bots cannot be read", async ({
  page,
}) => {
  const net: Network = { botsFail: true }
  const calls = await fakeServer(page, net)
  await page.goto("/?bot=writer")

  const box = page.getByRole("textbox", { name: "Message" })
  await box.fill("draft the release notes")
  await page.keyboard.press("Enter")

  // The Bots' read is tried again a few times before it fails.
  await expect(page.getByText(/^Not sent/)).toBeVisible({ timeout: 20_000 })
  await expect(box).toHaveValue("draft the release notes")
  expect(calls).toEqual([])

  net.botsFail = false
  await page.keyboard.press("Enter")
  await expect(page).toHaveURL(/\/chats\/cht_\w+$/)
  const made = (calls[0]!.body as { id: string }).id
  expect(calls.map((c) => c.call)).toEqual(["POST /chats/main", `POST /chats/${made}/messages`])
})

// Scenario: the browser goes offline while the Bots are being read (issue #1055): the first message is not sent, the
// page says so at once and the message stays in the box.
test("a first message is kept and the page says so when it goes offline", async ({
  page,
  context,
}) => {
  const calls = await fakeServer(page, { botsAfter: gate().opened })
  await page.goto("/?bot=writer")

  const box = page.getByRole("textbox", { name: "Message" })
  await box.fill("draft the release notes")
  await context.setOffline(true)
  await page.keyboard.press("Enter")

  await expect(page.getByText(/^Not sent/)).toBeVisible()
  await expect(box).toHaveValue("draft the release notes")
  expect(calls).toEqual([])
  await context.setOffline(false)
})

// Scenario: a first message reached the server, which made writer's main chat under the page's id, but its answer
// was lost and the page reloaded (issue #1055): the page's draft still has that id. Sent again, the message goes to
// that chat: the page's id names it, and no second chat is made.
test("a first message sent again after a lost answer goes to the chat made under the page's id", async ({
  page,
}) => {
  const id = `cht_${"q".repeat(26)}`
  const calls = await fakeServer(page, { lost: id })
  await page.addInitScript({
    content: `localStorage.setItem("hypomnema:chat-drafts:v1", ${JSON.stringify(
      JSON.stringify({
        new: { text: "draft the release notes", files: [], chat: id, bot: "writer" },
      }),
    )})`,
  })
  await page.goto("/?bot=writer")

  const box = page.getByRole("textbox", { name: "Message" })
  await expect(box).toHaveValue("draft the release notes")
  await expect(page.getByLabel("Bot", { exact: true })).toContainText("writer")
  await box.press("Enter")

  await expect(page).toHaveURL(new RegExp(`/chats/${id}$`))
  expect(calls.map((c) => c.call).at(-1)).toBe(`POST /chats/${id}/messages`)
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

// Scenario: a new chat with writer is being written in front of coordinator's main chat's page, and the Routine's run
// page is open on another visit while that page keeps the draft in front (issue #923). The sidebar does not offer to
// make the hidden chat the main one, the draft has no way back, and Back to main chat from the Routine's run lands on
// the main chat in front of its page, not on the draft.
test("nothing acts on a chat behind a draft, and Back to main chat lands on the main chat", async ({
  page,
}) => {
  const calls = await fakeServer(page)
  await keep(page, {
    "chat-cht_1": {
      tabs: [
        { kind: "chat", chat: "cht_1" },
        { kind: "draft", id: "d1", bot: "writer" },
      ],
      active: "draft:d1",
    },
  })
  await page.goto("/chats/cht_1")

  const back = page.getByRole("button", { name: "Back to main chat" })
  await expect(page.getByRole("tab", { name: "Fix the header" })).toHaveAttribute(
    "aria-selected",
    "false",
  )
  await actionsOf(page)
  await expect(page.getByRole("menuitem", { name: /Delete/ })).toBeVisible()
  await expect(page.getByRole("menuitem", { name: /Set as main chat/ })).toHaveCount(0)
  await page.keyboard.press("Escape")
  const drawer = page.getByRole("dialog", { name: "Sidebar" })
  if (await drawer.isVisible()) {
    await page.keyboard.press("Escape")
    await expect(drawer).toBeHidden()
  }
  await expect(back).toHaveCount(0)

  await page.goto("/chats/cht_run")
  await back.click()
  await expect(page).toHaveURL(/\/chats\/cht_1$/)
  await expect(page.getByRole("tab", { name: "Fix the header" })).toHaveAttribute(
    "aria-selected",
    "true",
  )
  expect(calls).toEqual([])
})
