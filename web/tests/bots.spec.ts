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
  connections: { limited: boolean; names: string[] }
  workers: { limited: boolean; names: string[] }
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
  connections: { limited: false, names: [] },
  workers: { limited: false, names: [] },
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

// The skills the owner added (issue #21): one for everyone, two an agent or a Bot can add.
const skill = (name: string, description: string, everyone = false) => ({
  name,
  everyone,
  latest: {
    revision: 1,
    description,
    digest: "d",
    size: 1,
    files: 1,
    source: "upload",
    created_at: hours(30),
  },
  created_at: hours(30),
  updated_at: hours(30),
})

const skills = [
  skill("pdf", "Read, fill and merge PDF files.", true),
  skill("review-go", "Review Go changes against the repository's conventions."),
  skill("lark-docs", "Write and update documents in Lark."),
]

// The accounts the owner connected (issue #169): two switched on for chats, one not.
const connection = (integration: string, label: string, account: string, chats: boolean) => ({
  integration,
  label,
  account,
  services: [],
  sign_in_required: false,
  chats,
  created_at: hours(100),
  updated_at: hours(100),
})

const integrations = [
  {
    id: "google",
    name: "Google",
    kind: "oauth",
    services: ["gmail", "calendar"],
    connections: [
      connection("google", "work", "admin@tasco.vn", true),
      connection("google", "home", "me@gmail.com", false),
    ],
  },
  {
    id: "lark",
    name: "Lark",
    kind: "app",
    services: [],
    connections: [connection("lark", "main", "Hypomnema bot", true)],
  },
]

const profile = (name: string, runtime: string, description: string) => ({
  name,
  description,
  version_id: `pfv_${name}`,
  version: 1,
  runtime,
  fallbacks: [],
  skills: [],
  skills_off: [],
  source: "",
  created_at: hours(100),
  available: true,
})

const profiles = [
  profile("explorer", "claude", "Reads a repository and answers a question about it."),
  profile("implementer", "claude", "Writes the change and opens a pull request."),
  profile("reviewer", "codex", "Reviews a pull request and reports what to fix."),
]

type Change = {
  id: string
  chat_id: string
  kind: "bot" | "profile"
  name: string
  action: "made" | "changed" | "removed"
  can_undo: boolean
  problem?: string
  created_at: string
  undone_at?: string
}

type Message = { seq: number; chat_id: string; role: string; text: string; created_at: string }

const minutesAgo = (n: number) => new Date(Date.now() - n * 60_000).toISOString()

// The fake keeps the Bots as the control plane does: a PUT makes one or replaces its model, effort and persona
// and keeps its memory; a DELETE is refused for the first Bot and for one with a memory. A chat is made with the
// Bot it names, or the first. A chat given here is there from the start, with its messages and the setup changes
// made in it; an undo marks a change undone.
async function fakeServer(
  page: Page,
  start: Bot[] = [coordinator],
  opts: { chat?: object; messages?: Message[]; changes?: Change[] } = {},
) {
  let bots = start
  let chat: object | null = opts.chat ?? null
  let changes = opts.changes ?? []
  const calls: { call: string; body: unknown }[] = []

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = decodeURIComponent(new URL(request.url()).pathname.replace("/api/v1", ""))
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (request.method() !== "GET") calls.push({ call, body: request.postDataJSON() })

    const undo = /^\/setup-changes\/([^/]+)\/undo$/.exec(path)
    if (undo && request.method() === "POST") {
      changes = changes.map((c) =>
        c.id === undo[1] ? { ...c, can_undo: false, undone_at: minutesAgo(0) } : c,
      )

      return json(changes.find((c) => c.id === undo[1]))
    }

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
      case "GET /skills":
        return json({ data: skills })
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
        return route.fulfill({
          status: 200,
          contentType: "text/event-stream",
          body:
            (opts.messages ?? [])
              .map((m) => `id: ${m.seq}\nevent: message\ndata: ${JSON.stringify(m)}\n\n`)
              .join("") || ": hello\n\n",
        })
      case "GET /setup-changes":
        return json({ data: changes })
      case "GET /integrations":
        return json({ data: integrations })
      case "GET /profiles":
        return json({ data: profiles })
      case "GET /chats":
        return json({ data: chat ? [chat] : [] })
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "GET /questions":
      case "GET /approvals":
      case "GET /routines":
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

  await expect(page.getByRole("heading", { name: "Bots", level: 1 })).toBeVisible()
  await expect(page.getByText("A Bot talks with you in chats")).toBeVisible()
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
      skills: [],
      skills_off: [],
    },
  })
})

