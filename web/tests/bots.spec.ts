import { expect, test, type Page } from "@playwright/test"

import { choose } from "./choose.js"
import { shot } from "./shot.js"

// The Bots pages and the Bot of a chat, built, against a fake client API, on a phone and a desktop.

type Bot = {
  name: string
  provider?: string
  model?: string
  effort?: string
  persona: string
  memory: string
  memory_limit: number
  skills: string[]
  skills_off: string[]
  created_at: string
  updated_at: string
}

const hours = (n: number) => new Date(Date.now() - n * 3_600_000).toISOString()

const bot = (name: string, m: Partial<Bot> = {}): Bot => ({
  name,
  persona: "",
  memory: "",
  memory_limit: 2200,
  skills: [],
  skills_off: [],
  created_at: hours(200),
  updated_at: hours(3),
  ...m,
})

const coordinator = bot("coordinator", {
  provider: "gateway",
  model: "gpt-luna",
  persona: "Plan the work, give it to workers and report what they did, briefly.",
  memory:
    "The owner writes in Vietnamese and wants short answers.\nDeploys go out on Fridays.\nThe staging server is zm.",
})

const models = [
  { provider: "claude-subscription", name: "opus", kind: "claude_subscription" },
  { provider: "gateway", name: "gpt-luna", kind: "model_gateway" },
  { provider: "gateway", name: "gpt-sol", kind: "model_gateway" },
]

