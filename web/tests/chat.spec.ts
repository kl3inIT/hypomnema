import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// One open chat against a fake client API, with its stream held open by the test: page.route can only answer a
// request whole, so the page's fetch of the chat's stream is answered by a stream the test writes frames into.

const chat = {
  id: "cht_1",
  title: "health page",
  bot: "builder",
  working: false,
  coordinator: true,
  created_at: "2026-11-09T10:00:00Z",
  updated_at: "2026-11-09T10:00:00Z",
}

const message = (seq: number, m: object) => ({
  seq,
  chat_id: chat.id,
  text: "",
  created_at: "2026-11-09T10:00:00Z",
  ...m,
})

// The work a chat's Run did, as GET /tasks and GET /questions answer: its wake-up cards name it by title.
const run = {
  id: "run_1",
  objective: "Add a /health page to the control plane",
  status: "open",
  created_by: "pi",
  chat_id: chat.id,
  created_at: "2026-11-09T10:00:00Z",
  updated_at: "2026-11-09T10:00:00Z",
}

const task = (id: string, position: number, spec: string, status: string) => ({
  id,
  run_id: run.id,
  position,
  profile: "implementer",
  runtime: "claude",
  spec,
  status,
  failure_count: 0,
  created_at: "2026-11-09T10:00:00Z",
  updated_at: "2026-11-09T10:00:00Z",
})

const tasks = [
  task("tsk_1", 1, "Add the /health handler and its test", "succeeded"),
  task("tsk_2", 2, "Deploy the control plane to zm", "failed"),
  task("tsk_3", 3, "Add Google login to the web client", "running"),
]

const asked = {
  id: "qst_1",
  dispatch_id: "dsp_3",
  task_id: "tsk_3",
  run_id: run.id,
  mode: "wait",
  addressed_to: "coordinator",
  text: "Use goth or hand-written OAuth for the Google login?\nBoth work with the vault.",
  options: [],
  status: "open",
  created_at: "2026-11-09T10:00:00Z",
}

const kept = (seq: number, m: object) =>
  `id: ${seq}\nevent: message\ndata: ${JSON.stringify(message(seq, m))}\n\n`

const state = (working: boolean) => `event: state\ndata: ${JSON.stringify({ working })}\n\n`

type Streams = { push: (text: string) => void; end: () => void }[]

// The page's global, as the scripts that run in it see it (this package's types have no DOM).
type PageGlobal = { fetch: typeof fetch; streams: Streams }

async function fakeServer(page: Page, send: (text: string) => { status: number; body: unknown }) {
  const calls: string[] = []

  await page.addInitScript(() => {
    const page = globalThis as unknown as PageGlobal
    const streams: Streams = []
    page.streams = streams
    const real = page.fetch.bind(globalThis)

    page.fetch = (input, init) => {
      const url = input instanceof Request ? input.url : String(input)
      if (!/\/chats\/[^/]+\/stream/.test(url)) return real(input, init)

      const encoder = new TextEncoder()
      let writer: ReadableStreamDefaultController<Uint8Array> | undefined
      const body = new ReadableStream<Uint8Array>({ start: (c) => void (writer = c) })
      streams.push({
        push: (text) => writer?.enqueue(encoder.encode(text)),
        end: () => writer?.close(),
      })

      return Promise.resolve(
        new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } }),
      )
    }
  })

  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    calls.push(`${route.request().method()} ${path}`)

    if (path === "/me") return json({ id: "usr_1", name: "admin", kind: "owner" })
    if (path === "/approvals") return json({ data: [] })
    if (path === "/tasks") return json({ data: tasks, runs: [run], after: 5 })
    if (path === "/questions") return json({ data: [asked] })
    if (path === "/chats") return json({ data: [chat] })
    if (path === `/chats/${chat.id}`) return json(chat)
    if (path === `/chats/${chat.id}/abort`) return route.fulfill({ status: 204 })
    if (path === `/chats/${chat.id}/messages`) {
      const answer = send((route.request().postDataJSON() as { text: string }).text)

      return json(answer.body, answer.status)
    }

    return json({ code: "not_found", message: "not found" }, 404)
  })

  return calls
}

// streams is how many times the page opened the chat's stream; write and end act on the last one.
const streams = (page: Page) =>
  page.evaluate(() => (globalThis as unknown as PageGlobal).streams.length)

const write = (page: Page, text: string) =>
  page.evaluate((t) => (globalThis as unknown as PageGlobal).streams.at(-1)?.push(t), text)

const end = (page: Page) =>
  page.evaluate(() => (globalThis as unknown as PageGlobal).streams.at(-1)?.end())

async function open(page: Page) {
  await page.goto(`/chats/${chat.id}`)
  await expect.poll(() => streams(page)).toBe(1)
}

