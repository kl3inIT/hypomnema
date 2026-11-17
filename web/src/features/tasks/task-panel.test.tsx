import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router"
import { render, screen } from "@testing-library/react"
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
  spec: "Add the /health handler",
  status: "running",
  failure_count: 0,
  created_at: "2026-11-17T10:00:00Z",
  updated_at: "2026-11-17T10:00:00Z",
  ...more,
})

function show(t: TaskDetail) {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = new URL((input as Request).url).pathname.replace("/api/v1", "")

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
