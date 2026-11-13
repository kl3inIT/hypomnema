import type { Chat } from "@/api/generated/types.gen"
import { firstBot } from "@/features/bots/bots-state"

// A Bot as the sidebar lists it (issue #361): its chats, newest first.
export type BotRow = { bot: string; chats: Chat[] }

const newest = (a: Chat, b: Chat) => Date.parse(b.updated_at) - Date.parse(a.updated_at)

// botRows groups the chats by their Bot, as OpenMausBot lists a Bot with its threads (Apache License 2.0,
// https://github.com/milind-soni/OpenMausBot; src/components/Sidebar.tsx BotListItem, BotThreadList). The Bot
// whose chat moved last comes first: OpenMausBot keeps an order the person drags, which is left out here until
// dragging comes (issue #361, owner, 2026-11-13). The first Bot, coordinator, is always listed, last while it has
// no chat.
export function botRows(chats: readonly Chat[]): BotRow[] {
  const byBot = new Map<string, Chat[]>()
  for (const chat of [...chats].sort(newest)) {
    const list = byBot.get(chat.bot)
    if (list) list.push(chat)
    else byBot.set(chat.bot, [chat])
  }

  const rows = [...byBot].map(([bot, list]) => ({ bot, chats: list }))
  if (!byBot.has(firstBot)) rows.push({ bot: firstBot, chats: [] })

  return rows
}

// Which Bots the owner opened to their chats, kept in this browser as t3code keeps which projects are expanded
// (MIT, Copyright (c) 2026 T3 Tools Inc., https://github.com/pingdotgg/t3code; apps/web/src/uiStateStore.ts
// projectExpandedById). A Bot is closed until opened, as OpenMausBot's (useSearchDisclosure's initial).
const openKey = "hypomnema.sidebar.open-bots"

export function readOpenBots(): Record<string, boolean> {
  try {
    const value: unknown = JSON.parse(window.localStorage.getItem(openKey) ?? "{}")
    if (typeof value !== "object" || value === null || Array.isArray(value)) return {}

    return Object.fromEntries(Object.entries(value).filter(([, open]) => typeof open === "boolean"))
  } catch {
    return {}
  }
}

export function keepOpenBots(open: Record<string, boolean>) {
  try {
    window.localStorage.setItem(openKey, JSON.stringify(open))
  } catch {
    // Kept until the page closes.
  }
}
