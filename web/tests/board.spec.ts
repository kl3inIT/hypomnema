import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The board, a Task's panel and the Runs pages, built, against a fake client API, on a phone and a desktop.

const now = new Date().toISOString()

const run = {
  id: "run_1",
  objective: "Add a /health page to the control plane",
  status: "open",
  created_by: "pi",
  repo: "kl3inIT/hypomnema",
  created_at: now,
  updated_at: now,
}

const task = (id: string, position: number, spec: string, m: object = {}) => ({
  id,
  run_id: run.id,
  position,
  profile: "implementer",
  runtime: "claude",
  spec,
  status: "running",
  failure_count: 0,
  created_at: now,
  updated_at: now,
  ...m,
})

const tasks = [
  task("tsk_1", 1, "Add the /health handler and its test", {
    status: "succeeded",
    latest_dispatch: {
      id: "dsp_1",
      task_id: "tsk_1",
      node_id: "nod_1",
      status: "succeeded",
      epoch: 1,
      created_at: now,
      updated_at: now,
      result: {
        summary: "GET /health returns the version.",
        evidence: [{ kind: "pull_request", ref: "https://github.com/kl3inIT/hypomnema/pull/63" }],
      },
    },
  }),
  task("tsk_2", 2, "Decide whether /health needs a login"),
  task("tsk_3", 3, "Update ARCHITECTURE.md", { status: "pending" }),
  task("tsk_4", 4, "Cover /health with an end-to-end test", {
    latest_dispatch: {
      id: "dsp_4",
      task_id: "tsk_4",
      node_id: "nod_1",
      status: "running",
      epoch: 1,
      created_at: now,
      updated_at: now,
      started_at: now,
      model: "gpt-sol",
      fallback_for: "implementer",
      fallback_until: new Date(Date.now() + 95 * 60_000).toISOString(),
      activity: "Bash: go test ./internal/e2e/",
    },
  }),
]

const item = (seq: number, m: object) => ({ seq, kind: "message", created_at: now, ...m })

// What the worker of tsk_4 said and did so far.
const transcript = [
  item(1, { text: "I will look at the existing end-to-end tests first." }),
  item(2, { kind: "tool_call", call_id: "c1", tool: "Grep", input: { pattern: "TestNode" } }),
  item(3, { kind: "tool_result", call_id: "c1", text: "internal/e2e/node_test.go:264" }),
  item(4, {
    kind: "tool_call",
    call_id: "c2",
    tool: "Read",
    input: { file_path: "internal/e2e/node_test.go" },
  }),
  item(5, { kind: "tool_result", call_id: "c2", text: "package e2e" }),
  item(6, {
    kind: "tool_call",
    call_id: "c3",
    tool: "Edit",
    input: {
      file_path: "internal/e2e/health_test.go",
      old_string: "t.Skip()",
      new_string: 'resp := get(t, "/health")\nrequire.Equal(t, 200, resp.StatusCode)',
    },
  }),
  item(7, { kind: "tool_result", call_id: "c3", text: "edited" }),
  item(8, {
    kind: "tool_call",
    call_id: "c4",
    tool: "Bash",
    input: { command: "go test ./internal/e2e/" },
  }),
  item(9, { kind: "tool_result", call_id: "c4", text: "FAIL TestHealth: got 404", failed: true }),
  item(10, {
    kind: "tool_call",
    call_id: "c5",
    tool: "Bash",
    input: { command: "CGO_ENABLED=0 go build ./cmd/hypomnema" },
  }),
]

const asked = {
  id: "qst_1",
  dispatch_id: "dsp_2",
  task_id: "tsk_2",
  run_id: run.id,
  mode: "wait",
  addressed_to: "owner",
  text: "Should /health be public or behind a login?",
  options: [{ label: "Public" }, { label: "Behind a login" }],
  status: "open",
  created_at: now,
}

// What the running worker asked to do with the owner's Google account, held for his approval.
// The answers the owner gave to it, as the fake server received them.
const decisions: unknown[] = []

const held = {
  id: "apr_1",
  integration: "google",
  label: "work",
  action: "drive.share",
  action_name: "Share a file",
  asked_by: "worker",
  dispatch_id: "dsp_4",
  task_id: "tsk_4",
  run_id: run.id,
  title: "Share a file: Release notes v0.6.docx",
  payload: [
    { label: "emailAddress", value: "team@example.com" },
    { label: "role", value: "commenter" },
    { label: "type", value: "group" },
  ],
  status: "pending",
  used: false,
  created_at: new Date().toISOString(),
}

