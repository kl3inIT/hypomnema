import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router"
import { render, screen } from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"

import type { ChatWake, TaskDetail } from "@/api/generated/types.gen"

import { DockContext } from "./dock-context"
import { WakeCard } from "./wake-card"

afterEach(() => vi.restoreAllMocks())

const json = (body: unknown) =>
  new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } })

const finished: TaskDetail = {
  id: "tsk_1",
  run_id: "run_1",
  position: 1,
  profile: "implementer",
  spec: "Add the /health handler",
  status: "succeeded",
  failure_count: 0,
  created_at: "2026-11-17T10:00:00Z",
  updated_at: "2026-11-17T10:05:00Z",
  latest_dispatch: {
    id: "dsp_1",
    task_id: "tsk_1",
    node_id: "n1",
    status: "succeeded",
    epoch: 1,
    created_at: "2026-11-17T10:00:00Z",
    updated_at: "2026-11-17T10:05:00Z",
    started_at: "2026-11-17T10:00:00Z",
    settled_at: "2026-11-17T10:05:00Z",
    result: {
      summary: "Added GET /health with a test, which returns 200 and the version.\nMore below.",
      evidence: [{ kind: "pull_request", ref: "kl3inIT/hypomnema#700" }],
    },
  },
}

// Scenario: a worker finished while the owner was in the chat (issue #687). Its wake-up card carries the first
// line of the result, read from the Task the chat's dock loaded with its Run, cut to the card's width with the
// whole line kept for a pointer and a screen reader, the Task's chip that opens its panel, and its pull request.
test("a finished Task's wake-up card shows its result and pull request", async () => {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = new URL((input as Request).url).pathname.replace("/api/v1", "")

    return json(path === "/tasks" ? { data: [], runs: [], after: 0 } : { data: [] })
  })
  const wake: ChatWake = {
    event: "task.status",
    run_id: "run_1",
    task_id: "tsk_1",
    status: "succeeded",
  }
  const dock = {
    docked: () => true,
    show: () => {},
    task: (id: string) => (id === "tsk_1" ? finished : undefined),
  }
  const root = createRootRoute({
    component: () => (
      <DockContext value={dock}>
        <WakeCard wake={wake} at="2026-11-17T10:05:01Z" />
      </DockContext>
    ),
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

  const result = "Added GET /health with a test, which returns 200 and the version."
  const card = await screen.findByRole("note")
  expect(card).toHaveAccessibleName(`Task Add the /health handler done: ${result}`)
  expect(screen.getByText(result)).toHaveAttribute("title", result)
  expect(screen.getByText(result)).toHaveClass("truncate")
  expect(screen.getByRole("button", { name: "Open Task Add the /health handler" })).toBeVisible()
  expect(screen.getByRole("link", { name: "Open PR #700" })).toHaveAttribute(
    "href",
    "https://github.com/kl3inIT/hypomnema/pull/700",
  )
})
