import { expect, test, type Page } from "@playwright/test"

import { openMenu } from "./menu.js"
import { shot } from "./shot.js"

// The Inbox page (issue #1093), built, against a fake client API, on a phone and a desktop: what waits on the owner
// first (an Approval, a worker's question, a reply with its draft, an account to log in again), then what a Bot
// does, what is open, the proposals, and the snoozed and closed items folded.

const at = (ms: number) => new Date(Date.now() + ms).toISOString()
const hour = 3_600_000
const day = 24 * hour

// today is a date in Asia/Ho_Chi_Minh, n days from now, as the Thư ký writes a reply's due date.
const dayIn = (n: number) =>
  new Intl.DateTimeFormat("en-CA", { timeZone: "Asia/Ho_Chi_Minh", dateStyle: "short" }).format(
    Date.now() + n * day,
  )

type Todo = Record<string, unknown> & { id: string; state: string; revision: number }

const todo = (id: string, title: string, m: object = {}): Todo => ({
  id,
  revision: 1,
  title,
  notes: "",
  done_when: "",
  draft: "",
  due: { kind: "none" },
  assignee: { kind: "me" },
  state: "open",
  source: { kind: "owner" },
  created_by: { kind: "bot", id: "thu-ky" },
  close_reason: "",
  evidence: [],
  stale: false,
  created_at: at(-5 * hour),
  updated_at: at(-hour),
  ...m,
})