async function fakeServer(page: Page, start: { approval?: boolean } = {}) {
  let approvals = start.approval ? [held] : []
  decisions.length = 0
  const answers: unknown[] = []

  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    switch (path) {
      case "/approvals": {
        const task = url.searchParams.get("task_id")

        return json({ data: approvals.filter((a) => !task || a.task_id === task) })
      }
      case `/approvals/${held.id}/decision`:
        decisions.push(route.request().postDataJSON())
        approvals = []

        return json({ ...held, status: "approved", scope: "session" })
      case "/me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "/chats":
        return json({ data: [] })
      case "/tasks":
        return json({ data: tasks, runs: [run], after: 5 })
      case "/questions":
        return json({ data: answers.length ? [] : [asked] })
      case "/nodes":
        return json({ data: [{ id: "nod_1", name: "aioffice-app" }] })
      case "/profiles":
        return json({ data: [] })
      case "/runs":
        return json({ data: [run] })
      case `/runs/${run.id}`:
        return json({ ...run, tasks })
      case "/dispatches/dsp_4/items":
        if (url.searchParams.has("before")) return json({ data: transcript, has_more: false })
        // Nothing follows: the wait ends empty.
        await new Promise((resolve) => setTimeout(resolve, 2000))
        return json({ data: [], has_more: false })
      case "/events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case `/questions/${asked.id}/answer`:
        answers.push(route.request().postDataJSON())
        return json({ ...asked, status: "answered" })
      default:
        return json({ code: "not_found", message: "not found" }, 404)
    }
  })

  return answers
}

// Scenario: the board shows what waits for the owner first; the owner opens the Task, reads the whole question,
// answers it, and Escape returns to the board.
test("the owner answers a worker from the board", async ({ page }) => {
  const answers = await fakeServer(page)
  await page.goto("/board")

  const needsYou = page.getByRole("region", { name: "Needs you" })
  await expect(needsYou.getByTestId("task-card")).toHaveCount(1)
  await expect(needsYou).toContainText("Should /health be public")
  await expect(page.getByRole("region", { name: "Done" })).toContainText("PR #63")
  await shot(page, "board")

  await needsYou.getByTestId("task-card").click()
  const panel = page.getByRole("dialog")
  await expect(
    panel.getByRole("heading", { name: "Decide whether /health needs a login" }),
  ).toBeVisible()
  await expect(page).toHaveURL(/task=tsk_2/)

  await panel.getByText("Behind a login", { exact: true }).click()
  await expect(panel.getByRole("radio", { name: "Behind a login" })).toBeChecked()
  await panel.getByRole("button", { name: "Send answer" }).click()
  await expect.poll(() => answers).toEqual([{ option: 1 }])

  await page.keyboard.press("Escape")
  await expect(panel).toBeHidden()
  await expect(page).not.toHaveURL(/task=/)
})

// Scenario: the Runs list puts a Run whose worker asks first, and the Run's page lets the owner answer in place.
test("a Run that asks comes first and is answered on its page", async ({ page }) => {
  const answers = await fakeServer(page)
  await page.goto("/runs")

  const first = page.getByRole("region", { name: "Needs you" })
  await expect(first).toContainText("Add a /health page")
  await shot(page, "runs")
  await first.getByRole("link").click()
  await page.waitForURL(/\/runs\/.+/)
  await expect(page.getByRole("button", { name: "Finish Run" })).toBeVisible()
  await shot(page, "run")

  await expect(page.getByRole("region", { name: "Tasks" })).toContainText("Waiting for you")
  await page.getByText("Public", { exact: true }).click()
  await page.getByRole("button", { name: "Send answer" }).click()
  await expect.poll(() => answers).toEqual([{ option: 0 }])
})

// Scenario: a running worker asked to share a file from the owner's Drive, and the request is held. Its Task
// shows what would be shared and with whom, above the transcript. The owner approves sharing for the rest of the
// Run, and the card goes.
test("the owner approves what a worker asked to do with an account", async ({ page }) => {
  await fakeServer(page, { approval: true })
  await page.goto("/board")

  await page
    .getByRole("region", { name: "Working" })
    .getByTestId("task-card")
    .filter({ hasText: "Cover /health" })
    .click()

  const panel = page.getByRole("dialog")
  const card = panel.getByRole("region", {
    name: "Approval needed: Share a file: Release notes v0.6.docx",
  })
  await expect(card).toContainText("team@example.com")
  await expect(card).toContainText("google:work · Share a file · asked by worker")
  expect(await page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")).toBe(
    true,
  )
  await shot(page, "task-approval")

  await card.getByRole("button", { name: "Approve for this Run" }).click()
  await expect(card).toHaveCount(0)
  expect(decisions).toEqual([{ decision: "approve_session" }])
})

// Scenario: a worker is running. Its card says what it does now; its Task opens at the transcript, where its
// last steps are in view, and a failed step opens to what it printed.
test("a running worker's transcript is read from its card", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/board")

  const working = page.getByRole("region", { name: "Working" })
  await expect(working).toContainText("Bash: go test ./internal/e2e/")

  await working.getByTestId("task-card").filter({ hasText: "Cover /health" }).click()
  const log = page.getByRole("dialog").getByRole("log", { name: "Transcript" })
  await expect(log).toContainText("I will look at the existing end-to-end tests first.")
  await shot(page, "task-panel")
  await expect(log.getByRole("button", { name: /Read 1 file, ran 1 search/ })).toBeVisible()
  await expect(
    log.getByRole("button", { name: /Edit internal\/e2e\/health_test\.go/ }),
  ).toContainText("+2 -1")

  await log.getByRole("button", { name: /go test \.\/internal\/e2e\// }).click()
  await expect(log).toContainText("FAIL TestHealth: got 404")
})
