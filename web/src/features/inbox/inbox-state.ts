import type { Approval, Question, Todo, TodoSourceKind } from "@/api/generated/types.gen"
import { safeLink } from "@/features/work/work-state"

// The Inbox's list (issue #1093): what waits on the owner and every to-do, in groups, after Orca's Items tab (MIT,
// Copyright (c) 2026 Lovecast Inc., https://github.com/stablyai/orca; src/shared/bots/bot-item-groups.ts,
// needs-you-fold.ts, bot-item-snooze.ts) and OpenMausBot's inbox (Apache-2.0, https://github.com/milind-soni/OpenMausBot;
// src/components/inbox/inbox-view.ts quickActions, inbox-answer.ts nextAfter and matchesSearch). An Approval and a
// worker's question are read from their own lists and answered with their own calls, never copied into to-dos
// (OpenMausBot's person inbox, "answered here as there", server/host/person-inbox.ts:223-249).

export type Row =
  | { key: string; kind: "approval"; approval: Approval }
  | { key: string; kind: "question"; question: Question }
  // needsYou: the server counts it in Needs you (GET /inbox?needs_you); approval, the pending Approval it waits for.
  | { key: string; kind: "todo"; todo: Todo; needsYou: boolean; approval?: Approval }

export const groupIds = ["needs_you", "working", "open", "proposed", "snoozed", "done"] as const
export type GroupId = (typeof groupIds)[number]
export type Groups = Record<GroupId, Row[]>

export const groupNames: Record<GroupId, string> = {
  needs_you: "Needs you",
  working: "Working",
  open: "Open",
  proposed: "Proposed",
  snoozed: "Snoozed",
  done: "Done",
}

// Groups that start folded, as Orca's BOT_ITEM_GROUPS_FOLDED_BY_DEFAULT: what is out of the way until asked for.
export const foldedByDefault: ReadonlySet<GroupId> = new Set(["snoozed", "done"])

const closed = new Set(["done", "dropped", "duplicate"])

export function isClosed(todo: Pick<Todo, "state">) {
  return closed.has(todo.state)
}

function isSnoozed(todo: Pick<Todo, "snoozed_until">, now: number) {
  return !!todo.snoozed_until && Date.parse(todo.snoozed_until) > now
}

// rowsOf gathers the Inbox's rows. A to-do in review shows the Approval it waits for as its own: one row for one
// decision, as the server counts it once (CountOwnerAsks). Only open questions to the owner are his to answer.
export function rowsOf(
  {
    todos = [],
    needsYou = [],
    closedTodos = [],
    approvals = [],
    questions = [],
  }: {
    todos?: Todo[]
    needsYou?: string[]
    closedTodos?: Todo[]
    approvals?: Approval[]
    questions?: Question[]
  },
  now: number,
): Row[] {
  const counted = new Set(needsYou)
  const pending = new Map(approvals.filter((a) => a.status === "pending").map((a) => [a.id, a]))
  const held = new Set<string>()
  const todoRows: Row[] = []

  for (const todo of [...todos, ...closedTodos.filter((t) => !todos.some((o) => o.id === t.id))]) {
    const approval =
      todo.state === "review" && todo.approval_id ? pending.get(todo.approval_id) : undefined
    if (approval && !isSnoozed(todo, now)) held.add(approval.id)
    todoRows.push({
      key: `todo:${todo.id}`,
      kind: "todo",
      todo,
      needsYou: counted.has(todo.id),
      approval,
    })
  }

  return [
    ...[...pending.values()]
      .filter((a) => !held.has(a.id))
      .map((approval): Row => ({ key: `approval:${approval.id}`, kind: "approval", approval })),
    ...questions
      .filter((q) => q.status === "open" && q.addressed_to === "owner")
      .map((question): Row => ({ key: `question:${question.id}`, kind: "question", question })),
    ...todoRows,
  ]
}

