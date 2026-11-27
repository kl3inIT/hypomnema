import {
  Bot,
  Box,
  Check,
  CircleCheck,
  CircleDot,
  CirclePlus,
  CircleX,
  Clock,
  Copy,
  Forward,
  GitPullRequest,
  Pencil,
  Play,
  RotateCcw,
  ShieldCheck,
  Signal,
  Undo2,
  UserRoundCheck,
  type LucideIcon,
} from "lucide-react"

import type { Todo, TodoEvent, TodoLink } from "@/api/generated/types.gen"

import { lineOf, sourceNames, type Source } from "./inbox-state"

// The priorities' names, by their value 0 to 4 (issue #1157), as an event says them.
const priorityNames = ["No priority", "Urgent", "High", "Medium", "Low"]

// An item's comments, handing it to a Bot by writing, and the owner's Send (issues #1111, #1143). The comments are
// the item's events with its comments among them, as Orca's BotItemView shows an item's comments in its activity
// (MIT, Copyright (c) 2026 Lovecast Inc., https://github.com/stablyai/orca; src/renderer/src/components/bots/items/
// BotItemView.tsx, BotItemActivityList.tsx), on a line as GitHub's issue timeline; writing hands the item over, as
// a comment to @copilot does on GitHub; Send is pressed by the owner on a draft, as inbox-zero's and Zero Mail's.

export type ThreadEntry =
  | {
      kind: "comment"
      id: number
      at: string
      actor: TodoEvent["actor"]
      text: string
      // What a Bot's comment carries (issue #1207): the paths of its files in the Bot's folder, read through chat,
      // and the ids of its Artifacts.
      files: string[]
      chat?: string
      artifacts: string[]
    }
  | { kind: "activity"; id: number; at: string; event: TodoEvent }
  | {
      kind: "steps"
      id: number
      from: string
      to: string
      actor: TodoEvent["actor"]
      steps: TodoEvent[]
    }

const closedStates = ["done", "dropped", "duplicate"]

// Waiting is whom an item waits on and the message that shows it (issue #1142), as a Bot's created or edited event
// holds it in after.waiting_on.
export type Waiting = { by: string; quote: string; ref?: TodoLink }

// waitingOf reads after.waiting_on, or nothing when the event has none.
export function waitingOf(after: unknown): Waiting | undefined {
  const w = (after as { waiting_on?: unknown } | null)?.waiting_on as
    | Record<string, unknown>
    | undefined
  if (!w || typeof w.by !== "string" || !w.by) return undefined
  const ref = w.ref as Record<string, unknown> | undefined
  const link =
    ref && typeof ref.kind === "string" && typeof ref.ref === "string"
      ? ({ kind: ref.kind, ref: ref.ref } as TodoLink)
      : undefined

  return { by: w.by, quote: typeof w.quote === "string" ? w.quote : "", ref: link }
}

// WaitingPlace is where the message that shows whom an item waits on is read, by the item's own source: a kept
// Zalo or WhatsApp message, by its chat and id, whose sender and time the server gives; a meeting's line, opened in
// the meeting; the item's Gmail thread; or, for a Lark chat, whose messages the server does not keep, the name alone.
export type WaitingPlace =
  | { kind: "message"; integration: "zalo" | "whatsapp"; chat: string; id: string }
  | { kind: "meeting"; meeting: string; line?: number }
  | { kind: "mail" }
  | { kind: "name" }

export function waitingPlace(source: Todo["source"], waiting: Waiting): WaitingPlace {
  const ref = waiting.ref
  if ((source.kind === "zalo" || source.kind === "whatsapp") && source.id) {
    return ref?.kind === `${source.kind}_message`
      ? { kind: "message", integration: source.kind, chat: source.id, id: ref.ref }
      : { kind: "name" }
  }
  // The store keeps a waiting item's line to one of its own meeting's (inboxstore's checkWaitingOn).
  if (source.kind === "meeting" && source.id && ref?.kind === "meeting_line") {
    return { kind: "meeting", meeting: source.id, line: lineOf(ref.ref) }
  }
  if (source.kind === "gmail" && source.id) return { kind: "mail" }

  return { kind: "name" }
}

