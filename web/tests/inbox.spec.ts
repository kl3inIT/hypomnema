import { expect, test, type Page } from "@playwright/test"

import { openMenu } from "./menu.js"
import { pasteImage } from "./paste-image.js"
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

// The meeting and the chat the report for Laura names by their ids (issue #1173).
const laurasMeeting = "mtg_v44xjlbxtmqeyig3jp3drxsany"
const laurasChat = "cht_eag7jejewob7v65py6ydqip4wu"

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
  priority: 0,
  priority_reason: "",
  created_at: at(-5 * hour),
  updated_at: at(-hour),
  ...m,
})

// world is the fake client API. review adds a to-do that waits for the Approval apr_1, which it then holds out of the
// list. hold keeps each done call unanswered until release is called, so a second press finds the first in flight.
// sources adds a to-do from a Gmail thread and one from a meeting's line (issue #1105), and the owner's Google
// account the first was read with. quiet leaves nothing waiting on the owner. The owner's WhatsApp and Zalo accounts
// keep their chats, which name Kai Yong and Lan, and the message Kai sent (issue #1110). triage makes Thư ký the main
// Bot with its triage Routine, last run 12 minutes ago; a run started by hand goes until endScan, which finds a new
// to-do (issue #1115). passed adds a to-do Hypo passed to the BeyondPilot Bot (issue #1118). worked adds one Hypo
// works on, with its steps and its report, and one it closed as done (issue #1143). projects keeps the owner's
// departments VMAP and BeyondPilot, in that order in the sidebar, the BeyondPilot Bot in the second, which are the
// Inbox's projects (issue #1188), puts the reply to Kai in BeyondPilot and the invoice in VMAP, and gives the open
// items their priorities (issue #1157). waiting moves the contract Lan sends to Waiting on others, with the message
// she took it in, and adds a meeting's action Hà took on, waiting on her from its line (issue #1142).
// described adds the owner's own report for Laura, two days late, its
// description in Markdown sections naming a meeting and a chat by their ids (issue #1173).
type SentFile = { path: string; name: string; type?: string; size: number }

