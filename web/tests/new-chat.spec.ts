import { expect, test, type Page } from "@playwright/test"

import { choose } from "./choose.js"
import { shot } from "./shot.js"

// The New chat page with files attached before its chat is made (issue #357), against a fake client API: the page
// chose its chat's id, the files go into the picked Bot's folder under that id, and the first message makes the chat
// under it with the files.

const at = "2026-11-13T10:00:00Z"
const bot = (name: string) => ({ name, model: "", persona: "", memory: "", updated_at: at })

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
  const posted: { call: string; body: Record<string, unknown> }[] = []
  // The Bots' folders, a file's place as "<bot>:<path>".
  const files = new Set<string>()
  const uploads = new Map<string, { bot: string; path: string; bytes: number }>()
  let chat: Record<string, unknown> | null = null
  let first: Record<string, unknown> | null = null

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
      case "GET /bots":
        return json({ data: [bot("coordinator"), bot("researcher")] })
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
      case "POST /chats":
        chat = {
          id: body?.id,
          title: body?.title || "Chat",
          bot: body?.bot ?? "coordinator",
          ...(body?.provider && { provider: body.provider }),
          ...(body?.model && { model: body.model }),
          ...(body?.effort && { effort: body.effort }),
          working: false,
          coordinator: true,
          created_at: at,
          updated_at: at,
        }

        return json(chat, 201)
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

  return { calls, posted, files }
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
