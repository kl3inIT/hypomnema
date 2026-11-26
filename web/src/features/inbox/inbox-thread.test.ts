import { expect, test } from "vitest"

import type { Todo, TodoEvent } from "@/api/generated/types.gen"

import {
  alone,
  canSend,
  eventLine,
  handoverWords,
  reportOf,
  sendQuestion,
  threadOf,
  throughWhom,
  waitingOf,
  waitingPlace,
  writeOf,
} from "./inbox-thread"

const source = (m: Partial<Todo["source"]>): Pick<Todo, "source" | "state"> => ({
  state: "open",
  source: { kind: "owner", ...m },
})

const event = (id: number, m: Partial<TodoEvent>): TodoEvent => ({
  id,
  at: "2026-11-23T08:00:00Z",
  revision: id,
  actor: { kind: "owner" },
  kind: "edited",
  before: {},
  after: {},
  ...m,
})

// Scenario: issue #1111, section 2. The item's history is its thread: the owner's prompt, a Bot's progress and his
// answer are comments with their text, in order; every other change is a line of activity.
test("the thread keeps the comments among the activity, in order", () => {
  const thread = threadOf([
    event(1, { kind: "created" }),
    event(2, { kind: "edited", after: { assignee: "bot_main" } }),
    event(3, { kind: "commented", after: { text: "Do it now" } }),
    event(4, {
      kind: "commented",
      actor: { kind: "bot", id: "bot_main" },
      after: { text: "Started" },
    }),
    event(5, { kind: "commented", after: { text: "Keep the old button" } }),
  ])

  expect(thread.map((e) => (e.kind === "comment" ? `${e.actor.kind}: ${e.text}` : e.kind))).toEqual(
    ["activity", "activity", "owner: Do it now", "bot: Started", "owner: Keep the old button"],
  )
  expect(threadOf(undefined)).toEqual([])
})

// Scenario: issue #1111, section 3. Send is offered on an item from the owner's own chat or thread, not closed, and
// its confirmation names the recipient and the place.
test("send names the recipient and the place, and only for the owner's own chats", () => {
  const kai = source({
    kind: "whatsapp",
    account: "me",
    ref: { kind: "whatsapp_message", ref: "3EB0", label: "Kai Yong" },
  })
  expect(canSend(kai)).toBe(true)
  expect(sendQuestion(kai)).toBe("Send to Kai Yong on WhatsApp (me)?")

  expect(sendQuestion({ source: { kind: "zalo", id: "lan", account: "main" } }, "Lan")).toBe(
    "Send to Lan on Zalo (main)?",
  )
  expect(sendQuestion(source({ kind: "lark", id: "oc_1" }))).toBe(
    "Send to the chat it came from on Lark?",
  )
  expect(sendQuestion(source({ kind: "gmail", account: "work", id: "t1" }))).toBe(
    "Send to the thread on Gmail (work)?",
  )

  expect(canSend(source({ kind: "owner" }))).toBe(false)
  expect(canSend(source({ kind: "meeting", id: "mtg_1" }))).toBe(false)
  expect(canSend({ ...kai, state: "done" })).toBe(false)
})

const botNames: Record<string, string> = { bot_main: "Hypo", bot_beyond: "BeyondPilot" }
const nameOf = (id: string) => botNames[id] ?? id

// Scenario: issue #1118, section 4. An item the main Bot passed to a project Bot shows who has it and through whom;
// one the owner gave shows its Bot alone.
test("the assignee shows the Bot it was passed through", () => {
  expect(throughWhom({ passed_by: "bot_main" }, "BeyondPilot", nameOf)).toBe("Hypo → BeyondPilot")
  expect(throughWhom({}, "Hypo", nameOf)).toBe("Hypo")
})

// Scenario: issue #1118, section 4. The thread says each hand-over: the main Bot passing it to a project Bot, that
// Bot giving it back to the owner, and the owner giving it to a Bot or a person.
test("the thread shows each hand-over", () => {
  expect(
    handoverWords(
      { assignee_kind: "bot", assignee: "bot_beyond", passed_by: "bot_main" },
      "Hypo",
      nameOf,
    ),
  ).toBe("Hypo passed it to BeyondPilot")
  expect(handoverWords({ assignee_kind: "me", assignee: "" }, "BeyondPilot", nameOf)).toBe(
    "BeyondPilot gave it back to you",
  )
  expect(handoverWords({ assignee_kind: "bot", assignee: "bot_main" }, "You", nameOf)).toBe(
    "You gave it to Hypo",
  )
  expect(handoverWords({ assignee_kind: "person", assignee: "Lan" }, "You", nameOf)).toBe(
    "You gave it to Lan",
  )
  expect(handoverWords({ notes: "x" }, "You", nameOf)).toBeUndefined()
})

const bot = { kind: "bot", id: "bot_main" } as const

