import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The Routines page with its panel and form, a Bot's short list, and the cards in a chat (issue #211), built,
// against a fake client API, on a phone and a desktop.

type Run = {
  id: string
  routine_id: string
  started_by: "schedule" | "manual"
  state: "running" | "waiting" | "succeeded" | "failed" | "canceled"
  started_at: string
  ended_at?: string
  exit_code?: number
  output: string
  error: string
  chat_id?: string
}

type Message = {
  seq: number
  chat_id: string
  role: "owner" | "coordinator" | "tool" | "system"
  text: string
  parts?: { type: string; id?: string; routine_id?: string; text?: string }[]
  created_at: string
}

type Grant = {
  integration: string
  label: string
  action: string
  action_name: string
  created_at: string
}

type Routine = {
  id: string
  bot: string
  name: string
  kind: "prompt" | "script"
  prompt?: string
  script?: string
  script_text?: string
  every_minutes?: number
  cron?: string
  timezone?: string
  schedule: string
  sentence: string
  timeout_seconds: number
  state: "on" | "paused"
  paused_reason?: string
  summary?: string
  chat_id?: string
  announce: boolean
  next_run_at?: string
  failures: number
  skipped: number
  last_skipped_at?: string
  can_undo: boolean
  created_by: string
  created_at: string
  updated_at: string
  last_run?: Run
  grants: Grant[]
}

const minutes = (n: number) => new Date(Date.now() + n * 60_000).toISOString()

const run = (id: string, m: Partial<Run> = {}): Run => ({
  id,
  routine_id: "rtn_sync",
  started_by: "schedule",
  state: "succeeded",
  started_at: minutes(-20),
  ended_at: minutes(-20),
  exit_code: 0,
  output: "",
  error: "",
  ...m,
})

const script = `#!/bin/sh
# Copies the figures of the Sales dashboard to the Lark Base "Sales, hourly".
set -eu

hypomnema superset chart sales 41 --format csv > /tmp/sales.csv
lark-cli base records batch-create --app bascnSales --table tblHourly --csv /tmp/sales.csv
echo "wrote $(($(wc -l < /tmp/sales.csv) - 1)) rows"
`

const sync: Routine = {
  id: "rtn_sync",
  bot: "coordinator",
  name: "superset-to-lark",
  kind: "script",
  script: "superset-to-lark.sh",
  script_text: script,
  every_minutes: 60,
  schedule: "every hour",
  sentence:
    "Every hour, coordinator runs the script superset-to-lark.sh: copy the figures of the Sales dashboard to the Lark Base.",
  timeout_seconds: 600,
  state: "on",
  summary: "copy the figures of the Sales dashboard to the Lark Base",
  chat_id: "cht_1",
  announce: false,
  next_run_at: minutes(40),
  failures: 0,
  skipped: 0,
  can_undo: false,
  created_by: "bot",
  created_at: minutes(-3000),
  updated_at: minutes(-20),
  last_run: run("rrn_5", { output: "wrote 214 rows" }),
  grants: [
    {
      integration: "lark",
      label: "work",
      action: "base.write",
      action_name: "Create or change in Base",
      created_at: minutes(-2900),
    },
  ],
}

const brief: Routine = {
  id: "rtn_brief",
  bot: "coordinator",
  name: "morning-brief",
  kind: "prompt",
  prompt:
    "Read the mail that came since yesterday evening and the day's calendar. Write a brief of at most ten lines: what needs an answer today, and the meetings.",
  cron: "0 7 * * 1-5",
  timezone: "Asia/Ho_Chi_Minh",
  schedule: "every weekday at 7:00 Ho Chi Minh time",
  sentence:
    "Every weekday at 7:00 Ho Chi Minh time, coordinator works on a prompt: a brief of the night's mail and the day's meetings.",
  timeout_seconds: 3600,
  state: "paused",
  paused_reason:
    "paused after 10 failed runs in a row; the last: pi's model call failed: 429 the account is out of quota",
  chat_id: "cht_1",
  announce: true,
  failures: 10,
  skipped: 0,
  can_undo: false,
  created_by: "owner",
  created_at: minutes(-9000),
  updated_at: minutes(-300),
  last_run: run("rrn_b9", {
    routine_id: "rtn_brief",
    state: "failed",
    exit_code: undefined,
    started_at: minutes(-300),
    ended_at: minutes(-299),
    error: "pi's model call failed: 429 the account is out of quota",
    chat_id: "cht_b9",
  }),
  grants: [],
}