function world({
  review = false,
  sources = false,
  quiet = false,
  triage = false,
  passed = false,
  worked = false,
  projects: withProjects = false,
  waiting = false,
  described = false,
}: {
  review?: boolean
  sources?: boolean
  quiet?: boolean
  triage?: boolean
  passed?: boolean
  worked?: boolean
  projects?: boolean
  waiting?: boolean
  described?: boolean
} = {}) {
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
        ref: { kind: "whatsapp_message", ref: "3EB0C1" },
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
        id: "2261258766901429891",
        account: "main",
        ref: { kind: "zalo_message", ref: "z1" },
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
  const departments: { id: string; name: string; position: number }[] = withProjects
    ? [
        { id: "dep_vmap", name: "VMAP", position: 0 },
        { id: "dep_bp", name: "BeyondPilot", position: 1 },
      ]
    : []
  if (withProjects) {
    const set = (id: string, m: object) =>
      Object.assign(
        todos.find((t) => t.id === id)!,
        m,
      )
    set("tdo_reply", {
      priority: 1,
      priority_reason: "Kai needs it before Friday",
      project_id: "dep_bp",
    })
    set("tdo_invoice", { priority: 3, project_id: "dep_vmap" })
    set("tdo_chase", { priority: 2, priority_reason: "Lan is a day late" })
    todos.push(
      todo("tdo_report", "Send Laura the weekly report", {
        priority: 1,
        priority_reason: "Due today",
        project_id: "dep_vmap",
        due: { kind: "date", date: dayIn(3), zone: "Asia/Ho_Chi_Minh" },
        source: { kind: "owner" },
      }),
    )
  }
  if (described)
    todos.push(
      todo("tdo_laura", "Báo cáo + kế hoạch hệ sinh thái Tasco quanh Cốc Cốc cho cô Laura", {
        due: { kind: "date", date: dayIn(-2), zone: "Asia/Ho_Chi_Minh" },
        why_open: "promised",
        created_by: { kind: "owner" },
        notes: [
          "## Source",
          "Zalo, chat Laura Nguyen. 17/11 cô nhờ nghiên cứu VETC, Goong, Cốc Cốc.",
          `20/11 anh hứa: "tầm chiều tối em gửi cô cái báo cáo". Chat của item: ${laurasChat}`,
          "",
          "## Meeting",
          `25/11 (${laurasMeeting}, dòng 69): Tasco mua Cốc Cốc; cô dặn "ngó về Cốc Cốc".`,
          "",
          "## Plan",
          "1. Tasco đang có gì: VETC, VMAP, Goong",
          "2. Cốc Cốc: người dùng, công nghệ",
          "3. Ý kiến về Cốc Cốc làm nền",
        ].join("\n"),
      }),
    )
  const needs = new Set(
    quiet ? [] : ["tdo_reply", "tdo_login", "tdo_idea", ...(review ? ["tdo_mail"] : [])],
  )
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
  ].filter(() => !quiet)
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
  ].filter(() => !quiet)
  if (passed)
    todos.push(
      todo("tdo_pilot", "Send Kai the pilot's report", {
        assignee: { kind: "bot", name: "bot_beyond" },
        passed_by: "bot_main",
        chat_id: "cht_beyond",
        source: { kind: "owner" },
      }),
    )
  const fromElitex = {
    kind: "lark",
    account: "me",
    quote: "Anh gửi giúp em invoice tháng 11 nhé",
    ref: { kind: "lark_message", ref: "om_2", label: "ELITEX" },
  }
  const pr = {
    kind: "pull_request",
    ref: "https://github.com/kl3inIT/elitex/pull/12",
    label: "#12",
  }
  if (worked)
    todos.push(
      todo("tdo_work", "Send the November invoice to ELITEX", {
        state: "doing",
        assignee: { kind: "bot", name: "bot_main" },
        chat_id: "cht_work",
        work: {
          attempt: "att_2",
          kind: "chat",
          id: "cht_work",
          bot: "bot_main",
          started_at: at(-48 * 60_000),
        },
        draft: "Hi ELITEX team, the November invoice is attached.",
        source: fromElitex,
      }),
      todo("tdo_shipped", "Fix the invoice total on the ELITEX site", {
        state: "done",
        assignee: { kind: "bot", name: "bot_main" },
        chat_id: "cht_shipped",
        closed_at: at(-5 * 60_000),
        close_reason: "The total is fixed; PR #12 is merged.",
        evidence: [pr],
        source: fromElitex,
      }),
    )
  // report is a Bot's long comment, its sections folded in <details>, as CodeRabbit's summary comment.
  const report = [
    "Drafted the reply and opened PR #12 for the total.",
    "",
    "<details>",
    "<summary>What was done</summary>",
    "",
    "- Read the November figures from the sheet.",
    "- Fixed the total's rounding in `invoice.ts`.",
    "</details>",
    "",
    "<details>",
    "<summary>Risks</summary>",
    "",
    "The October invoice used the old rounding.",
    "</details>",
    "",
    "<details>",
    "<summary>Checks</summary>",
    "",
    "Unit tests pass; the PR's CI is green.",
    "</details>",
  ].join("\n")
  const workedOn = (start: number) => [
    {
      id: start,
      at: at(-50 * 60_000),
      revision: start,
      actor: { kind: "owner" },
      kind: "edited",
      before: {},
      after: { assignee_kind: "bot", assignee: "bot_main" },
    },
    {
      id: start + 1,
      at: at(-50 * 60_000),
      revision: start + 1,
      actor: { kind: "owner" },
      kind: "commented",
      before: {},
      after: { text: "Do it, then draft a reply to ELITEX for me to send." },
    },
    {
      id: start + 2,
      at: at(-48 * 60_000),
      revision: start + 2,
      actor: { kind: "bot", id: "bot_main" },
      kind: "state",
      before: { state: "open" },
      after: { state: "doing", work_kind: "chat" },
    },
    {
      id: start + 3,
      at: at(-40 * 60_000),
      revision: start + 3,
      actor: { kind: "bot", id: "bot_main" },
      kind: "edited",
      before: {},
      after: { notes: "The total rounds the wrong way." },
    },
    {
      id: start + 4,
      at: at(-30 * 60_000),
      revision: start + 4,
      actor: { kind: "bot", id: "bot_main" },
      kind: "edited",
      before: {},
      after: { evidence: [pr] },
    },
    {
      id: start + 5,
      at: at(-25 * 60_000),
      revision: start + 5,
      actor: { kind: "bot", id: "bot_main" },
      kind: "edited",
      before: {},
      after: { draft: "Hi ELITEX team, the November invoice is attached." },
    },
    {
      id: start + 6,
      at: at(-20 * 60_000),
      revision: start + 6,
      actor: { kind: "bot", id: "bot_main" },
      kind: "commented",
      before: {},
      after: { text: report },
    },
  ]
  // createdWith is what an item's created event holds besides its title.
  const createdWith: Record<string, Record<string, unknown>> = {}
  if (waiting) {
    Object.assign(
      todos.find((t) => t.id === "tdo_chase")!,
      { why_open: "waiting", revision: 2 },
    )
    todos.push(
      todo("tdo_deck", "Hà sends the tiles report", {
        why_open: "waiting",
        assignee: { kind: "person", name: "Hà" },
        due: { kind: "date", date: dayIn(2), zone: "Asia/Ho_Chi_Minh" },
        source: { kind: "meeting", id: "mtg_1", quote: "Để em check tiles rồi gửi anh báo cáo." },
      }),
    )
    createdWith.tdo_deck = {
      assignee_kind: "person",
      assignee: "Hà",
      why_open: "waiting",
      waiting_on: {
        by: "Hà",
        quote: "Để em check tiles rồi gửi anh báo cáo.",
        ref: { kind: "meeting_line", ref: "mtg_1/12" },
      },
    }
  }
  const closed = (t: Todo) => ["done", "dropped", "duplicate"].includes(t.state)
  const sent: { path: string; body: unknown }[] = []
  // threads are each to-do's history past its creation: the hand-over, the comments (issue #1111).
  const threads: Record<string, Record<string, unknown>[]> = worked
    ? {
        tdo_work: workedOn(2),
        tdo_shipped: [
          ...workedOn(2),
          {
            id: 9,
            at: at(-5 * 60_000),
            revision: 9,
            actor: { kind: "bot", id: "bot_main" },
            kind: "state",
            before: { state: "doing" },
            after: { state: "done", close_reason: "The total is fixed; PR #12 is merged." },
          },
        ],
      }
    : passed
      ? {
          tdo_pilot: [
            {
              id: 2,
              at: at(-30 * 60_000),
              revision: 2,
              actor: { kind: "owner" },
              kind: "edited",
              before: {},
              after: { assignee_kind: "bot", assignee: "bot_main" },
            },
            {
              id: 3,
              at: at(-20 * 60_000),
              revision: 3,
              actor: { kind: "bot", id: "bot_main" },
              kind: "edited",
              before: {},
              after: { assignee_kind: "bot", assignee: "bot_beyond", passed_by: "bot_main" },
            },
          ],
        }
      : {}
  if (waiting)
    threads.tdo_chase = [
      {
        id: 2,
        at: at(-3 * hour),
        revision: 2,
        actor: { kind: "bot", id: "thu-ky" },
        kind: "edited",
        before: { assignee_kind: "me", assignee: "", why_open: "asked_directly" },
        after: {
          assignee_kind: "person",
          assignee: "Lan",
          why_open: "waiting",
          waiting_on: {
            by: "Lan",
            quote: "Em nhận nhé, chiều em gửi anh bản ký.",
            ref: { kind: "zalo_message", ref: "z2" },
          },
        },
      },
    ]
  const remember = (t: Todo, event: Record<string, unknown>) => {
    const thread = (threads[t.id] ??= [])
    thread.push({ id: thread.length + 2, at: at(0), revision: t.revision, before: {}, ...event })
  }
  const run = (id: string, m: object = {}) => ({
    id,
    routine_id: "rtn_triage",
    started_by: "schedule",
    state: "succeeded",
    started_at: at(-13 * 60_000),
    ended_at: at(-12 * 60_000),
    output: "",
    error: "",
    ...m,
  })
  let lastRun = run("rrn_1")
  const uploads = new Map<string, { path: string; bot: string | null; pieces: Buffer[] }>()
  const files = new Map<string, { bot: string | null; bytes: Buffer }>()

  return {
    sent,
    files,
    hold() {
      held = new Promise((done) => (release = done))
    },
    release: () => release(),
    endScan() {
      lastRun = { ...lastRun, state: "succeeded", ended_at: at(0) }
      todos.push(
        todo("tdo_new", "Answer Hà about the tiles", {
          why_open: "asked_directly",
          created_at: at(0),
          updated_at: at(0),
        }),
      )
      needs.add("tdo_new")
    },
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
        if (path === "/departments") return json({ data: departments })
        if (path === "/inbox/needs-you")
          return json({
            count:
              todos.filter((t) => needs.has(t.id) && !closed(t)).length +
              approvals.length +
              questions.length,
          })
        const workspace = /^\/chats\/(cht_\w+)\/workspaces\/bot\/(.+)$/.exec(path)
        if (workspace) {
          const body = request.postData() ? request.postDataJSON() : {}
          const bot = url.searchParams.get("bot")
          switch (workspace[2]) {
            case "folders":
              return json({ path: body.path }, 201)
            case "tree":
              return json({ entries: [], has_more: false })
            case "uploads": {
              if (body.path.endsWith("/blocked.txt"))
                return json({ code: "unavailable", message: "The Bot's folder is full." }, 503)
              const upload = `u${uploads.size + 1}`
              uploads.set(upload, { path: body.path, bot, pieces: [] })

              return json({ path: body.path, upload }, 201)
            }
            case "uploads/chunk":
              uploads.get(body.upload)?.pieces.push(Buffer.from(body.data, "base64"))

              return route.fulfill({ status: 204 })
            case "uploads/commit": {
              const up = uploads.get(body.upload)!
              files.set(up.path, { bot: up.bot, bytes: Buffer.concat(up.pieces) })

              return json({ path: up.path, sha256: "x" })
            }
            case "raw": {
              const file = files.get(url.searchParams.get("path") ?? "")

              return file
                ? route.fulfill({ contentType: "image/png", body: file.bytes })
                : json({ code: "not_found", message: "File not found." }, 404)
            }
          }
        }
        const acted = /^\/inbox\/(tdo_\w+)(?:\/(\w+))?$/.exec(path)
        if (acted) {
          const t = todos.find((x) => x.id === acted[1])!
          // An edit (issue #1157): a priority set by the owner has no reason; an empty project takes it out.
          if (!acted[2] && request.method() === "PATCH") {
            const body = request.postDataJSON() as Record<string, unknown>
            sent.push({ path, body })
            t.revision += 1
            if (typeof body.priority === "number")
              Object.assign(t, { priority: body.priority, priority_reason: "" })
            if (typeof body.project_id === "string") t.project_id = body.project_id || undefined
            // Take it back (issue #1142): his again, waiting on no one.
            if (body.assignee) {
              Object.assign(t, { assignee: body.assignee, why_open: undefined })
              remember(t, {
                actor: { kind: "owner" },
                kind: "edited",
                before: { assignee_kind: "person", assignee: "Lan", why_open: "waiting" },
                after: { assignee_kind: "me", assignee: "", why_open: null },
              })
            }

            return json(t)
          }
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
                  after: { title: t.title, ...createdWith[t.id] },
                },
                ...(threads[t.id] ?? []),
              ],
            })
          const body = request.postDataJSON() as Record<string, string> & {
            attachments?: SentFile[]
          }
          sent.push({ path, body })
          // Issue #1111: the hand-over gives the item to the Bot with its chat and the prompt as its first comment;
          // a comment is added to the thread; Send closes the item with the message sent.
          if (acted[2] === "hand") {
            t.revision += 1
            Object.assign(t, {
              assignee: { kind: "bot", name: body.bot },
              chat_id: body.chat_id ?? "cht_item",
            })
            remember(t, {
              actor: { kind: "owner" },
              kind: "edited",
              after: { assignee_kind: "bot", assignee: body.bot },
            })
            if (body.prompt || body.attachments?.length) {
              t.revision += 1
              remember(t, {
                actor: { kind: "owner" },
                kind: "commented",
                after: { text: body.prompt ?? "", attachments: body.attachments, chat: t.chat_id },
              })
            }

            return json(t)
          }
          if (acted[2] === "comments") {
            t.revision += 1
            remember(t, {
              actor: { kind: "owner" },
              kind: "commented",
              after: { text: body.text, attachments: body.attachments, chat: t.chat_id },
            })

            return json(t)
          }
          if (acted[2] === "send") {
            const sentLink = {
              kind: "whatsapp_sent",
              ref: "3EB0SENT",
              account: "main",
              chat: "6591234567@s.whatsapp.net",
            }
            t.revision += 1
            Object.assign(t, { state: "done", closed_at: at(0), evidence: [sentLink] })

            return json({ sent: sentLink, todo: t })
          }
          if (acted[2] === "done" && held) await held
          t.revision += 1
          t.updated_at = at(0)
          if (acted[2] === "done") Object.assign(t, { state: "done", closed_at: at(0) })
          if (acted[2] === "accept") t.state = "open"
          if (acted[2] === "reopen") {
            remember(t, {
              actor: { kind: "owner" },
              kind: "state",
              before: { state: t.state },
              after: { state: "open" },
            })
            Object.assign(t, { state: "open", closed_at: undefined, close_reason: "" })
          }

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
        if (path === "/routines")
          return json({
            data: triage
              ? [
                  {
                    id: "rtn_triage",
                    bot: "thu-ky",
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
                    created_by: "owner",
                    can_undo: false,
                    created_at: at(-day),
                    updated_at: at(-day),
                    last_run: lastRun,
                    grants: [],
                  },
                ]
              : [],
          })
        if (path === "/routines/rtn_triage/runs") {
          sent.push({ path, body: null })
          lastRun = run("rrn_2", {
            started_by: "manual",
            state: "running",
            started_at: at(0),
            ended_at: undefined,
          })

          return json(lastRun)
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
                {
                  id: "whatsapp",
                  name: "WhatsApp",
                  kind: "device",
                  connections: [
                    { integration: "whatsapp", label: "main", account: "+84369230426" },
                  ],
                },
                {
                  id: "zalo",
                  name: "Zalo",
                  kind: "device",
                  connections: [{ integration: "zalo", label: "main", account: "Hồng Đạt" }],
                },
              ],
            })
          case "/integrations/whatsapp/connections/main/chats":
            return json({
              data: [
                {
                  id: "6591234567@s.whatsapp.net",
                  kind: "dm",
                  name: "Kai Yong",
                  last_message_at: at(-2 * hour),
                },
              ],
            })
          case "/integrations/zalo/connections/main/chats":
            return json({
              data: [
                { id: "2261258766901429891", kind: "dm", name: "Lan", last_message_at: at(-day) },
              ],
            })
          case "/integrations/whatsapp/connections/main/messages":
            return json({
              data:
                url.searchParams.get("id") === "3EB0C1"
                  ? [
                      {
                        id: "3EB0C1",
                        chat: "6591234567@s.whatsapp.net",
                        chat_name: "Kai Yong",
                        sender: "6591234567@s.whatsapp.net",
                        sender_name: "Kai Yong",
                        at: at(-2 * hour),
                        from_me: false,
                        text: "Hi Dat, any news on the contract? We need it before Friday.",
                        edited: false,
                        revoked: false,
                        media_kept: false,
                      },
                    ]
                  : [],
            })
          case "/integrations/zalo/connections/main/messages":
            return json({
              data:
                url.searchParams.get("id") === "z2"
                  ? [
                      {
                        id: "z2",
                        chat: "2261258766901429891",
                        chat_name: "Lan",
                        sender: "lan",
                        sender_name: "Lan Phạm",
                        at: at(-3 * hour),
                        from_me: false,
                        text: "Em nhận nhé, chiều em gửi anh bản ký.",
                        edited: false,
                        revoked: false,
                        media_kept: false,
                      },
                    ]
                  : [],
            })
          case `/meetings/${laurasMeeting}`:
            return json({
              meeting: { id: laurasMeeting, title: "Họp với cô Laura về Cốc Cốc", speakers: [] },
              lines: [],
              items: [],
              suggestions: [],
            })
          case `/chats/${laurasChat}`:
            return json({
              id: laurasChat,
              title: "Inbox: báo cáo cho cô Laura",
              bot: "bot_main",
              working: false,
              coordinator: false,
              created_at: at(-day),
              updated_at: at(-hour),
            })
          case "/meetings/mtg_1":
            return json({
              meeting: {
                id: "mtg_1",
                title: "Tiles review",
                speakers: [
                  {
                    track: "mic",
                    label: "s1",
                    owner: false,
                    name: "Hà",
                    named: true,
                    talk_ms: 1,
                    share: 1,
                  },
                ],
              },
              lines: [
                {
                  n: 12,
                  track: "mic",
                  start_ms: 0,
                  end_ms: 1,
                  speaker: "s1",
                  heard: "s1",
                  text: "Để em check tiles.",
                },
              ],
              items: [],
              suggestions: [],
            })
          case "/me":
            return json({ id: "usr_1", name: "admin", kind: "owner" })
          case "/bots":
            return json({
              data: [
                {
                  id: "bot_main",
                  name: "Hypo",
                  // The main Bot, unless triage makes Thư ký the main Bot with its triage Routine (issue #1115).
                  main: !triage,
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
                {
                  id: "bot_beyond",
                  name: "BeyondPilot",
                  main: false,
                  ...(withProjects ? { department: "dep_bp" } : {}),
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
                {
                  id: "thu-ky",
                  name: "Thư ký",
                  main: triage,
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

test.describe("iPhone Home Screen layout", () => {
  test.use({ viewport: { width: 393, height: 852 }, deviceScaleFactor: 1 })

  test("the top band clears both Inbox headers and the item actions fit on one row", async ({
    page,
    isMobile,
  }) => {
    test.skip(!isMobile, "iPhone layout")
    await world({ triage: true }).serve(page)
    const band = page.locator("body > .ios-top-edge-band")
    const menu = list(page).getByRole("button", { name: "Menu", exact: true })
    const detailHeader = item(page).locator("header").first()
    const reply = "/inbox?item=todo%3Atdo_reply"

    // Map standalone to a media feature Chromium can emulate. Keep the WebKit
    // feature query in place until the simulated iOS visits.
    let ios = false
    await page.route("**/assets/*.css", async (route) => {
      const response = await route.fetch()
      let body = (await response.text()).replace(
        /display-mode\s*:\s*standalone/g,
        "prefers-reduced-motion: reduce",
      )
      if (ios) {
        body = body.replace(
          /-webkit-touch-callout\s*:\s*none(?=\)\s*{\s*\.ios-top-edge-band)/g,
          "display: block",
        )
      }
      await route.fulfill({ response, body })
    })
    await page.emulateMedia({ reducedMotion: "reduce" })
    await page.goto("/inbox")
    await expect(menu).toBeVisible()
    await expect(band).toBeHidden()
    const tabMenuTop = (await menu.boundingBox())!.y

    ios = true
    for (const theme of ["light", "dark"] as const) {
      await page.evaluate((choice) => globalThis.localStorage.setItem("theme", choice), theme)
      await page.goto("/inbox")
      await expect(menu).toBeVisible()
      await expect(band).toHaveCSS("position", "fixed")
      await expect(band).toHaveCSS("pointer-events", "none")
      const pageColor = await page.evaluate<string>(
        "getComputedStyle(document.body).backgroundColor",
      )
      await expect(band).toHaveCSS("background-color", pageColor)
      await expect(page.locator("html")).toHaveCSS("background-color", pageColor)
      expect(await band.boundingBox()).toEqual({ x: 0, y: 0, width: 393, height: 12 })
      expect((await menu.boundingBox())!.y).toBeGreaterThanOrEqual(30)
      await expect(rowsOf(page, "Needs you").first()).toBeVisible()
      await shot(page, `inbox-standalone-list-${theme}`)

      if (theme === "light") {
        await openMenu(page)
        // The band must leave the top of the sidebar's backdrop visually intact.
        const clip = { x: 350, y: 0, width: 32, height: 12 }
        const backdrop = await page.screenshot({ clip, animations: "disabled" })
        const withoutBand = await page.screenshot({
          clip,
          animations: "disabled",
          style: ".ios-top-edge-band { visibility: hidden }",
        })
        expect(backdrop.equals(withoutBand), "the band must not paint above the backdrop").toBe(
          true,
        )
      }

      await page.goto(reply)
      const position = detailHeader.getByText(/\d+ of \d+ in Needs you/)
      await expect(position).toBeVisible()
      const positionBox = (await position.boundingBox())!
      expect(positionBox.y).toBeGreaterThanOrEqual(30)
      for (const name of ["Inbox", "Previous", "Next", "Snooze", "Drop", "Done"]) {
        const button = detailHeader.getByRole("button", { name, exact: true })
        await expect(button).toBeVisible()
        const box = (await button.boundingBox())!
        expect(box.y).toBeGreaterThanOrEqual(30)
        expect(box.x + box.width).toBeLessThanOrEqual(393)
        expect(box.y + box.height / 2).toBeCloseTo(positionBox.y + positionBox.height / 2, 0)
      }
      await expect(item(page).getByRole("heading", { name: "Reply to Kai Yong" })).toBeVisible()
      await shot(page, `inbox-standalone-item-${theme}`)
    }

    await page.emulateMedia({ reducedMotion: "no-preference" })
    await page.goto("/inbox")
    await expect(menu).toBeVisible()
    await expect(band).toBeHidden()
    expect((await menu.boundingBox())!.y).toBe(tabMenuTop)
    await page.goto(reply)
    await expect(detailHeader.getByRole("button", { name: "Inbox" })).toBeVisible()
    expect(
      (await detailHeader.getByRole("button", { name: "Inbox" }).boundingBox())!.y,
    ).toBeLessThan(30)
  })
})

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

  // A row says who and where by name, read from the kept chats and the message, never the account's number; what
  // was said on its second line; its date labelled; the time of the message on its right.
  await expect(rowsOf(page, "Needs you")).toHaveText([
    /Send mail: kai@example.com/,
    /Should \/health be public/,
    /^WhatsAppReply to Kai Yong2h agoHi Dat, any news on the contract.*Kai YongDue todaydraft$/,
    /Log in again: lark:me/,
  ])
  const kai = rowsOf(page, "Needs you").filter({ hasText: "Reply to Kai Yong" })
  await expect(kai).not.toContainText("6591234567")
  await expect(kai.locator('[data-source="whatsapp"]')).toHaveText("WhatsApp")
  await expect(rowsOf(page, "Working")).toHaveText([/Draft the weekly report.*Thư ký/])
  await expect(rowsOf(page, "Open")).toHaveText([
    /Send the invoice to ELITEX.*ELITEX.*Due tomorrow/,
  ])
  await expect(rowsOf(page, "Proposed")).toHaveText([/Book the meeting room/])
  // Waiting on others, Snoozed and Done are folded, with their counts: what Lan sends is hers until its chase day.
  await expect(
    group(page, "Waiting on others").getByRole("button", { name: /Waiting on others/ }),
  ).toHaveAttribute("aria-expanded", "false")
  await expect(rowsOf(page, "Waiting on others")).toHaveCount(0)
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
    "Kai YongHi Dat, any news on the contract",
  )
  await expect(detail).toContainText("Kai Yong")
  await expect(detail).not.toContainText("WhatsApp ›")
  await expect(detail.locator('[data-source="whatsapp"]')).toHaveAttribute("aria-label", "WhatsApp")
  // Send is the owner's own, beside Copy (issue #1111); a Bot never sends.
  await expect(
    detail.getByRole("region", { name: "Draft" }).getByRole("button", { name: "Send" }),
  ).toBeVisible()
  const phone = (page.viewportSize()?.width ?? 1280) < 768
  if (phone) await expect(list(page)).toBeHidden()
  else await expect(list(page)).toBeVisible()
  await shot(page, "inbox-reply")

  await detail.getByRole("region", { name: "Draft" }).getByRole("button", { name: "Copy" }).click()
  await expect
    .poll(() => page.evaluate<string>("navigator.clipboard.readText()"))
    .toBe("Hi Kai, I will send the signed contract this afternoon.")
  await expect(
    detail.getByRole("region", { name: "Draft" }).getByRole("button", { name: "Copied" }),
  ).toBeVisible()

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
  // The meeting by its name, who said the line, and no empty field nor a note on a draft it does not need.
  // The source line is the meeting's icon, labelled Meeting, then its name, not "Meeting › Tiles review" (issue #1137).
  await expect(detail.locator('[data-source="meeting"]')).toHaveAttribute("aria-label", "Meeting")
  await expect(detail).toContainText("Tiles review")
  await expect(detail).not.toContainText("Meeting ›")
  await expect(detail.getByRole("region", { name: "Said" })).toContainText(
    "Hà · Tiles reviewĐể em check tiles.",
  )
  await expect(detail).not.toContainText("Not said")
  await expect(detail.getByRole("region", { name: "Draft" })).toHaveCount(0)
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
  // The first item of Needs you is open by itself; every row is bold until it is opened once.
  await expect(
    item(page).getByRole("heading", { name: "Send mail: kai@example.com", exact: true }),
  ).toBeVisible()
  const question = rowsOf(page, "Needs you").filter({ hasText: "Should /health" })
  await expect(question).toHaveAttribute("data-unread", "true")
  await page.keyboard.press("j")
  await expect(item(page).getByRole("heading", { name: /Should \/health/ })).toBeVisible()
  await page.keyboard.press("j")
  await expect(
    item(page).getByRole("heading", { name: "Reply to Kai Yong", exact: true }),
  ).toBeVisible()
  await expect(question).not.toHaveAttribute("data-unread")
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

// Scenario: issue #1143, Logic 1 and 2 (the hand-over of issue #1111). The invoice is not handed yet: its comments
// show where it came from and a composer that reads "Message Hypo…" with Hand to Hypo. The owner writes and sends:
// the item is handed to the main Bot with his text as the prompt, his comment shows as a card, Hypo has the item,
// and the button reads Send; what he writes next is a comment to Hypo.
test("the owner hands an item to Hypo by writing, and his comment shows", async ({ page }) => {
  const w = world()
  await w.serve(page)
  await page.goto("/inbox?item=todo%3Atdo_invoice")
  const detail = item(page)
  await expect(
    detail.getByRole("heading", { name: "Send the invoice to ELITEX", exact: true }),
  ).toBeVisible()
  await expect(detail.getByRole("button", { name: /^Hand to/ })).toHaveCount(1)
  await expect(page.getByRole("dialog")).toHaveCount(0)

  const comments = detail.getByRole("region", { name: "Comments" })
  await expect(comments).toContainText("Thư ký added it from Lark · ELITEX")
  const box = comments.getByRole("textbox", { name: "Message" })
  await expect(box).toHaveAttribute("placeholder", "Message Hypo…")
  await box.fill("Do it now, then draft a reply to ELITEX for me to send")
  await comments.getByRole("button", { name: "Hand to Hypo" }).click()

  await expect
    .poll(() => w.sent)
    .toContainEqual({
      path: "/inbox/tdo_invoice/hand",
      body: {
        revision: 1,
        bot: "bot_main",
        prompt: "Do it now, then draft a reply to ELITEX for me to send",
      },
    })
  await expect(comments).toContainText("You gave it to Hypo")
  await expect(comments.getByRole("article", { name: "Comment by You" })).toContainText(
    "Do it now, then draft a reply to ELITEX for me to send",
  )
  await expect(detail).toContainText(/Who\s*Hypo/)
  await expect(
    comments.getByRole("link", { name: "Open Hypo's chat for this item" }),
  ).toHaveAttribute("href", "/chats/cht_item")

  await box.fill("Use the November rate.")
  await comments.getByRole("button", { name: "Send", exact: true }).click()
  await expect
    .poll(() => w.sent)
    .toContainEqual({
      path: "/inbox/tdo_invoice/comments",
      body: { text: "Use the November rate." },
    })
  await expect(comments).toContainText("Use the November rate.")
  await shot(page, "inbox-thread")
})

// The first file goes to the chat the Inbox reserves for Hand. Later uploads
// use that live chat, and a pasted screenshot can be sent by itself and opened.
test("the Inbox hands over files and shows a pasted screenshot in an owner comment", async ({
  page,
}) => {
  const w = world()
  await w.serve(page)
  await page.goto("/inbox?item=todo%3Atdo_invoice")
  const comments = item(page).getByRole("region", { name: "Comments" })
  const picker = comments.getByLabel("Files to attach")
  await expect(comments.getByRole("button", { name: "Attach files" })).toBeVisible()
  await picker.setInputFiles({
    name: "brief.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("Check the November invoice"),
  })
  const chips = comments.getByLabel("Files to send")
  await expect(chips.getByText("brief.txt")).toBeVisible()
  await comments.getByRole("button", { name: "Hand to Hypo" }).click()

  const link = comments.getByRole("link", { name: "Open Hypo's chat for this item" })
  await expect(link).toHaveAttribute("href", /^\/chats\/cht_[a-z2-7]{26}$/)
  const chat = (await link.getAttribute("href"))?.split("/").at(-1)
  const folder = `attachments/${chat}`
  expect(w.files.get(`${folder}/brief.txt`)).toEqual({
    bot: "bot_main",
    bytes: Buffer.from("Check the November invoice"),
  })
  expect(w.sent[0]?.body).toMatchObject({
    chat_id: chat,
    attachments: [{ name: "brief.txt", path: `${folder}/brief.txt` }],
  })
  await expect(comments.getByRole("link", { name: "Download brief.txt" })).toBeVisible()

  await picker.setInputFiles({
    name: "blocked.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("cannot upload"),
  })
  await expect(chips.getByText("Not uploaded")).toBeVisible()
  const send = comments.getByRole("button", { name: "Send", exact: true })
  await send.click()
  await expect(comments.getByRole("alert")).toContainText("The Bot's folder is full.")
  expect(w.sent).toHaveLength(1)
  await chips.getByRole("button", { name: "Remove blocked.txt" }).click()
  await comments.getByRole("button", { name: "Dismiss" }).click()

  await pasteImage(page)
  await page.evaluate(`(() => {
    const data = new DataTransfer()
    data.items.add(new File(["November rate"], "notes.txt", { type: "text/plain" }))
    document.querySelector('textarea[aria-label="Message"]').dispatchEvent(
      new DragEvent("drop", { dataTransfer: data, bubbles: true, cancelable: true }),
    )
  })()`)
  await expect(chips.getByText("notes.txt")).toBeVisible()
  await expect(chips.getByText("image.png")).toBeVisible()
  await expect(chips.locator("img")).toBeVisible()
  await expect(chips.getByText("Uploading", { exact: false })).toHaveCount(0)
  await send.scrollIntoViewIfNeeded()
  await shot(page, "inbox-1245-chips")
  await send.click()

  const preview = comments.getByRole("button", { name: "Preview image.png" })
  await expect(preview.locator("img")).toBeVisible()
  await preview.scrollIntoViewIfNeeded()
  await shot(page, "inbox-1245-comment")
  expect(w.sent[1]?.body).toMatchObject({
    text: "",
    attachments: [
      { name: "image.png", path: `${folder}/image.png`, type: "image/png" },
      { name: "notes.txt", path: `${folder}/notes.txt`, type: "text/plain" },
    ],
  })
  await preview.click()
  await expect(page.getByRole("dialog").getByRole("img", { name: "image.png" })).toBeVisible()
})

// Scenario: issue #1143, Logic 6. Hypo closed the fix as done: its line is the violet "closed it as done" with its
// evidence; writing again reopens it and hands it back to Hypo with the text.
test("writing on a done item reopens it and hands it back", async ({ page }) => {
  const w = world({ worked: true })
  await w.serve(page)
  await page.goto("/inbox?item=todo%3Atdo_shipped")
  const comments = item(page).getByRole("region", { name: "Comments" })
  await expect(comments).toContainText("Hypo closed it as done")
  await expect(comments).toContainText("The total is fixed; PR #12 is merged.")

  await comments.getByRole("textbox", { name: "Message" }).fill("The October one too, please.")
  await comments.getByRole("button", { name: "Reopen and send" }).click()
  await expect
    .poll(() => w.sent.map((s) => s.path))
    .toEqual(["/inbox/tdo_shipped/reopen", "/inbox/tdo_shipped/hand"])
  expect(w.sent[1]?.body).toEqual({
    revision: 2,
    bot: "bot_main",
    prompt: "The October one too, please.",
  })
  await expect(comments).toContainText("You reopened it")
})

// Scenario: issue #1118, section 4. Hypo passed an item the owner handed it to the BeyondPilot Bot: the item shows
// who has it and through whom, and its thread shows each hand-over.
test("an item the main Bot passed shows who has it through whom", async ({ page }) => {
  const w = world({ passed: true })
  await w.serve(page)
  await page.goto("/inbox?item=todo%3Atdo_pilot")
  const detail = item(page)
  await expect(
    detail.getByRole("heading", { name: "Send Kai the pilot's report", exact: true }),
  ).toBeVisible()

  await expect(detail).toContainText(/Who\s*Hypo → BeyondPilot/)
  const thread = detail.getByRole("region", { name: "Comments" })
  await expect(thread).toContainText("You gave it to Hypo")
  await expect(thread).toContainText("Hypo passed it to BeyondPilot")
  await shot(page, "inbox-passed")
})

// Scenario: issue #1111, section 3. The owner edits the reply to Kai and presses Send: a confirmation names Kai
// Yong and WhatsApp and shows the text; once he confirms, the text as edited is sent with the revision he read, and
// nothing is sent before.
test("the owner sends a draft after confirming", async ({ page }) => {
  const w = world()
  await w.serve(page)
  await page.goto("/inbox?item=todo%3Atdo_reply")
  const detail = item(page)
  const draft = detail.getByRole("region", { name: "Draft" })
  await expect(draft).toContainText("Hi Kai, I will send the signed contract this afternoon.")

  await draft.getByRole("button", { name: "Edit" }).click()
  await draft
    .getByRole("textbox", { name: "Draft" })
    .fill("Hi Kai, the signed contract comes this afternoon.")
  await draft.getByRole("button", { name: "Send" }).click()

  const confirm = page.getByRole("alertdialog", { name: "Send to Kai Yong on WhatsApp (main)?" })
  await expect(confirm).toContainText("Hi Kai, the signed contract comes this afternoon.")
  expect(w.sent.filter((s) => s.path.endsWith("/send"))).toHaveLength(0)
  await shot(page, "inbox-send")
  await confirm.getByRole("button", { name: "Send" }).click()

  await expect
    .poll(() => w.sent)
    .toContainEqual({
      path: "/inbox/tdo_reply/send",
      body: { revision: 1, text: "Hi Kai, the signed contract comes this afternoon." },
    })
  await expect(confirm).toBeHidden()
  await expect(page.getByText("Sent", { exact: true })).toBeVisible()
})

// Scenario: the owner does not wait for the next 15-minute read of his messages. The Inbox says when they were last
// read; he presses Scan now, which runs the main Bot's triage Routine: the button says Scanning… and is disabled
// while the run goes, and when it ends the list shows what it found, with the new time (issue #1115).
test("Scan now runs the triage and the list shows what it found", async ({ page }) => {
  const w = world({ triage: true })
  await w.serve(page)
  await page.goto("/inbox")

  await expect(list(page).getByText(/^Read new messages 12/)).toBeVisible()
  await list(page).getByRole("button", { name: "Scan now" }).click()
  const scanning = list(page).getByRole("button", { name: "Scanning…" })
  await expect(scanning).toBeDisabled()
  await expect(list(page).getByText("Reading new messages…")).toBeVisible()
  expect(w.sent.filter((s) => s.path === "/routines/rtn_triage/runs")).toHaveLength(1)
  await shot(page, "inbox-scanning")

  w.endScan()
  // The Routines are read again every 5 seconds, the to-dos every 10: the end shows at the first, the list with it.
  await expect(list(page).getByRole("button", { name: "Scan now" })).toBeEnabled({
    timeout: 8_000,
  })
  await expect(list(page).getByText("Read new messages just now")).toBeVisible()
  await expect(
    rowsOf(page, "Needs you").filter({ hasText: "Answer Hà about the tiles" }),
  ).toBeVisible({
    timeout: 2_000,
  })
  await shot(page, "inbox-scanned")
})

// Scenario: without a triage Routine there is nothing to run, so the Inbox has no Scan now.
test("without a triage Routine, there is no Scan now", async ({ page }) => {
  const w = world()
  await w.serve(page)
  await page.goto("/inbox")

  await expect(rowsOf(page, "Needs you")).toHaveCount(4)
  await expect(page.getByRole("button", { name: "Scan now" })).toHaveCount(0)
})

// Scenario: on a computer, when nothing waits on the owner, the right pane says so with what the Inbox did today.
test("when nothing waits, the pane says so with today's counts", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 1280) < 768, "the pane beside the list is a computer's")
  const w = world({ quiet: true })
  await w.serve(page)
  await page.goto("/inbox")

  await expect(rowsOf(page, "Open")).not.toHaveCount(0)
  await expect(page.getByText("Nothing waits on you")).toBeVisible()
  await expect(page.getByText(/Today: \d+ added, \d+ closed\./)).toBeVisible()
  await expect(item(page)).toHaveCount(0)
  await shot(page, "inbox-nothing-waits")
})

// The pictures the owner looks at, in the light and the dark theme.
for (const theme of ["light", "dark"] as const) {
  test(`the Inbox in the ${theme} theme`, async ({ page }) => {
    await page.addInitScript((choice) => localStorage.setItem("theme", choice), theme)
    const w = world({ triage: true })
    await w.serve(page)
    await page.goto("/inbox")
    await expect(rowsOf(page, "Needs you")).toHaveCount(4)
    await expect(list(page).getByRole("button", { name: "Scan now" })).toBeVisible()
    await shot(page, `inbox-${theme}`)

    await page.goto("/inbox?item=todo%3Atdo_reply")
    await expect(
      item(page).getByRole("heading", { name: "Reply to Kai Yong", exact: true }),
    ).toBeVisible()
    await expect(item(page).getByRole("region", { name: "Comments" })).toContainText(
      "Thư ký added it",
    )
    await shot(page, `inbox-reply-${theme}`)

    // Issue #1111: the Send's confirmation.
    await item(page)
      .getByRole("region", { name: "Draft" })
      .getByRole("button", { name: "Send" })
      .click()
    await expect(page.getByRole("alertdialog", { name: /Send to Kai Yong/ })).toBeVisible()
    await shot(page, `inbox-send-${theme}`)
  })
}

// The pictures of an item's comments in its four states (issue #1143): not handed, Hypo working with its steps and
// its report, passed to the BeyondPilot Bot, and done; in the light and the dark theme.
for (const theme of ["light", "dark"] as const) {
  test(`an item's comments in the ${theme} theme`, async ({ page }) => {
    await page.addInitScript((choice) => localStorage.setItem("theme", choice), theme)
    const w = world({ passed: true, worked: true })
    await w.serve(page)
    const states = [
      ["not-handed", "tdo_invoice", "Hand to Hypo"],
      ["working", "tdo_work", "Send"],
      ["passed", "tdo_pilot", "Send"],
      ["done", "tdo_shipped", "Reopen and send"],
    ] as const
    for (const [state, id, button] of states) {
      await page.goto(`/inbox?item=todo%3A${id}`)
      const comments = item(page).getByRole("region", { name: "Comments" })
      const send = comments.getByRole("button", { name: button, exact: true })
      await expect(send).toBeVisible()
      if (state === "working") {
        await expect(comments.getByRole("button", { name: /^Hypo did 4 steps/ })).toBeVisible()
        await expect(comments.getByRole("button", { name: "Risks" })).toHaveAttribute(
          "aria-expanded",
          "false",
        )
        await comments.getByRole("button", { name: "What was done" }).click()
        await expect(comments).toContainText("Read the November figures from the sheet.")
      }
      await comments.getByRole("heading", { name: "Comments" }).scrollIntoViewIfNeeded()
      await shot(page, `inbox-comments-${state}-${theme}`)
      await send.scrollIntoViewIfNeeded()
      await shot(page, `inbox-comments-${state}-composer-${theme}`)
    }
  })
}

// Scenario: issue #1157, decisions 1 and 2. Each to-do starts with its priority's icon, and Open lists the urgent
// items first, then high and medium. The owner points at the reply's icon and reads the reason the Bot gave; he
// changes the invoice's priority with one click on its icon: the change is sent with the revision read and no
// reason, and the row moves up.
test("the owner changes a priority with one click and the list orders by it", async ({ page }) => {
  const w = world({ projects: true })
  await w.serve(page)
  await page.goto("/inbox")

  const open = rowsOf(page, "Open")
  await expect(open).toHaveCount(2)
  await expect(open.nth(0)).toContainText("Send Laura the weekly report")
  await expect(open.nth(1)).toContainText("Send the invoice to ELITEX")
  await expect(open.nth(0).getByRole("combobox", { name: "Priority: Urgent" })).toBeVisible()
  await expect(open.nth(1)).toContainText("VMAP")

  const reply = rowsOf(page, "Needs you").filter({ hasText: "Reply to Kai Yong" })
  await expect(reply.getByRole("combobox", { name: "Priority: Urgent" })).toHaveAttribute(
    "title",
    "Kai needs it before Friday",
  )

  await open.nth(1).getByRole("combobox", { name: "Priority: Medium" }).click()
  await expect(page.getByRole("option", { name: "Urgent" })).toBeVisible()
  await shot(page, "inbox-priority-picker")
  await page.getByRole("option", { name: "Urgent" }).click()
  await expect
    .poll(() => w.sent.find((s) => s.path === "/inbox/tdo_invoice")?.body)
    .toEqual({ revision: 1, priority: 1 })
  await expect(open.nth(0)).toContainText("Send the invoice to ELITEX")
  await shot(page, "inbox-priorities")
})

// Scenario: issue #1157, decision 5. On the open item, Cmd+K (Ctrl+K) names it in a chip and offers its commands:
// the owner sets its priority, moves it to a project and gives it to Hypo, each in a few keys. With the chip cleared,
// he types a title and the item he picks opens.
test("Cmd+K changes the selected item and jumps to another", async ({ page }) => {
  const w = world({ projects: true })
  await w.serve(page)
  await page.goto("/inbox?item=todo%3Atdo_invoice")
  await expect(
    item(page).getByRole("heading", { name: "Send the invoice to ELITEX" }),
  ).toBeVisible()

  const menu = page.getByRole("dialog", { name: "Command menu" })
  await page.keyboard.press("ControlOrMeta+k")
  await expect(menu.getByTestId("command-context")).toContainText("Send the invoice to ELITEX")
  await expect(menu.getByRole("option", { name: "Give to Hypo" })).toBeVisible()
  await shot(page, "inbox-command-menu")
  await menu.getByRole("option", { name: "Set priority…" }).click()
  await menu.getByRole("option", { name: "Urgent" }).click()
  await expect(menu).toBeHidden()
  await expect
    .poll(() => w.sent.find((s) => s.path === "/inbox/tdo_invoice")?.body)
    .toEqual({ revision: 1, priority: 1 })

  await page.keyboard.press("ControlOrMeta+k")
  await expect(menu.getByRole("combobox")).toBeFocused()
  await page.keyboard.type("project")
  await page.keyboard.press("Enter")
  await expect(menu.getByRole("option", { name: "BeyondPilot" })).toBeVisible()
  await shot(page, "inbox-command-menu-project")
  await menu.getByRole("option", { name: "BeyondPilot" }).click()
  await expect
    .poll(() => w.sent.filter((s) => s.path === "/inbox/tdo_invoice").at(-1)?.body)
    .toEqual({ revision: 2, project_id: "dep_bp" })
  await expect(item(page).getByRole("combobox", { name: "Project: BeyondPilot" })).toBeVisible()
  await shot(page, "inbox-item-priority-project")

  await page.keyboard.press("ControlOrMeta+k")
  await menu.getByRole("option", { name: "Give to Hypo" }).click()
  await expect
    .poll(() => w.sent.find((s) => s.path === "/inbox/tdo_invoice/hand")?.body)
    .toEqual({ revision: 3, bot: "bot_main" })

  await page.keyboard.press("ControlOrMeta+k")
  await menu.getByRole("button", { name: "Clear the item" }).click()
  await expect(menu.getByRole("combobox")).toBeFocused()
  await page.keyboard.type("renew")
  await menu.getByRole("option", { name: /Renew the domain/ }).click()
  await expect(item(page).getByRole("heading", { name: "Renew the domain" })).toBeVisible()
})

// Scenario: issue #1157, decision 4, and issue #1188, decision 2. The project filter, next to the other filter,
// shows every project at first; it lists the sidebar's departments in its order, VMAP then BeyondPilot, and no way
// to add, rename or remove one, which the sidebar does. The owner picks VMAP and sees its items only, then No
// project.
test("the project filter lists the departments and shows one's items", async ({ page }) => {
  const w = world({ projects: true })
  await w.serve(page)
  await page.goto("/inbox")
  await expect(rowsOf(page, "Open")).toHaveCount(2)

  const filter = list(page).getByRole("button", { name: "Project: Every project" })
  await filter.click()
  await expect(page.getByRole("option")).toHaveText([
    "Every project",
    /^No project/,
    /^VMAP/,
    /^BeyondPilot/,
  ])
  await expect(page.getByRole("option", { name: /New project|Edit/ })).toHaveCount(0)
  await shot(page, "inbox-project-filter")
  await page.getByRole("option", { name: /^VMAP/ }).click()
  await expect(rowsOf(page, "Open")).toHaveCount(2)
  await expect(group(page, "Needs you")).toHaveCount(0)

  await list(page).getByRole("button", { name: "Project: VMAP" }).click()
  await page.getByRole("option", { name: /^No project/ }).click()
  await expect(rowsOf(page, "Open").filter({ hasText: "VMAP" })).toHaveCount(0)
})

// Scenario: issue #1142, rule 2. Lan took the contract the owner was asked for, and the Thư ký moved it to Waiting on
// others; Hà took a meeting's action on. The group starts folded; opened, it lists both with their chase days. Lan's
// item says in its history who moved it, to whom, and her words, with the name and time the server keeps of her Zalo
// message; Hà's says she was waited on from the meeting's line, which opens the meeting there. Take it back gives
// Lan's item to the owner at the revision read: it is in Open, his again.
test("an item a teammate took waits on them, and Take it back makes it his again", async ({
  page,
}) => {
  const w = world({ waiting: true })
  await w.serve(page)
  await page.goto("/inbox")

  const fold = group(page, "Waiting on others").getByRole("button", { name: /Waiting on others/ })
  await expect(fold).toHaveAttribute("aria-expanded", "false")
  await fold.click()
  const waiting = rowsOf(page, "Waiting on others")
  await expect(waiting).toHaveText([
    /Lan sends the signed contract.*Lan.*Late · Chase yesterday/,
    /Hà sends the tiles report.*Chase /,
  ])
  await expect(rowsOf(page, "Open")).toHaveText([/Send the invoice to ELITEX/])
  await shot(page, "inbox-waiting")

  await waiting.filter({ hasText: "Lan sends" }).getByRole("button").first().click()
  const detail = item(page)
  const comments = detail.getByRole("region", { name: "Comments" })
  await expect(comments).toContainText("Thư ký moved it to Waiting on Lan")
  await expect(comments.getByRole("blockquote")).toHaveText("Em nhận nhé, chiều em gửi anh bản ký.")
  await expect(comments.getByRole("figure")).toContainText("Lan Phạm")
  await expect(detail).not.toContainText("You answer in this chat")
  await comments.scrollIntoViewIfNeeded()
  await shot(page, "inbox-waiting-history")

  await detail.getByRole("button", { name: "Take it back" }).click()
  await expect
    .poll(() => w.sent.find((s) => s.path === "/inbox/tdo_chase")?.body)
    .toEqual({ revision: 2, assignee: { kind: "me" } })
  await expect(comments).toContainText("You took it back")
  if ((page.viewportSize()?.width ?? 1280) < 768)
    await detail.getByRole("button", { name: "Inbox" }).click()
  await expect(rowsOf(page, "Open").filter({ hasText: "Lan sends" })).toHaveCount(1)
  await expect(waiting).toHaveText([/Hà sends the tiles report/])
  await shot(page, "inbox-taken-back")

  await page.goto("/inbox?item=todo%3Atdo_deck")
  const deck = item(page).getByRole("region", { name: "Comments" })
  await expect(deck).toContainText("waiting on Hà")
  await expect(deck.getByRole("link", { name: "Line 12 of the meeting" })).toHaveAttribute(
    "href",
    "/meetings/mtg_1?line=12",
  )
  await deck.scrollIntoViewIfNeeded()
  await shot(page, "inbox-waiting-meeting")
})

// Scenario: issue #1173. The owner opens his report for Laura, due two days ago: its properties are circle's, the
// status by its name, who does it with an avatar, the date with a calendar saying how late it is; its description
// shows its sections, and the meeting and the chat it names are links by their titles, not their ids. He marks it
// done from the status. A prioritized item and one in a project show their priority, its reason, and the project.
test("an item's detail shows its status, a late date and its ids as links", async ({ page }) => {
  const w = world({ described: true, projects: true })
  await w.serve(page)
  await page.goto("/inbox?item=todo%3Atdo_laura")
  const detail = item(page)
  await expect(detail.getByRole("heading", { name: /Báo cáo \+ kế hoạch/ })).toBeVisible()

  await expect(detail.getByRole("combobox", { name: "Status: Open" })).toBeVisible()
  await expect(detail).toContainText(/Due \d\d\/\d\d\s*Late · 2 days/)
  const meeting = detail.getByRole("link", { name: "Họp với cô Laura về Cốc Cốc" })
  await expect(meeting).toHaveAttribute("href", `/meetings/${laurasMeeting}`)
  await expect(detail.getByRole("link", { name: "Inbox: báo cáo cho cô Laura" })).toHaveAttribute(
    "href",
    `/chats/${laurasChat}`,
  )
  await expect(detail).not.toContainText(laurasMeeting)
  await expect(detail.getByRole("heading", { name: "Plan" })).toBeVisible()
  await expect(detail.locator("[data-slot=notes]").getByRole("listitem")).toHaveCount(3)
  await shot(page, "inbox-detail-late")

  await detail.getByRole("combobox", { name: "Status: Open" }).click()
  await expect(page.getByRole("option", { name: "Dropped" })).toBeVisible()
  await shot(page, "inbox-detail-status")
  await page.getByRole("option", { name: "Done" }).click()
  await expect.poll(() => w.sent.map((s) => s.path)).toContain("/inbox/tdo_laura/done")

  await page.goto("/inbox?item=todo%3Atdo_chase")
  await expect(detail.getByRole("heading", { name: "Lan sends the signed contract" })).toBeVisible()
  await expect(detail.getByRole("combobox", { name: "Priority: High" })).toBeVisible()
  await expect(detail).toContainText("Lan is a day late")
  await expect(detail.locator("[data-slot=who-avatar][data-who=person]")).toHaveText("L")
  await expect(detail).toContainText(/Chase yesterday\s*Late · 1 day/)
  await shot(page, "inbox-detail-prioritized")

  await page.goto("/inbox?item=todo%3Atdo_reply")
  await expect(detail.getByRole("heading", { name: "Reply to Kai Yong" })).toBeVisible()
  await expect(detail.getByRole("combobox", { name: "Priority: Urgent" })).toBeVisible()
  await expect(detail).toContainText("BeyondPilot")
  await expect(detail.locator("[data-slot=due]")).toHaveAttribute("data-tone", "today")
  await shot(page, "inbox-detail-project")
})
