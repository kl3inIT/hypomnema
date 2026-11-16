import type { Bot, Chat } from "@/api/generated/types.gen"

// A Bot as the sidebar lists it (issue #361): its id and its chats, newest first.
export type BotRow = { bot: string; chats: Chat[] }

// The sidebar's sections (issue #470): the main Bot first, then the pinned Bots, then the others, each in the one
// order the owner sets.
export type BotSections = { main?: BotRow; pinned: BotRow[]; rest: BotRow[] }

const newest = (a: Chat, b: Chat) => Date.parse(b.updated_at) - Date.parse(a.updated_at)

// botSections lists every Bot, with chats or not, as OpenMausBot's sidebar lists every bot with its threads
// (Apache License 2.0, https://github.com/milind-soni/OpenMausBot; src/lib/sidebar-layout.ts partitionSidebarBots and
// withPrimaryTile; src/components/Sidebar.tsx BotListItem, BotThreadList). The main Bot, whose id the server names
// (Bot.main), keeps the top place and is never pinned; the pinned Bots come next and the others after, both by the
// Bot's position. A chat whose Bot is not read yet (one made a moment ago) is listed at the end, so it is not lost.
export function botSections(bots: readonly Bot[], chats: readonly Chat[]): BotSections {
  const byBot = new Map<string, Chat[]>()
  for (const chat of [...chats].sort(newest)) {
    const list = byBot.get(chat.bot)
    if (list) list.push(chat)
    else byBot.set(chat.bot, [chat])
  }

  const row = (bot: string): BotRow => ({ bot, chats: byBot.get(bot) ?? [] })
  const ordered = [...bots].sort((a, b) => a.position - b.position)
  const main = ordered.find((bot) => bot.main)
  const others = ordered.filter((bot) => !bot.main)
  const known = new Set(bots.map((bot) => bot.id))
  const unread = [...byBot.keys()].filter((bot) => !known.has(bot))

  return {
    main: main && row(main.id),
    pinned: others.filter((bot) => bot.pinned).map((bot) => row(bot.id)),
    rest: [...others.filter((bot) => !bot.pinned).map((bot) => row(bot.id)), ...unread.map(row)],
  }
}

// visibleChats is what an open Bot lists of its chats (issue #599), OpenMausBot's visibleSidebarThreads (Apache
// License 2.0, https://github.com/milind-soni/OpenMausBot; src/components/SidebarThreadRow.tsx): its 6 newest, and,
// whatever their age, the chat that is open, a chat that is working and one waiting on the owner (OpenMausBot's
// demandsAttention), in the list's order. showAll lists them all. Left out of OpenMausBot's: the search, folders,
// pins, archived, snoozed, routine runs and helper chats, none of which a chat here has.
export function visibleChats(
  chats: readonly Chat[],
  current: string | undefined,
  waiting: ReadonlySet<string>,
  showAll: boolean,
): Chat[] {
  if (showAll) return [...chats]
  let shown = 0

  return chats.filter((c) => shown++ < 6 || c.id === current || c.working || waiting.has(c.id))
}

// reorderBots is the Bots as the server orders them after a reorder that names some of them: those named take, in
// the order given, the places they already hold together; every other Bot keeps its place. It is OpenMausBot's
// reorderStable (shared/bot-order.ts, Apache License 2.0), so the sidebar shows the new order before the server
// answers.
export function reorderBots(bots: readonly Bot[], ids: readonly string[]): Bot[] {
  const rank = new Map(ids.map((id, index) => [id, index] as const))
  const items = [...bots].sort((a, b) => a.position - b.position)
  const slots: number[] = []
  items.forEach((item, index) => {
    if (rank.has(item.id)) slots.push(index)
  })
  const moved = slots.map((slot) => items[slot]!).sort((a, b) => rank.get(a.id)! - rank.get(b.id)!)
  const out = [...items]
  slots.forEach((slot, index) => {
    out[slot] = moved[index]!
  })

  return out.map((bot, position) => (bot.position === position ? bot : { ...bot, position }))
}

// Which Bots the owner opened to their chats, kept in this browser as t3code keeps which projects are expanded
// (MIT, Copyright (c) 2026 T3 Tools Inc., https://github.com/pingdotgg/t3code; apps/web/src/uiStateStore.ts
// projectExpandedById). A Bot is closed until opened, as OpenMausBot's (useSearchDisclosure's initial). A Bot is kept
// by its id (issue #466): a key from before, a Bot's name, names no row and is not read.
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