// groupOf places a row. Asks wait on the owner; a to-do the server counts as needing him is his, unless it is a
// proposal, which has its own group; what a Bot does is Working; anything else not closed is Open.
export function groupOf(row: Row, now: number): GroupId {
  if (row.kind !== "todo") return "needs_you"

  const { todo } = row
  if (isClosed(todo)) return "done"
  if (isSnoozed(todo, now)) return "snoozed"
  if (todo.state === "proposed") return "proposed"
  if (row.needsYou) return "needs_you"
  if (todo.state === "doing" || todo.state === "review") return "working"

  return "open"
}

// groupRows sorts the rows into their groups. Needs you lists the asks first, Approvals before questions, as Orca's
// BotNeedsYouSection; to-dos keep the server's order, by due. Snoozed comes back soonest first; Done, the latest
// closed first.
export function groupRows(rows: Row[], now: number): Groups {
  const groups: Groups = {
    needs_you: [],
    working: [],
    open: [],
    proposed: [],
    snoozed: [],
    done: [],
  }

  for (const row of rows) groups[groupOf(row, now)].push(row)

  const at = (row: Row, field: "snoozed_until" | "closed_at") =>
    row.kind === "todo" ? Date.parse(row.todo[field] ?? "") || 0 : 0
  groups.needs_you.sort((a, b) => askRank(a) - askRank(b))
  groups.snoozed.sort((a, b) => at(a, "snoozed_until") - at(b, "snoozed_until"))
  groups.done.sort((a, b) => at(b, "closed_at") - at(a, "closed_at"))

  return groups
}

// askRank orders Needs you: Approvals (and the to-dos that wait for one), then questions, then the other to-dos.
function askRank(row: Row) {
  if (row.kind === "approval" || (row.kind === "todo" && row.approval)) return 0

  return row.kind === "question" ? 1 : 2
}

// Origin is what the server keeps beside a to-do's source, read for the Inbox (issue #1110): the chat's name among the
// owner's kept Zalo and WhatsApp chats; the kept message's sender, whether the owner sent it, and its time; a
// meeting's title and who said the line.
export type Origin = {
  chat?: string
  sender?: string
  fromMe?: boolean
  at?: string
  meeting?: string
  speaker?: string
}

// placeOf is the name a row's source shows after its channel's icon, as Chatwoot's InboxName shows an inbox's name
// beside its ChannelIcon (components/widgets/InboxName.vue:14): the chat's or the person's name, a mail's or a
// meeting's, never the account's number; "Added by you" for the owner's own; the Bot that added it from a chat or a
// Routine's run; who does it when that is someone else.
export function placeOf(
  row: Row,
  origin: Origin = {},
  botName: (id: string) => string = (id) => id,
): string {
  if (row.kind === "approval") {
    const { approval: a } = row

    return `${a.integration}:${a.label} · ${a.action_name} · asked by ${a.asked_by}`
  }
  if (row.kind === "question") return "A worker asks · Team"

  const { todo } = row
  const source = sourceOf(row)
  const label = todo.source.ref?.label
  const where = (() => {
    switch (source) {
      case "hand":
        return sourceNames.hand
      case "zalo":
      case "whatsapp":
        return origin.chat || label
      case "meeting":
        return origin.meeting || label
      case "chat":
      case "routine":
        return todo.created_by.kind === "bot" && todo.created_by.id
          ? botName(todo.created_by.id)
          : label
      default:
        return label
    }
  })()
  const { assignee } = todo
  const who =
    assignee.kind === "bot"
      ? botName(assignee.name ?? "")
      : assignee.kind === "person"
        ? assignee.name
        : undefined

  return [where, who === where ? undefined : who].filter(Boolean).join(" · ")
}

// fromText is where a row comes from in words, its channel then its place, for the search: "WhatsApp · Kai Yong".
export function fromText(
  row: Row,
  origin: Origin = {},
  botName: (id: string) => string = (id) => id,
): string {
  const source = sourceOf(row)
  const place = placeOf(row, origin, botName)
  if (row.kind !== "todo" || source === "hand") return place

  return [sourceNames[source], place].filter(Boolean).join(" · ")
}