// alone says an event that never folds into a group of steps: the item's creation, where its thread starts, its
// closing and reopening, which GitHub's timeline also shows each on its own line, and its move to Waiting on others,
// whose quote shows under it (issue #1142).
export function alone(event: TodoEvent) {
  if (event.kind === "created") return true
  if (event.kind === "edited") return !!waitingOf(event.after)
  if (event.kind !== "state") return false
  const states = [
    (event.after as Record<string, unknown>).state,
    (event.before as Record<string, unknown>).state,
  ]

  return states.some((state) => typeof state === "string" && closedStates.includes(state))
}

const actorKey = (actor: TodoEvent["actor"]) => `${actor.kind}:${actor.id ?? ""}`

// threadOf is an item's history as its timeline, oldest first: each comment with its text, every other change as a
// line of activity, and two or more changes in a row by one actor folded into one group of steps, as GitHub's issue
// timeline folds a push into "added 12 commits" (issue #1143).
export function threadOf(events: readonly TodoEvent[] = []): ThreadEntry[] {
  const entries: ThreadEntry[] = []
  let run: TodoEvent[] = []
  const flush = () => {
    const [first] = run
    const last = run.at(-1)
    if (first && last && run.length > 1) {
      entries.push({
        kind: "steps",
        id: first.id,
        from: first.at,
        to: last.at,
        actor: first.actor,
        steps: run,
      })
    } else if (first) {
      entries.push({ kind: "activity", id: first.id, at: first.at, event: first })
    }
    run = []
  }

  for (const event of events) {
    const after = event.after as Record<string, unknown>
    if (event.kind === "commented" && typeof after.text === "string") {
      flush()
      entries.push({
        kind: "comment",
        id: event.id,
        at: event.at,
        actor: event.actor,
        text: after.text,
        files: strings(after.files),
        chat: typeof after.chat === "string" && after.chat ? after.chat : undefined,
        artifacts: strings(after.artifacts),
      })
      continue
    }
    if (alone(event)) {
      flush()
      entries.push({ kind: "activity", id: event.id, at: event.at, event })
      continue
    }
    const [first] = run
    if (first && actorKey(first.actor) !== actorKey(event.actor)) flush()
    run.push(event)
  }
  flush()

  return entries
}

// strings is a list of strings an event holds, nothing for anything else.
function strings(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((v): v is string => typeof v === "string" && v !== "")
    : []
}

// ReportPart is a piece of a comment: Markdown shown as it is, or a section folded under its title.
export type ReportPart =
  | { kind: "text"; text: string }
  | { kind: "fold"; title: string; body: string }