// Scenario: the owner sends a message. It shows in the chat before the server has kept it, and the box names the
// chat's Bot. A send the server refuses says why, and the text is back in the box to send again.
test("a message shows at once, and a refused one is kept to send again", async ({ page }) => {
  let refuse = false
  await fakeServer(page, (text) =>
    refuse
      ? { status: 503, body: { code: "unavailable", message: "the coordinator is not running" } }
      : { status: 201, body: message(2, { role: "owner", text }) },
  )
  await open(page)
  await write(page, state(false) + kept(1, { role: "owner", text: "add a health page" }))

  const box = page.getByRole("textbox", { name: "Message" })
  await expect(box).toHaveAttribute("placeholder", "Ask builder to build something")

  const log = page.getByRole("log")
  await box.fill("and a test for it")
  await box.press("Enter")
  // Not kept yet: the stream has not sent it.
  await expect(log.getByText("and a test for it")).toBeVisible()
  await expect(box).toHaveValue("")

  // Kept: it shows once.
  await write(page, kept(2, { role: "owner", text: "and a test for it" }))
  await expect(log.getByText("and a test for it")).toHaveCount(1)

  refuse = true
  await box.fill("deploy it")
  await box.press("Enter")
  await expect(page.getByRole("alert")).toHaveText("Not sent: the coordinator is not running")
  await expect(box).toHaveValue("deploy it")
  await expect(log.getByText("deploy it")).toHaveCount(0)
  await shot(page, "chat-send-failed")
})

// Scenario: the Bot is working and the owner presses Stop. The server ends the turn with a stopped row; the chat
// says it was stopped, quietly, not that it failed.
test("a turn the owner stopped says Stopped", async ({ page }) => {
  const calls = await fakeServer(page, (text) => ({
    status: 201,
    body: message(2, { role: "owner", text }),
  }))
  await open(page)
  await write(page, state(true) + kept(1, { role: "owner", text: "refactor everything" }))

  await expect(page.getByRole("textbox", { name: "Message" })).toHaveAttribute(
    "placeholder",
    "Add to what builder is doing",
  )
  await page.getByRole("button", { name: "Stop" }).click()
  await expect.poll(() => calls).toContain(`POST /chats/${chat.id}/abort`)

  await write(page, kept(2, { role: "system", kind: "stopped", text: "Stopped" }) + state(false))

  const log = page.getByRole("log")
  await expect(log.getByRole("note", { name: "Stopped" })).toBeVisible()
  await expect(log.getByRole("note", { name: "Notice" })).toHaveCount(0)
  await shot(page, "chat-stopped")
})

// Scenario: the server restarts and the chat's stream ends. The chat says it is reconnecting until the stream is
// open again.
test("the chat says when its stream is reconnecting", async ({ page }) => {
  await fakeServer(page, (text) => ({ status: 201, body: message(2, { role: "owner", text }) }))
  await open(page)
  await write(page, state(false) + kept(1, { role: "owner", text: "add a health page" }))

  const reconnecting = page.getByText("Reconnecting…")
  await expect(reconnecting).toHaveCount(0)

  await end(page)
  await expect(reconnecting).toBeVisible()
  await shot(page, "chat-reconnecting")

  await expect.poll(() => streams(page)).toBe(2)
  await write(page, state(false))
  await expect(reconnecting).toHaveCount(0)
})

// Scenario: the chat's Run moved on while the owner was away. Each wake-up is a card across the lane that says
// what happened in words, the Task by its title: one done, one failed with its reason, and a worker's question.
// The Task's chip opens its panel over the chat, kept in the URL; the Run's goes to its page.
test("wake-ups are cards that name their Task and open it", async ({ page }) => {
  await fakeServer(page, (text) => ({ status: 201, body: message(2, { role: "owner", text }) }))
  await open(page)

  const wake = (seq: number, text: string, w: object) =>
    kept(seq, {
      role: "system",
      kind: "wake",
      event_seq: seq,
      text,
      wake: { run_id: run.id, ...w },
    })

  await write(
    page,
    state(false) +
      kept(1, { role: "owner", text: "add a /health page and deploy it" }) +
      kept(2, { role: "coordinator", text: "Given to three workers." }) +
      wake(3, "Task tsk_1 of run run_1 is succeeded", {
        event: "task.status",
        task_id: "tsk_1",
        status: "succeeded",
      }) +
      wake(4, "Task tsk_2 of run run_1 is failed", {
        event: "task.status",
        task_id: "tsk_2",
        status: "failed",
        reason: "3 failed attempts; the last: worker_error",
      }) +
      wake(5, "A worker on task tsk_3 asks you question qst_1", {
        event: "question.opened",
        task_id: "tsk_3",
        question_id: "qst_1",
      }) +
      wake(6, "Run run_1 is finished", { event: "run.finished", status: "finished" }),
  )

  const log = page.getByRole("log")
  await expect(
    log.getByRole("note", { name: "Task Add the /health handler and its test done" }),
  ).toBeVisible()
  await expect(
    log.getByRole("note", {
      name: "Task Deploy the control plane to zm failed: 3 failed attempts; the last: worker_error",
    }),
  ).toBeVisible()
  await expect(
    log.getByRole("note", {
      name: "implementer asks: Use goth or hand-written OAuth for the Google login?",
    }),
  ).toBeVisible()
  await expect(
    log.getByRole("link", { name: "Open Run Add a /health page to the control plane" }),
  ).toHaveAttribute("href", "/runs/run_1")
  await expect(log.getByRole("note", { name: /^Task tsk_/ })).toHaveCount(0)
  await shot(page, "chat-wake-ups")

  await log.getByRole("button", { name: "Open Task Deploy the control plane to zm" }).click()
  await expect(page).toHaveURL(/[?&]task=tsk_2/)
  await expect(page.getByRole("dialog")).toBeVisible()
  await shot(page, "chat-wake-task-open")
})
