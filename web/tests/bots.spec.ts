import { expect, test, type Page } from "@playwright/test"

import { choose } from "./choose.js"
import { shot } from "./shot.js"
import type {
  Bot,
  SetupChange as Change,
  ChatMessage as Message,
} from "../src/api/generated/types.gen.js"
import { fullToolAccess, toolFamilies } from "./fixtures/tools.js"

// The Bots pages and the Bot of a chat, built, against a fake client API, on a phone and a desktop.

const hours = (n: number) => new Date(Date.now() - n * 3_600_000).toISOString()

// Each Bot has a main chat (issue #471), so the start screen's New chat makes another: POST /chats.
const bot = (name: string, m: Partial<Bot> = {}): Bot => ({
  id: name,
  main_chat: `cht_main_${name}`,
  name,
  position: 0,
  main: name === "coordinator",
  persona: "",
  memory: "",
  memory_limit: 2200,
  fallbacks: [],
  skills: [],
  skills_off: [],
  preset: "full",
  tools: [],
  tools_off: [],
  connections: { limited: false, names: [] },
  workers: { limited: false, names: [] },
  scopes: {},
  tool_display: "on",
  guaranteed_dispatches: 4,
  created_at: hours(200),
  updated_at: hours(3),
  ...m,
})

const coordinator = bot("coordinator", {
  tool_access: fullToolAccess,
  provider: "gateway",
  model: "gpt-luna",
  persona: "Plan the work, give it to workers and report what they did, briefly.",
  memory:
    "The owner writes in Vietnamese and wants short answers.\nDeploys go out on Fridays.\nThe staging server is zm.",
})

const models = [
  { provider: "claude-subscription", name: "claude-opus-5-5", kind: "claude_subscription" },
  { provider: "opencode-go", name: "deepseek-v4.1-flash", kind: "opencode_go" },
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
  skill("android-device", "Drive an Android phone.", true),
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
  tools: [],
  tools_off: [],
  source: "",
  created_at: hours(100),
  available: true,
})

const profiles = [
  profile("explorer", "claude", "Reads a repository and answers a question about it."),
  profile("implementer", "claude", "Writes the change and opens a pull request."),
  profile("reviewer", "codex", "Reviews a pull request and reports what to fix."),
]

const minutesAgo = (n: number) => new Date(Date.now() - n * 60_000).toISOString()