const syncRuns: Run[] = [
  run("rrn_5", { ended_at: minutes(-19.9), output: "wrote 214 rows" }),
  run("rrn_4", {
    state: "failed",
    started_at: minutes(-80),
    ended_at: minutes(-79),
    exit_code: 1,
    output: 'lark-cli: {"code":1254290,"msg":"TooManyRequest"}',
    error: "the script exited with code 1",
  }),
  run("rrn_3", {
    state: "canceled",
    started_at: minutes(-140),
    ended_at: minutes(-140),
    exit_code: undefined,
    error: "the owner rejected it",
  }),
  run("rrn_2", {
    started_by: "manual",
    started_at: minutes(-200),
    ended_at: minutes(-150),
    output: "wrote 198 rows",
  }),
  run("rrn_1", {
    started_by: "manual",
    started_at: minutes(-2900),
    ended_at: minutes(-2899),
    output: "wrote 201 rows",
  }),
]

const approval = {
  id: "apr_1",
  integration: "lark",
  label: "work",
  action: "base.write",
  action_name: "Create or change in Base",
  asked_by: "pi",
  routine_id: "rtn_sync",
  routine_run_id: "rrn_6",
  title: "Create or change in Base: POST /open-apis/bitable/v1/apps/bascnSales/tables/tblHourly/…",
  payload: [
    {
      label: "Request",
      value: "POST /open-apis/bitable/v1/apps/bascnSales/tables/tblHourly/records/batch_create",
    },
    { label: "records", value: "214 items" },
  ],
  status: "pending",
  used: false,
  created_at: minutes(-2),
}

const coordinator = {
  name: "coordinator",
  provider: "gateway",
  model: "gpt-luna",
  persona: "Plan the work, give it to workers and report what they did, briefly.",
  memory: "The owner writes in Vietnamese and wants short answers.",
  memory_limit: 2200,
  created_at: minutes(-12000),
  updated_at: minutes(-180),
}

const chat = {
  id: "cht_1",
  title: "Send the sales figures to Lark every week",
  bot: "coordinator",
  working: false,
  coordinator: true,
  created_at: minutes(-30),
  updated_at: minutes(-1),
}

// A Routine that fails: twice or more in a row, it needs the owner, with "Run now".
const failing: Routine = {
  ...sync,
  id: "rtn_lark",
  name: "lark-depots-and-a-name-long-enough-to-be-cut-on-a-phone",
  script: "depots.sh",
  sentence:
    "Every 15 minutes, coordinator runs the script depots.sh: the depots' stock goes to the Lark sheet of the warehouse team.",
  every_minutes: 15,
  schedule: "every 15 minutes",
  failures: 3,
  can_undo: false,
  last_run: run("rrn_l3", {
    routine_id: "rtn_lark",
    state: "failed",
    exit_code: 1,
    error: "the script exited with code 1",
  }),
  grants: [],
}

// A Routine the Bot set up from the chat a moment ago, which the owner can still undo.
const setUp: Routine = {
  ...brief,
  id: "rtn_week",
  name: "weekly-sales",
  state: "on",
  paused_reason: undefined,
  failures: 0,
  cron: "0 17 * * 5",
  schedule: "weekly on Friday at 17:00 Ho Chi Minh time",
  sentence:
    "Weekly on Friday at 17:00 Ho Chi Minh time, coordinator works on a prompt: the week's sales figures, compared with last week.",
  next_run_at: minutes(3 * 24 * 60),
  created_by: "bot",
  can_undo: true,
  created_at: minutes(-4),
  updated_at: minutes(-4),
  last_run: undefined,
}

