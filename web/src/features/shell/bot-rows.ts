import type { Bot, Chat, Department } from "@/api/generated/types.gen"

// A Bot as the sidebar lists it (issue #361): its id, its main chat's id (issue #471) and every chat of it read,
// newest first. The row stands for the main chat; the other chats are not listed under it.
export type BotRow = { bot: string; mainChat?: string; chats: Chat[] }

// The sidebar's sections: the main Bot first, then each department with its Bots (issue #471), then the Bots in
// none, the departments and the Bots each in the one order the owner sets (issue #470).
export type DepartmentRows = { department: Department; rows: BotRow[] }
export type BotSections = { main?: BotRow; departments: DepartmentRows[]; rest: BotRow[] }

const newest = (a: Chat, b: Chat) => Date.parse(b.updated_at) - Date.parse(a.updated_at)

// botSections lists every Bot, with chats or not, as OpenMausBot's sidebar lists every bot (Apache License 2.0,
// https://github.com/milind-soni/OpenMausBot; src/lib/sidebar-layout.ts partitionSidebarBots and withPrimaryTile).
// The main Bot, whose id the server names (Bot.main), keeps the top place and is in no department; each department
// follows, by its position, with its Bots by theirs, as OpenMausBot's sidebar lists a header per department
// (src/components/Sidebar.tsx with bot.section), an empty one too; the Bots in none come last. A Bot whose
// department is not read yet, and a chat whose Bot is not read yet (one made a moment ago), are listed at the end,
// so neither is lost.
export function botSections(
  bots: readonly Bot[],
  chats: readonly Chat[],
  departments: readonly Department[] = [],
): BotSections {
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

  const listed = new Set(departments.map((d) => d.id))
  const inNone = (bot: Bot) => !bot.department || !listed.has(bot.department)

  return {
    main: main && row(main.id),
    departments: [...departments]
      .sort((a, b) => a.position - b.position)
      .map((department) => ({
        department,
        rows: others.filter((bot) => bot.department === department.id).map((bot) => row(bot.id)),
      })),
    rest: [...others.filter(inNone).map((bot) => row(bot.id)), ...unread.map(row)],
  }
}

// opensTo is the chat a Bot opens to (issue #923): its main chat; with none, its most recently active chat that is
// not a Routine's run (a Lark chat included, the owner's own direct chat among them), as OpenMausBot's botOpensTo
// falls back to the thread last open (src/state/thread-navigation.ts) and its main thread is never a routine's run
// (server/main-thread.ts), as OpenClaw opens an agent's latest active session and Hermes Desktop the last session.
// None when the Bot has no chat but Routines' runs, or none at all: then it opens an empty chat with the Bot, as the
// approved design says. chats are the Bot's, newest first.
export function opensTo(mainChat: string | undefined, chats: readonly Chat[]): string | undefined {
  return mainChat ?? chats.find((c) => !c.routine)?.id
}

// rowChat is the chat a Bot's row opens (issue #471): the chat the Bot opens to (opensTo), as OpenClaw's Home and
// Hermes Desktop's Bot Chat open one chat per agent, unless another chat of the Bot waits on the owner and that chat
// does not; then that chat, the newest such, so that the row's Needs you leads to what needs him. None when the Bot
// has no chat it opens to and none waits: the row starts a chat with the Bot.
export function rowChat(row: BotRow, waiting: ReadonlySet<string>): string | undefined {
  const home = opensTo(row.mainChat, row.chats)
  if (home && waiting.has(home)) return home

  return row.chats.find((c) => waiting.has(c.id))?.id ?? home
}

// reorderBots is the Bots as the server orders them after a reorder that names some of them: those named take, in
// the order given, the places they already hold together; every other Bot keeps its place. It is OpenMausBot's
// reorderStable (shared/bot-order.ts, Apache License 2.0), so the sidebar shows the new order before the server
// answers.
export function reorderBots(bots: readonly Bot[], ids: readonly string[]): Bot[] {
  return reorderStable(bots, ids)
}

// reorderDepartmentList is the departments as the server orders them after a reorder, by the same rule.
export function reorderDepartmentList(
  departments: readonly Department[],
  ids: readonly string[],
): Department[] {
  return reorderStable(departments, ids)
}

function reorderStable<T extends { id: string; position: number }>(
  list: readonly T[],
  ids: readonly string[],
): T[] {
  const rank = new Map(ids.map((id, index) => [id, index] as const))
  const items = [...list].sort((a, b) => a.position - b.position)
  const slots: number[] = []
  items.forEach((item, index) => {
    if (rank.has(item.id)) slots.push(index)
  })
  const moved = slots.map((slot) => items[slot]!).sort((a, b) => rank.get(a.id)! - rank.get(b.id)!)
  const out = [...items]
  slots.forEach((slot, index) => {
    out[slot] = moved[index]!
  })

  return out.map((item, position) => (item.position === position ? item : { ...item, position }))
}