// The fake keeps the Bots as the control plane does: a PUT makes one or replaces its model, effort and persona
// and keeps its memory; a DELETE is refused for the first Bot and for one with a memory. A chat is made with the
// Bot it names, or the first. A chat given here is there from the start, with its messages and the setup changes
// made in it; an undo marks a change undone.
async function fakeServer(
  page: Page,
  start: Bot[] = [coordinator],
  opts: {
    chat?: object
    messages?: Message[]
    changes?: Change[]
    integrations?: object[]
    notes?: Record<string, string>
  } = {},
) {
  let bots = start
  let chat: object | null = opts.chat ?? null
  let changes = opts.changes ?? []
  const notes = new Map(Object.entries(opts.notes ?? {}))
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

      const stored = { ...bots.find((b) => b.id === memoryOf[1])!, memory: kept }
      bots = [...bots.filter((b) => b.id !== memoryOf[1]), stored]

      return json(stored)
    }

    // A memory's notes (issue #589), the server's paths and sizes.
    const query = new URL(request.url()).searchParams
    if (path === "/memory-notes") {
      return json({
        data: [...notes.entries()].map(([p, text]) => ({
          path: p,
          bytes: new TextEncoder().encode(text).length,
          updated_at: hours(2),
        })),
        max_notes: 200,
        max_note_bytes: 8192,
      })
    }
    if (path === "/memory-notes/note") {
      const p = query.get("path")!
      const asNote = (text: string) => ({
        path: p,
        bytes: new TextEncoder().encode(text).length,
        updated_at: hours(0),
        text,
      })

      if (request.method() === "PUT") {
        if (!/^\/memories(\/[A-Za-z0-9_-][A-Za-z0-9._-]*){1,2}$/.test(p)) {
          return json({ code: "invalid", message: `invalid request: "${p}" is not a name` }, 422)
        }
        const { text } = request.postDataJSON() as { text: string }
        notes.set(p, text)

        return json(asNote(text))
      }
      if (request.method() === "DELETE") {
        notes.delete(p)

        return route.fulfill({ status: 204 })
      }

      return json(asNote(notes.get(p)!))
    }

    // A Bot is made under an id of its own and renamed by it (issue #466); the fixtures' ids are their names.
    if (call === "POST /bots") {
      const { name, settings } = request.postDataJSON() as { name: string; settings: Partial<Bot> }

      if (settings.model === "gpt-sol") {
        return json({ code: "invalid", message: "pi cannot use the model gpt-sol" }, 422)
      }

      const made = { ...bot(name), ...settings, id: `bot_${name}`, persona: settings.persona ?? "" }
      bots = [...bots, made]

      return json(made, 201)
    }

    const one = /^\/bots\/([^/]+)$/.exec(path)
    const found = one && bots.find((b) => b.id === one[1])

    if (one && request.method() === "PATCH") {
      const { name } = request.postDataJSON() as { name: string }

      if (bots.some((b) => b.name === name && b.id !== one[1])) {
        return json({ code: "conflict", message: `another bot is named "${name}"` }, 409)
      }

      const renamed = { ...found!, name, updated_at: hours(0) }
      bots = bots.map((b) => (b.id === one[1] ? renamed : b))

      return json(renamed)
    }

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
      bots = [...bots.filter((b) => b.id !== one[1]), stored]

      return json(stored)
    }

    if (one && request.method() === "DELETE") {
      if (one[1] === "coordinator") {
        return json(
          { code: "conflict", message: "the bot coordinator is always there and is not removed" },
          409,
        )
      }
      if (found?.memory && new URL(request.url()).searchParams.get("force") !== "true") {
        return json(
          { code: "conflict", message: `the bot ${one[1]} has a memory; it is not removed` },
          409,
        )
      }

      bots = bots.filter((b) => b.id !== one[1])

      return route.fulfill({ status: 204 })
    }

    // A Superset account's dashboards (issue #562), as the owner reads them.
    if (/^\/integrations\/superset\/connections\/[^/]+\/dashboards$/.test(path)) {
      return json({ data: dashboards })
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
      case "GET /tools":
        return json({ data: toolFamilies })
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
        return json({ data: opts.integrations ?? integrations })
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
  await fakeServer(page, [
    coordinator,
    bot("researcher", { connections: { limited: true, names: ["google:work", "lark:main"] } }),
  ])
  await page.goto("/bots")

  await expect(page.getByRole("heading", { name: "Bots", level: 1 })).toBeVisible()
  await expect(page.getByText("A Bot talks with you in chats")).toBeVisible()
  const first = page.getByRole("article", { name: "coordinator" })
  await expect(first).toContainText("Plan the work, give it to workers")
  await expect(first).toContainText("Model:gpt-luna(gateway)")
  await expect(first).toContainText("Memory:108 of 2,200 characters")
  await expect(first).toContainText("Works with:Everything you switched on")
  const second = page.getByRole("article", { name: "researcher" })
  await expect(second).toContainText("Model:Default (Claude Opus 5.5)")
  // The Connections a Bot is limited to, each as its Integration's mark and name (owner, 2026-11-13).
  await expect(second.getByRole("listitem")).toHaveText(["Google · work", "Lark · main"])
  expect(await fits(page)).toBe(true)
  await shot(page, "bots-grid")

  await page.getByRole("button", { name: "table" }).click()
  await expect(page.getByRole("row", { name: /researcher/ })).toContainText("0 of 2,200 characters")
  // The page itself does not scroll sideways: the table does, inside its box.
  expect(await fits(page)).toBe(true)
  await shot(page, "bots-table")
})

