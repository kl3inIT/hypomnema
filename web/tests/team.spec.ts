import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The Team page, built, against a fake client API, on a phone and a desktop: the Bots, the machines their work
// runs on, the links between them, and who hands work to whom.

const ago = (seconds: number) => new Date(Date.now() - seconds * 1000).toISOString()

const bot = (name: string, model?: string) => ({
  name,
  model,
  persona: "",
  memory: "",
  memory_limit: 2200,
  skills: [],
  skills_off: [],
  connections: { limited: false, names: [] },
  workers: { limited: false, names: [] },
  created_at: ago(86400),
  updated_at: ago(86400),
})

const bots = [
  bot("coordinator", "gpt-6.1-sol"),
  bot("key-check", "claude-sonnet-5-5"),
  bot("researcher"),
  bot("reporter", "claude-opus-5-5"),
]

const chat = (id: string, name: string, title: string, m: object = {}) => ({
  id,
  title,
  bot: name,
  working: false,
  coordinator: true,
  created_at: ago(3600),
  updated_at: ago(600),
  ...m,
})

const chats = [
  chat("cht_1", "coordinator", "Add a /health page", { working: true, updated_at: ago(30) }),
  chat("cht_2", "key-check", "Rotate the staging key"),
  chat("cht_3", "researcher", "Read the release notes", { coordinator: false }),
  chat("cht_4", "reporter", "Weekly usage summary", { updated_at: ago(7200) }),
]

const run = (id: string, objective: string, m: object = {}) => ({
  id,
  objective,
  status: "open",
  created_by: "pi",
  repo: "kl3inIT/hypomnema",
  created_at: ago(900),
  updated_at: ago(900),
  ...m,
})

const runs = [
  run("run_1", "Add a /health page to the control plane", { bot: "coordinator", chat_id: "cht_1" }),
  run("run_2", "Fix the header layout", {
    bot: "coordinator",
    chat_id: "cht_1",
    created_at: ago(400),
  }),
  run("run_3", "Weekly usage summary", {
    bot: "reporter",
    chat_id: "cht_4",
    status: "finished",
    created_at: ago(9000),
  }),
  // A Run made from the command line belongs to no Bot: its work shows on its machine, with no link.
  run("run_4", "Decide the /health access", { created_at: ago(300) }),
]

const dispatch = (id: string, task: string, node: string, m: object = {}) => ({
  id,
  task_id: task,
  node_id: node,
  status: "running",
  epoch: 1,
  created_at: ago(100),
  updated_at: ago(10),
  started_at: ago(94),
  ...m,
})

const task = (id: string, runId: string, spec: string, m: object = {}) => ({
  id,
  run_id: runId,
  position: 1,
  profile: "implementer",
  runtime: "claude",
  spec,
  status: "running",
  failure_count: 0,
  created_at: ago(300),
  updated_at: ago(10),
  ...m,
})

const tasks = [
  task("tsk_1", "run_1", "Cover /health with an end-to-end test", {
    latest_dispatch: dispatch("dsp_1", "tsk_1", "nod_1", {
      activity: "Bash: go test ./internal/e2e/",
    }),
  }),
  task("tsk_2", "run_2", "Move the counts into the View menu", {
    profile: "codex-implementer",
    runtime: "codex",
    latest_dispatch: dispatch("dsp_2", "tsk_2", "nod_3", {
      started_at: ago(362),
      activity: "Edit web/src/features/machines/columns-menu.tsx",
    }),
  }),
  task("tsk_3", "run_4", "Decide whether /health needs a login", {
    latest_dispatch: dispatch("dsp_3", "tsk_3", "nod_3", { waiting_since: ago(240) }),
  }),
  task("tsk_4", "run_3", "Summarise the week's usage", {
    profile: "explorer",
    status: "succeeded",
    finished_at: ago(7200),
    latest_dispatch: dispatch("dsp_4", "tsk_4", "nod_2", { status: "succeeded" }),
  }),
]

const asked = {
  id: "qst_1",
  dispatch_id: "dsp_3",
  task_id: "tsk_3",
  run_id: "run_4",
  mode: "wait",
  addressed_to: "owner",
  text: "Should /health be public or behind a login?",
  options: [{ label: "Public" }, { label: "Behind a login" }],
  status: "open",
  created_at: ago(240),
}

const approval = {
  id: "apr_1",
  integration: "google",
  label: "work",
  action: "drive.share",
  action_name: "Share a file",
  asked_by: "pi",
  chat_id: "cht_2",
  title: "Share a file: staging.env",
  payload: [],
  status: "pending",
  used: false,
  created_at: ago(120),
}

const node = (id: string, name: string, m: object = {}) => ({
  id,
  name,
  status: "active",
  max_dispatches: 2,
  runtimes: [],
  connected: true,
  active: 0,
  created_at: ago(86400 * 10),
  last_seen_at: ago(5),
  ...m,
})

const nodes = [
  node("nod_1", "kdlstc-dev", { active: 1 }),
  node("nod_2", "aioffice-app"),
  node("nod_3", "stc-hy-app", { active: 2 }),
  node("nod_4", "gpu-box", { status: "drained" }),
  node("nod_5", "elitex", { connected: false, last_seen_at: ago(2 * 86400) }),
]

const item = (seq: number, m: object) => ({ seq, kind: "message", created_at: ago(60), ...m })

const transcript = [
  item(1, { text: "I will look at the existing end-to-end tests first." }),
  item(2, {
    kind: "tool_call",
    call_id: "c1",
    tool: "Bash",
    input: { command: "go test ./internal/e2e/" },
  }),
]

