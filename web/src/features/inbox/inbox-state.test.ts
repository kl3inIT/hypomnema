import { expect, test } from "vitest"

import type { Approval, Question, Todo } from "@/api/generated/types.gen"

import {
  actionsOf,
  askerOf,
  atOf,
  detailActions,
  dueOf,
  firstWaiting,
  fold,
  fromText,
  groupRows,
  initialsOf,
  matches,
  nextAfter,
  noFilter,
  placeOf,
  rowsOf,
  snoozePresets,
  sourceOf,
  sourceTarget,
  todayCounts,
  type Row,
} from "./inbox-state"

// A Tuesday, 10:00 in the test's own time.
const now = new Date(2026, 10, 24, 10, 0).getTime()
const iso = (ms: number) => new Date(ms).toISOString()
const hour = 3_600_000

const todo = (id: string, m: Partial<Todo> = {}): Todo => ({
  id,
  revision: 1,
  title: `To-do ${id}`,
  notes: "",
  done_when: "",
  draft: "",
  due: { kind: "none" },
  assignee: { kind: "me" },
  state: "open",
  source: { kind: "owner" },
  created_by: { kind: "owner" },
  close_reason: "",
  evidence: [],
  stale: false,
  created_at: iso(now - 5 * hour),
  updated_at: iso(now - hour),
  ...m,
})

const approval = (id: string, m: Partial<Approval> = {}): Approval => ({
  id,
  integration: "google",
  label: "work",
  action: "gmail.send",
  action_name: "Send mail",
  asked_by: "pi",
  title: `Send mail ${id}`,
  payload: [],
  status: "pending",
  used: false,
  created_at: iso(now - hour),
  ...m,
})

const question = (id: string, m: Partial<Question> = {}): Question => ({
  id,
  dispatch_id: "dsp_1",
  task_id: "tsk_1",
  run_id: "run_1",
  mode: "wait",
  addressed_to: "owner",
  text: `Question ${id}`,
  options: [],
  status: "open",
  created_at: iso(now - hour),
  ...m,
})

const keys = (rows: Row[]) => rows.map((r) => r.key)

// Scenario: issue #1093, section 2. Every item falls in one group: the asks and the to-dos the server counts as
// needing the owner in Needs you, asks first; what a Bot does in Working; the rest open in Open; proposals, snoozed
// and closed items in their own. A to-do in review shows its pending Approval as its own row, never twice; a
// question to the coordinator and an answered Approval are not the owner's.
test("the Inbox groups its items, the asks first in Needs you, each once", () => {
  const rows = rowsOf(
    {
      todos: [
        todo("reply", { draft: "Dạ em gửi anh ạ", due: { kind: "date", date: "2026-11-24" } }),
        todo("review", {
          state: "review",
          approval_id: "apr_held",
          assignee: { kind: "bot", name: "thu-ky" },
        }),
        todo("doing", { state: "doing", assignee: { kind: "bot", name: "thu-ky" } }),
        todo("chase", { assignee: { kind: "person", name: "Lan" } }),
        todo("mine"),
        todo("idea", { state: "proposed" }),
        todo("later", { snoozed_until: iso(now + 2 * hour) }),
        todo("sooner", { snoozed_until: iso(now + hour) }),
      ],
      needsYou: ["reply", "review", "idea"],
      closedTodos: [
        todo("old", { state: "done", closed_at: iso(now - 9 * hour) }),
        todo("new", { state: "dropped", closed_at: iso(now - hour) }),
      ],
      approvals: [
        approval("apr_held"),
        approval("apr_loose", { chat_id: "cht_1" }),
        approval("apr_done", { status: "approved" }),
      ],
      questions: [question("q_owner"), question("q_pi", { addressed_to: "coordinator" })],
    },
    now,
  )
  const groups = groupRows(rows, now)

  expect(keys(groups.needs_you)).toEqual([
    "approval:apr_loose",
    "todo:review",
    "question:q_owner",
    "todo:reply",
  ])
  expect(keys(groups.working)).toEqual(["todo:doing"])
  expect(keys(groups.open)).toEqual(["todo:chase", "todo:mine"])
  expect(keys(groups.proposed)).toEqual(["todo:idea"])
  expect(keys(groups.snoozed)).toEqual(["todo:sooner", "todo:later"])
  expect(keys(groups.done)).toEqual(["todo:new", "todo:old"])
})

