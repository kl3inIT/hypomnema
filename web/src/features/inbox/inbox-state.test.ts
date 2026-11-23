import { expect, test } from "vitest"

import type { Approval, Question, Todo } from "@/api/generated/types.gen"

import {
  actionsOf,
  detailActions,
  dueOf,
  fold,
  groupRows,
  matches,
  nextAfter,
  noFilter,
  rowsOf,
  snoozePresets,
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

// Scenario: a date is due all its day; a past one is late, today's is today.
test("a due date says when, and whether it is today or late", () => {
  const today = new Intl.DateTimeFormat("en-CA", { dateStyle: "short" }).format(now)

  expect(dueOf(todo("a", { due: { kind: "date", date: today } }), now)).toEqual({
    text: "Today",
    tone: "today",
  })
  expect(dueOf(todo("b", { due: { kind: "date", date: "2026-11-20" } }), now)?.tone).toBe("late")
  expect(dueOf(todo("c", { due: { kind: "instant", at: iso(now - hour) } }), now)?.tone).toBe(
    "late",
  )
  expect(dueOf(todo("d"), now)).toBeNull()
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