async function fakeServer(page: Page, start: { bots?: object[] } = {}) {
  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    switch (path) {
      case "/me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "/bots":
        return json({ data: start.bots ?? bots })
      case "/chats":
        return json({ data: start.bots ? [] : chats })
      case "/approvals":
        return json({ data: url.searchParams.get("task_id") ? [] : [approval] })
      case "/tasks":
        return json({ data: tasks, runs, after: 5 })
      case "/questions":
        return json({ data: [asked] })
      case "/nodes":
        return json({ data: nodes })
      case "/profiles":
        return json({ data: [] })
      case "/runs":
        return json({ data: runs })
      case "/dispatches/dsp_1/items":
        if (url.searchParams.has("before")) return json({ data: transcript, has_more: false })
        await new Promise((resolve) => setTimeout(resolve, 2000))
        return json({ data: [], has_more: false })
      case "/events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      default:
        return json({ code: "not_found", message: "not found" }, 404)
    }
  })
}

// Scenario: the owner opens the Team page. Each Bot says in words what it is doing and why; each machine lists
// the work it runs; a live link joins a Bot to the machines its work runs on; under the map, the question that
// waits for the owner comes first, then who hands work to whom.
test("the Team page shows the Bots, their machines and who hands work to whom", async ({
  page,
}) => {
  await fakeServer(page)
  await page.goto("/team")

  await expect(page.getByRole("heading", { name: "Team", level: 1 })).toBeVisible()
  const map = page.getByRole("application", { name: "Team map" })
  const card = (name: string) => map.getByTestId("team-bot").filter({ hasText: name })
  await expect(card("coordinator")).toContainText("Working")
  await expect(card("coordinator")).toContainText("2 Tasks running on 2 machines")
  await expect(card("key-check")).toContainText("Waiting for you")
  await expect(card("key-check")).toContainText("An action waits for your approval")
  await expect(card("researcher")).toContainText("No signal")
  await expect(card("reporter")).not.toContainText(/Working|Waiting|No signal/)

  const machine = (name: string) => map.getByTestId("team-machine").filter({ hasText: name })
  await expect(machine("kdlstc-dev")).toContainText("1 of 2")
  await expect(machine("kdlstc-dev")).toContainText("Bash: go test ./internal/e2e/")
  await expect(machine("stc-hy-app")).toContainText("Waiting for an answer · 4m")
  await expect(machine("aioffice-app")).toContainText("–")
  await expect(machine("gpu-box")).toContainText("Drained")
  await expect(map.getByTestId("team-link")).toHaveCount(2)

  await shot(page, "team")

  // Under the map, closed at first as OpenMausBot's, the question to the owner comes first.
  await page.getByText("Who hands work to whom · 4").click()
  const list = page.getByRole("list", { name: "Who hands work to whom" })
  const rows = list.getByTestId("team-handoff")
  await expect(rows.nth(0)).toContainText("Needs you")
  await expect(rows.nth(0)).toContainText("implementer asks:")
  await expect(rows.nth(1)).toContainText("Running")
  await expect(rows.filter({ hasText: "reporter" })).toContainText("Done")
  await expect(rows.filter({ hasText: "codex-implementer" })).toContainText(/6m 0\ds/)
  expect(await page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")).toBe(
    true,
  )
  await shot(page, "team-list")

  // The toolbar zooms and fits, never past 100%.
  await page.getByRole("button", { name: "Zoom in" }).click()
  await page.getByRole("button", { name: "Fit", exact: true }).click()
  const percent = Number(
    (await page.getByRole("button", { name: "Fit to view" }).innerText()).replace("%", ""),
  )
  expect(percent).toBeLessThanOrEqual(100)
})

// Scenario: a Task's line on a machine opens its panel over the map, the other cards fade, and Escape returns.
test("a Task on a machine opens its panel over the map", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/team")

  await page.getByTestId("team-work").filter({ hasText: "Cover /health" }).click()
  await expect(page).toHaveURL(/task=tsk_1/)
  const panel = page.getByRole("dialog")
  await expect(
    panel.getByRole("heading", { name: "Cover /health with an end-to-end test" }),
  ).toBeVisible()
  await expect(panel.getByRole("log", { name: "Transcript" })).toContainText(
    "I will look at the existing end-to-end tests first.",
  )
  // Its machine comes into view beside the panel, without zooming.
  const machine = page.getByTestId("team-machine").filter({ hasText: "kdlstc-dev" })
  await expect
    .poll(async () => {
      const card = await machine.boundingBox()
      const side = await panel.boundingBox()

      return !!card && !!side && (side.x < 200 || card.x + card.width <= side.x)
    })
    .toBe(true)
  await shot(page, "team-task-panel")

  await page.keyboard.press("Escape")
  await expect(panel).toBeHidden()
  await expect(page).not.toHaveURL(/task=/)
})

// Scenario: a Bot opens the chat that waits for the owner; a Bot with no chat opens a new chat with it.
test("a Bot opens its chat", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/team")

  await page.getByTestId("team-bot").filter({ hasText: "key-check" }).click()
  await expect(page).toHaveURL(/\/chats\/cht_2$/)
})

// Scenario: with no Bots, the page says so.
test("the Team page with no Bots", async ({ page }) => {
  await fakeServer(page, { bots: [] })
  await page.goto("/team")

  await expect(page.getByText("No Bots yet")).toBeVisible()
})