// Scenario: Needs you shows five rows, then "Show N more"; the asks are never hidden, one hidden row is not worth
// a footer, and the selected row stays in view past the cap.
test("a group shows five rows, every ask, and the selected one", () => {
  const todos = Array.from({ length: 7 }, (_, i) => todo(`t${i}`))
  const rows = rowsOf({ todos, needsYou: todos.map((t) => t.id) }, now)

  const folded = fold(rows, { open: false })
  expect(folded.visible).toHaveLength(5)
  expect(folded.hidden).toBe(2)
  expect(fold(rows, { open: true }).hidden).toBe(0)
  expect(keys(fold(rows, { open: false, selected: "todo:t6" }).visible)).toContain("todo:t6")
  expect(fold(rows.slice(0, 6), { open: false }).folds).toBe(false)

  const asks = rowsOf(
    {
      approvals: Array.from({ length: 7 }, (_, i) => approval(`a${i}`)),
      todos: [todo("x"), todo("y")],
    },
    now,
  )
  expect(fold(asks, { open: false }).visible).toHaveLength(7)
})

// Scenario: issue #1093, section 3. Each kind offers its own actions on its row, and the detail's top bar its own.
test("each kind of row offers its actions", () => {
  const [held] = rowsOf({ approvals: [approval("a")] }, now)
  const [asked] = rowsOf({ questions: [question("q")] }, now)
  const one = (t: Todo, m: { approvals?: Approval[] } = {}) =>
    rowsOf({ todos: [t], ...m }, now).at(-1)!

  expect(actionsOf(held!, now)).toEqual(["approve", "decline"])
  expect(actionsOf(asked!, now)).toEqual(["answer"])
  expect(actionsOf(one(todo("d", { draft: "hi" })), now)).toEqual(["copy", "done", "snooze"])
  expect(actionsOf(one(todo("n")), now)).toEqual(["done", "snooze"])
  expect(actionsOf(one(todo("p", { state: "proposed" })), now)).toEqual(["accept", "drop"])
  expect(actionsOf(one(todo("s", { snoozed_until: iso(now + hour) })), now)).toEqual(["unsnooze"])
  expect(actionsOf(one(todo("c", { state: "done" })), now)).toEqual(["reopen"])
  expect(
    actionsOf(
      one(todo("r", { state: "review", approval_id: "a" }), { approvals: [approval("a")] }),
      now,
    ),
  ).toEqual(["approve", "decline"])

  expect(detailActions(one(todo("n")), now)).toEqual(["snooze", "drop", "done"])
  expect(detailActions(one(todo("p", { state: "proposed" })), now)).toEqual(["accept", "drop"])
  expect(detailActions(one(todo("g", { state: "doing" })), now)).toEqual(["drop", "done"])
  expect(detailActions(held!, now)).toEqual([])
})

// Scenario: after an action on the open item, the next one down opens, else the one above, else none.
test("the selection moves on to the next item", () => {
  expect(nextAfter(["a", "b", "c"], "a")).toBe("b")
  expect(nextAfter(["a", "b", "c"], "b")).toBe("c")
  expect(nextAfter(["a", "b", "c"], "c")).toBe("b")
  expect(nextAfter(["a"], "a")).toBeUndefined()
  expect(nextAfter(["a", "b"], "gone")).toBe("a")
})

// Scenario: the search finds every word in the title or the source; the chips keep a source and who does it.
test("the search and the filter keep what matches", () => {
  const [kai] = rowsOf(
    {
      todos: [
        todo("k", {
          title: "Reply to Kai Yong",
          source: { kind: "whatsapp", account: "main" },
          assignee: { kind: "me" },
        }),
      ],
    },
    now,
  )

  expect(matches(kai!, { ...noFilter, query: "kai whatsapp" }, "WhatsApp · main")).toBe(true)
  expect(matches(kai!, { ...noFilter, query: "lark" }, "WhatsApp · main")).toBe(false)
  expect(matches(kai!, { ...noFilter, sources: ["whatsapp"] }, "")).toBe(true)
  expect(matches(kai!, { ...noFilter, sources: ["zalo"] }, "")).toBe(false)
  expect(matches(kai!, { ...noFilter, people: ["bot"] }, "")).toBe(false)
})

