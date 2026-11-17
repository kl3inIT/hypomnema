import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, test, vi } from "vitest"

import type { RunDetail, TaskDetail } from "@/api/generated/types.gen"

import { DockedRuns } from "./docked-work"
import type { DockedWork } from "./use-docked-work"

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] })
  vi.setSystemTime(new Date("2026-11-17T10:03:12Z"))
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

const task = (id: string, status: TaskDetail["status"], more: Partial<TaskDetail> = {}) =>
  ({
    id,
    run_id: "run_1",
    position: 1,
    profile: "implementer",
    spec: id,
    status,
    failure_count: 0,
    created_at: "2026-11-17T10:00:00Z",
    updated_at: "2026-11-17T10:00:00Z",
    ...more,
  }) as TaskDetail

const running = task("Add the /health handler", "running", {
  id: "tsk_1",
  latest_dispatch: {
    id: "dsp_1",
    task_id: "tsk_1",
    node_id: "n1",
    status: "running",
    epoch: 1,
    created_at: "2026-11-17T10:00:00Z",
    updated_at: "2026-11-17T10:03:00Z",
    started_at: "2026-11-17T10:00:00Z",
    progress_at: "2026-11-17T10:03:00Z",
    activity: "Bash: go test ./...",
  },
})
const stopping = task("Write the release notes", "running", {
  id: "tsk_2",
  position: 2,
  cancel_requested_at: "2026-11-17T10:03:08Z",
})
const queued = task("Update the README", "ready", {
  id: "tsk_3",
  position: 3,
  waiting_reason: "nodes_full",
})

// The server: the profiles and Bots that name a row's worker, and the Tasks the owner cancels.
function server() {
  const canceled: string[] = []

  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    const url = new URL(request.url)
    const path = url.pathname.replace("/api/v1", "")

    if (request.method === "POST" && path.endsWith("/cancel")) {
      canceled.push(path)

      return json({ ...running, cancel_requested_at: new Date().toISOString() })
    }

    return json({ data: [] })
  })

  return canceled
}

function show(tasks: TaskDetail[]) {
  const run: RunDetail = {
    id: "run_1",
    objective: "A health page",
    status: "open",
    created_by: "pi",
    chat_id: "cht_1",
    created_at: "2026-11-17T10:00:00Z",
    updated_at: "2026-11-17T10:00:00Z",
    tasks,
  }
  const work: DockedWork = {
    runs: [run],
    questions: [],
    open: { run_1: true },
    setOpen: () => {},
    dock: { docked: () => true, show: () => {}, task: () => undefined },
  }
  const root = createRootRoute({ component: () => <DockedRuns chatId="cht_1" work={work} /> })
  const router = createRouter({
    routeTree: root,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

// Scenario: the owner follows a Run from its chat (issue #687). The running Task's row says how long its worker
// runs and what it does now, the strip's head says the same newest step, a Task asked to stop says so with how
// long since, and a queued Task still says why it waits.
test("a docked Run says what its running Tasks do and which one stops", async () => {
  server()
  show([running, stopping, queued])

  const head = await screen.findByRole("button", { name: /^A health page: Working/ })
  expect(head).toHaveTextContent("Bash: go test ./...")

  const row = screen.getByRole("button", { name: /^Open Task Add the \/health handler/ })
  expect(row).toHaveTextContent("Running 3m 12s · Bash: go test ./...")
  expect(screen.getByText("Running 3m 12s · Bash: go test ./...")).toHaveAttribute(
    "title",
    "Running 3m 12s · Bash: go test ./...",
  )

  const stops = screen.getByRole("button", { name: /^Open Task Write the release notes/ })
  expect(stops).toHaveTextContent("Stopping · 4s")
  expect(stops).toHaveTextContent("Stopping")
  expect(screen.queryByRole("button", { name: "Stop Task Write the release notes" })).toBeNull()

  expect(screen.getByRole("button", { name: /^Open Task Update the README/ })).toHaveTextContent(
    "Waiting: every machine that can run it is busy",
  )
})

// Scenario: the owner stops a running Task from its row. He is asked first; keeping it sends nothing, and
// confirming cancels that Task as the panel's Cancel Task does.
test("Stop on a Task's row asks first, then cancels the Task", async () => {
  const user = userEvent.setup()
  const canceled = server()
  show([running, queued])

  await user.click(await screen.findByRole("button", { name: "Stop Task Add the /health handler" }))
  let dialog = await screen.findByRole("alertdialog")
  expect(dialog).toHaveTextContent('Stop the Task "Add the /health handler"?')
  expect(dialog).toHaveTextContent("Its worker is stopped and the Task is canceled.")
  await user.click(within(dialog).getByRole("button", { name: "Keep it running" }))
  expect(canceled).toEqual([])

  await user.click(screen.getByRole("button", { name: "Stop Task Add the /health handler" }))
  dialog = await screen.findByRole("alertdialog")
  await user.click(within(dialog).getByRole("button", { name: "Stop Task" }))
  await vi.waitFor(() => expect(canceled).toEqual(["/tasks/tsk_1/cancel"]))

  expect(screen.getByRole("button", { name: "Stop Task Update the README" })).toBeInTheDocument()
})