// The fake keeps the Bots as the control plane does: a PUT makes one or replaces its model, effort and persona
// and keeps its memory; a DELETE is refused for the first Bot and for one with a memory. A chat is made with the
// Bot it names, or the first.
async function fakeServer(page: Page, start: Bot[] = [coordinator]) {
  let bots = start
  let chat: object | null = null
  const calls: { call: string; body: unknown }[] = []

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = decodeURIComponent(new URL(request.url()).pathname.replace("/api/v1", ""))
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (request.method() !== "GET") calls.push({ call, body: request.postDataJSON() })

    const memoryOf = /^\/bots\/([^/]+)\/memory$/.exec(path)
    if (memoryOf && request.method() === "PUT") {
      const { memory } = request.postDataJSON() as { memory: string }
      const kept = memory
        .split("\n")
        .map((line) => line.trim())
        .filter(Boolean)
        .join("\n")

      if (kept.length > 2200) {
        return json({ code: "invalid", message: "the memory is over its limit of 2200" }, 422)
      }

      const stored = { ...bots.find((b) => b.name === memoryOf[1])!, memory: kept }
      bots = [...bots.filter((b) => b.name !== memoryOf[1]), stored]

      return json(stored)
    }

    const one = /^\/bots\/([^/]+)$/.exec(path)
    const found = one && bots.find((b) => b.name === one[1])

    if (one && request.method() === "GET") {
      return found ? json(found) : json({ code: "not_found", message: "no such bot" }, 404)
    }

    if (one && request.method() === "PUT") {
      const set = request.postDataJSON() as Partial<Bot>

      if (set.model === "gpt-sol") {
        return json({ code: "invalid", message: "pi cannot use the model gpt-sol" }, 422)
      }

      const { provider: _provider, model: _model, effort: _effort, ...kept } = found ?? bot(one[1]!)
      const stored = { ...kept, ...set, persona: set.persona ?? "", updated_at: hours(0) }
      bots = [...bots.filter((b) => b.name !== one[1]), stored]

      return json(stored)
    }

    if (one && request.method() === "DELETE") {
      if (one[1] === "coordinator") {
        return json(
          { code: "conflict", message: "the bot coordinator is always there and is not removed" },
          409,
        )
      }
      if (found?.memory) {
        return json(
          { code: "conflict", message: `the bot ${one[1]} has a memory; it is not removed` },
          409,
        )
      }

      bots = bots.filter((b) => b.name !== one[1])

      return route.fulfill({ status: 204 })
    }

    switch (call) {
      case "GET /me":
        return json({ id: "usr_1", name: "admin", kind: "owner" })
      case "GET /bots":
        return json({ data: bots })
      case "GET /models":
        return json({ data: models })
      case "POST /chats": {
        const made = request.postDataJSON() as { title?: string; bot?: string }
        chat = {
          id: "cht_1",
          title: made.title ?? "New chat",
          bot: made.bot ?? "coordinator",
          working: false,
          coordinator: true,
          created_at: hours(0),
          updated_at: hours(0),
        }

        return json(chat, 201)
      }
      case "GET /chats/cht_1":
        return chat ? json(chat) : json({ code: "not_found", message: "no such chat" }, 404)
      case "POST /chats/cht_1/messages":
        return json(
          {
            seq: 1,
            chat_id: "cht_1",
            role: "owner",
            text: (request.postDataJSON() as { text: string }).text,
            created_at: hours(0),
          },
          201,
        )
      case "GET /chats/cht_1/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "GET /chats":
        return json({ data: chat ? [chat] : [] })
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "GET /questions":
      case "GET /approvals":
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

// Scenario: the owner looks at the Bots as cards, then as a table that scrolls inside its box.
test("the Bots show as cards and as a table, each with its model and its memory", async ({
  page,
}) => {
  await fakeServer(page, [coordinator, bot("researcher")])
  await page.goto("/bots")

  const first = page.getByRole("article", { name: "coordinator" })
  await expect(first).toContainText("Plan the work, give it to workers")
  await expect(first).toContainText("Model:gpt-luna(gateway)")
  await expect(first).toContainText("Memory:108 of 2,200 characters")
  await expect(page.getByRole("article", { name: "researcher" })).toContainText(
    "Model:pi's default model",
  )
  expect(await fits(page)).toBe(true)
  await shot(page, "bots-grid")

  await page.getByRole("button", { name: "table" }).click()
  await expect(page.getByRole("row", { name: /researcher/ })).toContainText("0 of 2,200 characters")
  // The page itself does not scroll sideways: the table does, inside its box.
  expect(await fits(page)).toBe(true)
  await shot(page, "bots-table")
})

// Scenario: the owner makes a Bot. A name with a capital keeps Create off; a model pi cannot use is refused in
// the server's words; with a model it can use the Bot is stored and its page opens, its memory empty.
test("a new Bot is made with a name, a model and a persona", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/bots")
  await page.getByRole("link", { name: "New bot" }).click()

  await page.getByLabel("Name").fill("Researcher")
  await expect(page.getByText("Use lowercase letters, digits and dashes")).toBeVisible()
  await expect(page.getByRole("button", { name: "Create Bot" })).toBeDisabled()

  await page.getByLabel("Name").fill("researcher")
  await page.getByLabel("Model", { exact: true }).click()
  await page.getByRole("option", { name: "gpt-sol" }).click()
  await page.getByLabel("Persona").fill("You are the team's researcher. Answer with sources.")
  await page.getByRole("button", { name: "Create Bot" }).click()
  await expect(page.getByText("pi cannot use the model gpt-sol")).toBeVisible()
  await shot(page, "bot-new-refused")

  await page.getByLabel("Model", { exact: true }).click()
  await page.getByRole("option", { name: "opus" }).click()
  await page
    .getByRole("group", { name: "Effort" })
    .getByRole("button", { name: "High", exact: true })
    .click()
  await page.getByRole("button", { name: "Create Bot" }).click()

  await expect(page).toHaveURL(/\/bots\/researcher$/)
  await expect(page.getByRole("heading", { name: "researcher", level: 2 })).toBeVisible()
  await expect(page.getByText("Bot · Claude Opus (claude-subscription)")).toBeVisible()
  await expect(page.getByRole("textbox", { name: "Memory" })).toHaveValue("")
  expect(calls.at(-1)).toEqual({
    call: "PUT /bots/researcher",
    body: {
      provider: "claude-subscription",
      model: "opus",
      effort: "high",
      persona: "You are the team's researcher. Answer with sources.",
    },
  })
})

// Scenario: the owner opens the first Bot, reads what it keeps, and rewrites its persona. The save carries the
// model with the persona. Removing it is refused, and the question stays open with the server's reason.
test("a Bot shows its memory, takes a new persona and refuses to be removed", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/bots")
  await page.getByRole("link", { name: "coordinator" }).click()

  await expect(page.getByRole("heading", { name: "coordinator", level: 2 })).toBeVisible()
  await expect(page.getByLabel("Model", { exact: true })).toContainText("gpt-luna")
  await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled()

  // The memory is the Bot's own writing; the owner reads it and edits it by hand.
  const memory = page.getByRole("form", { name: "Memory" })
  await expect(memory).toContainText("108 of 2,200 characters")
  await expect(page.getByRole("textbox", { name: "Memory" })).toHaveValue(
    "The owner writes in Vietnamese and wants short answers.\nDeploys go out on Fridays.\nThe staging server is zm.",
  )
  await expect(memory.getByRole("button", { name: "Save memory" })).toHaveCount(0)
  expect(await fits(page)).toBe(true)
  await shot(page, "bot")

  await page
    .getByRole("textbox", { name: "Memory" })
    .fill("Deploys go out on Mondays.\n\nThe staging server is zm.")
  await expect(memory).toContainText("52 of 2,200 characters")
  await shot(page, "bot-memory-edited")
  await memory.getByRole("button", { name: "Save memory" }).click()
  await expect(page.getByRole("textbox", { name: "Memory" })).toHaveValue(
    "Deploys go out on Mondays.\nThe staging server is zm.",
  )
  await expect(memory.getByRole("button", { name: "Save memory" })).toHaveCount(0)

  await page.getByLabel("Persona").fill("Plan the work and report in Vietnamese.")
  await page.getByRole("button", { name: "Save", exact: true }).click()
  await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled()
  await expect(page.getByLabel("Persona")).toHaveValue("Plan the work and report in Vietnamese.")

  await page.getByRole("button", { name: "Remove Bot" }).click()
  const dialog = page.getByRole("alertdialog")
  await dialog.getByRole("button", { name: "Remove" }).click()
  await expect(dialog.getByRole("alert")).toContainText(
    "the bot coordinator is always there and is not removed",
  )
  await shot(page, "bot-remove-refused")
  await dialog.getByRole("button", { name: "Cancel" }).click()
  await expect(dialog).toBeHidden()
  await expect(page.getByRole("heading", { name: "coordinator", level: 2 })).toBeVisible()

  expect(calls).toEqual([
    {
      call: "PUT /bots/coordinator/memory",
      body: { memory: "Deploys go out on Mondays.\n\nThe staging server is zm." },
    },
    {
      call: "PUT /bots/coordinator",
      body: {
        provider: "gateway",
        model: "gpt-luna",
        persona: "Plan the work and report in Vietnamese.",
      },
    },
    { call: "DELETE /bots/coordinator", body: null },
  ])
})