// Scenario: issue #1143, Logic 4. Hypo's changes in a row fold into one group of steps, as GitHub's "added 12
// commits"; a comment ends the group, one change alone stays a line, and the item's creation, its closing and its
// reopening never fold.
test("consecutive events of one actor group into steps", () => {
  const thread = threadOf([
    event(1, { kind: "created", actor: bot }),
    event(2, { actor: bot, after: { notes: "Read the mail" }, at: "2026-11-23T10:03:00Z" }),
    event(3, { actor: bot, kind: "state", after: { state: "doing", work_kind: "run" } }),
    event(4, { actor: bot, after: { draft: "Hi Kai" }, at: "2026-11-23T10:41:00Z" }),
    event(5, { kind: "commented", after: { text: "Thanks" } }),
    event(6, { actor: bot, after: { notes: "Sent" } }),
    event(7, { actor: { kind: "owner" }, after: { notes: "ok" } }),
    event(8, { actor: { kind: "owner" }, kind: "state", after: { state: "done" } }),
    event(9, {
      actor: { kind: "owner" },
      kind: "state",
      before: { state: "done" },
      after: { state: "open" },
    }),
    event(10, { actor: { kind: "owner" }, after: { assignee_kind: "bot", assignee: "bot_main" } }),
  ])

  expect(
    thread.map((e) =>
      e.kind === "steps" ? `steps ${e.steps.map((s) => s.id).join(",")}` : `${e.kind} ${e.id}`,
    ),
  ).toEqual([
    "activity 1",
    "steps 2,3,4",
    "comment 5",
    "activity 6",
    "activity 7",
    "activity 8",
    "activity 9",
    "activity 10",
  ])
  const steps = thread[1]
  expect(steps?.kind === "steps" && [steps.from, steps.to]).toEqual([
    "2026-11-23T10:03:00Z",
    "2026-11-23T10:41:00Z",
  ])
})

// Scenario: issue #1143, Logic 1, 3 and 6. The first line says where the item came from; a Run started and a pull
// request are said in words; closing it as done is the violet line.
test("events say who did what, and closing as done is marked", () => {
  expect(
    eventLine(event(1, { kind: "created", actor: bot }), nameOf, "Zalo · Laura Nguyen").words,
  ).toBe("Hypo added it from Zalo · Laura Nguyen")
  expect(
    eventLine(
      event(2, { kind: "state", actor: bot, after: { state: "doing", work_kind: "run" } }),
      nameOf,
    ).words,
  ).toBe("Hypo started a Run")
  expect(
    eventLine(
      event(3, { actor: bot, after: { evidence: [{ kind: "pull_request", ref: "1143" }] } }),
      nameOf,
    ).words,
  ).toBe("Hypo linked a pull request")
  const done = eventLine(event(4, { kind: "state", actor: bot, after: { state: "done" } }), nameOf)
  expect([done.words, done.done]).toEqual(["Hypo closed it as done", true])
  // Issue #1163: the owner answered in another chat, where the person who asked is.
  expect(
    eventLine(
      event(6, { kind: "state", actor: bot, after: { state: "done", answered_in: "Nhóm dự án" } }),
      nameOf,
    ).words,
  ).toBe("Hypo closed it as done: answered in Nhóm dự án")
  expect(
    eventLine(
      event(5, { kind: "state", before: { state: "done" }, after: { state: "open" } }),
      nameOf,
    ).words,
  ).toBe("You reopened it")
})

// Scenario: issue #1157, decision 3. A priority set says it with the reason it was set for, so the owner reads why;
// a project changed names it, and a project removed says why the item has none.
test("a priority and a project changed are said in words", () => {
  const projectName = (id: string) => (id === "prj_v" ? "VMAP" : undefined)
  const line = (after: Record<string, unknown>, actor: TodoEvent["actor"] = bot) =>
    eventLine(event(1, { actor, after }), nameOf, undefined, projectName).words

  expect(line({ priority: 1, priority_reason: "Her deadline is today" })).toBe(
    "Hypo set the priority to Urgent: Her deadline is today",
  )
  expect(line({ priority: 4, priority_reason: "" }, { kind: "owner" })).toBe(
    "You set the priority to Low",
  )
  expect(line({ priority: 0, priority_reason: "" }, { kind: "owner" })).toBe(
    "You removed the priority",
  )
  expect(line({ project_id: "prj_v" })).toBe("Hypo moved it to VMAP")
  expect(line({ project_id: null, reason: "Project VMAP was removed" }, { kind: "owner" })).toBe(
    "You took it out of its project (Project VMAP was removed)",
  )
})

// Scenario: issue #1143, Logic 5. A Bot's report shows its first line, then each <details> folded under its
// summary; a <details> in a code block, and one never closed, stay text.
test("a report's details become folded sections", () => {
  const report = [
    "Done: the button works.",
    "",
    "<details>",
    "<summary>What was done</summary>",
    "",
    "- moved the button",
    "<details><summary>Files</summary>web/src/x.tsx</details>",
    "</details>",
    "<details><summary><b>Risks</b></summary>",
    "None.",
    "</details>",
    "",
    "```html",
    "<details>",
    "```",
    "<details>",
    "left open",
  ].join("\n")

  expect(reportOf(report)).toEqual([
    { kind: "text", text: "Done: the button works." },
    {
      kind: "fold",
      title: "What was done",
      body: "- moved the button\n<details><summary>Files</summary>web/src/x.tsx</details>",
    },
    { kind: "fold", title: "Risks", body: "None." },
    { kind: "text", text: "```html\n<details>\n```" },
    { kind: "text", text: "<details>\nleft open" },
  ])
  expect(reportOf("Just a line")).toEqual([{ kind: "text", text: "Just a line" }])
})

