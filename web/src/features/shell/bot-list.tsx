import { Link } from "@tanstack/react-router"
import { CircleDashed } from "lucide-react"
import type { DragEvent, ReactNode } from "react"

import "@/api/client"
import type { Chat } from "@/api/generated/types.gen"
import { usePending } from "@/features/approvals/use-approvals"
import { AgentAvatar } from "@/components/agent-avatar"
import { useBotName, useBots, useReorderBots } from "@/features/bots/use-bots"
import { setDragged, tabDrag } from "@/features/workbench/use-grid"
import { useIsMobile } from "@/hooks/use-mobile"
import { since } from "@/lib/time"
import { cn } from "@/lib/utils"

import { BotMenu, PinItem } from "./bot-menu"
import { type BotRow, botSections, rowChat } from "./bot-rows"
import { ChatMenu } from "./chat-menu"
import { useChats } from "./chats"
import { ReorderableList, RowGrab } from "./reorderable-list"
import { type SortableBindings, useSortableBindings } from "./use-sortable-bindings"

// BotList is the sidebar's Bots (issue #361): a row a Bot, which opens its one main chat (issue #471), as
// OpenMausBot's sidebar with its threads hidden (Apache License 2.0, https://github.com/milind-soni/OpenMausBot;
// src/components/Sidebar.tsx BotListItem; src/lib/thread-preferences.ts, showThreads off) and Hermes Desktop's
// Bot Chat. A Bot's other chats are not listed: they open from their cards, questions, approvals and links, and from
// the Bot's page. Every Bot is listed, in the one order the owner sets by dragging its avatar, with the main Bot
// first and the pinned Bots in a Pinned section under it (issue #470; OpenMausBot's src/lib/sidebar-layout.ts, the
// "Pinned" header of t3code's Sidebar.tsx). A drag stays in its section; a phone shows the order without dragging.
// Left out of OpenMausBot's: departments, unread marks and a queued state, none of which a chat here has.
export function BotList({ current }: { current?: string }) {
  const chats = useChats()
  const bots = useBots()
  // A chat in which pi asked for an approval says so here: the owner may not have it open.
  const pending = usePending()
  const waiting = new Set(pending.data?.flatMap((a) => (a.chat_id ? [a.chat_id] : [])))
  const reorder = useReorderBots()
  const phone = useIsMobile()

  const pinned = new Set(bots.data?.filter((bot) => bot.pinned).map((bot) => bot.id))
  const sections = chats.data && botSections(bots.data ?? [], chats.data)

  const item = (row: BotRow, main: boolean, sortable?: SortableBindings) => (
    <BotItem
      key={row.bot}
      row={row}
      main={main}
      pinned={pinned.has(row.bot)}
      sortable={sortable}
      current={current}
      waiting={waiting}
    />
  )

  const section = (rows: BotRow[]) => (
    <ReorderableList ids={rows.map((row) => row.bot)} onReorder={(ids) => reorder.mutate(ids)}>
      {rows.map((row) => (
        <SortableBot key={row.bot} id={row.bot} phone={phone}>
          {(sortable) => item(row, false, sortable)}
        </SortableBot>
      ))}
    </ReorderableList>
  )

  return (
    <nav aria-label="Bots" className="-mx-2 min-h-0 flex-1 overflow-y-auto px-2">
      <p className="px-2.5 pt-2 pb-1 text-xs font-medium text-muted-foreground">Bots</p>
      {sections && (
        <div className="flex flex-col gap-px">
          {sections.main && item(sections.main, true)}
          {sections.pinned.length > 0 && (
            <section aria-label="Pinned" className="flex flex-col gap-px">
              <p className="px-2.5 pt-3 pb-1 text-xs font-medium text-muted-foreground">Pinned</p>
              {section(sections.pinned)}
            </section>
          )}
          {sections.pinned.length > 0 && sections.rest.length > 0 && (
            <hr className="mx-2.5 my-1.5 border-sidebar-border" />
          )}
          {section(sections.rest)}
        </div>
      )}
    </nav>
  )
}

// SortableBot ties a Bot's row to its section's order; a phone shows the order without dragging.
function SortableBot({
  id,
  phone,
  children,
}: {
  id: string
  phone: boolean
  children: (sortable: SortableBindings) => ReactNode
}) {
  return children(useSortableBindings(id, phone))
}

