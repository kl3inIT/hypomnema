import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import type { Routine, RoutineRun } from "@/api/generated/types.gen"
import { Toaster } from "@/components/ui/toast"

import { ScanNow } from "./scan-now"

afterEach(() => vi.restoreAllMocks())

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })

const minutes = (n: number) => new Date(Date.now() + n * 60_000).toISOString()

const run = (id: string, m: Partial<RoutineRun> = {}): RoutineRun => ({
  id,
  routine_id: "rtn_triage",
  started_by: "schedule",
  state: "succeeded",
  started_at: minutes(-13),
  ended_at: minutes(-12),
  output: "",
  error: "",
  ...m,
})

const triage = (m: Partial<Routine> = {}): Routine => ({
  id: "rtn_triage",
  bot: "bot_main",
  name: "triage",
  kind: "prompt",
  access: "read",
  every_minutes: 15,
  schedule: "every 15 minutes",
  sentence: "Every 15 minutes, Thư ký reads the new messages.",
  timeout_seconds: 300,
  state: "on",
  announce: false,
  failures: 0,
  skipped: 0,
  can_undo: false,
  created_by: "owner",
  created_at: minutes(-3000),
  updated_at: minutes(-3000),
  grants: [],
  last_run: run("rrn_1"),
  ...m,
})

// server is the control plane: the main Bot, its Routines, and a run started by hand that goes until end() is called.
// Every call that is not a read is kept.
function server(routines: Routine[]) {
  let list = routines
  const calls: string[] = []

  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    const path = new URL(request.url).pathname.replace("/api/v1", "")
    if (request.method !== "GET") calls.push(`${request.method} ${path}`)

    if (path === "/bots") return json({ data: [{ id: "bot_main", name: "Thư ký", main: true }] })
    if (path === "/routines") return json({ data: list })
    if (path === "/routines/rtn_triage/runs") {
      const begun = run("rrn_2", {
        started_by: "manual",
        state: "running",
        started_at: minutes(0),
        ended_at: undefined,
      })
      list = list.map((r) => ({ ...r, last_run: begun }))

      return json(begun)
    }

    return json({ data: [] })
  })

  return {
    calls,
    end() {
      list = list.map((r) => ({
        ...r,
        last_run: { ...r.last_run!, state: "succeeded" as const, ended_at: minutes(0) },
      }))
    },
  }
}

function show() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={queryClient}>
      <ScanNow now={Date.now()} />
      <Toaster />
    </QueryClientProvider>,
  )

  return queryClient
}

// Scenario: the owner presses Scan now. The button runs the triage once, says Scanning… and is disabled while the
// run goes; when the run ends, it comes back with the new time, and the Inbox's lists are read again.
test("Scan now runs the triage, waits for it, then shows the new time", async () => {
  const s = server([triage()])
  const queryClient = show()

  expect(await screen.findByText(/^Read new messages 12/)).toBeInTheDocument()
  await userEvent.click(screen.getByRole("button", { name: "Scan now" }))

  expect(await screen.findByRole("button", { name: "Scanning…" })).toBeDisabled()
  expect(screen.getByText("Reading new messages…")).toBeInTheDocument()
  expect(s.calls).toEqual(["POST /routines/rtn_triage/runs"])

  const reads = vi.spyOn(queryClient, "invalidateQueries")
  s.end()
  await act(() => queryClient.refetchQueries())

  await waitFor(() => expect(screen.getByRole("button", { name: "Scan now" })).toBeEnabled())
  expect(screen.getByText("Read new messages just now")).toBeInTheDocument()
  expect(reads).toHaveBeenCalled()
  expect(s.calls).toEqual(["POST /routines/rtn_triage/runs"])
})

// Scenario: a scheduled run is going when the owner opens the Inbox; the button shows it rather than starting a
// second one.
test("a run already going shows as Scanning…", async () => {
  server([triage({ last_run: run("rrn_1", { state: "running", ended_at: undefined }) })])
  show()

  expect(await screen.findByRole("button", { name: "Scanning…" })).toBeDisabled()
})

// Scenario: the last read failed; the line says so, and the button may try again.
test("a failed last read says so", async () => {
  server([triage({ last_run: run("rrn_1", { state: "failed" }) })])
  show()

  expect(await screen.findByText(/^Couldn't read new messages 12/)).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Scan now" })).toBeEnabled()
})

// Scenario: the main Bot has no triage Routine (another Bot's does not count); there is nothing to run.
test("without the main Bot's triage Routine, nothing shows", async () => {
  server([triage({ bot: "bot_other" })])
  const queryClient = show()

  // The Bots and then the Routines are read before the answer is known.
  await waitFor(() => expect(queryClient.getQueryCache().findAll().length).toBeGreaterThan(1))
  await waitFor(() => expect(queryClient.isFetching()).toBe(0))
  expect(screen.queryByRole("button", { name: /Scan now|Scanning/ })).not.toBeInTheDocument()
  expect(screen.queryByText(/new messages/)).not.toBeInTheDocument()
})
