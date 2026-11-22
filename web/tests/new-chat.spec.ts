import { expect, test, type Page } from "@playwright/test"

import { choose } from "./choose.js"
import { shot } from "./shot.js"

// The New chat page with files attached before its chat is made (issue #357), against a fake client API: the page
// chose its chat's id, the files go into the picked Bot's folder under that id, and the first message makes the chat
// under it with the files.

const at = "2026-11-13T10:00:00Z"
// Each Bot has a main chat (issue #471), so New chat makes another: POST /chats.
const bot = (name: string) => ({
  id: name,
  main: name === "coordinator",
  main_chat: `cht_main_${name}`,
  name,
  model: "",
  persona: "",
  memory: "",
  updated_at: at,
})

// The models a chat may run with (issue #369) and the gateway's word on whether each reasons, and at which levels.
const models = [
  { provider: "gateway", name: "gpt-sol", kind: "model_gateway" },
  { provider: "gateway", name: "plain-mini", kind: "model_gateway" },
]

const gatewayModel = (name: string, reasoning: boolean, efforts: string[]) => ({
  name,
  targets: [],
  updated_at: at,
  attributes: {
    context_window: { source: "unknown" },
    max_output: { source: "unknown" },
    input: { source: "unknown" },
    reasoning: { value: reasoning, source: "models_dev" },
    efforts: { value: efforts, source: "models_dev" },
  },
})

async function fakeServer(page: Page) {
  // calls says what was asked of the Bots' folders, with the Bot named, and what was posted.
  const calls: string[] = []
  // The Bots are answered once slow.until settles, as on a slow network, as the server read them when asked (issue
  // #1055).
  const slow: { until?: Promise<void> } = {}
  // Each Bot's main chat, and when it was made.
  const mains: Record<string, { id: string; created_at: string }> = {
    coordinator: { id: "cht_main_coordinator", created_at: at },
    researcher: { id: "cht_main_researcher", created_at: at },
  }
  // cliNew starts a new chat with coordinator from the CLI (`hypomnema chat --new`), its main chat from now on.
  const cliNew = () => {
    mains.coordinator = { id: "cht_cli_new", created_at: new Date().toISOString() }
  }
  const posted: { call: string; body: Record<string, unknown> }[] = []
  // The Bots' folders, a file's place as "<bot>:<path>".
  const files = new Set<string>()
  const uploads = new Map<string, { bot: string; path: string; bytes: number }>()
  let chat: Record<string, unknown> | null = null
  let first: Record<string, unknown> | null = null
  // make makes the chat asked for.
  const make = (asked: Record<string, string> | null) => {
    chat = {
      id: asked?.id,
      title: asked?.title || "Chat",
      bot: asked?.bot ?? "coordinator",
      ...(asked?.provider && { provider: asked.provider }),
      ...(asked?.model && { model: asked.model }),
      ...(asked?.effort && { effort: asked.effort }),
      working: false,
      coordinator: true,
      created_at: at,
      updated_at: at,
    }

    return chat
  }

  // page.route answers a stream whole and ends it, which the page takes for a dropped stream; the page's fetch of a
  // stream gives what was answered and then stays open, as the server's does.
  await page.addInitScript(`{
    const real = globalThis.fetch.bind(globalThis)
    globalThis.fetch = async (input, init) => {
      const url = input instanceof Request ? input.url : String(input)
      if (!/\\/stream/.test(url)) return real(input, init)
      const text = await (await real(input, init)).text()
      const body = new ReadableStream({ start: (c) => c.enqueue(new TextEncoder().encode(text)) })
      return new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } })
    }
  }`)

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })
    const body =
      request.method() === "GET" ? null : (request.postDataJSON() as Record<string, string>)

    const folder = /^\/chats\/(cht_\w+)\/workspaces\/bot\/([a-z/]+)$/.exec(path)
    if (folder) {
      const named = url.searchParams.get("bot") ?? `${chat?.bot}`
      const where = body?.path ?? url.searchParams.get("path") ?? ""
      calls.push(`${request.method()} ${folder[2]} ${where} ${named}`)

      switch (folder[2]) {
        case "folders":
          return json({ path: where }, 201)
        case "tree":
          return json({ entries: [], has_more: false })
        case "uploads": {
          const upload = `u${uploads.size + 1}`
          uploads.set(upload, { bot: named, path: where, bytes: 0 })

          return json({ path: where, upload }, 201)
        }
        case "uploads/chunk": {
          const up = uploads.get(body?.upload ?? "")
          if (up) up.bytes += Buffer.from(body?.data ?? "", "base64").length

          return route.fulfill({ status: 204 })
        }
        case "uploads/commit": {
          const up = uploads.get(body?.upload ?? "")
          if (!up) return json({ code: "not_found", message: "no upload" }, 404)
          files.add(`${up.bot}:${up.path}`)

          return json({ path: up.path, sha256: "x" })
        }
        case "file":
          files.delete(`${named}:${where}`)

          return route.fulfill({ status: 204 })
        default:
          return json({ code: "not_found", message: "not found" }, 404)
      }
    }

    if (body) posted.push({ call, body })
    const id = typeof chat?.id === "string" ? chat.id : "none"

    switch (call) {
      case "GET /me":
        return json({ id: "usr_1", name: "admin", kind: "owner" })
      case "GET /bots": {
        const read = [bot("coordinator"), bot("researcher")].map((b) => ({
          ...b,
          main_chat: mains[b.id]?.id,
        }))
        await slow.until

        return json({ data: read })
      }
      case "GET /models":
        return json({ data: models })
      case "GET /gateway/models":
        return json({
          data: [
            gatewayModel("gpt-sol", true, ["minimal", "low", "medium", "high"]),
            gatewayModel("plain-mini", false, []),
          ],
        })
      case "GET /chats":
        return json({ data: chat ? [chat] : [] })
      case "POST /chats/main": {
        // As the server: the Bot's main chat is answered, unless the page says how long ago it opened and the main
        // chat is older (issue #1055); then the chat asked for is made its main chat.
        const of = body?.bot ?? "coordinator"
        const main = mains[of]
        const age = body?.opened_ms_ago as number | undefined
        if (main && (age === undefined || Date.parse(main.created_at) >= Date.now() - age)) {
          chat = { ...make({ bot: of, title: "Main" }), id: main.id }

          return json(chat)
        }
        mains[of] = { id: `${body?.id}`, created_at: new Date().toISOString() }

        return json(make(body), 201)
      }
      case "POST /chats": {
        const made = make(body)
        mains[`${made.bot}`] = { id: `${made.id}`, created_at: new Date().toISOString() }

        return json(made, 201)
      }
      case `GET /chats/${id}`:
        return json(chat)
      case `GET /chats/${id}/workspaces`:
        return json({
          data: [{ id: "bot", kind: "bot", name: chat?.bot, root: "/bots", online: true }],
        })
      case `POST /chats/${id}/messages`:
        first = { seq: 1, chat_id: id, role: "owner", created_at: at, ...body }

        return json(first, 201)
      case `GET /chats/${id}/stream`:
        return route.fulfill({
          status: 200,
          contentType: "text/event-stream",
          body: first ? `id: 1\nevent: message\ndata: ${JSON.stringify(first)}\n\n` : ": hello\n\n",
        })
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "GET /browser":
        return json({ code: "unavailable", message: "this control plane has no browser" }, 503)
      default:
        return json({ data: [] })
    }
  })

  return { calls, posted, files, slow, cliNew }
}

