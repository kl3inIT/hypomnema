import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  useNavigate,
  useSearch,
} from "@tanstack/react-router"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, test, vi } from "vitest"

import type { Approval, ChatMessage, Routine, RoutineRun } from "@/api/generated/types.gen"
import { Toaster } from "@/components/ui/toast"
import { TooltipProvider } from "@/components/ui/tooltip"
import { chatItems, emptyChat } from "@/features/chat/chat-state"

import { BotRoutines } from "./bot-routines"
import { RunCard, SetUpCard } from "./chat-cards"
import { RoutinesScreen } from "./routines-screen"
import { draftOf, scheduleOf } from "./schedule-draft"

beforeEach(() => {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
})
afterEach(() => vi.restoreAllMocks())

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })

const minutes = (n: number) => new Date(Date.now() + n * 60_000).toISOString()

const run = (id: string, m: Partial<RoutineRun> = {}): RoutineRun => ({
  id,
  routine_id: "rtn_sync",
  started_by: "schedule",
  state: "succeeded",
  started_at: minutes(-20),
  ended_at: minutes(-19),
  exit_code: 0,
  output: "214 rows copied",
  error: "",
  ...m,
})

const routine = (id: string, m: Partial<Routine> = {}): Routine => ({
  id,
  bot: "coordinator",
  name: id.replace("rtn_", ""),
  kind: "script",
  script: "sync.sh",
  script_text: "#!/bin/sh\nhypomnema superset chart 9 164 --format csv\n",
  every_minutes: 60,
  schedule: "every hour",
  sentence:
    "Every hour, coordinator runs the script sync.sh: the usage figures go to the Lark Base.",
  timeout_seconds: 600,
  state: "on",
  announce: true,
  next_run_at: minutes(40),
  failures: 0,
  skipped: 0,
  can_undo: false,
  created_by: "owner",
  created_at: minutes(-3000),
  updated_at: minutes(-3000),
  grants: [],
  ...m,
})

const approval = (m: Partial<Approval> = {}): Approval => ({
  id: "apr_1",
  integration: "lark",
  label: "work",
  action: "base.write",
  action_name: "Change a Base",
  asked_by: "coordinator",
  routine_id: "rtn_sync",
  routine_run_id: "rrn_9",
  title: "Change a Base: POST /open-apis/bitable/v1/apps/x/tables/y/records",
  payload: [{ label: "Request", value: "POST /open-apis/bitable/v1/apps/x/tables/y/records" }],
  status: "pending",
  used: false,
  created_at: minutes(-2),
  ...m,
})

type Call = { call: string; query?: string; body?: unknown }