// Scenario: issue #1143, Logic 1, 2 and 6. Writing on an item no Bot has hands it to the main Bot; on one a Bot has,
// it is a comment to that Bot (handed again when it has no chat); on a closed one, it reopens and hands it back.
test("the composer hands over, comments or reopens", () => {
  const todo = (m: Partial<Todo>): Pick<Todo, "state" | "assignee" | "chat_id"> => ({
    state: "open",
    assignee: { kind: "me" },
    ...m,
  })

  expect(writeOf(todo({}), "bot_main", nameOf)).toEqual({
    act: "hand",
    bot: "bot_main",
    label: "Hand to Hypo",
    placeholder: "Message Hypo…",
  })
  expect(
    writeOf(
      todo({ state: "doing", assignee: { kind: "bot", name: "bot_beyond" }, chat_id: "cht_1" }),
      "bot_main",
      nameOf,
    ),
  ).toMatchObject({ act: "comment", bot: "bot_beyond", label: "Send" })
  expect(
    writeOf(todo({ assignee: { kind: "bot", name: "bot_beyond" } }), "bot_main", nameOf),
  ).toMatchObject({ act: "hand", bot: "bot_beyond", label: "Send" })
  expect(
    writeOf(
      todo({ state: "done", assignee: { kind: "bot", name: "bot_beyond" }, chat_id: "cht_1" }),
      "bot_main",
      nameOf,
    ),
  ).toMatchObject({ act: "reopen", bot: "bot_beyond", label: "Reopen and send" })
  expect(writeOf(todo({ state: "dropped" }), "bot_main", nameOf)).toMatchObject({
    act: "reopen",
    bot: "bot_main",
  })
})

// Scenario: issue #1142. A Bot's move to Waiting on others, and an item it added waiting on someone, each say whom
// on their own line, never folded into the Bot's steps; waitingOf reads whom, the quote and the message; the owner
// taking it back says so.
test("a move to Waiting on others is its own line, with whom", () => {
  const bot = { kind: "bot" as const, id: "bot_main" }
  const waitingOn = {
    by: "Việt",
    quote: "em nhận",
    ref: { kind: "zalo_message", ref: "m_2" },
  }
  const moved = event(2, {
    actor: bot,
    after: { assignee: "Việt", assignee_kind: "person", waiting_on: waitingOn },
  })
  const added = event(1, {
    actor: bot,
    kind: "created",
    after: { title: "x", waiting_on: waitingOn },
  })
  const name = () => "Hypo"

  expect(waitingOf(moved.after)).toEqual(waitingOn)
  expect(waitingOf({ waiting_on: { quote: "x" } })).toBeUndefined()
  expect(waitingOf({})).toBeUndefined()
  expect(eventLine(moved, name).words).toBe("Hypo moved it to Waiting on Việt")
  expect(eventLine(added, name, "Zalo · Team").words).toBe(
    "Hypo added it from Zalo · Team, waiting on Việt",
  )
  expect(alone(moved)).toBe(true)
  expect(alone(added)).toBe(true)
  expect(alone(event(3, { actor: bot, after: { notes: "x" } }))).toBe(false)

  const steps = threadOf([
    event(5, { actor: bot, after: { notes: "a" } }),
    moved,
    event(6, { actor: bot, after: { notes: "b" } }),
  ])
  expect(steps.map((e) => e.kind)).toEqual(["activity", "activity", "activity"])

  expect(
    eventLine(event(7, { after: { assignee: "", assignee_kind: "me", why_open: null } }), name)
      .words,
  ).toBe("You took it back")
})

// Scenario: issue #1142. The message that shows whom an item waits on is read where the item came from: a Zalo or
// WhatsApp message by its own chat, a line of its own meeting, its Gmail thread; Lark, whose messages the server
// does not keep, by the name alone.
test("the message waited on is placed by the item's own source", () => {
  const w = (kind: string, ref: string) => ({
    by: "Lan",
    quote: "ok em làm",
    ref: { kind, ref } as never,
  })

  expect(
    waitingPlace({ kind: "zalo", id: "g_team", account: "main" }, w("zalo_message", "m_2")),
  ).toEqual({
    kind: "message",
    integration: "zalo",
    chat: "g_team",
    id: "m_2",
  })
  expect(waitingPlace({ kind: "meeting", id: "mtg_1" }, w("meeting_line", "mtg_1/12"))).toEqual({
    kind: "meeting",
    meeting: "mtg_1",
    line: 12,
  })
  expect(waitingPlace({ kind: "gmail", id: "thr_1", account: "work" }, w("mail", "m"))).toEqual({
    kind: "mail",
  })
  expect(waitingPlace({ kind: "lark", id: "oc_1" }, w("lark_message", "om_1"))).toEqual({
    kind: "name",
  })
})
