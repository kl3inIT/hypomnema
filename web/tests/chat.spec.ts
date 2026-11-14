import { writeFile } from "node:fs/promises"

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

// The models a chat may run with (issue #369): what the providers offer, and the gateway's attributes of each, which
// say whether it reasons and at which levels.
const models = [
  { provider: "gateway", name: "gpt-sol", kind: "model_gateway" },
  { provider: "gateway", name: "gpt-luna", kind: "model_gateway" },
  { provider: "gateway", name: "plain-mini", kind: "model_gateway" },
]

const gatewayModel = (name: string, reasoning: boolean, efforts: string[]) => ({
  name,
  targets: [{ provider: "openai", upstream_model: name }],
  updated_at: "2026-11-09T10:00:00Z",
  attributes: {
    context_window: { source: "unknown" },
    max_output: { source: "unknown" },
    input: { source: "unknown" },
    reasoning: { value: reasoning, source: "models_dev" },
    efforts: { value: efforts, source: "models_dev" },
  },
})

const gatewayModels = [
  gatewayModel("gpt-sol", true, ["low", "medium", "high", "xhigh"]),
  gatewayModel("gpt-luna", true, ["none", "low", "high"]),
  gatewayModel("plain-mini", false, []),
]

const builder = {
  name: "builder",
  provider: "gateway",
  model: "gpt-sol",
  effort: "high",
  persona: "",
  memory: "",
  memory_limit: 2200,
  skills: [],
  skills_off: [],
  connections: { limited: false, names: [] },
  workers: { limited: false, names: [] },
  tool_display: "on",
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

// The chat's own work as GET /runs?chat_id=, GET /runs/{id} and GET /questions?chat_id= answer it: the Runs docked
// above the composer and the questions their workers put to the owner. Finishing a Run or answering a question
// takes it out of what the next read answers.
type Work = {
  runs: { id: string; status: string; tasks: object[] }[]
  questions: Record<string, unknown>[]
}

const noWork = (): Work => ({
  runs: [{ ...run, status: "finished", tasks }],
  questions: [],
})

// The chat's Bot's folder (issue #343): its folder of attachments holds a report.pdf before the test, and a file
// uploaded into it is kept with its bytes, which the raw read gives back.
const botFolder = () =>
  new Map<string, Buffer>([["attachments/cht_1/report.pdf", Buffer.from("%PDF-1.7")]])

async function fakeServer(
  page: Page,
  send: (text: string, body: Record<string, unknown>) => { status: number; body: unknown },
  work: Work = noWork(),
  files = botFolder(),
  kept: Record<string, unknown> = chat,
) {
  const calls: string[] = []
  const uploads = new Map<string, { path: string; bytes: Buffer }>()
  // The chat as the server keeps it: a PATCH changes its model and effort.
  let current: Record<string, unknown> = { ...kept }

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

    const bot = /^\/chats\/cht_1\/workspaces\/bot\/([a-z/]+)$/.exec(path)
    if (bot) {
      const op = bot[1]
      const where = url.searchParams.get("path") ?? ""
      const body = route.request().postDataJSON() as Record<string, string> | null
      calls.push(`${route.request().method()} ${op} ${body?.path ?? where}`)

      if (op === "folders") return json({ path: body?.path })
      if (op === "changes") return json({ git: true, changes: [], truncated: false })
      if (op === "tree") {
        const prefix = `${where}/`
        const entries = [...files]
          .filter(([p]) => p.startsWith(prefix) && !p.slice(prefix.length).includes("/"))
          .map(([p, bytes]) => ({
            name: p.slice(prefix.length),
            path: p,
            type: "file",
            bytes: bytes.length,
            modified_at: 1,
          }))

        return json({ entries, has_more: false })
      }
      if (op === "uploads" && body) {
        const upload = `u${uploads.size + 1}`
        uploads.set(upload, { path: body.path ?? "", bytes: Buffer.alloc(0) })

        return json({ path: body.path, upload }, 201)
      }
      if (op === "uploads/chunk" && body) {
        const up = uploads.get(body.upload ?? "")
        if (!up) return json({ code: "not_found", message: "no upload" }, 404)
        up.bytes = Buffer.concat([up.bytes, Buffer.from(body.data ?? "", "base64")])

        return route.fulfill({ status: 204 })
      }
      if (op === "uploads/commit" && body) {
        const up = uploads.get(body.upload ?? "")
        if (!up) return json({ code: "not_found", message: "no upload" }, 404)
        if (files.has(up.path)) {
          return json({ code: "conflict", message: `${up.path} is already there` }, 409)
        }
        files.set(up.path, up.bytes)

        return json({ path: up.path, sha256: "x" })
      }
      if (op === "raw") {
        const bytes = files.get(where)
        if (!bytes) return json({ code: "not_found", message: "not there" }, 404)

        return route.fulfill({ status: 200, contentType: "application/octet-stream", body: bytes })
      }
      if (op === "file") {
        const bytes = files.get(where)
        if (!bytes) return json({ code: "not_found", message: "not there" }, 404)

        return json({
          path: where,
          bytes: bytes.length,
          binary: true,
          truncated: false,
          content: "",
          sha256: "x",
        })
      }

      return json({ code: "not_found", message: "not found" }, 404)
    }
    if (path === `/chats/${chat.id}/workspaces`) {
      return json({
        data: [{ id: "bot", kind: "bot", name: chat.bot, root: "/bots/builder", online: true }],
      })
    }

    if (path === "/me") return json({ id: "usr_1", name: "admin", kind: "owner" })
    if (path === "/approvals") return json({ data: [] })
    if (path === "/tasks") return json({ data: tasks, runs: [run], after: 5 })
    if (path === "/runs" && url.searchParams.get("chat_id") === chat.id)
      return json({ data: work.runs.filter((r) => r.status === "open") })
    const runPath = /^\/runs\/([^/]+)(\/finish|\/cancel)?$/.exec(path)
    if (runPath) {
      const found = work.runs.find((r) => r.id === runPath[1])
      if (!found) return json({ code: "not_found", message: "not found" }, 404)
      if (runPath[2]) found.status = runPath[2] === "/finish" ? "finished" : "canceled"

      return runPath[2] ? route.fulfill({ status: 204 }) : json(found)
    }
    if (path === "/questions" && url.searchParams.get("chat_id") === chat.id)
      return json({ data: work.questions })
    const answering = /^\/questions\/([^/]+)\/answer$/.exec(path)
    if (answering) {
      const q = work.questions.find((x) => x.id === answering[1])
      work.questions = work.questions.filter((x) => x.id !== answering[1])

      return json({ ...q, status: "answered" })
    }
    if (path === "/questions") return json({ data: [asked] })
    if (path === "/chats") return json({ data: [chat] })
    if (path === "/bots") return json({ data: [builder] })
    if (path === "/models") return json({ data: models })
    if (path === "/gateway/models") return json({ data: gatewayModels })
    if (path === `/chats/${chat.id}` && route.request().method() === "PATCH") {
      const change = route.request().postDataJSON() as Record<string, string>
      calls.push(`PATCH ${JSON.stringify(change)}`)
      current = { ...current }
      for (const [k, v] of Object.entries(change)) {
        if (v) current[k] = v
        else delete current[k]
      }

      return json(current)
    }
    if (path === `/chats/${chat.id}`) return json(current)
    if (path === `/chats/${chat.id}/abort`) return route.fulfill({ status: 204 })
    if (path === `/chats/${chat.id}/compact`) return route.fulfill({ status: 202 })
    if (path === `/chats/${chat.id}/messages`) {
      const body = route.request().postDataJSON() as { text: string }
      const answer = send(body.text, body)

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
// The Task's chip opens its panel over the chat, kept in the URL; the Run's shows it docked.
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
  await expect(log.getByRole("note", { name: /^Task tsk_/ })).toHaveCount(0)
  await shot(page, "chat-wake-ups")

  // The Run is this chat's: its chip shows it docked above the composer, finished, rather than leaving the chat.
  await log
    .getByRole("button", { name: "Show Run Add a /health page to the control plane" })
    .click()
  const docked = page.getByRole("list", { name: "Runs of this chat" })
  await expect(
    docked.getByRole("button", { name: /^Add a \/health page to the control plane: Finished/ }),
  ).toHaveAttribute("aria-expanded", "true")
  await shot(page, "chat-wake-run-shown")

  await log.getByRole("button", { name: "Open Task Deploy the control plane to zm" }).click()
  await expect(page).toHaveURL(/[?&]task=tsk_2/)
  await expect(page.getByRole("dialog")).toBeVisible()
  await shot(page, "chat-wake-task-open")
})

// Scenario: a wake-up names a Run of another chat and one made from the command line. Neither docks here: once
// read, the first's chip opens the chat it belongs to, and the second's, with no chat, opens its first Task's panel
// over this chat.
test("a wake-up's Run of another chat opens that chat", async ({ page }) => {
  const other = { ...run, id: "run_7", chat_id: "cht_9", status: "finished", tasks: [] }
  const cli = {
    ...run,
    id: "run_8",
    chat_id: undefined,
    status: "finished",
    tasks: [task("tsk_8", 1, "Check the release notes", "succeeded")],
  }
  await fakeServer(page, (text) => ({ status: 201, body: message(2, { role: "owner", text }) }), {
    runs: [other, cli],
    questions: [],
  })
  await open(page)

  const wake = (seq: number, runId: string) =>
    kept(seq, {
      role: "system",
      kind: "wake",
      event_seq: seq,
      text: `Run ${runId} is finished`,
      wake: { run_id: runId, event: "run.finished", status: "finished" },
    })
  await write(page, state(false) + wake(1, "run_7") + wake(2, "run_8"))

  const log = page.getByRole("log")
  await log.getByRole("button", { name: "Show Run run_8" }).click()
  await log.getByRole("button", { name: "Open Run run_8" }).click()
  await expect(page).toHaveURL(/[?&]task=tsk_8/)
  await page.keyboard.press("Escape")

  await log.getByRole("button", { name: "Show Run run_7" }).click()
  await log.getByRole("link", { name: "Open Run run_7 in its chat" }).click()
  await expect(page).toHaveURL(/\/chats\/cht_9$/)
})

const openRun = (id: string, objective: string, list: object[]) => ({
  ...run,
  id,
  objective,
  status: "open",
  tasks: list.map((t) => ({ ...t, run_id: id })),
})

// Scenario: the chat has two Runs going. Each is a closed strip above the composer that says where it stands and
// how many of its Tasks are done; one opens to its Tasks, each with its status in words, and a Task opens its panel
// over the chat.
test("a docked Run opens to its Tasks", async ({ page }) => {
  await fakeServer(page, (text) => ({ status: 201, body: message(2, { role: "owner", text }) }), {
    runs: [
      openRun("run_1", run.objective, tasks),
      openRun("run_2", "Write the release notes for 0.9", [
        task("tsk_9", 1, "Draft the notes from the merged pull requests", "pending"),
      ]),
    ],
    questions: [],
  })
  await open(page)
  await write(page, state(false) + kept(1, { role: "owner", text: "add a /health page" }))

  const docked = page.getByRole("list", { name: "Runs of this chat" })
  const strip = docked.getByRole("button", {
    name: "Add a /health page to the control plane: Failed, 1 of 3 Tasks done",
  })
  await expect(strip).toHaveAttribute("aria-expanded", "false")
  await expect(
    docked.getByRole("button", {
      name: "Write the release notes for 0.9: Queued, 0 of 1 Task done",
    }),
  ).toHaveAttribute("aria-expanded", "false")
  await shot(page, "chat-dock-collapsed")

  await strip.click()
  const first = docked.getByRole("listitem", {
    name: "Run Add a /health page to the control plane",
  })
  const rows = first.getByRole("list", { name: "Tasks" })
  await expect(rows.getByRole("button")).toHaveText([
    /Add the \/health handler and its test\s*Done/,
    /Deploy the control plane to zm\s*Failed/,
    /Add Google login to the web client\s*Working/,
  ])
  await expect(first.getByRole("button", { name: "Finish Run" })).toBeDisabled()
  await shot(page, "chat-dock-expanded")

  await rows.getByRole("button", { name: /^Open Task Deploy the control plane to zm/ }).click()
  await expect(page).toHaveURL(/[?&]task=tsk_2/)
})

// Scenario: every Task of the chat's Run has ended. The owner presses Finish Run, is asked first, and the Run
// finishes and leaves the dock.
test("Finish asks and finishes the docked Run", async ({ page }) => {
  const calls = await fakeServer(
    page,
    (text) => ({ status: 201, body: message(2, { role: "owner", text }) }),
    {
      runs: [
        openRun("run_1", run.objective, [
          task("tsk_1", 1, "Add the /health handler and its test", "succeeded"),
          task("tsk_2", 2, "Deploy the control plane to zm", "canceled"),
        ]),
      ],
      questions: [],
    },
  )
  await open(page)
  await write(page, state(false))

  const docked = page.getByRole("list", { name: "Runs of this chat" })
  await expect(
    docked.getByRole("button", {
      name: "Add a /health page to the control plane: Ready to finish, 1 of 2 Tasks done",
    }),
  ).toHaveAttribute("aria-expanded", "true")

  await docked.getByRole("button", { name: "Finish Run" }).click()
  const ask = page.getByRole("alertdialog")
  await expect(ask).toContainText('Finish the Run "Add a /health page to the control plane"?')
  await shot(page, "chat-dock-finish")
  await ask.getByRole("button", { name: "Finish Run" }).click()

  await expect.poll(() => calls).toContain("POST /runs/run_1/finish")
  await expect(ask).toHaveCount(0)
  await expect(docked).toHaveCount(0)
})

// Scenario: a worker of the chat's Run asks the owner. The question waits above the composer with the Task it
// comes from; answered there, it goes.
test("a question answered in the chat disappears", async ({ page }) => {
  const calls = await fakeServer(
    page,
    (text) => ({ status: 201, body: message(2, { role: "owner", text }) }),
    {
      runs: [openRun("run_1", run.objective, tasks)],
      questions: [
        {
          ...asked,
          id: "qst_9",
          addressed_to: "owner",
          text: "May I drop the old sessions table?",
          options: [{ label: "Yes, drop it" }, { label: "No, keep it", allows_text: true }],
        },
      ],
    },
  )
  await open(page)
  await write(page, state(false))

  const question = page.getByRole("region", {
    name: "Question: May I drop the old sessions table?",
  })
  await expect(question).toContainText("Task 3 · Add Google login to the web client")
  await shot(page, "chat-dock-question")

  await question.getByRole("button", { name: "Send answer" }).click()
  await expect.poll(() => calls).toContain("POST /questions/qst_9/answer")
  await expect(question).toHaveCount(0)
})

// settled waits for the page's animations and transitions to end, so that a picture shows where things came to rest.
const settled = (page: Page) =>
  page.waitForFunction("document.getAnimations().every((a) => a.playState !== 'running')")

// pasteImage pastes a picture into the message box, as a screenshot copied to the clipboard is: a PNG drawn by the
// page itself. It runs in the page, written as text since this package's types have no DOM.
async function pasteImage(page: Page) {
  await page.evaluate(`(async () => {
    const canvas = document.createElement("canvas")
    canvas.width = 480
    canvas.height = 300
    const g = canvas.getContext("2d")
    const fill = g.createLinearGradient(0, 0, 480, 300)
    fill.addColorStop(0, "#4f46e5")
    fill.addColorStop(1, "#06b6d4")
    g.fillStyle = fill
    g.fillRect(0, 0, 480, 300)
    g.fillStyle = "#ffffff"
    g.font = "bold 36px sans-serif"
    g.fillText("/health 200 OK", 110, 160)
    const blob = await new Promise((done) => canvas.toBlob(done, "image/png"))
    const data = new DataTransfer()
    data.items.add(new File([blob], "image.png", { type: "image/png" }))
    const box = document.querySelector('textarea[aria-label="Message"]')
    box.dispatchEvent(new ClipboardEvent("paste", { clipboardData: data, bubbles: true, cancelable: true }))
  })()`)
}

// Scenario: the owner attaches a report with the paperclip and pastes a screenshot (issue #343). Each is a chip
// that uploads into the chat's folder of attachments in the Bot's folder, the report under "report (2).pdf" since
// one is there; Send sends the text with both. In the chat the message shows the screenshot as a picture that
// opens large and the report as a chip that opens it in the pane. A chip removed before Send is not sent.
test("files attached by button and paste are sent and shown with the message", async ({ page }) => {
  let sent: Record<string, unknown> = {}
  const calls = await fakeServer(page, (text, body) => {
    sent = body

    return { status: 201, body: message(2, { role: "owner", text, attachments: body.attachments }) }
  })
  await open(page)
  await write(page, state(false) + kept(1, { role: "owner", text: "add a health page" }))

  await page.getByLabel("Files to attach").setInputFiles([
    { name: "report.pdf", mimeType: "application/pdf", buffer: Buffer.from("%PDF-1.7 report") },
    { name: "stray.txt", mimeType: "text/plain", buffer: Buffer.from("not this one") },
  ])
  await pasteImage(page)

  const chips = page.getByLabel("Files to send")
  await expect(chips.getByText("report.pdf")).toBeVisible()
  await expect(chips.getByText("image.png")).toBeVisible()
  await expect(chips.getByText("Uploading", { exact: false })).toHaveCount(0)
  await chips.getByRole("button", { name: "Remove stray.txt" }).click()
  await expect(chips.getByText("stray.txt")).toHaveCount(0)
  expect(calls).toContain("POST folders attachments/cht_1")
  expect(calls).toContain("POST uploads attachments/cht_1/report (2).pdf")
  expect(calls).toContain("DELETE file attachments/cht_1/stray.txt")

  const box = page.getByRole("textbox", { name: "Message" })
  await box.fill("the page works, see the report")
  await shot(page, "chat-attach-chips")
  await box.press("Enter")

  await expect
    .poll(() => sent.attachments)
    .toEqual([
      {
        name: "report.pdf",
        path: "attachments/cht_1/report (2).pdf",
        size: 15,
        type: "application/pdf",
      },
      expect.objectContaining({ name: "image.png", path: "attachments/cht_1/image.png" }),
    ])
  await expect(chips).toHaveCount(0)

  await write(
    page,
    kept(2, {
      role: "owner",
      text: "the page works, see the report",
      attachments: sent.attachments,
    }),
  )
  const log = page.getByRole("log")
  const picture = log.getByRole("button", { name: "Preview image.png" })
  await expect(picture.getByRole("img", { name: "image.png" })).toBeVisible()
  await expect(log.getByText("report.pdf")).toBeVisible()
  await settled(page)
  await shot(page, "chat-attachments-sent")

  await picture.click()
  const dialog = page.getByRole("dialog", { name: "image.png" })
  await expect(dialog.getByRole("img", { name: "image.png" })).toBeVisible()
  await settled(page)
  await shot(page, "chat-attachment-image")
  await page.keyboard.press("Escape")
  await expect(dialog).toHaveCount(0)

  await log.getByRole("button", { name: "Open report.pdf" }).click()
  // A PDF opens in the file preview (issue #422), which reads its bytes as /raw does.
  await expect.poll(() => calls).toContain("GET raw attachments/cht_1/report (2).pdf")
  await settled(page)
  await shot(page, "chat-attachment-opened")
})

// Scenario: the owner picks a file over 50 MiB. It is refused with the reason above the box and never uploaded.
test("a file over 50 MiB is refused before it uploads", async ({ page }) => {
  const calls = await fakeServer(page, (text) => ({
    status: 201,
    body: message(2, { role: "owner", text }),
  }))
  await open(page)
  await write(page, state(false))

  // A file on disk, as the owner's would be: Playwright sends no buffer over 50 MB.
  const big = test.info().outputPath("video.mp4")
  await writeFile(big, Buffer.alloc((50 << 20) + 1))
  await page.getByLabel("Files to attach").setInputFiles(big)
  await expect(page.getByRole("alert")).toHaveText("video.mp4 is larger than 50 MiB.")
  expect(calls.filter((c) => c.startsWith("POST uploads"))).toEqual([])
  await shot(page, "chat-attach-refused")
})

// Scenario: the owner chooses the chat's model and effort in the composer (issue #369). The chat starts with its
// Bot's; the model's list is searched and grouped by provider; the effort lists only the chosen model's levels, the
// Bot's first; a model that does not reason has no effort. Each choice is kept on the chat at once.
test("the owner chooses the chat's model and effort in the composer", async ({ page }) => {
  const calls = await fakeServer(page, (text) => ({
    status: 201,
    body: message(2, { role: "owner", text }),
  }))
  await open(page)
  await write(page, state(false))

  const model = page.getByRole("combobox", { name: /^Model:/ })
  await expect(model).toHaveAccessibleName("Model: gpt-sol")
  await expect(page.getByRole("button", { name: "Effort: High" })).toBeVisible()
  await shot(page, "chat-model-bots")

  await model.click()
  const search = page.getByRole("combobox", { name: "Search models" })
  await expect(page.getByRole("group").getByText("gateway")).toBeVisible()
  await shot(page, "chat-model-list")
  await search.fill("luna")
  await expect(page.getByRole("option")).toHaveCount(1)
  await page.getByRole("option", { name: /gpt-luna/ }).click()
  await expect(model).toHaveAccessibleName("Model: gpt-luna")
  await expect
    .poll(() => calls.filter((c) => c.startsWith("PATCH {")))
    .toEqual(['PATCH {"provider":"gateway","model":"gpt-luna","effort":""}'])

  await page.getByRole("button", { name: "Effort: High" }).click()
  await expect(page.getByRole("menuitemradio")).toHaveText(["Bot's: High", "None", "Low", "High"])
  await shot(page, "chat-model-effort")
  await page.getByRole("menuitemradio", { name: "Low" }).click()
  await expect(page.getByRole("button", { name: "Effort: Low" })).toBeVisible()
  await expect
    .poll(() => calls.filter((c) => c.startsWith("PATCH {")).at(-1))
    .toBe('PATCH {"provider":"gateway","model":"gpt-luna","effort":"low"}')
  await shot(page, "chat-model-chosen")

  await model.click()
  await page.getByRole("option", { name: /plain-mini/ }).click()
  await expect(model).toHaveAccessibleName("Model: plain-mini")
  await expect(page.getByRole("button", { name: /^Effort/ })).toHaveCount(0)
  await shot(page, "chat-model-no-effort")
})

const usage = (u: object) => `event: usage
data: ${JSON.stringify(u)}

`

// Scenario: the composer's context meter (issue #384). A chat pi has reported on shows a ring by the model, amber
// from 80 %; its card says how much of the window is used and where pi compacts. Compact asks the server; the chat
// works while pi compacts, and the ring says "–" until pi's next answer measures it again, then red from 90 %.
test("the context meter shows the window and compacts", async ({ page }) => {
  const calls = await fakeServer(
    page,
    (text) => ({ status: 201, body: message(2, { role: "owner", text }) }),
    noWork(),
    botFolder(),
    { ...chat, context_usage: { context_window: 128000, tokens: 105000, percent: 82.03 } },
  )
  await open(page)
  await write(page, state(false))

  const ring = page.getByRole("button", { name: "Context: 82% used" })
  await expect(ring).toBeVisible()
  await expect(ring).toHaveAttribute("data-tone", "warning")
  await ring.click()
  const card = page.getByRole("dialog")
  await expect(card.getByText("105k / 128k (82%)")).toBeVisible()
  await expect(card.getByText("pi compacts on its own at about 111,616 tokens.")).toBeVisible()
  await settled(page)
  await shot(page, "chat-context-amber")

  await card.getByRole("button", { name: "Compact" }).click()
  await expect.poll(() => calls).toContain(`POST /chats/${chat.id}/compact`)
  await write(page, state(true))
  await expect(card.getByRole("button", { name: "Compact" })).toBeDisabled()

  await write(page, usage({ context_window: 128000 }) + state(false))
  await expect(
    page.getByRole("button", { name: "Context: not measured since it was compacted" }),
  ).toHaveText("–")
  await expect(card.getByText("– / 128k")).toBeVisible()
  await expect(card.getByRole("button", { name: "Compact" })).toBeEnabled()
  await settled(page)
  await shot(page, "chat-context-compacted")

  await page.keyboard.press("Escape")
  await write(page, usage({ context_window: 128000, tokens: 120000, percent: 93.75 }))
  const full = page.getByRole("button", { name: "Context: 94% used" })
  await expect(full).toHaveAttribute("data-tone", "critical")
  await full.click()
  await expect(card.getByText("120k / 128k (94%)")).toBeVisible()
  await settled(page)
  await shot(page, "chat-context-red")
})

// Scenario: a chat pi has not reported on yet has no ring at all, as in Orca.
test("no context meter before pi reported", async ({ page }) => {
  await fakeServer(page, (text) => ({ status: 201, body: message(2, { role: "owner", text }) }))
  await open(page)
  await write(page, state(false))

  await expect(page.getByRole("combobox", { name: /^Model:/ })).toBeVisible()
  await expect(page.getByRole("button", { name: /^Context:/ })).toHaveCount(0)
})

// The tool calls of one turn as the server sends them at each level of the Bot's setting (issue #441): at full with
// their arguments, output and time; at on with the tool's name only; at off none.
const longOutput = `${"412 rows of the report\n".repeat(60)}END OF REPORT`
const fullCalls = [
  {
    role: "tool",
    text: "pi ran read: report.sql",
    parts: [
      {
        type: "tool_result",
        id: "t1",
        name: "read",
        args: { path: "report.sql" },
        output: "SELECT 1",
        duration_ms: 40,
      },
    ],
  },
  {
    role: "tool",
    text: "pi ran bash: superset export",
    parts: [
      {
        type: "tool_result",
        id: "t2",
        name: "bash",
        args: { command: "superset export --dashboard 12" },
        output: longOutput,
        duration_ms: 4200,
      },
    ],
  },
  {
    role: "tool",
    text: "pi ran bash: lark send",
    parts: [
      {
        type: "tool_result",
        id: "t3",
        name: "bash",
        args: { command: "lark send report.xlsx" },
        output: "permission denied",
        error: true,
        duration_ms: 900,
      },
    ],
  },
]
const onCalls = fullCalls.map((m) => ({
  role: "tool",
  text: `pi used ${m.parts[0]!.name}`,
  parts: m.parts.map((p) => ({
    type: p.type,
    id: p.id,
    name: p.name,
    ...("error" in p && { error: true }),
  })),
}))

async function turn(page: Page, calls: object[]) {
  await fakeServer(page, (text) => ({ status: 201, body: message(9, { role: "owner", text }) }))
  await open(page)
  await write(
    page,
    state(false) +
      kept(1, { role: "owner", text: "send the report" }) +
      calls.map((c, i) => kept(i + 2, c)).join("") +
      kept(calls.length + 2, {
        role: "coordinator",
        text: "The report is ready; Lark refused the send.",
      }),
  )
  await expect(page.getByRole("log").getByText("The report is ready")).toBeVisible()
}

// Scenario: the Bot shows its calls in full. Three calls in a row are one group, "Ran 3 tools", marked as failed
// since one did; open, each row says what it did and how long it took, the failed one in red. A row opens to its
// command and the start of its output, and Show all gives the rest.
test("tool calls in full group, open to their command and output, and show all of it", async ({
  page,
}) => {
  await turn(page, fullCalls)
  const log = page.getByRole("log")

  const group = log.getByRole("button", { name: /Ran 3 tools/ })
  await expect(group.getByLabel("failed")).toBeVisible()
  await group.click()
  await expect(log.getByRole("button", { name: /Read\s*report\.sql/ })).toContainText("40ms")
  await expect(log.getByRole("button", { name: /Ran\s*lark send report\.xlsx/ })).toContainText(
    "900ms",
  )
  await shot(page, "chat-tools-full")

  await log.getByRole("button", { name: /Ran\s*superset export/ }).click()
  await expect(log.getByLabel("Input")).toHaveText("superset export --dashboard 12")
  await expect(log.getByLabel("Output")).not.toContainText("END OF REPORT")
  await shot(page, "chat-tools-full-open")

  await log.getByRole("button", { name: /Show all/ }).click()
  await expect(log.getByLabel("Output")).toContainText("END OF REPORT")
})

// Scenario: the Bot shows its calls on: the rows name each tool, without a command, and do not open.
test("tool calls shown on name their tools and do not open", async ({ page }) => {
  await turn(page, onCalls)
  const log = page.getByRole("log")

  await log.getByRole("button", { name: /Ran 3 tools/ }).click()
  await expect(log.getByText("read", { exact: true })).toBeVisible()
  await expect(log.getByText("bash", { exact: true })).toHaveCount(2)
  await expect(log.getByText(/superset export|lark send/)).toHaveCount(0)
  await expect(log.getByRole("button", { name: /^bash/ })).toHaveCount(0)
  await shot(page, "chat-tools-on")
})

// Scenario: the Bot shows no calls: the server sent none, and the chat has the question and the answer only.
test("tool calls shown off leave only the answers", async ({ page }) => {
  await turn(page, [])
  const log = page.getByRole("log")

  await expect(log.getByRole("button", { name: /Ran \d+ tools?/ })).toHaveCount(0)
  await expect(log.getByText("send the report")).toBeVisible()
  await shot(page, "chat-tools-off")
})