// askerOf is who a row comes from, for its avatar, as Chatwoot's card shows the contact's (ConversationCard.vue:129)
// and OpenMausBot's row its sender's (InboxRow.tsx, InboxSender): the sender of the message it was read from, "You"
// for one the owner sent or added, the speaker of a meeting's line, the chat or the person a reference names, the
// Bot that added it or asks for an Approval. A worker's question has none.
export function askerOf(
  row: Row,
  origin: Origin = {},
  botName: (id: string) => string = (id) => id,
): string | undefined {
  if (row.kind === "approval") return botName(row.approval.asked_by)
  if (row.kind === "question") return undefined

  const { todo } = row
  switch (sourceOf(row)) {
    case "hand":
      return "You"
    case "zalo":
    case "whatsapp":
      if (origin.fromMe) return "You"

      return origin.sender || todo.source.ref?.label || origin.chat
    case "meeting":
      return origin.speaker || undefined
    case "chat":
    case "routine":
      return todo.created_by.kind === "bot" && todo.created_by.id
        ? botName(todo.created_by.id)
        : undefined
    default:
      return todo.source.ref?.label || undefined
  }
}

// initialsOf is a name's first letters, at most two, for an avatar without a picture, as Chatwoot's Avatar does.
export function initialsOf(name: string) {
  const words = name.trim().split(/\s+/).filter(Boolean)
  const letters = words.length > 1 ? [words[0] ?? "", words.at(-1) ?? ""] : words

  return letters
    .map((w) => Array.from(w)[0] ?? "")
    .join("")
    .toUpperCase()
}

// atOf is the time a row shows on its right, as Chatwoot shows its last message's: the message's own time when it is
// known, else when the item was added or asked.
export function atOf(row: Row, origin: Origin = {}) {
  if (origin.at) return origin.at

  switch (row.kind) {
    case "approval":
      return row.approval.created_at
    case "question":
      return row.question.created_at
    case "todo":
      return row.todo.created_at
  }
}

// quoteOf is what was said, a row's second line, as a mail's preview (Chatwoot's MessagePreview,
// ConversationCard.vue:192; OpenMausBot's snippetOf): the to-do's quote, on one line.
export function quoteOf(row: Row) {
  return row.kind === "todo" ? (row.todo.source.quote ?? "").replace(/\s+/g, " ").trim() : ""
}

// firstWaiting is the item the Inbox opens on a computer when none is chosen: the first of Needs you, as
// OpenMausBot's InboxPanel keeps an item open (InboxPanel.tsx:47-56). Nothing when nothing waits.
export function firstWaiting(groups: Groups): string | undefined {
  return groups.needs_you[0]?.key
}

// todayCounts is what the Inbox did today, for the pane that says nothing waits: the to-dos added and closed since
// midnight, the owner's own time.
export function todayCounts(rows: Row[], now: number) {
  const midnight = new Date(now)
  midnight.setHours(0, 0, 0, 0)
  const since = (at?: string) => !!at && Date.parse(at) >= midnight.getTime()
  const todos = rows.flatMap((r) => (r.kind === "todo" ? [r.todo] : []))

  return {
    added: todos.filter((t) => since(t.created_at)).length,
    closed: todos.filter((t) => isClosed(t) && since(t.closed_at)).length,
  }
}

// The rows a group shows before "Show N more", as Orca's NEEDS_YOU_CAP and BOT_ITEM_GROUP_ROWS.
const groupCap = 5

// fold is which of a group's rows show, after Orca's needsYouFold: the asks are never hidden, then the first five;
// one hidden row is not worth a footer; the selected row stays in view though it is past the cap.
export function fold(rows: Row[], { open, selected }: { open: boolean; selected?: string }) {
  const asks = rows.filter((r) => r.kind !== "todo" || !!r.approval).length
  const shown = Math.max(groupCap, asks)
  const folds = rows.length - shown >= 2
  const visible = rows.filter((r, i) => !folds || open || i < shown || r.key === selected)

  return { visible, hidden: rows.length - visible.length, folds }
}

