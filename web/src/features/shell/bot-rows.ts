import type { Bot, Chat } from "@/api/generated/types.gen"

// A Bot as the sidebar lists it (issue #361): its id, its main chat's id (issue #471) and every chat of it read,
// newest first. The row stands for the main chat; the other chats are not listed under it.
export type BotRow = { bot: string; mainChat?: string; chats: Chat[] }

// The sidebar's sections (issue #470): the main Bot first, then the pinned Bots, then the others, each in the one
// order the owner sets.
export type BotSections = { main?: BotRow; pinned: BotRow[]; rest: BotRow[] }

const newest = (a: Chat, b: Chat) => Date.parse(b.updated_at) - Date.parse(a.updated_at)

// botSections lists every Bot, with chats or not, as OpenMausBot's sidebar lists every bot (Apache License 2.0,
// https://github.com/milind-soni/OpenMausBot; src/lib/sidebar-layout.ts partitionSidebarBots and withPrimaryTile).
// The main Bot, whose id the server names (Bot.main), keeps the top place and is never pinned; the pinned Bots come
// next and the others after, both by the Bot's position. A chat whose Bot is not read yet (one made a moment ago) is
// listed at the end, so it is not lost.
export function botSections(bots: readonly Bot[], chats: readonly Chat[]): BotSections {
  const byBot = new Map<string, Chat[]>()
  for (const chat of [...chats].sort(newest)) {
    const list = byBot.get(chat.bot)
    if (list) list.push(chat)
    else byBot.set(chat.bot, [chat])
  }

  const mains = new Map(bots.map((bot) => [bot.id, bot.main_chat] as const))
  const row = (bot: string): BotRow => ({
    bot,
    ...(mains.get(bot) && { mainChat: mains.get(bot) }),
    chats: byBot.get(bot) ?? [],
  })
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

// rowChat is the chat a Bot's row opens (issue #471): its main chat, as OpenClaw's Home and Hermes Desktop's Bot
// Chat open one chat per agent, unless another chat of the Bot waits on the owner and the main chat does not; then
// that chat, the newest such, so that the row's Needs you leads to what needs him. None when the Bot has no main
// chat and none waits: the row starts a chat with the Bot.
export function rowChat(row: BotRow, waiting: ReadonlySet<string>): string | undefined {
  if (row.mainChat && waiting.has(row.mainChat)) return row.mainChat

  return row.chats.find((c) => waiting.has(c.id))?.id ?? row.mainChat
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