// Scenario: the owner gives the first Bot a skill and turns off one given to everyone (issue #21); both are
// saved with the Bot.
test("a Bot's skills are chosen in its form", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/bots/coordinator")

  const everyone = page.getByRole("list", { name: "Skills for everyone" })
  await expect(everyone).toContainText("Read, fill and merge PDF files.")
  await everyone.getByRole("switch", { name: /pdf/ }).click()
  await choose(page, page.getByLabel("Skills", { exact: true }), "review-go")
  await expect(page.getByRole("list", { name: "Skills added" })).toContainText(
    "Review Go changes against the repository's conventions.",
  )
  await page.getByRole("list", { name: "Skills added" }).scrollIntoViewIfNeeded()
  expect(await fits(page)).toBe(true)
  await shot(page, "bot-skills")

  await page.getByRole("button", { name: "Save", exact: true }).click()
  await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled()
  expect(calls.at(-1)).toEqual({
    call: "PUT /bots/coordinator",
    body: {
      provider: "gateway",
      model: "gpt-luna",
      persona: "Plan the work, give it to workers and report what they did, briefly.",
      skills: ["review-go"],
      skills_off: ["pdf"],
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
        skills: [],
        skills_off: [],
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

// Scenario: the owner limits what a Bot may use (issue #169). Without a list it may use every account switched
// on for chats and every worker; "Limit Connections" starts with none, and each account switched on for chats is
// a switch; the account not used in chats is not offered. "Limit workers" is the same card. Each switch saves at
// once with the Bot's model and persona, so nothing else changes.
test("a Bot's Connections and workers are limited on its page", async ({ page }) => {
  const researcher = bot("researcher", { persona: "You find sources and say where they are." })
  const calls = await fakeServer(page, [coordinator, researcher])
  await page.goto("/bots/researcher")

  const connections = page.getByRole("region", { name: "Connections" })
  const workers = page.getByRole("region", { name: "Workers" })
  await connections.scrollIntoViewIfNeeded()
  await expect(connections).toContainText("Every account switched on for chats")
  await expect(workers).toContainText("Every worker profile")
  expect(await fits(page)).toBe(true)
  await shot(page, "bot-access")

  await connections.getByRole("switch", { name: "Limit Connections" }).click()
  await expect(connections).toContainText("No account: every Connection is off for this Bot.")
  await expect(connections.getByRole("switch", { name: "Connections: google:home" })).toHaveCount(0)
  await connections.getByRole("switch", { name: "Connections: google:work" }).click()
  await expect(connections).toContainText("1 of 2 on")

  await workers.getByRole("switch", { name: "Limit workers" }).click()
  await workers.getByRole("switch", { name: "Workers: reviewer" }).click()
  await expect(workers).toContainText("1 of 3 on")
  await workers.scrollIntoViewIfNeeded()
  expect(await fits(page)).toBe(true)
  await shot(page, "bot-access-limited")

  expect(calls.slice(-2)).toEqual([
    {
      call: "PUT /bots/researcher",
      body: {
        persona: "You find sources and say where they are.",
        workers: { limited: true, names: [] },
      },
    },
    {
      call: "PUT /bots/researcher",
      body: {
        persona: "You find sources and say where they are.",
        workers: { limited: true, names: ["reviewer"] },
      },
    },
  ])
})

// Scenario: the Bot coordinator made a Bot and changed a profile in this chat (issue #169). Each change has its
// card where it was made, with Open, and Undo while nothing changed or used it since; the profile, given a Task
// since, says why it cannot be undone. The owner undoes the new Bot: its card says it was undone.
test("a chat shows what the Bot coordinator changed, and the owner undoes it", async ({ page }) => {
  const chat = {
    id: "cht_1",
    title: "A researcher for the VMAP docs",
    bot: "coordinator",
    working: false,
    coordinator: true,
    created_at: minutesAgo(30),
    updated_at: minutesAgo(1),
  }
  const messages: Message[] = [
    {
      seq: 1,
      chat_id: "cht_1",
      role: "owner",
      text: "We need someone to find sources for the VMAP docs, and reviews should run on Codex.",
      created_at: minutesAgo(12),
    },
    {
      seq: 2,
      chat_id: "cht_1",
      role: "coordinator",
      text: "I made the Bot researcher for sources and moved the reviewer profile to Codex. You can undo either on its card.",
      created_at: minutesAgo(10),
    },
  ]
  const changes: Change[] = [
    {
      id: "chg_1",
      chat_id: "cht_1",
      kind: "bot",
      name: "researcher",
      action: "made",
      can_undo: true,
      created_at: minutesAgo(11),
    },
    {
      id: "chg_2",
      chat_id: "cht_1",
      kind: "profile",
      name: "reviewer",
      action: "changed",
      can_undo: false,
      problem: "profile reviewer was changed or used since; change it by hand",
      created_at: minutesAgo(11),
    },
  ]
  const calls = await fakeServer(page, [coordinator, bot("researcher")], {
    chat,
    messages,
    changes,
  })
  await page.goto("/chats/cht_1")

  const made = page.getByRole("region", { name: "Made Bot researcher" })
  await expect(made).toContainText("Applied. You can undo it.")
  await expect(made.getByRole("link", { name: "Open" })).toHaveAttribute("href", "/bots/researcher")
  const changed = page.getByRole("region", { name: "Changed profile reviewer" })
  await expect(changed).toContainText("Can't be undone: profile reviewer was changed or used since")
  await expect(changed.getByRole("button", { name: "Undo" })).toHaveCount(0)
  expect(await fits(page)).toBe(true)
  await shot(page, "chat-setup-changes")

  await made.getByRole("button", { name: "Undo" }).click()
  await expect(made).toContainText("Undone.")
  await expect(made.getByRole("button", { name: "Undo" })).toHaveCount(0)
  expect(calls).toContainEqual({ call: "POST /setup-changes/chg_1/undo", body: null })
  await shot(page, "chat-setup-changes-undone")
})