// Scenario: issue #1110. A date says what it is: Due for the owner's or a Bot's, Chase for one given to someone else,
// the day and the month past tomorrow; a date is due all its day, a past one is late, today's is today.
test("a date is labelled Due or Chase, and says whether it is today or late", () => {
  const day = (n: number) =>
    new Intl.DateTimeFormat("en-CA", { dateStyle: "short" }).format(now + n * 24 * hour)

  expect(dueOf(todo("a", { due: { kind: "date", date: day(0) } }), now)).toEqual({
    text: "Due today",
    tone: "today",
  })
  expect(dueOf(todo("t", { due: { kind: "date", date: day(1) } }), now)?.text).toBe("Due tomorrow")
  expect(dueOf(todo("l", { due: { kind: "date", date: "2026-11-25" } }), now)?.text).toBe(
    "Due tomorrow",
  )
  expect(dueOf(todo("m", { due: { kind: "date", date: "2026-12-05" } }), now)).toEqual({
    text: "Due 05/12",
    tone: "later",
  })
  expect(
    dueOf(
      todo("c", {
        due: { kind: "date", date: "2026-11-20" },
        assignee: { kind: "person", name: "Lan" },
      }),
      now,
    ),
  ).toEqual({ text: "Chase 20/11", tone: "late" })
  expect(dueOf(todo("i", { due: { kind: "instant", at: iso(now - hour) } }), now)?.tone).toBe(
    "late",
  )
  expect(dueOf(todo("i", { due: { kind: "instant", at: iso(now + hour) } }), now)?.text).toMatch(
    /^Due today 11:00/,
  )
  expect(dueOf(todo("d"), now)).toBeNull()
})

// Scenario: issue #1110. A row names where it came from by the chat's or the person's name, never the account's
// number: the kept chat's name, else its reference's label; the owner's own is "Added by you"; a Bot's chat is the
// Bot's. The avatar is the sender's, "You" for what the owner said, the speaker of a meeting's line.
test("a row names the chat and the person, not the account's number", () => {
  const one = (t: Todo) => rowsOf({ todos: [t] }, now)[0]!
  const zalo = one(
    todo("z", {
      source: {
        kind: "zalo",
        id: "2261258766901429891",
        account: "2223515701293677881",
        quote: "a kieemr tra cho :O",
        ref: { kind: "zalo_message", ref: "8520580535290" },
      },
      created_by: { kind: "bot", id: "thu-ky" },
    }),
  )
  const bots = (id: string) => (id === "thu-ky" ? "Thư ký" : id)

  expect(placeOf(zalo, { chat: "Vân Anh" }, bots)).toBe("Vân Anh")
  expect(fromText(zalo, { chat: "Vân Anh" }, bots)).toBe("Zalo · Vân Anh")
  expect(placeOf(zalo, {}, bots)).toBe("")
  expect(fromText(zalo, {}, bots)).not.toContain("2223515701293677881")
  expect(askerOf(zalo, { chat: "Dự Án Ma", sender: "Vân Anh" }, bots)).toBe("Vân Anh")
  expect(askerOf(zalo, { chat: "Vân Anh", sender: "Đạt", fromMe: true }, bots)).toBe("You")
  expect(askerOf(zalo, { chat: "Vân Anh" }, bots)).toBe("Vân Anh")

  const lark = one(
    todo("l", {
      source: {
        kind: "lark",
        account: "me",
        ref: { kind: "lark_message", ref: "om_1", label: "ELITEX" },
      },
    }),
  )
  expect(fromText(lark)).toBe("Lark · ELITEX")

  const mine = one(todo("h"))
  expect(placeOf(mine)).toBe("Added by you")
  expect(fromText(mine)).toBe("Added by you")
  expect(askerOf(mine)).toBe("You")

  const chat = one(
    todo("c", { source: { kind: "chat", id: "cht_1" }, created_by: { kind: "bot", id: "thu-ky" } }),
  )
  expect(fromText(chat, {}, bots)).toBe("Chat · Thư ký")

  const meeting = one(
    todo("m", {
      source: { kind: "routine", id: "rrn_1", ref: { kind: "meeting_line", ref: "mtg_1/37" } },
    }),
  )
  expect(fromText(meeting, { meeting: "CDP" })).toBe("Meeting · CDP")
  expect(askerOf(meeting, { speaker: "Thành" })).toBe("Thành")
  expect(sourceTarget(meeting.kind === "todo" ? meeting.todo : todo("x"), () => undefined)).toEqual(
    {
      meeting: "mtg_1",
      line: 37,
    },
  )

  expect(initialsOf("Vân Anh")).toBe("VA")
  expect(initialsOf("Kai")).toBe("K")
  expect(initialsOf("Phan Hồng Đạt")).toBe("PĐ")
})

