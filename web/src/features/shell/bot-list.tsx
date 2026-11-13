import { Link } from "@tanstack/react-router"
import { ChevronRight, CircleDashed, Plus } from "lucide-react"
import { type DragEvent, useState } from "react"

import "@/api/client"
import type { Chat } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { usePending } from "@/features/approvals/use-approvals"
import { AgentAvatar } from "@/features/agents/agent-avatar"
import { setDragged, tabDrag } from "@/features/workbench/use-grid"
import { since } from "@/lib/time"
import { cn } from "@/lib/utils"

import { type BotRow, botRows, keepOpenBots, readOpenBots } from "./bot-rows"
import { useChats } from "./chats"

// BotList is the sidebar's Bots (issue #361): a row a Bot, which opens to its chats, as OpenMausBot's sidebar
// (Apache License 2.0, https://github.com/milind-soni/OpenMausBot; src/components/Sidebar.tsx BotListItem,
// BotThreadList). Left out of OpenMausBot's: departments, dragging Bots into an order, the row's menu, unread
// marks and a queued state, none of which a chat here has.
export function BotList({ current }: { current?: string }) {
  const chats = useChats()
  // A chat in which pi asked for an approval says so here: the owner may not have it open.
  const pending = usePending()
  const waiting = new Set(pending.data?.flatMap((a) => (a.chat_id ? [a.chat_id] : [])))
  const [open, setOpen] = useState(readOpenBots)

  function toggle(bot: string) {
    const next = { ...open, [bot]: !open[bot] }
    setOpen(next)
    keepOpenBots(next)
  }

  return (
    <nav aria-label="Bots" className="-mx-2 min-h-0 flex-1 overflow-y-auto px-2">
      <p className="px-2.5 pt-2 pb-1 text-xs font-medium text-muted-foreground">Bots</p>
      {chats.data && (
        <div className="flex flex-col gap-px">
          {botRows(chats.data).map((row) => (
            <BotItem
              key={row.bot}
              row={row}
              current={current}
              waiting={waiting}
              open={open[row.bot] === true}
              onToggle={() => toggle(row.bot)}
            />
          ))}
        </div>
      )}
    </nav>
  )
}

function BotItem({
  row,
  current,
  waiting,
  open,
  onToggle,
}: {
  row: BotRow
  current?: string
  waiting: Set<string>
  open: boolean
  onToggle: () => void
}) {
  const { bot, chats } = row
  const latest = chats.at(0)
  // OpenMausBot's hasThreadList: a Bot with one chat has nothing to open.
  const hasChatList = chats.length > 1
  const expanded = hasChatList && open
  const needsYou = chats.some((c) => waiting.has(c.id))
  const working = chats.some((c) => c.working)
  // The Bot's row stands for the open chat while its chats are not shown, as OpenMausBot's aria-current.
  const here = !expanded && chats.some((c) => c.id === current)

  return (
    <>
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
              : "hover:bg-sidebar-row-hover",
          )}
        >
          <span className="relative flex shrink-0">
            <AgentAvatar name={bot} className="size-7" />
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
          <span className="flex min-w-0 flex-1 flex-col">
            <span className="flex items-baseline gap-2">
              <span className="min-w-0 flex-1 truncate text-sm leading-5 font-medium text-foreground">
                {bot}
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
        {hasChatList && (
          <button
            type="button"
            aria-label={`${open ? "Hide" : "Show"} the chats of ${bot}`}
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
            "pointer-events-none absolute end-1 opacity-0 transition-opacity group-focus-within/bot:pointer-events-auto group-focus-within/bot:opacity-100 max-md:hidden md:group-hover/bot:pointer-events-auto md:group-hover/bot:opacity-100",
            expanded ? "top-1/2 -translate-y-1/2" : "top-1",
          )}
        >
          <Tooltip>
            <TooltipTrigger
              render={
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={`New chat with ${bot}`}
                  nativeButton={false}
                  render={<Link to="/" search={{ bot }} />}
                />
              }
            >
              <Plus aria-hidden />
            </TooltipTrigger>
            <TooltipContent>New chat with {bot}</TooltipContent>
          </Tooltip>
        </span>
      </div>
      {expanded && (
        // The Bot's chats, under it, as t3code nests a project's threads (components/ui/sidebar.tsx SidebarMenuSub).
        <ul
          aria-label={`Chats of ${bot}`}
          className="ms-5 flex flex-col gap-px border-s border-sidebar-border ps-1.5"
        >
          {chats.map((c) => (
            <li key={c.id}>
              <ChatRow chat={c} current={c.id === current} waits={waiting.has(c.id)} />
            </li>
          ))}
        </ul>
      )}
    </>
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

function ChatRow({ chat, current, waits }: { chat: Chat; current: boolean; waits: boolean }) {
  return (
    <Link
      to="/chats/$chatId"
      params={{ chatId: chat.id }}
      aria-current={current ? "page" : undefined}
      {...chatDrag(chat.id)}
      className={cn(
        "group/sidebar-row relative flex h-9 w-full items-center gap-2.5 overflow-hidden rounded-md px-2.5 text-left outline-none select-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset",
        current
          ? "bg-sidebar-row-active [:root:not(.dark)_&]:ring-1 [:root:not(.dark)_&]:ring-sidebar-border/70 [:root:not(.dark)_&]:ring-inset"
          : "hover:bg-sidebar-row-hover",
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
      <span className="ml-auto flex h-6 min-w-8 shrink-0 items-center justify-end text-xs text-secondary-label tabular-nums">
        <ChatState chat={chat} needsYou={waits} working={chat.working} />
      </span>
    </Link>
  )
}