// world is the fake client API. review adds a to-do that waits for the Approval apr_1, which it then holds out of the
// list. hold keeps each done call unanswered until release is called, so a second press finds the first in flight.
// sources adds a to-do from a Gmail thread and one from a meeting's line (issue #1105), and the owner's Google
// account the first was read with.
function world({ review = false, sources = false }: { review?: boolean; sources?: boolean } = {}) {
  let release = () => {}
  let held: Promise<void> | undefined
  const todos: Todo[] = [
    todo("tdo_reply", "Reply to Kai Yong", {
      due: { kind: "date", date: dayIn(0), zone: "Asia/Ho_Chi_Minh" },
      why_open: "asked_directly",
      draft: "Hi Kai, I will send the signed contract this afternoon.",
      source: {
        kind: "whatsapp",
        id: "6591234567@s.whatsapp.net",
        account: "main",
        quote: "Hi Dat, any news on the contract? We need it before Friday.",
        ref: { kind: "whatsapp_message", ref: "3EB0C1", label: "Kai Yong" },
      },
      updated_at: at(-20 * 60_000),
    }),
    todo("tdo_login", "Log in again: lark:me", {
      notes:
        "A read of lark:me was refused for its sign-in. Sign in to it again; the next read that works closes this.",
      source: { kind: "routine", id: "rrn_1", quote: "401 from Lark" },
      updated_at: at(-2 * hour),
    }),
    todo("tdo_chase", "Lan sends the signed contract", {
      due: { kind: "date", date: dayIn(-1), zone: "Asia/Ho_Chi_Minh" },
      assignee: { kind: "person", name: "Lan" },
      source: {
        kind: "zalo",
        account: "main",
        ref: { kind: "zalo_message", ref: "z1", label: "Lan" },
      },
    }),
    todo("tdo_invoice", "Send the invoice to ELITEX", {
      due: { kind: "date", date: dayIn(1), zone: "Asia/Ho_Chi_Minh" },
      source: {
        kind: "lark",
        account: "me",
        ref: { kind: "lark_message", ref: "om_1", label: "ELITEX" },
      },
    }),
    todo("tdo_doing", "Draft the weekly report", {
      state: "doing",
      assignee: { kind: "bot", name: "thu-ky" },
      work: { attempt: "att_1", kind: "chat", id: "cht_2", bot: "thu-ky", started_at: at(-hour) },
      source: { kind: "chat", id: "cht_2" },
    }),
    todo("tdo_idea", "Book the meeting room for Friday", {
      state: "proposed",
      source: { kind: "zalo", account: "main", quote: "Ai đặt phòng họp thứ Sáu nhé" },
    }),
    todo("tdo_later", "Renew the domain", { snoozed_until: at(day) }),
    todo("tdo_paid", "Pay the electricity bill", {
      state: "done",
      closed_at: at(-3 * hour),
      close_reason: "Paid",
    }),
  ]
  if (review)
    todos.unshift(
      todo("tdo_mail", "Send the contract to Kai", {
        state: "review",
        approval_id: "apr_1",
        assignee: { kind: "bot", name: "thu-ky" },
        source: { kind: "chat", id: "cht_1" },
        updated_at: at(-10 * 60_000),
      }),
    )
  if (sources)
    todos.push(
      todo("tdo_poc", "Xem đề xuất PoC của Alibaba Cloud", {
        state: "proposed",
        source: {
          kind: "gmail",
          id: "18c2f0a9b1d2e3f4",
          account: "work",
          quote: "Please find our PoC proposal attached.",
          ref: { kind: "mail", ref: "18c2f0a9b1d2e3f5", label: "Alibaba Cloud" },
        },
      }),
      todo("tdo_tiles", "Check tiles", {
        why_open: "promised",
        source: {
          kind: "meeting",
          id: "mtg_1",
          quote: "Để em check tiles.",
          ref: { kind: "meeting_line", ref: "mtg_1/12" },
        },
      }),
    )
  const needs = new Set(["tdo_reply", "tdo_login", "tdo_idea", ...(review ? ["tdo_mail"] : [])])
  let approvals = [
    {
      id: "apr_1",
      integration: "google",
      label: "work",
      action: "gmail.send",
      action_name: "Send mail",
      asked_by: "pi",
      chat_id: "cht_1",
      title: "Send mail: kai@example.com",
      payload: [
        { label: "To", value: "kai@example.com" },
        { label: "Subject", value: "The contract" },
        { label: "Body", value: "Hi Kai, the contract is attached." },
      ],
      status: "pending",
      used: false,
      created_at: at(-30 * 60_000),
    },
  ]
  let questions = [
    {
      id: "qst_1",
      dispatch_id: "dsp_1",
      task_id: "tsk_1",
      run_id: "run_1",
      mode: "wait",
      addressed_to: "owner",
      text: "Should /health be public or behind a login?",
      options: [{ label: "Public" }, { label: "Behind a login" }],
      status: "open",
      created_at: at(-40 * 60_000),
    },
  ]
  const closed = (t: Todo) => ["done", "dropped", "duplicate"].includes(t.state)
  const sent: { path: string; body: unknown }[] = []

  return {
    sent,
    hold() {
      held = new Promise((done) => (release = done))
    },
    release: () => release(),
    async serve(page: Page) {
      await page.route("**/api/v1/**", async (route) => {
        const request = route.request()
        const url = new URL(request.url())
        const path = url.pathname.replace("/api/v1", "")
        const json = (body: unknown, status = 200) =>
          route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

        if (path === "/inbox") {
          const states = url.searchParams.getAll("state")
          const list = url.searchParams.get("needs_you")
            ? todos.filter((t) => needs.has(t.id) && !closed(t))
            : states.length
              ? todos.filter((t) => states.includes(t.state))
              : todos.filter((t) => !closed(t))

          return json({ data: list, total: list.length })
        }
        if (path === "/inbox/needs-you")
          return json({
            count:
              todos.filter((t) => needs.has(t.id) && !closed(t)).length +
              approvals.length +
              questions.length,
          })
        const acted = /^\/inbox\/(tdo_\w+)(?:\/(\w+))?$/.exec(path)
        if (acted) {
          const t = todos.find((x) => x.id === acted[1])!
          if (!acted[2])
            return json({
              ...t,
              events: [
                {
                  id: 1,
                  at: t.created_at,
                  revision: 1,
                  actor: { kind: "bot", id: "thu-ky" },
                  kind: "created",
                  before: {},
                  after: { title: t.title },
                },
              ],
            })
          sent.push({ path, body: request.postDataJSON() })
          if (acted[2] === "done" && held) await held
          t.revision += 1
          t.updated_at = at(0)
          if (acted[2] === "done") Object.assign(t, { state: "done", closed_at: at(0) })
          if (acted[2] === "accept") t.state = "open"

          return json(t)
        }
        if (path === "/approvals") {
          const chat = url.searchParams.get("chat_id")

          return json({ data: approvals.filter((a) => !chat || a.chat_id === chat) })
        }
        if (path === "/approvals/apr_1/decision") {
          sent.push({ path, body: request.postDataJSON() })
          const answered = { ...approvals[0], status: "approved", scope: "once" }
          approvals = []
          // The to-do that waited for it goes on with its work.
          const waited = todos.find((t) => t.approval_id === "apr_1")
          if (waited) {
            needs.delete(waited.id)
            Object.assign(waited, { state: "doing", revision: waited.revision + 1 })
          }

          return json(answered)
        }
        if (path === "/questions") return json({ data: questions })
        if (path === "/questions/qst_1/answer") {
          sent.push({ path, body: request.postDataJSON() })
          questions = []

          return json({})
        }
        if (path === "/chats/cht_1/stream")
          return route.fulfill({
            status: 200,
            contentType: "text/event-stream",
            body: ": hello\n\n",
          })
        if (path === "/chats/cht_1/messages")
          return json({ data: [], next_after: 0, has_more: false })
        if (path === "/chats/cht_1")
          return json({
            id: "cht_1",
            title: "The contract with Kai",
            bot: "thu-ky",
            working: false,
            coordinator: false,
            created_at: at(-day),
            updated_at: at(-hour),
          })

        switch (path) {
          case "/integrations":
            return json({
              data: [
                {
                  id: "google",
                  name: "Google",
                  connections: [
                    { integration: "google", label: "work", account: "dat@example.com" },
                  ],
                },
              ],
            })
          case "/me":
            return json({ id: "usr_1", name: "admin", kind: "owner" })
          case "/bots":
            return json({
              data: [
                {
                  id: "thu-ky",
                  name: "Thư ký",
                  main: false,
                  persona: "",
                  memory: "",
                  memory_limit: 2200,
                  skills: [],
                  skills_off: [],
                  tools: [],
                  tools_off: [],
                  connections: { limited: false, names: [] },
                  workers: { limited: false, names: [] },
                  scopes: {},
                  created_at: at(-day),
                  updated_at: at(-day),
                },
              ],
            })
          case "/tasks":
            return json({ data: [], runs: [], after: 0 })
          case "/events/stream":
            return route.fulfill({
              status: 200,
              contentType: "text/event-stream",
              body: ": hello\n\n",
            })
          default:
            return json({ data: [] })
        }
      })
    },
  }
}