// Scenario: the owner makes a Bot. A name a Bot has keeps Create off; a model pi cannot use is refused in the server's
// words; with a model it can use the Bot is made under an id of its own and its page opens, its memory empty.
test("a new Bot is made with a name, a model and a persona", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/bots")
  await page.getByRole("link", { name: "New bot" }).click()

  await page.getByLabel("Name").fill("coordinator")
  await expect(page.getByText("A Bot has this name already.")).toBeVisible()
  await expect(page.getByRole("button", { name: "Create Bot" })).toBeDisabled()

  await page.getByLabel("Name").fill("Researcher")
  await page.getByLabel("Model", { exact: true }).click()
  await page.getByRole("option", { name: "gpt-sol" }).click()
  await page.getByLabel("Persona").fill("You are the team's researcher. Answer with sources.")
  await page.getByRole("button", { name: "Create Bot" }).click()
  await expect(page.getByText("pi cannot use the model gpt-sol")).toBeVisible()
  await shot(page, "bot-new-refused")

  await page.getByLabel("Model", { exact: true }).click()
  await page.getByRole("option", { name: /claude-opus-5-5/ }).click()
  await page
    .getByRole("group", { name: "Effort" })
    .getByRole("button", { name: "High", exact: true })
    .click()
  await page.getByRole("button", { name: "Create Bot" }).click()

  await expect(page).toHaveURL(/\/bots\/bot_Researcher$/)
  await expect(page.getByRole("heading", { name: "Researcher", level: 2 })).toBeVisible()
  await expect(page.getByText("Bot · Claude Opus 5.5 (claude-subscription)")).toBeVisible()
  await expect(page.getByRole("textbox", { name: "Memory" })).toHaveValue("")
  expect(calls.at(-1)).toEqual({
    call: "POST /bots",
    body: {
      name: "Researcher",
      settings: {
        provider: "claude-subscription",
        model: "claude-opus-5-5",
        effort: "high",
        persona: "You are the team's researcher. Answer with sources.",
        skills: [],
        skills_off: [],
        preset: "full",
        tools: [],
        tools_off: [],
      },
    },
  })
})

// Scenario: the owner renames a Bot on its page (issue #466). A name another Bot has keeps Rename off; the new name
// is the page's and the list's, and the address, which carries the id, stays.
test("a Bot is renamed and keeps its page", async ({ page }) => {
  const calls = await fakeServer(page, [coordinator, bot("researcher")])
  await page.goto("/bots/researcher")

  const name = page.getByRole("form", { name: "Name" })
  await expect(name.getByRole("textbox")).toHaveValue("researcher")
  await expect(name.getByRole("button", { name: "Rename" })).toBeDisabled()
  await shot(page, "bot-rename")

  await name.getByRole("textbox").fill("coordinator")
  await expect(name).toContainText("A Bot has this name already.")
  await expect(name.getByRole("button", { name: "Rename" })).toBeDisabled()
  await shot(page, "bot-rename-taken")

  await name.getByRole("textbox").fill("Nhà nghiên cứu")
  await name.getByRole("button", { name: "Rename" }).click()
  await expect(page.getByRole("heading", { name: "Nhà nghiên cứu", level: 2 })).toBeVisible()
  await expect(page).toHaveURL(/\/bots\/researcher$/)
  await expect(name.getByRole("button", { name: "Rename" })).toBeDisabled()
  expect(await fits(page)).toBe(true)
  await shot(page, "bot-renamed")
  expect(calls).toEqual([{ call: "PATCH /bots/researcher", body: { name: "Nhà nghiên cứu" } }])

  await page.goto("/bots")
  await expect(page.getByRole("article", { name: "Nhà nghiên cứu" })).toBeVisible()
  await shot(page, "bots-renamed")
})