const manyRuns: Run[] = [
  ...syncRuns,
  ...Array.from({ length: 22 }, (_, i) =>
    run(`rrn_old${i}`, {
      started_at: minutes(-3000 - 60 * i),
      ended_at: minutes(-3000 - 60 * i + 1),
      output: `wrote ${180 + i} rows`,
    }),
  ),
]

const chatMessages = (): Message[] => [
  {
    seq: 1,
    chat_id: "cht_1",
    role: "owner",
    text: "Every Friday at 5 pm, send me the week's sales compared with last week.",
    created_at: minutes(-6),
  },
  {
    seq: 2,
    chat_id: "cht_1",
    role: "coordinator",
    text: "Weekly on Friday at 17:00 Ho Chi Minh time I will send you the week's sales figures, compared with last week. It is on; I ran it once below.",
    created_at: minutes(-3),
  },
  {
    seq: 3,
    chat_id: "cht_1",
    role: "coordinator",
    text: 'Routine "weekly-sales" ran (rrn_w1):\n\nSales this week: 1,284 orders, 12% more than last week.',
    parts: [
      {
        type: "routine_run",
        id: "rrn_w1",
        routine_id: "rtn_week",
        text: 'Routine "weekly-sales" ran (rrn_w1):\n\nSales this week: 1,284 orders, 12% more than last week.',
      },
    ],
    created_at: minutes(-1),
  },
]

