import { expect, test } from "vitest"

import type { Approval, Question, Todo } from "@/api/generated/types.gen"

import {
  actionsOf,
  askerOf,
  atOf,
  botOf,
  detailActions,
  doneWhenOf,
  dueOf,
  firstWaiting,
  fold,
  foldedByDefault,
  fromLineOf,
  fromText,
  groupIds,
  groupNames,
  groupRows,
  lateBy,
  matches,
  nextAfter,
  noFilter,
  noProject,
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
  priority: 0,
  priority_reason: "",
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
  expect(keys(groups.open)).toEqual(["todo:mine"])
  expect(keys(groups.waiting)).toEqual(["todo:chase"])
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

// Scenario: issue #1173, plan row 2. How late an item is: days since a date, counted in its zone; the minutes,
// hours or days since a moment; nothing for one not late.
test("an item says how late it is", () => {
  const day = (n: number) =>
    new Intl.DateTimeFormat("en-CA", { dateStyle: "short" }).format(now + n * 24 * hour)

  expect(lateBy(todo("y", { due: { kind: "date", date: day(-1) } }), now)).toBe("1 day")
  expect(lateBy(todo("w", { due: { kind: "date", date: day(-7) } }), now)).toBe("7 days")
  expect(
    lateBy(todo("i", { due: { kind: "instant", at: iso(now - 3 * hour - 60_000) } }), now),
  ).toBe("3 hours")
  expect(lateBy(todo("m", { due: { kind: "instant", at: iso(now - 5 * 60_000) } }), now)).toBe(
    "5 minutes",
  )
  expect(lateBy(todo("a", { due: { kind: "date", date: day(0) } }), now)).toBeUndefined()
  expect(lateBy(todo("d"), now)).toBeUndefined()
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
        id: "1000000000000000001",
        account: "2223515701293677881",
        quote: "a kieemr tra cho :O",
        ref: { kind: "zalo_message", ref: "8520580535290" },
      },
      created_by: { kind: "bot", id: "thu-ky" },
    }),
  )
  const bots = (id: string) => (id === "thu-ky" ? "Thư ký" : id)

  expect(placeOf(zalo, { chat: "Minh Thư" }, bots)).toBe("Minh Thư")
  expect(fromText(zalo, { chat: "Minh Thư" }, bots)).toBe("Zalo · Minh Thư")
  expect(placeOf(zalo, {}, bots)).toBe("")
  expect(fromText(zalo, {}, bots)).not.toContain("2223515701293677881")
  expect(askerOf(zalo, { chat: "Dự Án Ma", sender: "Minh Thư" }, bots)).toBe("Minh Thư")
  expect(askerOf(zalo, { chat: "Minh Thư", sender: "Đạt", fromMe: true }, bots)).toBe("You")
  expect(askerOf(zalo, { chat: "Minh Thư" }, bots)).toBe("Minh Thư")

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
})

