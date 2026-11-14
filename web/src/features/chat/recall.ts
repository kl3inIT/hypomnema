import type { ChatMessage } from "@/api/generated/types.gen"

// ↑ in the composer brings back what this chat sent (issue #433), adapted from opencode's walk through its prompt
// history (https://github.com/anomalyco/opencode, packages/app/src/composer/suggestions/machine.ts, stepHistory and
// canNavigateHistory, and editor/interaction.ts, navigateHistory; MIT, Copyright (c) 2025 opencode):
//
// - The walk starts with ↑ only in an empty box with the caret at its start, as opencode's does and OpenMausBot's ↑
//   in an empty composer (src/components/Composer.tsx, onEditLast) is taken only then.
// - While walking, ↑ and ↓ step when the caret is at the start or the end of the text, so that a message of several
//   lines can still be moved through with the caret. ↑ goes to an older message with the caret at its start, ↓ to a
//   newer one with the caret at its end, and ↓ past the newest gives the empty box back (opencode's saved draft,
//   which is always empty here: the walk starts only in an empty box, and the files attached stay as they are).
// - Typing ends the walk and keeps what is in the box (opencode's draft.edited).
//
// The messages walked are this chat's, as OpenMausBot's ↑ takes the chat's last message, not opencode's history of
// every session.

// A walk is the index of the message in the box, 0 the newest, or null when none is.
export type Walk = number | null

export type Recalled = { walk: Walk; text: string; caret: number }

export type Direction = "up" | "down"

function canWalk(direction: Direction, text: string, caret: number, walking: boolean) {
  const at = Math.max(0, Math.min(caret, text.length))
  if (walking) return at === 0 || at === text.length
  if (direction === "up") return at === 0 && text.length === 0

  return false
}

// recall is the box after ↑ or ↓, or null when the key is the textarea's own: entries are the messages sent,
// newest first.
export function recall(
  walk: Walk,
  direction: Direction,
  text: string,
  caret: number,
  entries: readonly string[],
): Recalled | null {
  if (!canWalk(direction, text, caret, walk !== null)) return null

  if (direction === "up") {
    const index = walk === null ? 0 : walk + 1
    const entry = entries[index]
    if (entry === undefined) return null

    return { walk: index, text: entry, caret: 0 }
  }

  if (walk === null) return null
  if (walk === 0) return { walk: null, text: "", caret: 0 }
  const entry = entries[walk - 1]
  if (entry === undefined) return null

  return { walk: walk - 1, text: entry, caret: entry.length }
}

// sentTexts are the texts the owner sent in a chat, newest first; one sent twice in a row is there once, as
// opencode's prependHistoryEntry skips an entry equal to the last.
export function sentTexts(messages: readonly ChatMessage[]): string[] {
  const texts: string[] = []
  for (let i = messages.length - 1; i >= 0; i--) {
    const message = messages[i]
    if (message?.role !== "owner" || !message.text.trim()) continue
    if (texts.at(-1) === message.text) continue
    texts.push(message.text)
  }

  return texts
}
