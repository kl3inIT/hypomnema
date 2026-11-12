import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// A pane's tabs, built, against a fake client API: a new chat opened from "+" beside the chat on screen.

const at = "2026-11-09T10:00:00Z"
const chat = (id: string, title: string, bot = "coordinator") => ({
  id,
  title,
  bot,
  working: false,
  coordinator: true,
  created_at: at,
  updated_at: at,
})
const bot = (name: string) => ({ name, model: "", persona: "", memory: "", updated_at: at })

async function fakeServer(page: Page, bots: string[]) {
  const calls: { call: string; body?: unknown }[] = []
  const chats = [chat("cht_1", "health page")]

  await page.addInitScript({ content: "localStorage.setItem('hypomnema.token', 'secret')" })
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (request.method() === "POST") calls.push({ call, body: request.postDataJSON() })

    const one = /^GET \/chats\/(cht_\w+)(\/stream)?$/.exec(call)
    if (one) {
      const found = chats.find((c) => c.id === one[1])
      if (!found) return json({ code: "not_found", message: "no such chat" }, 404)
      if (!one[2]) return json(found)

      const said = {
        seq: 1,
        chat_id: found.id,
        role: "owner",
        text: `about the ${found.title}`,
        created_at: at,
      }

      return route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: `id: 1\nevent: message\ndata: ${JSON.stringify(said)}\n\n`,
      })
    }

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /bots":
        return json({ data: bots.map(bot) })
      case "GET /chats":
        return json({ data: chats })
      case "POST /chats": {
        const made = request.postDataJSON() as { title: string; bot?: string }
        const started = chat("cht_2", made.title, made.bot)
        chats.push(started)

        return json(started, 201)
      }
      case "POST /chats/cht_2/messages":
        return json({ seq: 1, chat_id: "cht_2", role: "owner", text: "x", created_at: at }, 201)
      case "GET /browser":
        return json({ code: "unavailable", message: "this control plane has no browser" }, 503)
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      default:
        return json({ data: [] })
    }
  })

  return calls
}

// Scenario: while a chat is open, the owner starts another beside it, as Orca opens a new terminal in a tab. The
// new chat is only a tab until its first message makes it; then it is a chat like the other, and both stay open
// after a reload.
test("a new chat is opened from + as a tab beside the chat", async ({ page }) => {
  const calls = await fakeServer(page, ["coordinator"])
  await page.goto("/chats/cht_1")
  await expect(page.getByText("about the health page")).toBeVisible()

  await page.getByRole("button", { name: "Open new" }).click()
  await page.getByRole("menuitem", { name: "New chat" }).click()
  await expect(page.getByRole("tab", { name: "New chat" })).toHaveAttribute("aria-selected", "true")
  await expect(page.getByText("A new chat with coordinator")).toBeVisible()
  expect(calls, "nothing is made before the first message").toEqual([])
  await shot(page, "pane-new-chat")

  const composer = page.getByRole("textbox", { name: "Message" }).filter({ visible: true })
  await composer.fill("a login button")
  await composer.press("Enter")

  await expect(page.getByRole("tab", { name: "a login button" })).toHaveAttribute(
    "aria-selected",
    "true",
  )
  expect(calls[0]).toEqual({ call: "POST /chats", body: { title: "a login button" } })
  await expect(page).toHaveURL(/\/chats\/cht_1$/)
  await expect(page.getByRole("tab")).toHaveText(["health page", "a login button"])

  // The chat of the page is always its first tab; another closes.
  await expect(page.getByRole("button", { name: "Close health page" })).toHaveCount(0)
  await page.reload()
  await expect(page.getByRole("tab")).toHaveText(["health page", "a login button"])
  await page.getByRole("button", { name: "Close a login button" }).click()
  await expect(page.getByRole("tab")).toHaveText(["health page"])
})

// With several Bots, "+" asks which the new chat talks with.
test("with several Bots, a new chat is opened with the Bot picked", async ({ page }) => {
  const calls = await fakeServer(page, ["coordinator", "researcher"])
  await page.goto("/chats/cht_1")

  await page.getByRole("button", { name: "Open new" }).click()
  await page.getByRole("menuitem", { name: "New chat" }).click()
  await page.getByRole("menuitem", { name: "researcher" }).click()
  await expect(page.getByText("A new chat with researcher")).toBeVisible()

  const composer = page.getByRole("textbox", { name: "Message" }).filter({ visible: true })
  await composer.fill("what changed in pi?")
  await composer.press("Enter")
  await expect(page.getByRole("tab", { name: "what changed in pi?" })).toHaveAttribute(
    "title",
    "Chat with researcher",
  )
  expect(calls[0]).toEqual({
    call: "POST /chats",
    body: { title: "what changed in pi?", bot: "researcher" },
  })
})