// gate is a promise opened by hand: what waits on it goes on once the test opens it.
function gate() {
  let open = () => {}
  const opened = new Promise<void>((done) => (open = done))

  return { opened, open: () => open() }
}

// Scenario: on the New chat page the owner attaches a report, then picks another Bot, then sends (issue #357). The
// report goes into the first Bot's folder under the chat's id chosen when the page opened, then again into the
// picked Bot's, and the first copy is deleted. The first message makes the chat under that id with the picked Bot,
// and the chat opens with the report in its first message.
test("files attached on the New chat page go with the first message", async ({ page }) => {
  const { calls, posted, files } = await fakeServer(page)
  await page.goto("/")
  await expect(page.getByLabel("Bot", { exact: true })).toContainText("coordinator")

  await page.getByLabel("Files to attach").setInputFiles({
    name: "report.pdf",
    mimeType: "application/pdf",
    buffer: Buffer.from("%PDF-1.7 report"),
  })
  const chips = page.getByLabel("Files to send")
  await expect(chips.getByText("report.pdf")).toBeVisible()
  await expect(chips.getByText("Uploading", { exact: false })).toHaveCount(0)

  const begun = calls.find((c) => c.startsWith("POST uploads "))
  const id = /attachments\/(cht_[a-z2-7]{26})\/report\.pdf coordinator$/.exec(begun ?? "")?.[1]
  expect(id, `${begun}`).toBeDefined()
  expect([...files]).toEqual([`coordinator:attachments/${id}/report.pdf`])

  const box = page.getByRole("textbox", { name: "Message" })
  await box.fill("summarise this report")
  await shot(page, "new-chat-attach")

  await choose(page, page.getByLabel("Bot", { exact: true }), "researcher")
  await expect.poll(() => [...files]).toEqual([`researcher:attachments/${id}/report.pdf`])
  expect(calls).toContain(`POST uploads attachments/${id}/report.pdf researcher`)
  expect(calls).toContain(`DELETE file attachments/${id}/report.pdf coordinator`)
  await expect(chips.getByText("report.pdf")).toBeVisible()

  await box.press("Enter")
  await expect(page).toHaveURL(new RegExp(`/chats/${id}$`))
  expect(posted.map((p) => p.call)).toEqual(["POST /chats", `POST /chats/${id}/messages`])
  expect(posted[0]?.body).toEqual({ id, title: "summarise this report", bot: "researcher" })
  expect(posted[1]?.body).toEqual({
    text: "summarise this report",
    attachments: [
      {
        name: "report.pdf",
        path: `attachments/${id}/report.pdf`,
        size: 15,
        type: "application/pdf",
      },
    ],
  })

  const log = page.getByRole("log")
  await expect(log.getByText("summarise this report")).toBeVisible()
  await expect(log.getByRole("button", { name: "Open report.pdf" })).toBeVisible()
  await shot(page, "new-chat-attach-sent")
})