// Scenario: issue #1110. The time on a row's right is the message's own, else when the item was added.
test("a row's time is its message's, else when it was added", () => {
  const [row] = rowsOf({ todos: [todo("t")] }, now)

  expect(atOf(row!, { at: iso(now - 3 * hour) })).toBe(iso(now - 3 * hour))
  expect(atOf(row!)).toBe(iso(now - 5 * hour))
})

// Scenario: issue #1110. On a computer the first item of Needs you opens by itself; when nothing waits, none does,
// and the pane counts what was added and closed today.
test("the first item of Needs you opens, and today's counts when nothing waits", () => {
  const rows = rowsOf(
    {
      todos: [todo("open"), todo("reply")],
      needsYou: ["reply"],
      approvals: [approval("a")],
      closedTodos: [
        todo("today", {
          state: "done",
          closed_at: iso(now - hour),
          created_at: iso(now - 30 * hour),
        }),
        todo("before", {
          state: "done",
          closed_at: iso(now - 30 * hour),
          created_at: iso(now - 40 * hour),
        }),
      ],
    },
    now,
  )

  expect(firstWaiting(groupRows(rows, now))).toBe("approval:a")
  expect(
    firstWaiting(
      groupRows(
        rows.filter((r) => r.kind === "todo" && r.todo.id === "open"),
        now,
      ),
    ),
  ).toBeUndefined()
  expect(todayCounts(rows, now)).toEqual({ added: 2, closed: 1 })
})

// Scenario: the snooze presets, Orca's, in the owner's time: later today at 6 PM, tomorrow and Monday at 9 AM, and
// next week.
test("the snooze presets come back at their times", () => {
  const presets = snoozePresets(now)

  expect(presets.map((p) => p.name)).toEqual(["Later today", "Tomorrow", "Monday", "Next week"])
  expect(new Date(presets[0]!.at).getHours()).toBe(18)
  expect(new Date(presets[1]!.at).getDate()).toBe(25)
  expect(new Date(presets[2]!.at).getDay()).toBe(1)
  expect(presets.every((p) => p.at > now)).toBe(true)
})

// Issue #1105: a to-do from a Gmail thread opens that thread in Gmail, in the owner's account the Bot read it with
// (inbox-zero's getGmailUrl, "https://mail.google.com/mail/u/<email>/#all/<thread>"); one from a meeting opens the
// meeting at its line. Each is named by its own source kind, and an item made before them by its reference.
test("a mail and a meeting open where they were read", () => {
  const mails = new Map([["work", "dat@example.com"]])
  const mailOf = (label: string) => mails.get(label)
  const mail = todo("m", {
    source: {
      kind: "gmail",
      id: "18c2f0a9b1d2e3f4",
      account: "work",
      ref: { kind: "mail", ref: "18c2f0a9b1d2e3f5" },
    },
  })
  const meeting = todo("t", {
    source: { kind: "meeting", id: "mtg_1", ref: { kind: "meeting_line", ref: "mtg_1/12" } },
  })

  expect(sourceTarget(mail, mailOf)).toEqual({
    href: "https://mail.google.com/mail/u/dat@example.com/#all/18c2f0a9b1d2e3f4",
  })
  expect(sourceTarget({ ...mail, source: { ...mail.source, account: "gone" } }, mailOf)).toEqual({
    href: "https://mail.google.com/mail/u/0/#all/18c2f0a9b1d2e3f4",
  })
  expect(sourceTarget(meeting, mailOf)).toEqual({ meeting: "mtg_1", line: 12 })
  expect(sourceTarget(todo("n", { source: { kind: "meeting", id: "mtg_1" } }), mailOf)).toEqual({
    meeting: "mtg_1",
  })
  expect(sourceTarget(todo("c", { source: { kind: "chat", id: "cht_1" } }), mailOf)).toEqual({
    chat: "cht_1",
  })
  expect(
    sourceTarget(todo("z", { source: { kind: "zalo", id: "g_1", account: "me" } }), mailOf),
  ).toBe(null)

  const row = (t: Todo): Row => ({ key: `todo:${t.id}`, kind: "todo", todo: t, needsYou: false })
  expect(sourceOf(row(mail))).toBe("gmail")
  expect(sourceOf(row(meeting))).toBe("meeting")
  expect(
    sourceOf(
      row(todo("r", { source: { kind: "routine", id: "rrn_1", ref: { kind: "mail", ref: "x" } } })),
    ),
  ).toBe("gmail")
})
