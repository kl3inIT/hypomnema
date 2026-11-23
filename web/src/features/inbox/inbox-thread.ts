import type { Todo, TodoEvent } from "@/api/generated/types.gen"

import { sourceNames, type Source } from "./inbox-state"

// An item's thread, handing it to a Bot, and the owner's Send (issue #1111). The thread is the item's events with
// its comments among them, as Orca's BotItemView shows an item's comments in its activity (MIT, Copyright (c) 2026
// Lovecast Inc., https://github.com/stablyai/orca; src/renderer/src/components/bots/items/BotItemView.tsx,
// BotItemActivityList.tsx); a hand-over takes a prompt, as GitHub's "Assign to Copilot" takes additional
// instructions; Send is pressed by the owner on a draft, as inbox-zero's and Zero Mail's.

export type ThreadEntry =
  | { kind: "comment"; id: number; at: string; actor: TodoEvent["actor"]; text: string }
  | { kind: "activity"; id: number; at: string; event: TodoEvent }

// threadOf is an item's history as its thread, oldest first: each comment with its text, every other change as a
// line of activity.
export function threadOf(events: readonly TodoEvent[] = []): ThreadEntry[] {
  return events.map((event): ThreadEntry => {
    const text = (event.after as Record<string, unknown>).text
    if (event.kind === "commented" && typeof text === "string") {
      return { kind: "comment", id: event.id, at: event.at, actor: event.actor, text }
    }

    return { kind: "activity", id: event.id, at: event.at, event }
  })
}

// quickPrompts fill the hand-over's prompt in one click (issue #1111, section 1).
export const quickPrompts = [
  "Do it",
  "Only look into it",
  "Draft a reply",
  "Give it to a worker to fix the code",
] as const

// canHand says whether an item may be handed to a Bot: not closed, and not waiting in review for an Approval.
export function canHand(todo: Pick<Todo, "state">) {
  return !["done", "dropped", "duplicate", "review"].includes(todo.state)
}

// The sources a reply is sent to: a chat or thread of the owner's own account, as the server sends it.
const sendable = new Set<Todo["source"]["kind"]>(["zalo", "whatsapp", "lark", "gmail"])

// canSend says whether the owner may send a reply from the item: it came from his own chat or thread, and is not
// closed.
export function canSend(todo: Pick<Todo, "state" | "source">) {
  return sendable.has(todo.source.kind) && !["done", "dropped", "duplicate"].includes(todo.state)
}

// sendQuestion is what the confirmation asks, naming the recipient and the place, as "Send to Kai Yong on
// WhatsApp?": the person or chat the item names in its message's label, else the chat or thread it came from.
export function sendQuestion(todo: Pick<Todo, "source">) {
  const { source } = todo
  const place = sourceNames[source.kind as Source] ?? source.kind
  const who =
    source.ref?.label?.trim() || (source.kind === "gmail" ? "the thread" : "the chat it came from")
  const account = source.account && source.kind !== "lark" ? ` (${source.account})` : ""

  return `Send to ${who} on ${place}${account}?`
}