// Scenario: issue #1140. The line below a row's title is who then where, without the platform, which its avatar
// shows; a name both give once; with one known, that one.
test("a row's line names the person and the place, not the platform", () => {
  const one = (t: Todo) => rowsOf({ todos: [t] }, now)[0]!
  const bots = (id: string) => (id === "thu-ky" ? "Thư ký" : id)
  const group = one(
    todo("z", {
      source: { kind: "zalo", id: "1", ref: { kind: "zalo_message", ref: "m1" } },
      created_by: { kind: "bot", id: "thu-ky" },
    }),
  )

  expect(
    fromLineOf(group, { chat: "BeyondPilot Platform Discussion", sender: "Eugene" }, bots),
  ).toBe("Eugene · BeyondPilot Platform Discussion")
  expect(fromLineOf(group, { chat: "Minh Thư", sender: "Minh Thư" }, bots)).toBe("Minh Thư")
  expect(fromLineOf(group, { chat: "Dự Án Ma" }, bots)).toBe("Dự Án Ma")
  expect(fromLineOf(group, {}, bots)).toBe("")
  expect(fromLineOf(one(todo("h")))).toBe("Added by you")
  const chat = one(
    todo("c", { source: { kind: "chat", id: "cht_1" }, created_by: { kind: "bot", id: "thu-ky" } }),
  )
  expect(fromLineOf(chat, {}, bots)).toBe("Thư ký")
  expect(botOf(chat)).toBe("thu-ky")
  expect(botOf(group)).toBeUndefined()
  expect(botOf(rowsOf({ approvals: [approval("a", { chat_id: "cht_1" })] }, now)[0]!)).toBe("pi")
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

// Scenario: issue #1157, decision 2. Inside each group the rows go by priority, urgent first and no priority last,
// then as before: Open keeps the server's order by due for one priority, Needs you its asks first, Done the latest
// closed first.
test("each group is ordered by priority, then as before", () => {
  const due = (date: string) => ({ kind: "date" as const, date, zone: "Asia/Ho_Chi_Minh" })
  const rows = rowsOf(
    {
      approvals: [approval("a")],
      todos: [
        todo("none-early", { due: due("2026-11-25") }),
        todo("low", { priority: 4, due: due("2026-11-26") }),
        todo("high-early", { priority: 2, due: due("2026-11-27") }),
        todo("high-late", { priority: 2, due: due("2026-11-30") }),
        todo("urgent", { priority: 1, due: due("2026-12-01") }),
        todo("mine-urgent", { priority: 1 }),
      ],
      needsYou: ["mine-urgent"],
      closedTodos: [
        todo("closed-old", { state: "done", priority: 1, closed_at: iso(now - 2 * hour) }),
        todo("closed-new", { state: "done", priority: 1, closed_at: iso(now - hour) }),
        todo("closed-none", { state: "done", closed_at: iso(now) }),
      ],
    },
    now,
  )
  const keys = (rs: Row[]) => rs.map((r) => r.key)
  const groups = groupRows(rows, now)

  expect(keys(groups.open)).toEqual([
    "todo:urgent",
    "todo:high-early",
    "todo:high-late",
    "todo:low",
    "todo:none-early",
  ])
  expect(keys(groups.needs_you)).toEqual(["approval:a", "todo:mine-urgent"])
  expect(keys(groups.done)).toEqual(["todo:closed-new", "todo:closed-old", "todo:closed-none"])
})

// Scenario: issue #1157, decision 4. The project filter keeps every project, the items of none, or one project's.
test("the project filter keeps one project's items or those of none", () => {
  const [inVmap, inNone] = rowsOf({ todos: [todo("v", { project_id: "prj_v" }), todo("n")] }, now)

  expect(matches(inVmap!, { ...noFilter, project: "prj_v" }, "")).toBe(true)
  expect(matches(inNone!, { ...noFilter, project: "prj_v" }, "")).toBe(false)
  expect(matches(inNone!, { ...noFilter, project: noProject }, "")).toBe(true)
  expect(matches(inVmap!, { ...noFilter, project: noProject }, "")).toBe(false)
  expect(matches(inVmap!, noFilter, "")).toBe(true)
})

// Scenario: issue #1142. An open item given to a person waits on them, in its own group, with its chase day; one
// given to nobody known stays Open; a proposal, a snoozed or a closed one stays where it was. The group starts folded,
// after Open, and its rows offer Take it back; no hint says the owner answers in its chat.
test("an item given to a person is in Waiting on others, with Take it back", () => {
  const waiting = todo("viet", {
    assignee: { kind: "person", name: "Việt" },
    why_open: "waiting",
    due: { kind: "instant", at: iso(now - hour), zone: "Asia/Ho_Chi_Minh" },
  })
  const rows = rowsOf(
    {
      todos: [
        waiting,
        todo("nobody", { assignee: { kind: "unknown" } }),
        todo("proposal", { state: "proposed", assignee: { kind: "person", name: "Lan" } }),
        todo("later", {
          assignee: { kind: "person", name: "Lan" },
          snoozed_until: iso(now + hour),
        }),
      ],
      closedTodos: [
        todo("closed", {
          state: "done",
          assignee: { kind: "person", name: "Lan" },
          closed_at: iso(now),
        }),
      ],
    },
    now,
  )
  const groups = groupRows(rows, now)
  const keys = (rs: Row[]) => rs.map((r) => r.key)

  expect(keys(groups.waiting)).toEqual(["todo:viet"])
  expect(keys(groups.open)).toEqual(["todo:nobody"])
  expect(keys(groups.proposed)).toEqual(["todo:proposal"])
  expect(keys(groups.snoozed)).toEqual(["todo:later"])
  expect(keys(groups.done)).toEqual(["todo:closed"])
  expect(groupIds.indexOf("waiting")).toBe(groupIds.indexOf("open") + 1)
  expect(groupNames.waiting).toBe("Waiting on others")
  expect(foldedByDefault.has("waiting")).toBe(true)

  expect(dueOf(waiting, now)).toEqual({ text: expect.stringMatching(/^Chase /), tone: "late" })
  expect(actionsOf(rows[0]!, now)).toEqual(["take_back", "done", "snooze"])
  expect(detailActions(rows[0]!, now)).toEqual(["take_back", "snooze", "drop", "done"])
  expect(actionsOf(rows[1]!, now)).not.toContain("take_back")

  expect(doneWhenOf(waiting)).toBe("")
  expect(doneWhenOf({ done_when: "", why_open: "asked_directly" })).toMatch(/^You answer/)
})
