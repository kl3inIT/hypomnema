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

async function fakeServer(
  page: Page,
  send: (text: string) => { status: number; body: unknown },
  work: Work = noWork(),
) {
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