// The server keeps the Routines as the control plane does, pages their runs by offset, says a schedule in words
// with its next times, and keeps every call that is not a read.
function server(
  start: Routine[],
  opts: { runs?: RoutineRun[]; approvals?: Approval[]; refuse?: Record<string, string> } = {},
) {
  let routines = start
  let approvals = opts.approvals ?? []
  const calls: Call[] = []

  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    const url = new URL(request.url)
    const path = decodeURIComponent(url.pathname.replace("/api/v1", ""))
    const call = `${request.method} ${path}`
    const text = request.method === "GET" ? "" : await request.clone().text()
    const body = text ? (JSON.parse(text) as Record<string, unknown>) : undefined

    calls.push({ call, query: url.search, ...(body && { body }) })

    const refused = opts.refuse?.[call]
    if (refused) return json({ code: "conflict", message: refused }, 409)

    if (call === "GET /bots") return json({ data: [{ name: "coordinator" }, { name: "maps" }] })
    if (call === "GET /routines") {
      const q = url.searchParams

      return json({
        data: routines.filter(
          (r) =>
            (!q.get("bot") || r.bot === q.get("bot")) &&
            (!q.get("chat_id") || r.chat_id === q.get("chat_id")),
        ),
      })
    }
    if (call === "POST /routine-schedule") {
      if (body?.every_minutes === 4) {
        return json(
          { code: "invalid_request", message: "a Routine runs every 5 to 1440 minutes" },
          400,
        )
      }

      return json({
        schedule: body?.cron
          ? `cron ${String(body.cron)}`
          : `every ${String(body?.every_minutes)} minutes`,
        next: ["2026-11-16T00:30:00Z", "2026-11-17T00:30:00Z", "2026-11-18T00:30:00Z"],
      })
    }
    const put = /^\/bots\/([^/]+)\/routines\/([^/]+)$/.exec(path)
    if (put && request.method === "PUT") {
      const made = routine(`rtn_${put[2]}`, {
        bot: put[1],
        name: put[2],
        ...(body as Partial<Routine>),
      })
      routines = [...routines, made]

      return json(made)
    }
    if (call === "GET /approvals") {
      const id = url.searchParams.get("routine_id")

      return json({ data: approvals.filter((a) => !id || a.routine_id === id) })
    }

    const [, id, rest = ""] = /^\/routines\/([^/]+)(\/.*)?$/.exec(path) ?? []
    const found = routines.find((r) => r.id === id)
    if (!found) return json({ code: "not_found", message: "no such routine" }, 404)

    const set = (change: Partial<Routine>) => {
      routines = routines.map((r) => (r.id === id ? { ...r, ...change } : r))

      return json({ ...found, ...change })
    }

    switch (`${request.method} ${rest}`) {
      case "GET ":
        return json(found)
      case "DELETE ":
        routines = routines.filter((r) => r.id !== id)

        return new Response(null, { status: 204 })
      case "POST /pause":
        return set({
          state: "paused",
          paused_reason: "paused by the owner",
          next_run_at: undefined,
        })
      case "POST /resume":
        return set({ state: "on", paused_reason: undefined, next_run_at: minutes(60) })
      case "POST /undo":
        routines = routines.filter((r) => r.id !== id)

        return json({ removed: true })
      case "GET /runs": {
        const all = (opts.runs ?? []).filter((r) => r.routine_id === id)
        const offset = Number(url.searchParams.get("offset") ?? 0)
        const limit = Number(url.searchParams.get("limit") ?? 200)
        const next = offset + limit

        return json({
          data: all.slice(offset, next),
          total: all.length,
          ...(next < all.length && { next_offset: next }),
        })
      }
      case "POST /runs":
        return json(run("rrn_now", { started_by: "manual", state: "running", ended_at: undefined }))
      default:
        if (request.method === "DELETE" && rest.startsWith("/grants/")) {
          const [integration, label, action] = rest.replace("/grants/", "").split("/")
          set({
            grants: found.grants.filter(
              (g) => !(g.integration === integration && g.label === label && g.action === action),
            ),
          })

          return new Response(null, { status: 204 })
        }
        if (request.method === "POST" && /^\/approvals\//.test(path)) {
          approvals = []
        }

        return json({ code: "not_found", message: "not found" }, 404)
    }
  })

  return calls
}

const changes = (calls: Call[]) => calls.filter((c) => !c.call.startsWith("GET "))