// Scenario: on the New chat page the owner chooses the model and effort beside the Bot (issue #369), and the first
// message makes the chat with them; a model that does not reason has no effort. The open chat shows what was chosen.
test("the model and effort chosen on the New chat page are the chat's", async ({ page }) => {
  const { posted } = await fakeServer(page)
  await page.goto("/")
  await expect(page.getByLabel("Bot", { exact: true })).toContainText("coordinator")

  const model = page.getByRole("combobox", { name: /^Model:/ })
  await expect(model).toHaveAccessibleName("Model: Default model")
  await expect(page.getByRole("button", { name: /^Effort/ })).toHaveCount(0)

  await model.click()
  await page.getByRole("option", { name: /plain-mini/ }).click()
  await expect(model).toHaveAccessibleName("Model: plain-mini")
  await expect(page.getByRole("button", { name: /^Effort/ })).toHaveCount(0)

  await model.click()
  await page.getByRole("option", { name: /gpt-sol/ }).click()
  await page.getByRole("button", { name: "Effort: Default" }).click()
  await expect(page.getByRole("menuitemradio")).toHaveText([
    "Bot's default",
    "Minimal",
    "Low",
    "Medium",
    "High",
  ])
  await page.getByRole("menuitemradio", { name: "Medium" }).click()
  await expect(page.getByRole("button", { name: "Effort: Medium" })).toBeVisible()
  await shot(page, "new-chat-model")

  const box = page.getByRole("textbox", { name: "Message" })
  await box.fill("plan the release")
  await box.press("Enter")
  await expect(page).toHaveURL(/\/chats\/cht_/)
  expect(posted[0]?.body).toMatchObject({
    title: "plan the release",
    provider: "gateway",
    model: "gpt-sol",
    effort: "medium",
  })
  await expect(page.getByRole("combobox", { name: "Model: gpt-sol" })).toBeVisible()
  await expect(page.getByRole("button", { name: "Effort: Medium" })).toBeVisible()
  await shot(page, "new-chat-model-sent")
})

// Scenario: on the New chat page the owner attaches a report and writes, then reloads (issue #433). The text and
// the report are back, still in the folder of the chat's id the page chose, and the first message makes the chat
// under that id with them.
test("the New chat page keeps its draft across a reload", async ({ page }) => {
  const { calls, posted } = await fakeServer(page)
  await page.goto("/")
  await expect(page.getByLabel("Bot", { exact: true })).toContainText("coordinator")

  await page.getByLabel("Files to attach").setInputFiles({
    name: "report.pdf",
    mimeType: "application/pdf",
    buffer: Buffer.from("%PDF-1.7 report"),
  })
  const chips = page.getByLabel("Files to send")
  await expect(chips.getByText("Uploading", { exact: false })).toHaveCount(0)
  const begun = calls.find((c) => c.startsWith("POST uploads "))
  const id = /attachments\/(cht_[a-z2-7]{26})\/report\.pdf coordinator$/.exec(begun ?? "")?.[1]
  const box = page.getByRole("textbox", { name: "Message" })
  await box.fill("summarise this report")

  await page.reload()
  await expect(box).toHaveValue("summarise this report")
  await expect(chips.getByText("report.pdf")).toBeVisible()
  await shot(page, "new-chat-draft-kept")

  await box.press("Enter")
  await expect(page).toHaveURL(new RegExp(`/chats/${id}$`))
  expect(posted[1]?.body).toEqual({
    text: "summarise this report",
    attachments: [
      {
        name: "report.pdf",
        path: `attachments/${id}/report.pdf`,
        size: 15,
        type: "application/pdf",
      },
    ],
  })

  await page.goto("/")
  await expect(box).toHaveValue("")
  await expect(chips).toHaveCount(0)
})

