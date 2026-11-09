import { expect, test, type Page } from "@playwright/test"

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

async function fakeServer(page: Page) {
  const answers: unknown[] = []

  await page.addInitScript({ content: "localStorage.setItem('hypomnema.token', 'secret')" })
  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    switch (path) {
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
  await first.getByRole("link").click()

  await expect(page.getByRole("region", { name: "Tasks" })).toContainText("Waiting for you")
  await page.getByText("Public", { exact: true }).click()
  await page.getByRole("button", { name: "Send answer" }).click()
  await expect.poll(() => answers).toEqual([{ option: 0 }])
})