export type Action =
  | "approve"
  | "decline"
  | "answer"
  | "copy"
  | "done"
  | "snooze"
  | "accept"
  | "drop"
  | "reopen"
  | "unsnooze"

// actionsOf is what a row offers on hover or focus, by its kind, its own answer first, as OpenMausBot's
// quickActions: an Approval's Approve and Decline (also on the to-do that waits for it), a question's Answer, a
// to-do's Copy draft, Done and Snooze, a proposal's Accept and Drop, and a snoozed or closed item's way back.
export function actionsOf(row: Row, now: number): Action[] {
  if (row.kind === "approval") return ["approve", "decline"]
  if (row.kind === "question") return ["answer"]

  const { todo } = row
  if (isClosed(todo)) return ["reopen"]
  if (isSnoozed(todo, now)) return ["unsnooze"]
  if (row.approval) return ["approve", "decline"]
  if (todo.state === "proposed") return ["accept", "drop"]

  const draft: Action[] = todo.draft ? ["copy"] : []
  // In review, the Approval it waits for is answered first: the server closes neither done nor dropped.
  if (todo.state === "review") return draft
  if (todo.state === "doing") return [...draft, "done"]

  return [...draft, "done", "snooze"]
}

// detailActions is what the detail's top bar offers (issue #1093, section 4): a to-do's Snooze, Drop and Done, a
// proposal's Accept and Drop, a snoozed or closed item's way back. An Approval and a question are answered on their
// own card below it, and a draft is copied beside it.
export function detailActions(row: Row, now: number): Action[] {
  if (row.kind !== "todo" || row.approval) return []

  const { todo } = row
  if (isClosed(todo)) return ["reopen"]
  if (isSnoozed(todo, now)) return ["unsnooze"]
  if (todo.state === "proposed") return ["accept", "drop"]
  if (todo.state === "open") return ["snooze", "drop", "done"]

  return todo.state === "doing" ? ["drop", "done"] : []
}

// nextAfter is where the selection goes once a row is acted on, as OpenMausBot's: the next row down, else the one
// above, else none.
export function nextAfter(keys: readonly string[], key: string): string | undefined {
  const at = keys.indexOf(key)
  const rest = keys.filter((k) => k !== key)
  if (!rest.length) return undefined
  if (at < 0) return rest[0]

  return rest[at] ?? rest[at - 1]
}

// The sources the filter offers (issue #1093, section 5): where a to-do came from, as the owner names it. A mail
// and a meeting have their own source kinds (issue #1105); an item a Routine's run found before them is named by its
// reference.
export type Source =
  | "zalo"
  | "whatsapp"
  | "lark"
  | "gmail"
  | "meeting"
  | "chat"
  | "hand"
  | "routine"

export const sourceNames: Record<Source, string> = {
  zalo: "Zalo",
  whatsapp: "WhatsApp",
  lark: "Lark",
  gmail: "Gmail",
  meeting: "Meeting",
  chat: "Chat",
  hand: "Added by you",
  routine: "Routine",
}

export const filterSources: Source[] = ["zalo", "whatsapp", "lark", "gmail", "meeting", "chat"]

const sourceOfKind: Record<TodoSourceKind, Source> = {
  owner: "hand",
  chat: "chat",
  routine: "routine",
  lark: "lark",
  zalo: "zalo",
  whatsapp: "whatsapp",
  gmail: "gmail",
  meeting: "meeting",
}

export function sourceOf(row: Row): Source {
  if (row.kind === "approval") return row.approval.chat_id ? "chat" : "routine"
  if (row.kind === "question") return "routine"

  const ref = row.todo.source.ref?.kind
  if (ref === "mail") return "gmail"
  if (ref === "meeting_line") return "meeting"

  return sourceOfKind[row.todo.source.kind]
}

// SourceTarget is where "Open in <source>" leads: a chat of the app, a web address, or a meeting at its line.
export type SourceTarget = { chat: string } | { href: string } | { meeting: string; line?: number }