// Scenario: the owner sends the New chat page's first message to coordinator, which has a main chat, before the Bots
// are read (issue #1055). The page waits for them, learns that coordinator has one, and makes a new chat under the
// page's id, as New chat does.
test("New chat makes a new chat with a Bot that has a main chat while the Bots are slow", async ({
  page,
}) => {
  const { posted, slow } = await fakeServer(page)
  const bots = gate()
  slow.until = bots.opened
  await page.goto("/")

  await page.getByRole("textbox", { name: "Message" }).fill("plan the release")
  await page.keyboard.press("Enter")
  bots.open()

  await expect.poll(() => posted.length).toBe(2)
  const id = posted[0]?.body.id
  expect(posted.map((p) => p.call)).toEqual(["POST /chats", `POST /chats/${id}/messages`])
  expect(posted[0]?.body).toEqual({ id, title: "plan the release", bot: "coordinator" })
  await expect(page).toHaveURL(new RegExp(`/chats/${id}$`))
})

// Scenario: the New chat page is open on coordinator, which has a main chat, when the CLI starts a new chat with it
// (`hypomnema chat --new`), and the owner sends before the Bots are read (issue #1055). The page's first message is
// still New chat: a new chat under the page's id, not the CLI's.
test("New chat makes a new chat when the CLI starts one meanwhile while the Bots are slow", async ({
  page,
}) => {
  const { posted, slow, cliNew } = await fakeServer(page)
  const bots = gate()
  slow.until = bots.opened
  await page.goto("/")

  const box = page.getByRole("textbox", { name: "Message" })
  await expect(box).toBeVisible()
  cliNew()
  await box.fill("plan the release")
  await page.keyboard.press("Enter")
  bots.open()

  await expect.poll(() => posted.length).toBe(2)
  const id = posted[0]?.body.id
  expect(posted.map((p) => p.call)).toEqual(["POST /chats", `POST /chats/${id}/messages`])
  await expect(page).toHaveURL(new RegExp(`/chats/${id}$`))
})

// Scenario: the owner attached a report on the New chat page and picked researcher, which has a main chat, then
// reloads the page and sends while the Bots are slow (issue #1055). The draft keeps the report and the Bot; the
// first message makes a new chat under the page's id, and the report goes with it.
test("a file on the New chat page goes with the first message while the Bots are slow", async ({
  page,
}) => {
  const { calls, posted, slow } = await fakeServer(page)
  await page.goto("/")
  await expect(page.getByLabel("Bot", { exact: true })).toContainText("coordinator")
  await choose(page, page.getByLabel("Bot", { exact: true }), "researcher")

  await page.getByLabel("Files to attach").setInputFiles({
    name: "report.pdf",
    mimeType: "application/pdf",
    buffer: Buffer.from("%PDF-1.7 report"),
  })
  const chips = page.getByLabel("Files to send")
  await expect(chips.getByText("report.pdf")).toBeVisible()
  await expect(chips.getByText("Uploading", { exact: false })).toHaveCount(0)
  const box = page.getByRole("textbox", { name: "Message" })
  await box.fill("summarise this report")
  const begun = calls.find((c) => c.startsWith("POST uploads "))
  const id = /attachments\/(cht_[a-z2-7]{26})\/report\.pdf researcher$/.exec(begun ?? "")?.[1]
  expect(id, `${begun}`).toBeDefined()

  const bots = gate()
  slow.until = bots.opened
  await page.reload()
  await expect(chips.getByText("report.pdf")).toBeVisible()
  await expect(box).toHaveValue("summarise this report")
  await box.press("Enter")
  bots.open()

  await expect(page).toHaveURL(new RegExp(`/chats/${id}$`))
  expect(posted.map((p) => p.call)).toEqual(["POST /chats", `POST /chats/${id}/messages`])
  expect(posted[0]?.body).toEqual({ id, title: "summarise this report", bot: "researcher" })
  expect(posted[1]?.body).toMatchObject({
    text: "summarise this report",
    attachments: [{ name: "report.pdf", path: `attachments/${id}/report.pdf` }],
  })
})