// Scenario: a Bot with no chats and no memory is removed, and the list shows without it.
test("a Bot with nothing kept is removed", async ({ page }) => {
  await fakeServer(page, [coordinator, bot("researcher")])
  await page.goto("/bots/researcher")

  await page.getByRole("button", { name: "Remove Bot" }).click()
  await page.getByRole("alertdialog").getByRole("button", { name: "Remove" }).click()

  await expect(page).toHaveURL(/\/bots$/)
  await expect(page.getByRole("article", { name: "coordinator" })).toBeVisible()
  await expect(page.getByRole("article", { name: "researcher" })).toHaveCount(0)
})

// Scenario: there are two Bots. The start screen offers them, the first chosen; the owner picks the other, and
// the chat is made with it and names it in its header.
test("a new chat is started with the Bot the owner picks", async ({ page }) => {
  const calls = await fakeServer(page, [coordinator, bot("researcher")])
  await page.goto("/")

  await expect(page.getByLabel("Bot", { exact: true })).toContainText("coordinator")
  expect(await fits(page)).toBe(true)
  await shot(page, "chat-pick-bot")
  await choose(page, page.getByLabel("Bot", { exact: true }), "researcher")
  await page.getByRole("textbox", { name: "Message" }).fill("what changed in pi this week?")
  await page.keyboard.press("Enter")

  await expect(page).toHaveURL(/\/chats\/cht_1$/)
  await expect(page.getByRole("tab", { name: "what changed in pi this week?" })).toHaveAttribute(
    "title",
    "Chat with researcher",
  )
  await shot(page, "chat-bot")
  expect(calls[0]).toEqual({
    call: "POST /chats",
    body: { title: "what changed in pi this week?", bot: "researcher" },
  })
})

// Scenario: there is one Bot. Nothing is offered, and the chat is made without naming a Bot: the server takes
// the first.
test("with one Bot the start screen offers no choice", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/")

  await page.getByRole("textbox", { name: "Message" }).fill("add a health page")
  await expect(page.getByLabel("Bot", { exact: true })).toHaveCount(0)
  await page.keyboard.press("Enter")

  await expect(page.getByRole("tab", { name: "add a health page" })).toHaveAttribute(
    "title",
    "Chat with coordinator",
  )
  expect(calls[0]).toEqual({ call: "POST /chats", body: { title: "add a health page" } })
})