// sourceTarget is where a to-do's source opens (issue #1105): the chat a Bot added it from; a Gmail thread in Gmail,
// in the owner's account the Bot read it with, whose address mailOf gives by its Connection's label; a meeting at
// the action's line; or the message's own link when it is a web address. A Zalo, WhatsApp or Lark chat has no web
// address to open.
export function sourceTarget(
  todo: Todo,
  mailOf: (label: string) => string | undefined,
): SourceTarget | null {
  const { source } = todo
  if (source.kind === "chat" && source.id) return { chat: source.id }
  if (source.kind === "gmail" && source.id) {
    return { href: gmailThread(source.id, source.account ? mailOf(source.account) : undefined) }
  }
  const meeting = meetingOf(todo)
  if (meeting) {
    const line = source.ref?.kind === "meeting_line" ? lineOf(source.ref.ref) : undefined

    return line ? { meeting, line } : { meeting }
  }

  const href = source.ref?.kind === "url" ? safeLink(source.ref.ref) : undefined

  return href ? { href } : null
}

// gmailThread is a thread's address in Gmail, after inbox-zero's getGmailUrl (apps/web/utils/url.ts): the account's
// own address picks it among those signed in, the first one when it is not known.
function gmailThread(thread: string, address?: string) {
  const account = address && /^[^\s/@#?]+@[^\s/@#?]+$/.test(address) ? address : "0"

  return `https://mail.google.com/mail/u/${account}/#all/${encodeURIComponent(thread)}`
}

// meetingOf is the meeting a to-do came from: its source's, or the one its meeting_line reference names, as an item
// a Routine's run found in a meeting before meetings were a source of their own (issue #1105) names it.
export function meetingOf(todo: Pick<Todo, "source">): string | undefined {
  const { source } = todo
  if (source.kind === "meeting" && source.id) return source.id
  if (source.ref?.kind !== "meeting_line") return undefined

  const at = source.ref.ref.lastIndexOf("/")

  return at > 0 ? source.ref.ref.slice(0, at) : undefined
}

// lineOf is the line a meeting_line reference names, "<meeting>/<line>" or the line alone.
export function lineOf(ref: string) {
  const n = Number(ref.slice(ref.lastIndexOf("/") + 1))

  return Number.isInteger(n) && n > 0 ? n : undefined
}

export type Person = "me" | "bot" | "other"

export const personNames: Record<Person, string> = { me: "Me", bot: "A Bot", other: "Someone else" }

// personOf is who does it: an ask waits on the owner himself.
function personOf(row: Row): Person {
  if (row.kind !== "todo") return "me"

  switch (row.todo.assignee.kind) {
    case "me":
      return "me"
    case "bot":
      return "bot"
    default:
      return "other"
  }
}

export type Filter = { query: string; sources: Source[]; people: Person[] }

export const noFilter: Filter = { query: "", sources: [], people: [] }

export function titleOf(row: Row): string {
  switch (row.kind) {
    case "approval":
      return row.approval.title
    case "question":
      return row.question.text
    case "todo":
      return row.todo.title
  }
}

// matches says whether a row passes the search, every word in its title or its source, as OpenMausBot's
// matchesSearch, and the filter's chips.
export function matches(row: Row, filter: Filter, from: string): boolean {
  if (filter.sources.length && !filter.sources.includes(sourceOf(row))) return false
  if (filter.people.length && !filter.people.includes(personOf(row))) return false

  const words = filter.query.toLowerCase().split(/\s+/).filter(Boolean)
  const hay = `${titleOf(row)} ${from}`.toLowerCase()

  return words.every((w) => hay.includes(w))
}

// dayIn is a moment's calendar date in a time zone, as YYYY-MM-DD.
function dayIn(at: number, zone?: string) {
  try {
    return new Intl.DateTimeFormat("en-CA", { timeZone: zone, dateStyle: "short" }).format(at)
  } catch {
    return new Intl.DateTimeFormat("en-CA", { dateStyle: "short" }).format(at)
  }
}

export type DueTone = "late" | "today" | "later"

// dueOf says when a to-do is due, in words that say what the date is: "Due 25/11" for the owner's or a Bot's, "Chase
// 25/11" for one given to someone else, the day to chase them (inboxstore's DueIn); and whether that is past or today,
// which the row colours. A date is due all of its day in its zone; a moment, at it.
export function dueOf(
  todo: Pick<Todo, "due"> & Partial<Pick<Todo, "assignee">>,
  now: number,
): { text: string; tone: DueTone } | null {
  const { due } = todo
  const verb = todo.assignee?.kind === "person" ? "Chase" : "Due"
  if (due.kind === "unknown") return { text: `${verb} date unknown`, tone: "later" }

  if (due.kind === "date" && due.date) {
    const today = dayIn(now, due.zone)
    const tone = due.date < today ? "late" : due.date === today ? "today" : "later"

    return { text: `${verb} ${dayWords(due.date, today)}`, tone }
  }

  if (due.kind === "instant" && due.at) {
    const at = Date.parse(due.at)
    const today = dayIn(now)
    const day = dayIn(at)
    const time = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" }).format(
      at,
    )
    const tone = at < now ? "late" : day === today ? "today" : "later"

    return { text: `${verb} ${dayWords(day, today)} ${time}`, tone }
  }

  return null
}

