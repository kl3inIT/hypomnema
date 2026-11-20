import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// A Bot's main chat in the web (issue #471), built, against a fake client API: the first message to a Bot with no
// main chat goes to the one the CLI made meanwhile, and /new in a chat starts a new main chat.

const ago = (seconds: number) => new Date(Date.now() - seconds * 1000).toISOString()

const chat = (id: string, title: string, bot: string) => ({
  id,
  title,
  bot,
  working: false,
  coordinator: true,
  created_at: ago(600),
  updated_at: ago(60),
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

// fakeServer answers as the control plane: coordinator's main chat is cht_1; writer has none until something makes
// it. POST /chats/main answers writer's main chat, the CLI's (cht_cli) when cliFirst says the CLI wrote first.
async function fakeServer(page: Page, { cliFirst = false } = {}) {
  const calls: Call[] = []
  const chats = [chat("cht_1", "Fix the header", "coordinator")]
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