// The fake keeps the Routines as the control plane does: a pause, a resume, an undo and a removal change them; a
// grant taken back is gone; an answered Approval no longer waits; a Routine's runs come by offset. It says a
// schedule in words with its next three times, and keeps every call that changes something.
async function fakeServer(
  page: Page,
  start: Routine[],
  opts: {
    runs?: Run[]
    approvals?: (typeof approval)[]
    fail?: boolean
    messages?: Message[]
  } = {},
) {
  let routines = start
  let approvals = opts.approvals ?? []
  const calls: { call: string; body: unknown }[] = []

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = decodeURIComponent(url.pathname.replace("/api/v1", ""))
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (request.method() !== "GET") calls.push({ call, body: request.postDataJSON() })

    switch (call) {
      case "GET /me":
        return json({ id: "usr_1", name: "admin", kind: "owner" })
      case "GET /bots":
        return json({ data: [coordinator] })
      case "GET /bots/coordinator":
        return json(coordinator)
      case "GET /models":
        return json({ data: [{ provider: "gateway", name: "gpt-luna", kind: "model_gateway" }] })
      case "GET /chats/cht_1":
        return json(chat)
      case "GET /chats/cht_1/stream":
        return route.fulfill({
          status: 200,
          contentType: "text/event-stream",
          body:
            (opts.messages ?? [])
              .map((m) => `id: ${m.seq}\nevent: message\ndata: ${JSON.stringify(m)}\n\n`)
              .join("") || ": hello\n\n",
        })
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "GET /chats":
        return json({ data: [chat] })
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      case "GET /questions":
      case "GET /profiles":
      case "GET /lark/channels":
        return json({ data: [] })
      case "GET /approvals": {
        const q = url.searchParams

        return json({
          data: approvals.filter(
            (a) =>
              (!q.get("routine_id") || a.routine_id === q.get("routine_id")) &&
              (!q.get("chat_id") || ("chat_id" in a && a.chat_id === q.get("chat_id"))) &&
              !q.get("task_id"),
          ),
        })
      }
      case "GET /routines": {
        if (opts.fail) return json({ code: "internal", message: "the database is busy" }, 500)

        const q = url.searchParams

        return json({
          data: routines.filter(
            (r) =>
              (!q.get("bot") || r.bot === q.get("bot")) &&
              (!q.get("chat_id") || r.chat_id === q.get("chat_id")),
          ),
        })
      }
      case "POST /routine-schedule": {
        const body = request.postDataJSON() as { every_minutes?: number; cron?: string }
        if (body.every_minutes !== undefined && body.every_minutes < 5) {
          return json(
            {
              code: "invalid_request",
              message: "a Routine runs every 5 to 1440 minutes, or by a cron expression",
            },
            400,
          )
        }
        const words: Record<string, string> = {
          "30 7 * * 1-5": "every weekday at 7:30 Ho Chi Minh time",
          "0 9 * * *": "every day at 9:00 Ho Chi Minh time",
          "*/20 9-17 * * 1-5": "cron */20 9-17 * * 1-5 Ho Chi Minh time",
        }

        return json({
          schedule: body.cron
            ? (words[body.cron] ?? `cron ${body.cron} Ho Chi Minh time`)
            : body.every_minutes === 60
              ? "every hour"
              : `every ${body.every_minutes} minutes`,
          next: ["2026-11-16T00:30:00Z", "2026-11-17T00:30:00Z", "2026-11-18T00:30:00Z"],
        })
      }
    }

    const decided = /^\/approvals\/([^/]+)\/decision$/.exec(path)
    if (decided) {
      const answered = approvals.find((a) => a.id === decided[1])
      approvals = approvals.filter((a) => a.id !== decided[1])

      return json({ ...answered, status: "approved" })
    }

    const put = /^\/bots\/([^/]+)\/routines\/([^/]+)$/.exec(path)
    if (put && request.method() === "PUT") {
      const made: Routine = {
        ...brief,
        id: `rtn_${put[2]}`,
        name: put[2]!,
        state: "on",
        failures: 0,
      }
      routines = [...routines, made]

      return json(made)
    }

    const [, id, rest = ""] = /^\/routines\/([^/]+)(\/.*)?$/.exec(path) ?? []
    const found = routines.find((r) => r.id === id)
    if (!found) return json({ code: "not_found", message: "not found" }, 404)

    const keep = (change: Partial<Routine>) => {
      routines = routines.map((r) => (r.id === id ? { ...r, ...change } : r))
    }
    const set = (change: Partial<Routine>) => {
      keep(change)

      return json({ ...found, ...change })
    }

    switch (`${request.method()} ${rest}`) {
      case "GET ":
        return json(found)
      case "DELETE ":
        routines = routines.filter((r) => r.id !== id)

        return route.fulfill({ status: 204 })
      case "POST /pause":
        return set({
          state: "paused",
          paused_reason: "paused by the owner",
          next_run_at: undefined,
        })
      case "POST /resume":
        return set({ state: "on", paused_reason: undefined, failures: 0, next_run_at: minutes(60) })
      case "POST /undo":
        routines = routines.filter((r) => r.id !== id)

        return json({ removed: true })
      case "GET /runs": {
        const all =
          found.id === "rtn_sync"
            ? (opts.runs ?? [])
            : found.id === "rtn_week"
              ? [
                  run("rrn_w1", {
                    routine_id: "rtn_week",
                    started_by: "manual",
                    chat_id: "cht_w1",
                    exit_code: undefined,
                    started_at: minutes(-2),
                    ended_at: minutes(-1),
                    output: "Sales this week: 1,284 orders, 12% more than last week.",
                  }),
                ]
              : []
        const offset = Number(url.searchParams.get("offset") ?? 0)
        const limit = Number(url.searchParams.get("limit") ?? 200)

        return json({
          data: all.slice(offset, offset + limit),
          total: all.length,
          ...(offset + limit < all.length && { next_offset: offset + limit }),
        })
      }
      case "POST /runs":
        return json(run("rrn_now", { started_by: "manual", state: "running", ended_at: undefined }))
    }

    if (request.method() === "DELETE" && rest.startsWith("/grants/")) {
      const [integration, label, action] = rest.replace("/grants/", "").split("/")
      keep({
        grants: found.grants.filter(
          (g) => !(g.integration === integration && g.label === label && g.action === action),
        ),
      })

      return route.fulfill({ status: 204 })
    }

    return json({ code: "not_found", message: "not found" }, 404)
  })

  return calls
}

const fits = (page: Page) =>
  page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")

const phone = (page: Page) => (page.viewportSize()?.width ?? 1280) < 600

