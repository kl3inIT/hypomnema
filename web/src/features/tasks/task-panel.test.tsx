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

import type { Run, TaskDetail } from "@/api/generated/types.gen"

import { TaskPanel } from "./task-panel"

const json = (body: unknown) =>
  new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } })

beforeEach(() => {
  // The panel follows its transcript with use-stick-to-bottom, which watches sizes jsdom does not have.
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  )
  vi.useFakeTimers({ toFake: ["Date"] })
  vi.setSystemTime(new Date("2026-11-17T10:03:12Z"))
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const run: Run = {
  id: "run_1",
  objective: "A health page",
  status: "open",
  created_by: "pi",
  created_at: "2026-11-17T10:00:00Z",
  updated_at: "2026-11-17T10:00:00Z",
}

const task = (more: Partial<TaskDetail>): TaskDetail => ({
  id: "tsk_1",
  run_id: "run_1",
  position: 1,
  profile: "implementer",
  kind: "narrow",
  spec: "Add the /health handler",
  status: "running",
  failure_count: 0,
  created_at: "2026-11-17T10:00:00Z",
  updated_at: "2026-11-17T10:00:00Z",
  ...more,
})

// show opens the panel on one Task and returns the cancels the server was sent.
function show(t: TaskDetail) {
  const canceled: string[] = []
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    const path = new URL(request.url).pathname.replace("/api/v1", "")
    if (request.method === "POST" && path.endsWith("/cancel")) {
      canceled.push(path)

      return json({ ...t, cancel_requested_at: new Date().toISOString() })
    }

    return json(path === "/tasks" ? { data: [t], runs: [run], after: 0 } : { data: [] })
  })
  const root = createRootRoute({
    component: () => <TaskPanel taskId="tsk_1" onClose={() => {}} />,
  })
  const router = createRouter({
    routeTree: root,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  })

  render(
    <QueryClientProvider client={new QueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )

  return canceled
}

// Scenario: the owner stopped a Task (issue #687). Its panel no longer offers Cancel Task and says it is stopping,
// with how long since he asked, until the Task ends; a Task not asked to stop still offers Cancel Task.
test("a Task asked to stop says so in its panel instead of offering Cancel Task", async () => {
  show(task({ cancel_requested_at: "2026-11-17T10:03:08Z" }))

  expect(await screen.findByText("Stopping · 4s")).toBeVisible()
  expect(screen.queryByRole("button", { name: "Cancel Task" })).toBeNull()
})

test("a running Task not asked to stop offers Cancel Task", async () => {
  show(task({}))

  expect(await screen.findByRole("button", { name: "Cancel Task" })).toBeVisible()
  expect(screen.queryByText(/^Stopping/)).toBeNull()
})

// Scenario: the owner cancels a running Task from its panel (issue #695). He is asked first, as Stop on its row
// asks; keeping it sends nothing, and confirming cancels the Task.
test("Cancel Task asks first; keeping it cancels nothing, confirming cancels the Task", async () => {
  const user = userEvent.setup()
  const canceled = show(task({}))

  await user.click(await screen.findByRole("button", { name: "Cancel Task" }))
  let dialog = await screen.findByRole("alertdialog")
  expect(dialog).toHaveTextContent('Cancel the Task "Add the /health handler"?')
  expect(dialog).toHaveTextContent("Its worker is stopped and the Task is canceled.")
  expect(canceled).toEqual([])

  await user.click(within(dialog).getByRole("button", { name: "Keep it running" }))
  await vi.waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
  expect(canceled).toEqual([])

  await user.click(screen.getByRole("button", { name: "Cancel Task" }))
  dialog = await screen.findByRole("alertdialog")
  await user.click(within(dialog).getByRole("button", { name: "Cancel Task" }))
  await vi.waitFor(() => expect(canceled).toEqual(["/tasks/tsk_1/cancel"]))
})

test("a Task not yet running's Cancel Task offers to keep it queued", async () => {
  const user = userEvent.setup()
  show(task({ status: "ready" }))

  await user.click(await screen.findByRole("button", { name: "Cancel Task" }))
  const dialog = await screen.findByRole("alertdialog")
  expect(dialog).toHaveTextContent("The Task is canceled before a worker starts on it.")
  expect(within(dialog).getByRole("button", { name: "Keep it queued" })).toBeVisible()
})