// open draws the Routines page, a Bot's page, the chat cards, and a chat, at a path.
function open(path: string) {
  const root = createRootRoute({ component: Outlet })
  const routes = [
    createRoute({
      getParentRoute: () => root,
      path: "/routines",
      validateSearch: (s: Record<string, unknown>) => ({
        bot: typeof s.bot === "string" ? s.bot : undefined,
        open: typeof s.open === "string" ? s.open : undefined,
      }),
      component: function Page() {
        const search = useSearch({ strict: false }) as { bot?: string; open?: string }
        const navigate = useNavigate()

        return (
          <RoutinesScreen
            bot={search.bot ?? ""}
            open={search.open}
            onChange={(next) =>
              void navigate({
                to: "/routines",
                search: (prev: Record<string, unknown>) => ({ ...prev, ...next }),
              })
            }
          />
        )
      },
    }),
    createRoute({
      getParentRoute: () => root,
      path: "/bots/$name",
      component: () => <BotRoutines bot="coordinator" />,
    }),
    createRoute({
      getParentRoute: () => root,
      path: "/cards",
      component: () => (
        <>
          <SetUpCard routineId="rtn_brief" />
          <RunCard
            routineId="rtn_brief"
            runId="rrn_7"
            text={'Routine "brief" ran (rrn_7):\n\nTwo pull requests were merged overnight.'}
            createdAt={minutes(-5)}
          />
        </>
      ),
    }),
    createRoute({
      getParentRoute: () => root,
      path: "/chats/$chatId",
      component: () => <p>A chat</p>,
    }),
  ]
  const router = createRouter({
    routeTree: root.addChildren(routes),
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

  render(
    <QueryClientProvider client={queryClient}>
      <TooltipProvider>
        <RouterProvider router={router} />
        <Toaster />
      </TooltipProvider>
    </QueryClientProvider>,
  )

  return router
}

// What the form sends: each quick choice is the schedule the server keeps, and a familiar cron expression reads
// back as its choice while any other stays raw.
test("a schedule's quick choices are what the server is sent, and read back", () => {
  const draft = {
    ...draftOf({ every_minutes: 60 }),
    time: "07:30",
    weekday: 5,
    timeZone: "Asia/Ho_Chi_Minh",
  }

  expect(scheduleOf({ ...draft, choice: "hourly" })).toEqual({ every_minutes: 60 })
  expect(scheduleOf({ ...draft, choice: "minutes", minutes: 15 })).toEqual({ every_minutes: 15 })
  expect(scheduleOf({ ...draft, choice: "daily" })).toEqual({
    cron: "30 7 * * *",
    timezone: "Asia/Ho_Chi_Minh",
  })
  expect(scheduleOf({ ...draft, choice: "weekdays" })).toEqual({
    cron: "30 7 * * 1-5",
    timezone: "Asia/Ho_Chi_Minh",
  })
  expect(scheduleOf({ ...draft, choice: "weekly" })).toEqual({
    cron: "30 7 * * 5",
    timezone: "Asia/Ho_Chi_Minh",
  })

  expect(draftOf({ every_minutes: 60 }).choice).toBe("hourly")
  expect(draftOf({ every_minutes: 20 })).toMatchObject({ choice: "minutes", minutes: 20 })
  expect(draftOf({ cron: "0 7 * * 1-5", timezone: "UTC" })).toMatchObject({
    choice: "weekdays",
    time: "07:00",
  })
  expect(draftOf({ cron: "15 18 * * 0", timezone: "UTC" })).toMatchObject({
    choice: "weekly",
    weekday: 0,
  })
  expect(draftOf({ cron: "*/20 9-17 * * 1-5", timezone: "UTC" })).toMatchObject({
    choice: "cron",
    expression: "*/20 9-17 * * 1-5",
  })
})

// Scenario: one Routine failed three times in a row, one is on, one is paused. The one that needs the owner comes
// first with its reason and one button; the others say their status in a line. Its switch pauses one.
test("the Routines list puts what needs the owner first, with one button, and switches one off", async () => {
  const calls = server([
    routine("rtn_sync", { last_run: run("rrn_1") }),
    routine("rtn_backup", {
      state: "paused",
      paused_reason: "paused by the owner",
      next_run_at: undefined,
    }),
    routine("rtn_brief", {
      failures: 3,
      last_run: run("rrn_2", { state: "failed", error: "exit 1" }),
    }),
  ])
  open("/routines")

  const list = await screen.findByRole("list", { name: "Routines" })
  const rows = within(list).getAllByRole("listitem")
  expect(rows.map((r) => r.dataset.routine)).toEqual(["rtn_brief", "rtn_sync", "rtn_backup"])

  expect(rows[0]).toHaveTextContent("Needs you: It didn't finish the last 3 times.")
  expect(
    within(rows[0]!)
      .getAllByRole("button")
      .map((b) => b.textContent),
  ).toEqual(["brief", "Run now"])
  expect(rows[1]).toHaveTextContent(/On · last ran .+ · next: .+/)
  expect(rows[2]).toHaveTextContent("Paused · paused by the owner · hasn't run yet")

  await userEvent.click(within(rows[1]!).getByRole("switch", { name: "Turn sync on or off" }))
  await waitFor(() =>
    expect(changes(calls)).toContainEqual(
      expect.objectContaining({ call: "POST /routines/rtn_sync/pause" }),
    ),
  )
  await userEvent.click(within(rows[0]!).getByRole("button", { name: "Run now" }))
  await waitFor(() =>
    expect(changes(calls)).toContainEqual(
      expect.objectContaining({ call: "POST /routines/rtn_brief/runs" }),
    ),
  )
})

test("the Routines page without Routines says what one is; one that cannot be read offers to try again", async () => {
  server([])
  open("/routines?bot=maps")
  expect(await screen.findByRole("heading", { name: "maps has no Routines yet." })).toBeVisible()

  vi.restoreAllMocks()
  vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ code: "internal", message: "down" }, 500))
  open("/routines")
  expect(await screen.findByText("The Routines could not be read.")).toBeVisible()
  expect(screen.getByRole("button", { name: "Try again" })).toBeVisible()
})