// Scenario: the owner gives the first Bot a skill and turns off one given to everyone (issue #21), and turns its
// browser off by its family (issue #536); all are saved with the Bot.
test("a Bot's skills and tool preset are chosen in its form", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/bots/coordinator")

  const everyone = page.getByRole("list", { name: "Skills for everyone" })
  await expect(everyone).toContainText("Read, fill and merge PDF files.")
  await expect(everyone).toContainText("Missing: Phone tools are off")
  await everyone.getByRole("switch", { name: /pdf/ }).click()
  await choose(page, page.getByLabel("Skills", { exact: true }), "review-go")
  await expect(page.getByRole("list", { name: "Skills added" })).toContainText(
    "Review Go changes against the repository's conventions.",
  )
  await page.getByRole("list", { name: "Skills added" }).scrollIntoViewIfNeeded()
  expect(await fits(page)).toBe(true)
  await shot(page, "bot-skills")

  const tools = page.getByRole("group", { name: "Tools", exact: true })
  const saved = tools.getByRole("list", { name: "Saved tool access" })
  await expect(saved.getByText("No WhatsApp Connection", { exact: true }).first()).toBeVisible()
  await tools.evaluate((element) => element.scrollIntoView({ block: "start" }))
  expect(await fits(page)).toBe(true)
  await shot(page, "bot-tools")
  await saved.getByText("whatsapp", { exact: true }).click()
  await expect(saved.getByText("whatsapp_chats", { exact: true })).toBeVisible()
  await choose(page, tools.getByLabel("Preset"), "Report")
  await tools.getByLabel("Add exceptions").fill("phone")
  await tools.getByLabel("Remove exceptions").fill("browser")

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
      preset: "report",
      tools: ["phone"],
      tools_off: ["browser"],
    },
  })
})

// Scenario: a Bot keeps notes beside its memory (issue #589). The owner sees them listed with their sizes, opens
// one and corrects it, writes a new one by hand, and removes one after a question; a path the server refuses keeps
// the dialog open with its reason.
test("a Bot's notes are listed, opened, written and removed", async ({ page }) => {
  const calls = await fakeServer(page, [coordinator], {
    notes: { "/memories/vmap/dashboard.md": "DAU counts guests.\nTotal users: 3,586.\n" },
  })
  await page.goto("/bots/coordinator")

  const notes = page.getByRole("region", { name: "Notes" })
  await expect(notes).toContainText("1 of 200 notes")
  await expect(notes.getByRole("listitem")).toContainText([
    "/memories/vmap/dashboard.md39B · 2h ago",
  ])
  expect(await fits(page)).toBe(true)
  await notes.scrollIntoViewIfNeeded()
  await shot(page, "bot-notes")

  await notes.getByRole("button", { name: /dashboard\.md/ }).click()
  // The note's dialog, not the toast a save leaves, which is a dialog too.
  const dialog = page.locator('[data-slot="dialog-content"]')
  await expect(dialog.getByRole("textbox", { name: "Text" })).toHaveValue(
    "DAU counts guests.\nTotal users: 3,586.\n",
  )
  await expect(dialog.getByRole("button", { name: "Save note" })).toBeDisabled()
  await shot(page, "bot-note-open")
  await dialog
    .getByRole("textbox", { name: "Text" })
    .fill("DAU counts guests.\nTotal users: 3,600.\n")
  await dialog.getByRole("button", { name: "Save note" }).click()
  await expect(dialog).toBeHidden()

  await notes.getByRole("button", { name: "New note" }).click()
  await dialog.getByRole("textbox", { name: "Path" }).fill("/memories/../secrets")
  await dialog.getByRole("textbox", { name: "Text" }).fill("Linh leads maps.")
  await dialog.getByRole("button", { name: "Save note" }).click()
  await expect(dialog.getByRole("alert")).toContainText("is not a name")
  await dialog.getByRole("textbox", { name: "Path" }).fill("/memories/team.md")
  await shot(page, "bot-note-new")
  await dialog.getByRole("button", { name: "Save note" }).click()
  await expect(dialog).toBeHidden()
  await expect(notes).toContainText("2 of 200 notes")

  await notes.getByRole("button", { name: /team\.md/ }).click()
  await dialog.getByRole("button", { name: "Remove note" }).click()
  await page.getByRole("alertdialog").getByRole("button", { name: "Remove" }).click()
  await expect(notes).toContainText("1 of 200 notes")
  await expect(notes).not.toContainText("team.md")

  expect(calls.map((c) => c.call)).toEqual([
    "PUT /memory-notes/note",
    "PUT /memory-notes/note",
    "PUT /memory-notes/note",
    "DELETE /memory-notes/note",
  ])
  expect(calls[0]!.body).toEqual({ text: "DAU counts guests.\nTotal users: 3,600.\n" })
})

