import { Link } from "@tanstack/react-router"
import { ChevronRight, CircleDashed, Plus } from "lucide-react"
import { type DragEvent, type ReactNode, useState } from "react"

import "@/api/client"
import type { Chat } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { usePending } from "@/features/approvals/use-approvals"
import { AgentAvatar } from "@/components/agent-avatar"
import { useBotName, useBots, useReorderBots } from "@/features/bots/use-bots"
import { setDragged, tabDrag } from "@/features/workbench/use-grid"
import { useIsMobile } from "@/hooks/use-mobile"
import { since } from "@/lib/time"
import { cn } from "@/lib/utils"

import { BotMenu, PinItem } from "./bot-menu"
import { type BotRow, botSections, keepOpenBots, readOpenBots } from "./bot-rows"
import { ChatMenu } from "./chat-menu"
import { useChats } from "./chats"
import { ReorderableList, RowGrab } from "./reorderable-list"
import { type SortableBindings, useSortableBindings } from "./use-sortable-bindings"

// BotList is the sidebar's Bots (issue #361): a row a Bot, which opens to its chats, as OpenMausBot's sidebar
// (Apache License 2.0, https://github.com/milind-soni/OpenMausBot; src/components/Sidebar.tsx BotListItem,
// BotThreadList). Every Bot is listed, in the one order the owner sets by dragging its avatar, with the main Bot
// first and the pinned Bots in a Pinned section under it (issue #470; OpenMausBot's src/lib/sidebar-layout.ts, the
// "Pinned" header of t3code's Sidebar.tsx). A drag stays in its section; a phone shows the order without dragging.
// Left out of OpenMausBot's: departments, unread marks and a queued state, none of which a chat here has.
export function BotList({ current }: { current?: string }) {
  const chats = useChats()
  const bots = useBots()
  // A chat in which pi asked for an approval says so here: the owner may not have it open.
  const pending = usePending()
  const waiting = new Set(pending.data?.flatMap((a) => (a.chat_id ? [a.chat_id] : [])))
  const [open, setOpen] = useState(readOpenBots)
  const reorder = useReorderBots()
  const phone = useIsMobile()

  function toggle(bot: string) {
    const next = { ...open, [bot]: !open[bot] }
    setOpen(next)
    keepOpenBots(next)
  }

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
      open={open[row.bot] === true}
      onToggle={() => toggle(row.bot)}
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
  open,
  onToggle,
}: {
  row: BotRow
  main: boolean
  pinned: boolean
  sortable?: SortableBindings
  current?: string
  waiting: Set<string>
  open: boolean
  onToggle: () => void
}) {
  const { bot, chats } = row
  // The row is the Bot's id; it is shown by its name, which may change (issue #466).
  const name = useBotName(bot)
  const latest = chats.at(0)
  // OpenMausBot's hasThreadList: a Bot with one chat has nothing to open.
  const hasChatList = chats.length > 1
  // The chat a Bot's row stands for when it has no other.
  const onlyChat = chats.length === 1 ? latest : undefined
  const expanded = hasChatList && open
  const needsYou = chats.some((c) => waiting.has(c.id))
  const working = chats.some((c) => c.working)
  // The Bot's row stands for the open chat while its chats are not shown, as OpenMausBot's aria-current.
  const here = !expanded && chats.some((c) => c.id === current)
  // Every Bot but the main one has a menu, to pin it; a Bot that stands for its one chat has that chat's too.
  const hasMenu = Boolean(onlyChat) || !main
  const menuPlace = cn(
    "absolute end-1 transition-opacity md:pointer-events-none md:opacity-0 md:group-focus-within/bot:pointer-events-auto md:group-focus-within/bot:opacity-100 md:group-hover/bot:pointer-events-auto md:group-hover/bot:opacity-100 md:has-[[data-popup-open]]:pointer-events-auto",
    expanded ? "top-1/2 -translate-y-1/2" : "top-1.5",
  )

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
    // The Bot and its open chats move together when it is dragged.
    <div
      ref={sortable?.ref}
      style={sortable?.style}
      className={cn(sortable?.dragging && "relative z-10 rounded-md bg-sidebar shadow-md")}
    >
      <div className="group/bot relative">
        <Link
          {...(latest
            ? { to: "/chats/$chatId", params: { chatId: latest.id } }
            : { to: "/", search: { bot } })}
          // The row carries its latest chat to the grid, as a chat's row does: a Bot with one chat shows no other.
          {...(latest ? chatDrag(latest.id) : { draggable: false })}
          aria-current={here ? "page" : undefined}
          className={cn(
            "flex min-h-11 w-full items-center gap-2.5 rounded-md py-1.5 ps-6 pe-2.5 text-left outline-none select-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset",
            here
              ? "bg-sidebar-row-active [:root:not(.dark)_&]:ring-1 [:root:not(.dark)_&]:ring-sidebar-border/70 [:root:not(.dark)_&]:ring-inset"
              : "group-hover/bot:bg-sidebar-row-hover",
            sortable?.dragging && "bg-sidebar-row-hover",
            // Room for the row's menu, always shown on a phone (issue #503), as a chat row keeps it.
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
              {!expanded && (
                <span className="shrink-0 text-xs text-secondary-label tabular-nums transition-opacity md:group-focus-within/bot:opacity-0 md:group-hover/bot:opacity-0">
                  <ChatState chat={latest} needsYou={needsYou} working={working} />
                </span>
              )}
            </span>
            {!expanded && (
              <span className="truncate text-xs leading-4 text-muted-foreground">
                {latest?.title ?? "No chats yet"}
              </span>
            )}
          </span>
        </Link>
        {/* Hermes's avatar that turns into a grabber when pointed at or focused (chrome.tsx SidebarRowGrab); the
            main Bot, always first, and a phone have none, and their avatar lets a press through to the row. */}
        {sortable && !sortable.disabled ? (
          <RowGrab
            ariaLabel={`Move ${name}`}
            dragging={sortable.dragging}
            dragHandleProps={sortable.dragHandleProps}
            className="absolute start-6 top-1/2 size-7 -translate-y-1/2"
          >
            {face}
          </RowGrab>
        ) : (
          <span className="pointer-events-none absolute start-6 top-1/2 flex -translate-y-1/2">
            {face}
          </span>
        )}
        {hasChatList && (
          <button
            type="button"
            aria-label={`${open ? "Hide" : "Show"} the chats of ${name}`}
            aria-expanded={open}
            onClick={onToggle}
            className="absolute start-0.5 top-1/2 flex size-5 -translate-y-1/2 items-center justify-center rounded text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
          >
            <ChevronRight
              className={cn("size-3.5 transition-transform", open && "rotate-90")}
              aria-hidden
            />
          </button>
        )}
        {/* OpenMausBot's new-chat button (L903), shown on hover; a phone starts a chat from New chat.
            An anchor drawn by Button, as the pagination's links are (components/ui/pagination.tsx). */}
        <span
          className={cn(
            "pointer-events-none absolute opacity-0 transition-opacity group-focus-within/bot:pointer-events-auto group-focus-within/bot:opacity-100 max-md:hidden md:group-hover/bot:pointer-events-auto md:group-hover/bot:opacity-100",
            hasMenu ? "end-8" : "end-1",
            expanded ? "top-1/2 -translate-y-1/2" : "top-1",
          )}
        >
          <Tooltip>
            <TooltipTrigger
              render={
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={`New chat with ${name}`}
                  nativeButton={false}
                  render={<Link to="/" search={{ bot }} />}
                />
              }
            >
              <Plus aria-hidden />
            </TooltipTrigger>
            <TooltipContent>New chat with {name}</TooltipContent>
          </Tooltip>
        </span>
        {/* The row's menu, as OpenMausBot's Bot row has an actions button after the new-thread one: Pin or Unpin
            (issue #470), and, on a Bot that stands for its one chat, that chat's Delete (issue #503). Shown as a
            chat row's is: on hover or focus, always on a phone. */}
        {onlyChat ? (
          <ChatMenu
            chat={onlyChat}
            current={onlyChat.id === current}
            label={`Actions for ${name}`}
            className={menuPlace}
          >
            {!main && <PinItem bot={bot} pinned={pinned} />}
          </ChatMenu>
        ) : (
          !main && <BotMenu bot={bot} name={name} pinned={pinned} className={menuPlace} />
        )}
      </div>
      {expanded && (
        // The Bot's chats, under it, as t3code nests a project's threads (components/ui/sidebar.tsx SidebarMenuSub).
        <ul
          aria-label={`Chats of ${name}`}
          className="ms-5 flex flex-col gap-px border-s border-sidebar-border ps-1.5"
        >
          {chats.map((c) => (
            <li key={c.id}>
              <ChatRow chat={c} current={c.id === current} waits={waiting.has(c.id)} />
            </li>
          ))}
        </ul>
      )}
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

// ChatRow is a chat under its Bot; its menu (issue #484) shows over the row's end when the row is pointed at or
// focused, and always on a phone, which has no pointer to rest on it.
function ChatRow({ chat, current, waits }: { chat: Chat; current: boolean; waits: boolean }) {
  return (
    <div className="group/chat relative">
      <Link
        to="/chats/$chatId"
        params={{ chatId: chat.id }}
        aria-current={current ? "page" : undefined}
        {...chatDrag(chat.id)}
        className={cn(
          "group/sidebar-row relative flex h-9 w-full items-center gap-2.5 overflow-hidden rounded-md px-2.5 text-left outline-none select-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset max-md:pe-9",
          current
            ? "bg-sidebar-row-active [:root:not(.dark)_&]:ring-1 [:root:not(.dark)_&]:ring-sidebar-border/70 [:root:not(.dark)_&]:ring-inset"
            : "group-hover/chat:bg-sidebar-row-hover group-has-[[data-popup-open]]/chat:bg-sidebar-row-hover",
        )}
      >
        <span
          className={cn(
            "min-w-0 flex-1 truncate text-sm",
            current || chat.working ? "font-medium text-foreground" : "text-foreground/75",
          )}
        >
          {chat.title}
        </span>
        <span className="ml-auto flex h-6 min-w-8 shrink-0 items-center justify-end text-xs text-secondary-label tabular-nums transition-opacity md:group-focus-within/chat:opacity-0 md:group-hover/chat:opacity-0 md:group-has-[[data-popup-open]]/chat:opacity-0">
          <ChatState chat={chat} needsYou={waits} working={chat.working} />
        </span>
      </Link>
      <ChatMenu
        chat={chat}
        current={current}
        className="absolute end-1.5 top-1/2 -translate-y-1/2 transition-opacity md:pointer-events-none md:opacity-0 md:group-focus-within/chat:pointer-events-auto md:group-focus-within/chat:opacity-100 md:group-hover/chat:pointer-events-auto md:group-hover/chat:opacity-100 md:has-[[data-popup-open]]:pointer-events-auto"
      />
    </div>
  )
}