// Scenario: a script Routine with 25 runs, a grant, and a run that waits for the owner. Its panel shows its facts,
// its script, the Approval its run waits on, the grant to take back, and its runs twenty at a time.
test("a Routine's panel shows its facts, script, Approval, grants and runs a page at a time", async () => {
  const runs = Array.from({ length: 25 }, (_, i) =>
    run(`rrn_${i}`, { started_at: minutes(-60 * (i + 1)), output: `${i} rows copied` }),
  )
  const calls = server(
    [
      routine("rtn_sync", {
        skipped: 2,
        last_skipped_at: minutes(-90),
        grants: [
          {
            integration: "lark",
            label: "work",
            action: "base.write",
            action_name: "Change a Base",
            created_at: minutes(-100),
          },
        ],
        last_run: run("rrn_9", { state: "waiting", ended_at: undefined }),
      }),
    ],
    { runs, approvals: [approval()] },
  )
  open("/routines?open=rtn_sync")

  const panel = await screen.findByRole("complementary", { name: "Routine" })
  const p = within(panel)
  expect(await p.findByText("Every hour")).toBeVisible()
  expect(p.getByText("10 minutes")).toBeVisible()
  expect(p.getByText(/2 times while a run still went/)).toBeVisible()
  expect(p.getByTestId("routine-work")).toHaveTextContent("hypomnema superset chart 9 164")
  expect(p.getByTestId("routine-status")).toHaveTextContent("Needs you")
  expect(await p.findByRole("region", { name: /Approval needed: Change a Base/ })).toBeVisible()

  expect(await p.findByText("20 of 25 shown")).toBeVisible()
  expect(p.getAllByTestId("run")).toHaveLength(20)
  await userEvent.click(p.getByRole("button", { name: "Load more" }))
  expect(await p.findByText("25 of 25 shown")).toBeVisible()
  expect(p.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument()
  expect(
    calls.filter((c) => c.call === "GET /routines/rtn_sync/runs").map((c) => c.query),
  ).toContain("?limit=20&offset=20")

  await userEvent.click(p.getByRole("button", { name: "Take back" }))
  await waitFor(() =>
    expect(changes(calls)).toContainEqual(
      expect.objectContaining({ call: "DELETE /routines/rtn_sync/grants/lark/work/base.write" }),
    ),
  )
})

// Scenario: the owner makes a Routine from the page: weekdays at 7:30 in Ho Chi Minh City, a prompt. The form shows
// the server's words and next three runs, and refuses a schedule the server refuses before it is sent.
test("a Routine is made from the form with a quick choice, its words and next runs from the server", async () => {
  const calls = server([])
  open("/routines?bot=coordinator")

  await userEvent.click(await screen.findByRole("button", { name: "New Routine" }))
  const dialog = await screen.findByRole("dialog", { name: "New Routine" })
  const d = within(dialog)

  await userEvent.type(d.getByLabelText("Name"), "morning-brief")
  await userEvent.click(d.getByRole("button", { name: "Every N minutes" }))
  await userEvent.click(d.getByRole("combobox", { name: "Minutes between runs" }))
  await userEvent.click(await screen.findByRole("option", { name: "Custom" }))
  await userEvent.type(d.getByRole("spinbutton", { name: "Custom minutes" }), "4")
  expect(await d.findByRole("alert")).toHaveTextContent("every 5 to 1440 minutes")

  await userEvent.click(d.getByRole("button", { name: "Weekdays" }))
  const time = d.getByLabelText("Time")
  await userEvent.clear(time)
  await userEvent.type(time, "07:30")
  await waitFor(() =>
    expect(d.getByTestId("schedule-words")).toHaveTextContent("Cron 30 7 * * 1-5"),
  )
  expect(d.getByLabelText("Next runs").querySelectorAll("time")).toHaveLength(3)

  await userEvent.type(d.getByLabelText("Prompt"), "Summarize what changed overnight.")
  await userEvent.click(d.getByRole("button", { name: "Make it" }))

  await waitFor(() =>
    expect(changes(calls)).toContainEqual({
      call: "PUT /bots/coordinator/routines/morning-brief",
      query: "",
      body: expect.objectContaining({
        kind: "prompt",
        prompt: "Summarize what changed overnight.",
        cron: "30 7 * * 1-5",
        timezone: expect.any(String),
        announce: true,
      }),
    }),
  )
  expect(await screen.findByText("Routine made")).toBeVisible()
})

test("a Bot's page lists its Routines, each leading to it on the Routines page", async () => {
  server([routine("rtn_sync", { last_run: run("rrn_1") }), routine("rtn_other", { bot: "maps" })])
  open("/bots/coordinator")

  const link = await screen.findByRole("link", { name: /sync/ })
  expect(link).toHaveAttribute("href", "/routines?bot=coordinator&open=rtn_sync")
  expect(link).toHaveTextContent("Every hour")
  expect(screen.queryByText("other")).not.toBeInTheDocument()
  expect(screen.getByRole("link", { name: /All of coordinator's Routines/ })).toHaveAttribute(
    "href",
    "/routines?bot=coordinator",
  )
})

// Scenario: the Bot set up a Routine from a chat, and a run told its result there. The card of the setup can be
// undone, and the run's card opens the run's chat.
test("a chat's Routine cards undo the Bot's setup and open a run", async () => {
  const calls = server(
    [
      routine("rtn_brief", {
        kind: "prompt",
        created_by: "bot",
        can_undo: true,
        sentence: "Every weekday at 7:00 Ho Chi Minh time, coordinator works on a prompt.",
        chat_id: "cht_1",
      }),
    ],
    {
      runs: [
        run("rrn_7", { routine_id: "rtn_brief", chat_id: "cht_run", output: "Two pull requests." }),
      ],
    },
  )
  open("/cards")

  const setup = await screen.findByRole("region", {
    name: "Set up: Every weekday at 7:00 Ho Chi Minh time, coordinator works on a prompt.",
  })
  const card = await screen.findByRole("region", { name: "brief: Done" })
  await waitFor(() =>
    expect(within(card).getByRole("link", { name: "Open brief" })).toHaveAttribute(
      "href",
      "/chats/cht_run",
    ),
  )
  expect(card).toHaveTextContent("Two pull requests.")

  await userEvent.click(within(setup).getByRole("button", { name: "Undo" }))
  expect(await screen.findByText("Routine removed · undone")).toBeVisible()
  expect(changes(calls)).toContainEqual(
    expect.objectContaining({ call: "POST /routines/rtn_brief/undo" }),
  )
})

// The conversation puts a setup's card among the messages at the moment it was made, and a result's part as the
// run's card.
test("a chat's items place a setup by its time and a run's result as its card", () => {
  const message = (seq: number, at: string, m: Partial<ChatMessage> = {}): ChatMessage => ({
    seq,
    chat_id: "cht_1",
    role: "owner",
    text: `message ${seq}`,
    created_at: at,
    ...m,
  })
  const view = {
    ...emptyChat,
    messages: [
      message(1, "2026-11-12T09:00:00Z"),
      message(2, "2026-11-12T09:02:00Z", { role: "coordinator", text: "Set." }),
      message(3, "2026-11-12T10:00:00Z", {
        role: "coordinator",
        text: 'Routine "brief" ran (rrn_7):\n\nDone.',
        parts: [{ type: "routine_run", id: "rrn_7", routine_id: "rtn_brief" }],
      }),
    ],
  }

  const items = chatItems(view, {}, [{ id: "rtn_brief", at: "2026-11-12T09:01:00Z" }])
  expect(items.map((i) => i.kind)).toEqual(["owner", "routine-setup", "reply", "routine-run"])
  expect(items[3]).toMatchObject({ runId: "rrn_7", routineId: "rtn_brief" })
})