// Scenario: the owner opens Routines from the sidebar. The one that keeps failing comes first with its reason and
// one button; the others say their status in a line; the switch pauses one.
test("the Routines page lists what needs the owner first and switches one off", async ({
  page,
}) => {
  const calls = await fakeServer(page, [sync, brief, failing])
  await page.goto("/routines")

  const list = page.getByRole("list", { name: "Routines" })
  await expect(list.getByRole("listitem").first()).toContainText(
    "Needs you: It didn't finish the last 3 times.",
  )
  await expect(list.getByRole("listitem").nth(1)).toContainText("On · last ran")
  await expect(list.getByRole("listitem").nth(2)).toContainText(
    "Paused · paused after 10 failed runs",
  )
  expect(await fits(page)).toBe(true)
  await shot(page, "routines-list")

  await list.getByRole("switch", { name: "Turn superset-to-lark on or off" }).click()
  await expect(list.getByRole("listitem").nth(2)).toContainText("Paused · paused by the owner")
  expect(calls).toContainEqual({ call: "POST /routines/rtn_sync/pause", body: null })
})

test("the Routines page without Routines says what one is; one that cannot be read offers to try again", async ({
  page,
}) => {
  await fakeServer(page, [])
  await page.goto("/routines")
  await expect(
    page.getByRole("heading", { name: "Your Bots can do work on their own." }),
  ).toBeVisible()
  expect(await fits(page)).toBe(true)
  await shot(page, "routines-empty")

  await page.unrouteAll()
  await fakeServer(page, [], { fail: true })
  await page.goto("/routines")
  // The page tries a read again a few times before it says it failed.
  await expect(page.getByText("The Routines could not be read.")).toBeVisible({ timeout: 20_000 })
  await expect(page.getByRole("button", { name: "Try again" })).toBeVisible()
  await shot(page, "routines-error")
})

// Scenario: the owner opens a script Routine whose run waits for him. Its panel shows the facts, the Approval, the
// script, the grant, and the runs twenty at a time; "Always for this Routine" answers the Approval.
test("a Routine's panel shows its facts, script, grants and runs, and its run's Approval is answered", async ({
  page,
}) => {
  const waiting = run("rrn_6", { state: "waiting", ended_at: undefined, exit_code: undefined })
  const calls = await fakeServer(
    page,
    [{ ...sync, skipped: 2, last_skipped_at: minutes(-75), last_run: waiting }, brief],
    { runs: [waiting, ...manyRuns], approvals: [approval] },
  )
  await page.goto("/routines?open=rtn_sync")

  const panel = page.getByRole("complementary", { name: "Routine" })
  await expect(panel.getByTestId("routine-status")).toContainText("Needs you")
  await expect(panel.getByRole("region", { name: /Approval needed/ })).toContainText("214 items")
  await expect(
    panel.getByText("28 of 28 shown").or(panel.getByText("20 of 28 shown")),
  ).toBeVisible()
  expect(await fits(page)).toBe(true)
  await shot(page, "routine-panel")

  await panel.getByTestId("routine-work").scrollIntoViewIfNeeded()
  await shot(page, "routine-panel-script")

  await panel.getByRole("button", { name: "Load more" }).click()
  await expect(panel.getByText("28 of 28 shown")).toBeVisible()
  await panel.getByText("28 of 28 shown").scrollIntoViewIfNeeded()
  await shot(page, "routine-panel-runs")

  const card = panel.getByRole("region", { name: /Approval needed/ })
  await card.scrollIntoViewIfNeeded()
  await card.getByRole("button", { name: "More approval options" }).click()
  await expect(page.getByRole("menuitem", { name: /Approve for/ })).toHaveCount(0)
  await shot(page, "routine-approval-menu")
  await page.getByRole("menuitem", { name: "Always for this Routine" }).click()
  await expect(page.getByText("Approved always for this Routine")).toBeVisible()
  expect(calls).toContainEqual({
    call: "POST /approvals/apr_1/decision",
    body: { decision: "approve_routine" },
  })

  await panel.getByRole("button", { name: "Take back" }).click()
  expect(calls).toContainEqual({
    call: "DELETE /routines/rtn_sync/grants/lark/work/base.write",
    body: null,
  })
})