const fence = /^\s*(```|~~~)/
const opens = /^\s*<details(?:\s[^>]*)?>/i
const closes = /<\/details>\s*$/i
const summary = /<summary(?:\s[^>]*)?>([\s\S]*?)<\/summary>/i

// reportOf splits a Bot's long comment into its parts: what is outside any <details> is shown, and each top-level
// <details> with its <summary> is a folded section, as CodeRabbit's summary comment folds its Walkthrough, Merge
// Risk and Pre-merge checks (issue #1143). A <details> starts and ends on a line of its own; one inside a code
// block is text, and an unclosed one is shown as written. Nested <details> stay in their section's body, folded in
// their turn when it is shown.
export function reportOf(text: string): ReportPart[] {
  const parts: ReportPart[] = []
  const lines = text.split("\n")
  let plain: string[] = []
  let fold: string[] | null = null
  let depth = 0
  let inFence = false
  const keep = () => {
    const shown = plain.join("\n").trim()
    if (shown) parts.push({ kind: "text", text: shown })
    plain = []
  }

  for (const line of lines) {
    if (fence.test(line)) inFence = !inFence
    if (inFence || fence.test(line)) {
      ;(fold ?? plain).push(line)
      continue
    }
    if (!fold && opens.test(line)) {
      keep()
      fold = [line]
      depth = 0
    } else if (fold) {
      fold.push(line)
    } else {
      plain.push(line)
      continue
    }
    if (opens.test(line)) depth += line.match(/<details(?:\s[^>]*)?>/gi)?.length ?? 0
    if (closes.test(line)) depth -= line.match(/<\/details>/gi)?.length ?? 0
    if (depth <= 0) {
      parts.push(foldOf(fold.join("\n")))
      fold = null
    }
  }
  if (fold) plain.push(...fold)
  keep()

  return parts
}

function foldOf(html: string): ReportPart {
  const inner = html.replace(opens, "").replace(closes, "")
  const title = summary.exec(inner)
  const words = (title?.[1] ?? "Details").replace(/<[^>]+>/g, "").trim() || "Details"

  return { kind: "fold", title: words, body: (title ? inner.replace(summary, "") : inner).trim() }
}

// throughWhom names who has an item and, when a Bot passed it to them, through whom (issue #1118): the main Bot
// passed it to a project Bot, "Hypo → BeyondPilot", as Linear shows an issue's assignee with the agent it is
// delegated to.
export function throughWhom(
  todo: Pick<Todo, "passed_by">,
  who: string,
  botName: (id: string) => string,
) {
  return todo.passed_by ? `${botName(todo.passed_by)} → ${who}` : who
}

// handoverWords says a change of who has the item in words, or nothing when the change gave it to no one new: to
// the owner, given back; to a Bot through the Bot who passed it, passed; else given (issues #1111, #1118).
export function handoverWords(
  after: Record<string, unknown>,
  who: string,
  botName: (id: string) => string,
) {
  const to = typeof after.assignee === "string" ? after.assignee : ""
  switch (after.assignee_kind) {
    case "me":
      return who === "You" ? "You took it back" : `${who} gave it back to you`
    case "bot":
      return typeof after.passed_by === "string"
        ? `${who} passed it to ${botName(to)}`
        : `${who} gave it to ${botName(to)}`
    case "person":
      return to ? `${who} gave it to ${to}` : undefined
    default:
      return undefined
  }
}

// Write is what the item's composer does with what the owner writes (issue #1143): an item no Bot has is handed to
// the main Bot with the text as its prompt, as GitHub hands an issue to Copilot from a comment that names it; one a
// Bot has takes the text as a comment, which reaches the Bot in the item's chat, as Linear's agent works from a
// comment; a closed one is reopened, then handed back to its Bot with the text. A Bot given the item with no chat
// of its own is handed it again, which wakes it in a new one.
export type Write = { act: "hand" | "comment" | "reopen"; bot: string; label: string }

export function writeOf(
  todo: Pick<Todo, "state" | "assignee" | "chat_id">,
  mainBot: string,
  botName: (id: string) => string,
): Write & { placeholder: string } {
  const held = todo.assignee.kind === "bot" && todo.assignee.name ? todo.assignee.name : ""
  const bot = held || mainBot
  const name = bot ? botName(bot) : "Hypo"
  const placeholder = `Message ${name}…`

  if (closedStates.includes(todo.state))
    return { act: "reopen", bot, label: "Reopen and send", placeholder }
  if (!held) return { act: "hand", bot, label: `Hand to ${name}`, placeholder }

  return { act: todo.chat_id ? "comment" : "hand", bot, label: "Send", placeholder }
}

// actorName names who wrote or changed the item: the owner, a Bot by its name, or Hypomnema.
export function actorName(actor: TodoEvent["actor"], botName: (id: string) => string) {
  if (actor.kind === "owner") return "You"

  return actor.kind === "bot" ? botName(actor.id ?? "") : "Hypomnema"
}

// EventLine is one change of the item's history as its timeline shows it: its words, its icon on the line, and
// whether it closed the item as done, which shows in violet, as GitHub's "closed this as completed".
export type EventLine = { words: string; icon: LucideIcon; done?: boolean }

// eventLine says one change in words, who and what; from is where an added item came from ("Zalo · Laura
// Nguyen"), said on its creation.
export function eventLine(
  e: TodoEvent,
  botName: (id: string) => string,
  from?: string,
  projectName: (id: string) => string | undefined = () => undefined,
): EventLine {
  const who = actorName(e.actor, botName)
  const after = e.after as Record<string, unknown>
  const before = e.before as Record<string, unknown>

  const waiting = waitingOf(after)

  switch (e.kind) {
    case "created":
      if (waiting) {
        const added = from ? `${who} added it from ${from}` : `${who} added it`

        return { words: `${added}, waiting on ${waiting.by}`, icon: CirclePlus }
      }

      return { words: from ? `${who} added it from ${from}` : `${who} added it`, icon: CirclePlus }
    case "state":
      return stateLine(who, String(after.state ?? ""), String(before.state ?? ""), after)
    case "snoozed":
      return {
        words: after.snoozed_until ? `${who} snoozed it` : `${who} brought it back`,
        icon: Clock,
      }
    case "linked_approval":
      return { words: `${who} asked for an Approval`, icon: ShieldCheck }
    case "edited":
      if (waiting)
        return { words: `${who} moved it to Waiting on ${waiting.by}`, icon: UserRoundCheck }

      return editLine(who, after, botName, projectName)
    default:
      return { words: `${who}: ${e.kind}`, icon: CircleDot }
  }
}

function stateLine(
  who: string,
  state: string,
  was: string,
  after: Record<string, unknown>,
): EventLine {
  switch (state) {
    case "doing":
      return after.work_kind === "run"
        ? { words: `${who} started a Run`, icon: Play }
        : { words: `${who} started on it`, icon: Play }
    case "review":
      return { words: `${who} waits for an Approval`, icon: ShieldCheck }
    case "done": {
      // The owner's answer in another chat where the person who asked is (issue #1163) says that chat.
      const where = typeof after.answered_in === "string" && after.answered_in
      const words = where
        ? `${who} closed it as done: answered in ${where}`
        : `${who} closed it as done`

      return { words, icon: CircleCheck, done: true }
    }
    case "dropped":
      return { words: `${who} dropped it`, icon: CircleX }
    case "duplicate":
      return { words: `${who} closed it as a duplicate`, icon: Copy }
    case "open":
      if (closedStates.includes(was)) return { words: `${who} reopened it`, icon: RotateCcw }
      if (was === "proposed") return { words: `${who} accepted it`, icon: Check }

      return { words: `${who} moved it to open`, icon: CircleDot }
    default:
      return { words: `${who} moved it to ${state || "a new state"}`, icon: CircleDot }
  }
}

function editLine(
  who: string,
  after: Record<string, unknown>,
  botName: (id: string) => string,
  projectName: (id: string) => string | undefined,
): EventLine {
  const handed = handoverWords(after, who, botName)
  if (handed) {
    const icon =
      after.assignee_kind === "me" ? Undo2 : typeof after.passed_by === "string" ? Forward : Bot

    return { words: handed, icon }
  }
  // A priority set, with the reason it was set for, and a project changed (issue #1157).
  if (typeof after.priority === "number") {
    const name = priorityNames[after.priority] ?? String(after.priority)
    const why = typeof after.priority_reason === "string" && after.priority_reason
    const words = after.priority
      ? `${who} set the priority to ${name}`
      : `${who} removed the priority`

    return { words: why ? `${words}: ${why}` : words, icon: Signal }
  }
  if ("project_id" in after) {
    const reason = typeof after.reason === "string" ? ` (${after.reason})` : ""

    return {
      words:
        typeof after.project_id === "string"
          ? `${who} moved it to ${projectName(after.project_id) ?? "a project"}`
          : `${who} took it out of its project${reason}`,
      icon: Box,
    }
  }
  const links = Array.isArray(after.evidence) ? (after.evidence as { kind?: string }[]) : []
  if (links.some((l) => l.kind === "pull_request"))
    return { words: `${who} linked a pull request`, icon: GitPullRequest }
  if (typeof after.draft === "string" && Object.keys(after).length === 1)
    return { words: `${who} wrote a draft`, icon: Pencil }

  return { words: `${who} changed ${Object.keys(after).join(", ") || "it"}`, icon: Pencil }
}

// The sources a reply is sent to: a chat or thread of the owner's own account, as the server sends it.
const sendable = new Set<Todo["source"]["kind"]>(["zalo", "whatsapp", "lark", "gmail"])

// canSend says whether the owner may send a reply from the item: it came from his own chat or thread, and is not
// closed.
export function canSend(todo: Pick<Todo, "state" | "source">) {
  return sendable.has(todo.source.kind) && !["done", "dropped", "duplicate"].includes(todo.state)
}

// sendQuestion is what the confirmation asks, naming the recipient and the place, as "Send to Kai Yong on
// WhatsApp?": the chat's or person's name as the server keeps it (issue #1110's useOrigin), else the label the item's
// message has, else the chat or thread it came from.
export function sendQuestion(todo: Pick<Todo, "source">, name?: string) {
  const { source } = todo
  const place = sourceNames[source.kind as Source] ?? source.kind
  const who =
    name?.trim() ||
    source.ref?.label?.trim() ||
    (source.kind === "gmail" ? "the thread" : "the chat it came from")
  const account = source.account && source.kind !== "lark" ? ` (${source.account})` : ""

  return `Send to ${who} on ${place}${account}?`
}