const list = (page: Page) => page.getByRole("region", { name: "Inbox list" })
const group = (page: Page, name: string) => list(page).getByRole("region", { name, exact: true })
const rowsOf = (page: Page, name: string) => group(page, name).getByTestId("inbox-row")
const item = (page: Page) => page.getByRole("article", { name: "Item" })

// Scenario: the owner opens the Inbox from the sidebar, whose count is what waits on him. Needs you lists the
// Approval and the worker's question first, then the reply to Kai Yong, due today, with its draft, then the
// account to log in again. He opens the reply, reads what Kai said, copies the draft, and marks it done: the next
// item opens. On a phone one pane shows at a time, and Back returns to the list.
test("the owner copies a reply's draft, marks it done, and the next item opens", async ({
  page,
  context,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"])
  const w = world()
  await w.serve(page)
  await page.goto("/")
  await openMenu(page)

  const place = page
    .getByRole("navigation", { name: "Places" })
    .getByRole("link", { name: /Inbox/ })
  await expect(place).toContainText("5")
  await place.click()
  await expect(page).toHaveURL(/\/inbox$/)

  await expect(rowsOf(page, "Needs you")).toHaveText([
    /Send mail: kai@example.com/,
    /Should \/health be public/,
    /Reply to Kai Yong.*WhatsApp · Kai Yong.*Today.*draft/,
    /Log in again: lark:me/,
  ])
  await expect(rowsOf(page, "Working")).toHaveText([/Draft the weekly report.*Thư ký/])
  await expect(rowsOf(page, "Open")).toHaveText([
    /Lan sends the signed contract.*Late · Yesterday/,
    /Send the invoice to ELITEX.*Tomorrow/,
  ])
  await expect(rowsOf(page, "Proposed")).toHaveText([/Book the meeting room/])
  // Snoozed and Done are folded, with their counts.
  await expect(group(page, "Snoozed").getByRole("button", { name: /Snoozed/ })).toHaveAttribute(
    "aria-expanded",
    "false",
  )
  await expect(rowsOf(page, "Done")).toHaveCount(0)
  expect(await page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")).toBe(
    true,
  )
  await shot(page, "inbox")

  await rowsOf(page, "Needs you")
    .filter({ hasText: "Reply to Kai Yong" })
    .getByRole("button")
    .first()
    .click()
  await expect(page).toHaveURL(/item=todo%3Atdo_reply|item=todo:tdo_reply/)
  const detail = item(page)
  await expect(
    detail.getByRole("heading", { name: "Reply to Kai Yong", exact: true }),
  ).toBeVisible()
  await expect(detail).toContainText("3 of 4 in Needs you")
  await expect(detail).toContainText("You answer in this chat; it closes by itself")
  await expect(detail.getByRole("region", { name: "Said" })).toContainText(
    "any news on the contract",
  )
  // No Send: the owner sends the reply himself.
  await expect(detail.getByRole("button", { name: /^Send/ })).toHaveCount(0)
  const phone = (page.viewportSize()?.width ?? 1280) < 768
  if (phone) await expect(list(page)).toBeHidden()
  else await expect(list(page)).toBeVisible()
  await shot(page, "inbox-reply")

  await detail.getByRole("region", { name: "Draft" }).getByRole("button", { name: "Copy" }).click()
  await expect
    .poll(() => page.evaluate<string>("navigator.clipboard.readText()"))
    .toBe("Hi Kai, I will send the signed contract this afternoon.")

  await detail.getByRole("button", { name: "Done", exact: true }).click()
  await expect.poll(() => w.sent.map((s) => s.path)).toContain("/inbox/tdo_reply/done")
  await expect(
    detail.getByRole("heading", { name: "Log in again: lark:me", exact: true }),
  ).toBeVisible()
  await expect(page).toHaveURL(/tdo_login/)

  if (phone) {
    await detail.getByRole("button", { name: "Inbox" }).click()
    await expect(list(page)).toBeVisible()
    await expect(item(page)).toBeHidden()
  }
})

// Scenario: issue #1105. A to-do the Thư ký found in the owner's Gmail opens its thread in Gmail, in the account it
// was read with; one from a meeting opens the meeting at the action's line.
test("a mail opens in Gmail and a meeting's action at its line", async ({ page }) => {
  const w = world({ sources: true })
  await w.serve(page)

  await page.goto("/inbox?item=todo%3Atdo_poc")
  const detail = item(page)
  await expect(
    detail.getByRole("heading", { name: "Xem đề xuất PoC của Alibaba Cloud", exact: true }),
  ).toBeVisible()
  await expect(detail.getByRole("link", { name: "Open in Gmail" })).toHaveAttribute(
    "href",
    "https://mail.google.com/mail/u/dat@example.com/#all/18c2f0a9b1d2e3f4",
  )

  await page.goto("/inbox?item=todo%3Atdo_tiles")
  await expect(detail.getByRole("heading", { name: "Check tiles", exact: true })).toBeVisible()
  const meeting = detail.getByRole("link", { name: "Open meeting" })
  await expect(meeting).toHaveAttribute("href", "/meetings/mtg_1?line=12")
  await shot(page, "inbox-meeting")
})

// Scenario: the owner allows an Approval from the Inbox, the same call as its card in the chat: it leaves Needs
// you and the chat's card, and the next item opens.
test("an Approval allowed in the Inbox leaves Needs you and its chat card", async ({ page }) => {
  const w = world()
  await w.serve(page)

  await page.goto("/chats/cht_1")
  await expect(page.getByRole("region", { name: /Approval needed: Send mail/ })).toBeVisible()

  await page.goto("/inbox?item=approval%3Aapr_1")
  const detail = item(page)
  await expect(
    detail.getByRole("heading", { name: "Send mail: kai@example.com", exact: true }),
  ).toBeVisible()
  await expect(detail.getByRole("region", { name: /Approval needed/ })).toContainText(
    "The contract",
  )
  await shot(page, "inbox-approval")

  await detail.getByRole("button", { name: "Approve", exact: true }).click()
  await expect
    .poll(() => w.sent)
    .toContainEqual({
      path: "/approvals/apr_1/decision",
      body: { decision: "approve" },
    })
  await expect(detail.getByRole("heading", { name: /Should \/health be public/ })).toBeVisible()
  await expect(page.getByTestId("inbox-row").filter({ hasText: "Send mail: kai" })).toHaveCount(0)

  await page.goto("/chats/cht_1")
  await expect(page.getByText("The contract with Kai").first()).toBeVisible()
  await expect(page.getByRole("region", { name: /Approval needed/ })).toHaveCount(0)
})

// Scenario: on a computer the owner works from the keyboard: j and k move through the list, e marks the open item
// done, and the selection moves on. A row's actions show when it is pointed at: a proposal's Accept and Drop.
test("j, k and e work through the list, and a row shows its actions", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 1280) < 768, "keys and hover are a computer's")
  const w = world()
  await w.serve(page)
  await page.goto("/inbox")

  await expect(rowsOf(page, "Needs you")).toHaveCount(4)
  await page.keyboard.press("j")
  await expect(
    item(page).getByRole("heading", { name: "Send mail: kai@example.com", exact: true }),
  ).toBeVisible()
  await page.keyboard.press("j")
  await page.keyboard.press("j")
  await expect(
    item(page).getByRole("heading", { name: "Reply to Kai Yong", exact: true }),
  ).toBeVisible()
  await page.keyboard.press("k")
  await expect(item(page).getByRole("heading", { name: /Should \/health/ })).toBeVisible()
  await page.keyboard.press("j")
  await page.keyboard.press("e")
  await expect.poll(() => w.sent.map((s) => s.path)).toContain("/inbox/tdo_reply/done")
  await expect(
    item(page).getByRole("heading", { name: "Log in again: lark:me", exact: true }),
  ).toBeVisible()

  const proposal = rowsOf(page, "Proposed").first()
  await proposal.hover()
  await expect(proposal.getByRole("button", { name: "Accept" })).toBeVisible()
  await expect(proposal.getByRole("button", { name: "Drop" })).toBeVisible()
  await shot(page, "inbox-row-actions")
  await proposal.getByRole("button", { name: "Accept" }).click()
  await expect.poll(() => w.sent.map((s) => s.path)).toContain("/inbox/tdo_idea/accept")
})