// Scenario: the owner opens the first Bot, reads what it keeps, and rewrites its persona. The save carries the
// model with the persona. Removing it is refused, and the question stays open with the server's reason.
test("a Bot shows its memory, takes a new persona and refuses to be removed", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/bots")
  await page.getByRole("link", { name: "Settings of coordinator" }).click()

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
  await expect(page.getByRole("button", { name: "Remove with its chats" })).toHaveCount(0)
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
        preset: "full",
        tools: [],
        tools_off: [],
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

// Scenario: a Bot keeps a memory, so the plain removal is refused; the owner removes it with its chats instead,
// which sends force, and the list shows without it.
test("a Bot with a memory is removed with its chats", async ({ page }) => {
  const kept = { ...bot("trial"), memory: "A test Bot." }
  const calls = await fakeServer(page, [coordinator, kept])
  await page.goto("/bots/trial")

  await page.getByRole("button", { name: "Remove Bot" }).click()
  const plain = page.getByRole("alertdialog")
  await plain.getByRole("button", { name: "Remove" }).click()
  await expect(plain.getByRole("alert")).toContainText("has a memory")
  await plain.getByRole("button", { name: "Cancel" }).click()

  await page.getByRole("button", { name: "Remove with its chats" }).click()
  const forced = page.getByRole("alertdialog")
  await expect(forced).toContainText("its chats and their messages are removed")
  await expect(forced).toHaveCSS("opacity", "1")
  await shot(page, "bot-remove-with-chats")
  await forced.getByRole("button", { name: "Remove all" }).click()

  await expect(page).toHaveURL(/\/bots$/)
  await expect(page.getByRole("article", { name: "trial" })).toHaveCount(0)
  expect(calls.map((c) => c.call)).toEqual(["DELETE /bots/trial", "DELETE /bots/trial"])
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
    body: {
      id: expect.stringMatching(/^cht_[a-z2-7]{26}$/),
      title: "what changed in pi this week?",
      bot: "researcher",
    },
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
  expect(calls[0]).toEqual({
    call: "POST /chats",
    body: { id: expect.stringMatching(/^cht_[a-z2-7]{26}$/), title: "add a health page" },
  })
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
  const workers = page.getByRole("region", { name: "Workers", exact: true })
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

// The dashboards a Superset account sees (issue #562).
const dashboards = [
  { id: 8, title: "Đóng góp VMAP — Cộng đồng", slug: "vmap-dong-gop", published: true },
  { id: 9, title: "Đóng góp VMAP — Nhân sự theo đơn vị", published: true },
  {
    id: 12,
    title: "Search logs and AI Assistant usage by day, unit and channel",
    published: false,
  },
]

// Scenario: two Bots share a Superset account and one answers anyone in a Lark group (issue #562). Under the
// Connections, each Superset account the Bot may use has its dashboards card: off, every dashboard and its own
// requests to Superset's API; "Limit dashboards" starts with none and each dashboard the account sees is a switch,
// saved at once as the Bot's scopes. A Superset account the Bot may not use has no card, and taking the limit
// away sends no scope for it.
test("a Bot is limited to some dashboards of a Superset account", async ({ page }) => {
  const community = bot("community", { persona: "You answer the community group." })
  const calls = await fakeServer(page, [coordinator, community], {
    integrations: [
      ...integrations,
      {
        id: "superset",
        name: "Superset",
        kind: "account",
        services: [],
        connections: [
          connection("superset", "prod", "svc-lark-report", true),
          connection("superset", "staging", "svc-staging", false),
        ],
      },
    ],
  })
  await page.goto("/bots/community")

  const card = page.getByRole("region", { name: "Superset · prod dashboards" })
  await card.scrollIntoViewIfNeeded()
  await expect(card).toContainText("Every dashboard the account sees")
  await expect(page.getByRole("region", { name: "Superset · staging dashboards" })).toHaveCount(0)
  expect(await fits(page)).toBe(true)
  await shot(page, "bot-dashboards")

  await card.getByRole("switch", { name: "Limit dashboards" }).click()
  await expect(card).toContainText("No dashboard: it reads nothing of this account.")
  await card.getByRole("switch", { name: "Superset · prod dashboards: 8" }).click()
  await expect(card).toContainText("1 of 3 on")
  await card.scrollIntoViewIfNeeded()
  expect(await fits(page)).toBe(true)
  await shot(page, "bot-dashboards-limited")

  await card.getByRole("switch", { name: "Limit dashboards" }).click()
  await expect(card).toContainText("Every dashboard the account sees")

  expect(calls.slice(-3)).toEqual([
    {
      call: "PUT /bots/community",
      body: {
        persona: "You answer the community group.",
        scopes: { "superset:prod": { dashboards: [] } },
      },
    },
    {
      call: "PUT /bots/community",
      body: {
        persona: "You answer the community group.",
        scopes: { "superset:prod": { dashboards: [8] } },
      },
    },
    {
      call: "PUT /bots/community",
      body: { persona: "You answer the community group.", scopes: {} },
    },
  ])
})

test("a Bot page shows and saves its ordered fallback models", async ({ page }) => {
  const calls = await fakeServer(page, [
    { ...coordinator, fallbacks: ["claude-subscription/claude-opus-5-5"] },
  ])
  await page.goto("/bots/coordinator")

  const chain = page.getByRole("list", { name: "Fallback models, in order" })
  await expect(chain.getByRole("listitem")).toHaveCount(1)
  await page.locator("#bot-fallback-add").click()
  await page.getByRole("option", { name: /deepseek-v4\.1-flash/i }).click()
  await expect(chain.getByRole("listitem")).toHaveCount(2)
  const save = page.getByRole("button", { name: "Save", exact: true })
  await save.click()
  await expect
    .poll(() => calls.at(-1))
    .toMatchObject({
      call: "PUT /bots/coordinator",
      body: {
        fallbacks: ["claude-subscription/claude-opus-5-5", "opencode-go/deepseek-v4.1-flash"],
      },
    })
  await expect(save).toBeDisabled()
  await chain.scrollIntoViewIfNeeded()
  expect(await fits(page)).toBe(true)
  await shot(page, "bot-fallback-models")
})

// Scenario: a Bot others use must not show its bash runs (issue #441). Its page says what its chats show of its
// tool calls, on by default, with a line for each level; choosing off saves at once with the Bot's model and persona,
// so nothing else changes.
test("a Bot's tool calls are shown off, on or full as its page sets", async ({ page }) => {
  const reporter = bot("reporter", { persona: "You send the Superset report." })
  const calls = await fakeServer(page, [coordinator, reporter])
  await page.goto("/bots/reporter")

  const tools = page.getByRole("region", { name: "Tool calls in chats" })
  await tools.scrollIntoViewIfNeeded()
  await expect(tools.getByRole("radio", { name: /^On/ })).toBeChecked()
  await expect(tools).toContainText("Only the answers: no tool call is shown.")
  await expect(tools).toContainText("without its command or output")
  await expect(tools).toContainText("the output and the time")
  expect(await fits(page)).toBe(true)
  await shot(page, "bot-tool-display")

  await tools.getByRole("radio", { name: /^Off/ }).click()
  await expect(tools.getByRole("radio", { name: /^Off/ })).toBeChecked()
  expect(calls.at(-1)).toEqual({
    call: "PUT /bots/reporter",
    body: { persona: "You send the Superset report.", tool_display: "off" },
  })
  await shot(page, "bot-tool-display-off")
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