function BotItem({
  row,
  main,
  pinned,
  sortable,
  current,
  waiting,
}: {
  row: BotRow
  main: boolean
  pinned: boolean
  sortable?: SortableBindings
  current?: string
  waiting: Set<string>
}) {
  const { bot, chats } = row
  // The row is the Bot's id; it is shown by its name, which may change (issue #466).
  const name = useBotName(bot)
  const mainChat = chats.find((c) => c.id === row.mainChat)
  // The main chat, or the chat that waits on the owner when the main chat does not (bot-rows.ts rowChat).
  const target = rowChat(row, waiting)
  const needsYou = chats.some((c) => waiting.has(c.id))
  const working = chats.some((c) => c.working)
  // The Bot's row stands for every chat of the Bot that is open, as OpenMausBot's aria-current.
  const here =
    current !== undefined && (current === row.mainChat || chats.some((c) => c.id === current))
  // Every Bot but the main one has a menu, to pin it; a Bot with a main chat has that chat's too.
  const hasMenu = Boolean(mainChat) || !main
  const menuPlace =
    "absolute end-1 top-1.5 transition-opacity md:pointer-events-none md:opacity-0 md:group-focus-within/bot:pointer-events-auto md:group-focus-within/bot:opacity-100 md:group-hover/bot:pointer-events-auto md:group-hover/bot:opacity-100 md:has-[[data-popup-open]]:pointer-events-auto"

  const face = (
    <span className="relative flex">
      <AgentAvatar name={name} className="size-7" />
      {/* OpenMausBot's status dots, at the avatar's foot: needs you before working (L744-761). */}
      {needsYou ? (
        <span
          data-testid="needs-you-dot"
          className="absolute -right-0.5 -bottom-0.5 flex size-3.5 items-center justify-center rounded-full border-2 border-sidebar bg-needs-you text-[9px] leading-none font-black text-sidebar"
          aria-hidden
        >
          !
        </span>
      ) : working ? (
        <span
          data-testid="working-dot"
          className="absolute -right-0.5 -bottom-0.5 size-2.5 rounded-full border-2 border-sidebar bg-running"
          aria-hidden
        />
      ) : null}
    </span>
  )

  return (
    <div
      ref={sortable?.ref}
      style={sortable?.style}
      className={cn(sortable?.dragging && "relative z-10 rounded-md bg-sidebar shadow-md")}
    >
      <div className="group/bot relative">
        <Link
          {...(target
            ? { to: "/chats/$chatId", params: { chatId: target } }
            : { to: "/", search: { bot } })}
          // The row carries its main chat to the grid (issue #235).
          {...(row.mainChat ? chatDrag(row.mainChat) : { draggable: false })}
          aria-current={here ? "page" : undefined}
          className={cn(
            "flex min-h-11 w-full items-center gap-2.5 rounded-md px-2.5 py-1.5 text-left outline-none select-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset",
            here
              ? "bg-sidebar-row-active [:root:not(.dark)_&]:ring-1 [:root:not(.dark)_&]:ring-sidebar-border/70 [:root:not(.dark)_&]:ring-inset"
              : "group-hover/bot:bg-sidebar-row-hover",
            sortable?.dragging && "bg-sidebar-row-hover",
            // Room for the row's menu, always shown on a phone (issue #503).
            hasMenu && "max-md:pe-9",
          )}
        >
          {/* The avatar's place: the avatar is drawn over it, outside the link, so that it can be the handle that
              moves the Bot while the row keeps its own drag to the grid (issue #470). */}
          <span className="size-7 shrink-0" aria-hidden />
          <span className="flex min-w-0 flex-1 flex-col">
            <span className="flex items-baseline gap-2">
              <span className="min-w-0 flex-1 truncate text-sm leading-5 font-medium text-foreground">
                {name}
              </span>
              <span className="shrink-0 text-xs text-secondary-label tabular-nums transition-opacity md:group-focus-within/bot:opacity-0 md:group-hover/bot:opacity-0">
                <ChatState chat={mainChat} needsYou={needsYou} working={working} />
              </span>
            </span>
            <span className="truncate text-xs leading-4 text-muted-foreground">
              {mainChat ? mainChat.last_line || mainChat.title : row.mainChat ? "" : "No chat yet"}
            </span>
          </span>
        </Link>
        {/* Hermes's avatar that turns into a grabber when pointed at or focused (chrome.tsx SidebarRowGrab); the
            main Bot, always first, and a phone have none, and their avatar lets a press through to the row. */}
        {sortable && !sortable.disabled ? (
          <RowGrab
            ariaLabel={`Move ${name}`}
            dragging={sortable.dragging}
            dragHandleProps={sortable.dragHandleProps}
            className="absolute start-2.5 top-1/2 size-7 -translate-y-1/2"
          >
            {face}
          </RowGrab>
        ) : (
          <span className="pointer-events-none absolute start-2.5 top-1/2 flex -translate-y-1/2">
            {face}
          </span>
        )}
        {/* The row's menu, as OpenMausBot's Bot row has an actions button: Pin or Unpin (issue #470), and, on a Bot
            with a main chat, that chat's Delete (issue #503). Shown on hover or focus, always on a phone. */}
        {mainChat ? (
          <ChatMenu
            chat={mainChat}
            current={mainChat.id === current}
            label={`Actions for ${name}`}
            className={menuPlace}
          >
            {!main && <PinItem bot={bot} pinned={pinned} />}
          </ChatMenu>
        ) : (
          !main && <BotMenu bot={bot} name={name} pinned={pinned} className={menuPlace} />
        )}
      </div>
    </div>
  )
}

// ChatState is what a chat's row says at its end: that it needs the owner, that it is working, or how long ago it
// moved.
function ChatState({
  chat,
  needsYou,
  working,
}: {
  chat?: Chat
  needsYou: boolean
  working: boolean
}) {
  if (needsYou) return <span className="font-medium text-needs-you">Needs you</span>
  if (working) {
    return (
      <span className="inline-flex items-center gap-1 font-medium text-info-foreground">
        <CircleDashed className="size-3.5 animate-spin [animation-duration:3s]" aria-hidden />
        Working
      </span>
    )
  }

  return chat ? since(chat.updated_at) : null
}

// A chat is dragged onto an open chat, or a pane of the grid, to be shown beside it (issue #235).
function chatDrag(chat: string) {
  return {
    onDragStart: (event: DragEvent<HTMLAnchorElement>) => {
      event.dataTransfer.effectAllowed = "move"
      event.dataTransfer.setData(tabDrag, `chat:${chat}`)
      setDragged({ tab: { kind: "chat", chat }, from: null })
    },
    onDragEnd: () => setDragged(null),
  }
}