// Scenario: a Bot's to-do waits for the owner's Approval. He opens the to-do and approves in its page: the call is
// sent once and the next item opens, as approving on its row does (issue #1097).
test("approving in a to-do's page moves on to the next item", async ({ page }) => {
  const w = world({ review: true })
  await w.serve(page)
  await page.goto("/inbox?item=todo%3Atdo_mail")
  const detail = item(page)
  await expect(
    detail.getByRole("heading", { name: "Send the contract to Kai", exact: true }),
  ).toBeVisible()
  const card = detail.getByRole("region", { name: "Waits for your approval" })
  await card.getByRole("button", { name: "Approve", exact: true }).click()
  await expect
    .poll(() => w.sent)
    .toContainEqual({ path: "/approvals/apr_1/decision", body: { decision: "approve" } })
  await expect(detail.getByRole("heading", { name: /Should \/health be public/ })).toBeVisible()
  await expect(page).toHaveURL(/qst_1/)
})

// Scenario: the owner presses e twice, and clicks Done, before the first done is answered. One done is sent, no
// error toast shows, and the next item opens (issue #1097).
test("e pressed twice sends done once", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 1280) < 768, "keys are a computer's")
  const w = world()
  await w.serve(page)
  await page.goto("/inbox?item=todo%3Atdo_reply")
  const detail = item(page)
  await expect(
    detail.getByRole("heading", { name: "Reply to Kai Yong", exact: true }),
  ).toBeVisible()

  w.hold()
  await page.keyboard.press("e")
  await expect.poll(() => w.sent.map((s) => s.path)).toContain("/inbox/tdo_reply/done")
  await page.keyboard.press("e")
  await detail.getByRole("button", { name: "Done", exact: true }).click()
  w.release()

  await expect(
    detail.getByRole("heading", { name: "Log in again: lark:me", exact: true }),
  ).toBeVisible()
  expect(w.sent.filter((s) => s.path === "/inbox/tdo_reply/done")).toHaveLength(1)
  await expect(page.getByText("Not changed")).toHaveCount(0)
})

// The pictures the owner looks at, in the light and the dark theme.
for (const theme of ["light", "dark"] as const) {
  test(`the Inbox in the ${theme} theme`, async ({ page }) => {
    await page.addInitScript((choice) => localStorage.setItem("theme", choice), theme)
    const w = world()
    await w.serve(page)
    await page.goto("/inbox")
    await expect(rowsOf(page, "Needs you")).toHaveCount(4)
    await shot(page, `inbox-${theme}`)

    await page.goto("/inbox?item=todo%3Atdo_reply")
    await expect(
      item(page).getByRole("heading", { name: "Reply to Kai Yong", exact: true }),
    ).toBeVisible()
    await expect(item(page).getByRole("region", { name: "Activity" })).toContainText(
      "Thư ký added it",
    )
    await shot(page, `inbox-reply-${theme}`)
  })
}