// dayWords is a day as the owner reads it: today, tomorrow or yesterday, else its day and month, 25/11, 05/12.
function dayWords(day: string, today: string) {
  const diff = Math.round((Date.parse(day) - Date.parse(today)) / 86_400_000)
  if (diff === 0) return "today"
  if (diff === 1) return "tomorrow"
  if (diff === -1) return "yesterday"

  const [, month, date] = day.split("-")

  return `${date}/${month}`
}

// The snooze presets, after Orca's botItemSnoozePresets and botItemSnoozePickerPresets (bot-item-snooze.ts), in the
// owner's own time: Later today (6 PM, or three hours out after 5 PM), Tomorrow and Monday at 9 AM, and a week out
// at 9 AM. On a Monday, Monday is next Monday, the same as Next week, so only Next week stays.
export type SnoozePreset = {
  id: "later" | "tomorrow" | "monday" | "week"
  name: string
  at: number
}

const morning = 9
const evening = 18
const laterCutoff = 17
const laterHours = 3

function atHour(day: Date, hour: number) {
  const time = new Date(day)
  time.setHours(hour, 0, 0, 0)

  return time.getTime()
}

export function snoozePresets(now: number): SnoozePreset[] {
  const today = new Date(now)
  const later =
    today.getHours() < laterCutoff
      ? atHour(today, evening)
      : atHour(today, today.getHours() + laterHours + (today.getMinutes() > 0 ? 1 : 0))
  const tomorrow = new Date(today)
  tomorrow.setDate(today.getDate() + 1)
  const monday = new Date(today)
  monday.setDate(today.getDate() + ((8 - today.getDay()) % 7 || 7))
  const week = new Date(today)
  week.setDate(today.getDate() + 7)
  const inAWeek = atHour(week, morning)

  const presets: SnoozePreset[] = [
    {
      id: "later",
      name: today.getHours() < laterCutoff ? "Later today" : `In ${laterHours} hours`,
      at: later,
    },
    { id: "tomorrow", name: "Tomorrow", at: atHour(tomorrow, morning) },
    { id: "monday", name: "Monday", at: atHour(monday, morning) },
  ]

  return [
    ...presets.filter((p) => p.at !== inAWeek),
    { id: "week", name: "Next week", at: inAWeek },
  ]
}

// snoozeWords is when a snoozed item comes back, as Orca's botItemSnoozeTimeFormat: a time today, a weekday and a
// time within the week, else a date and a time.
export function snoozeWords(until: number, now: number) {
  const time = { hour: "numeric", minute: "2-digit" } as const
  const sameDay = new Date(until).toDateString() === new Date(now).toDateString()
  const options: Intl.DateTimeFormatOptions = sameDay
    ? time
    : until - now < 6 * 86_400_000
      ? { weekday: "short", ...time }
      : { month: "short", day: "numeric", ...time }

  return new Intl.DateTimeFormat(undefined, options).format(until)
}