// Scenario: the owner makes a Routine from the page. The quick choices say the schedule in words with its next
// three runs; a schedule the server refuses says why; a script shows its file and its text.
test("a Routine is made from the form, with its schedule in words and its next runs", async ({
  page,
}) => {
  const calls = await fakeServer(page, [sync])
  await page.goto("/routines?bot=coordinator")

  await page.getByRole("button", { name: "New", exact: true }).click()
  const dialog = page.getByRole("dialog", { name: "New Routine" })
  await dialog.getByLabel("Name").fill("morning-brief")
  await dialog.getByRole("button", { name: "Weekdays" }).click()
  await dialog.getByLabel("Time", { exact: true }).fill("07:30")
  await expect(dialog.getByTestId("schedule-words")).toHaveText(
    "Every weekday at 7:30 Ho Chi Minh time",
  )
  await expect(dialog.getByLabel("Next runs").locator("time")).toHaveCount(3)
  await dialog
    .getByLabel("Prompt")
    .fill("Summarize the night's mail and the day's meetings in ten lines.")
  await shot(page, "routine-form")

  await dialog.getByRole("button", { name: "Every N minutes" }).click()
  await dialog.getByRole("combobox", { name: "Minutes between runs" }).click()
  await page.getByRole("option", { name: "Custom" }).click()
  await dialog.getByRole("spinbutton", { name: "Custom minutes" }).fill("4")
  await expect(dialog.getByRole("alert")).toContainText("every 5 to 1440 minutes")
  await shot(page, "routine-form-refused")

  await dialog.getByRole("button", { name: "Cron" }).click()
  await dialog.getByLabel("Cron expression").fill("*/20 9-17 * * 1-5")
  await expect(dialog.getByTestId("schedule-words")).toHaveText(
    "Cron */20 9-17 * * 1-5 Ho Chi Minh time",
  )
  await dialog.getByRole("button", { name: "A script" }).click()
  await dialog.getByLabel("Script file").fill("depots.sh")
  await dialog.getByLabel("Script", { exact: true }).fill(script)
  await shot(page, "routine-form-script")

  await dialog.getByRole("button", { name: "Make it" }).click()
  await expect(page.getByText("Routine made")).toBeVisible()
  expect(calls).toContainEqual({
    call: "PUT /bots/coordinator/routines/morning-brief",
    body: expect.objectContaining({
      kind: "script",
      script: "depots.sh",
      cron: "*/20 9-17 * * 1-5",
      timeout_seconds: 600,
    }),
  })
})

// Scenario: a Bot's page has the short list of its Routines, each leading to it on the Routines page.
test("a Bot's page lists its Routines", async ({ page }) => {
  await fakeServer(page, [sync, brief, failing])
  await page.goto("/bots/coordinator")

  const section = page.getByRole("region", { name: "Routines" })
  await section.scrollIntoViewIfNeeded()
  await expect(section.getByRole("link", { name: /superset-to-lark/ })).toHaveAttribute(
    "href",
    "/routines?bot=coordinator&open=rtn_sync",
  )
  expect(await fits(page)).toBe(true)
  await shot(page, "bot-routines")
})

// Scenario: the Bot set up a Routine in the chat and ran it once. The chat shows the setup's card where it was set
// up, with Pause, Edit and Undo, and the run's card with its result and Open; Undo removes the Routine.
test("a chat shows the Routine the Bot set up and its run, and the setup is undone", async ({
  page,
}) => {
  const calls = await fakeServer(page, [setUp], { messages: chatMessages() })
  await page.goto("/chats/cht_1")

  const setup = page.getByRole("region", { name: /^Set up: Weekly on Friday/ })
  await expect(setup).toContainText("First run")
  const card = page.getByRole("region", { name: "weekly-sales: Done" })
  await expect(card).toContainText("1,284 orders")
  await expect(card.getByRole("link", { name: "Open weekly-sales" })).toHaveAttribute(
    "href",
    "/chats/cht_w1",
  )
  expect(await fits(page)).toBe(true)
  await shot(page, "chat-cards")

  await setup.getByRole("button", { name: "Undo" }).click()
  await expect(page.getByText("Routine removed · undone")).toBeVisible()
  expect(calls).toContainEqual({ call: "POST /routines/rtn_week/undo", body: null })
  await expect(setup).toHaveCount(0)
  if (!phone(page)) await shot(page, "chat-cards-undone")
})
