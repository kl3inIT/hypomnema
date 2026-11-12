import { Link } from "@tanstack/react-router"
import {
  Bot,
  BotMessageSquare,
  CircleDashed,
  KanbanSquare,
  LayoutGrid,
  ListTree,
  LogOut,
  Menu,
  Server,
  Settings,
  SquarePen,
} from "lucide-react"
import type { ReactNode } from "react"

import "@/api/client"
import type { Chat } from "@/api/generated/types.gen"
import { HypomnemaIcon, HypomnemaWordmark } from "@/components/icons/brand-icons"
import { signOut } from "@/features/auth/session"
import { needsYou } from "@/features/board/board-state"
import { useBoard, useLiveUpdates } from "@/features/board/use-board"
import { since } from "@/lib/time"
import { useBrowser } from "@/features/browser/browser-state"
import { gridPanes } from "@/features/workbench/grid-layout"
import { paneChats } from "@/features/workbench/pane"
import { setDragged, tabDrag, useBench, useGridOffered } from "@/features/workbench/use-grid"
import { cn } from "@/lib/utils"

import { useChats } from "./chats"
import { usePending } from "@/features/approvals/use-approvals"

// The app's frame, as t3code's (MIT, Copyright (c) 2026 T3 Tools Inc., https://github.com/pingdotgg/t3code;
// components/ui/sidebar.tsx, sidebar/SidebarThreadRow.tsx): a full-height sidebar with the places (a new chat, the
// board with what waits for the owner, the Runs, the Bots, the agents, the machines) and the chats, and a quiet 52px header over each page. On a phone
// the sidebar hides; the header's menu leads to the start page, which lists the same places.

// LiveUpdates keeps every page current from the event stream, from the board's cursor.
export function LiveUpdates() {
  const { after } = useBoard()
  useLiveUpdates(after)

  return null
}

const placeClass =
  "flex h-8 items-center gap-2 rounded-lg px-2.5 text-sm font-medium text-muted-foreground/80 hover:bg-sidebar-row-hover hover:text-foreground"

export function Places() {
  const { cards } = useBoard()
  const waiting = needsYou(cards)

  return (
    <nav aria-label="Places" className="flex flex-col gap-px">
      <Link to="/" activeOptions={{ exact: true }} className={placeClass}>
        <SquarePen className="size-4" />
        New chat
      </Link>
      <Link
        to="/board"
        className={cn(placeClass, "[&.active]:bg-sidebar-row-active [&.active]:text-foreground")}
      >
        <KanbanSquare className="size-4" />
        Board
        {waiting > 0 && (
          <span
            className="ms-auto inline-flex h-5 min-w-5 items-center justify-center rounded-full bg-needs-you/18 px-1.5 text-2xs font-semibold text-needs-you"
            aria-label={`${waiting} need you`}
          >
            {waiting}
          </span>
        )}
      </Link>
      <Link
        to="/runs"
        className={cn(placeClass, "[&.active]:bg-sidebar-row-active [&.active]:text-foreground")}
      >
        <ListTree className="size-4" />
        Runs
      </Link>
      <Link
        to="/bots"
        className={cn(placeClass, "[&.active]:bg-sidebar-row-active [&.active]:text-foreground")}
      >
        <BotMessageSquare className="size-4" />
        Bots
      </Link>
      <Link
        to="/agents"
        className={cn(placeClass, "[&.active]:bg-sidebar-row-active [&.active]:text-foreground")}
      >
        <Bot className="size-4" />
        Agents
      </Link>
      <Link
        to="/machines"
        className={cn(placeClass, "[&.active]:bg-sidebar-row-active [&.active]:text-foreground")}
      >
        <Server className="size-4" />
        Machines
      </Link>
    </nav>
  )
}

export function AppSidebar({ currentChat }: { currentChat?: string }) {
  return (
    <aside className="hidden h-full w-64 shrink-0 flex-col border-r bg-sidebar text-sidebar-foreground md:flex">
      <div className="flex h-(--workspace-topbar-height) shrink-0 items-center px-3.5">
        <Link to="/" aria-label="Hypomnema" className="flex items-center gap-2">
          <HypomnemaIcon className="size-6 shrink-0" />
          <HypomnemaWordmark className="h-[17.5px] w-auto" />
        </Link>
      </div>
      <div className="flex min-h-0 flex-1 flex-col gap-2 px-2">
        <Places />
        <BrowserHeld />
        <GridLink />
        <ChatList current={currentChat} />
      </div>
      <div className="flex flex-col gap-px p-2">
        <SettingsLink />
        <SignOut />
      </div>
    </aside>
  )
}

// SettingsLink is the one way to everything the owner sets up, at the sidebar's foot.
export function SettingsLink() {
  return (
    <Link to="/settings" className={placeClass}>
      <Settings className="size-4" />
      Settings
    </Link>
  )
}

export function SignOut() {
  return (
    <button type="button" onClick={() => void signOut()} className={cn(placeClass, "w-full")}>
      <LogOut className="size-4" />
      Sign out
    </button>
  )
}

// BrowserHeld says that the owner holds the shared browser, wherever the owner is: every agent's browser tools
// are paused meanwhile, and the tab where control was taken may not be on screen (issue #235; the owner saw it in
// the prototype, 2026-11-12).
function BrowserHeld() {
  const browser = useBrowser()
  if (!browser.data?.held) return null

  return (
    <output className="mx-0.5 rounded-md border border-primary/50 bg-primary/10 px-2.5 py-2 text-xs text-foreground">
      You hold the browser. Every agent's browser tools are paused until you hand it back.
    </output>
  )
}

// GridLink leads to the grid of chats while this browser keeps one (issue #235; Omnigent's GridSidebar).
function GridLink() {
  const bench = useBench()
  const offered = useGridOffered()
  if (!offered || bench === null) return null

  const chats = gridPanes(bench.layout).flatMap((id) => paneChats(bench.panes[id])).length

  return (
    <Link
      to="/grid"
      className={cn(placeClass, "[&.active]:bg-sidebar-row-active [&.active]:text-foreground")}
    >
      <LayoutGrid className="size-4" />
      Grid
      <span className="ml-auto text-xs text-secondary-label tabular-nums">
        {chats} {chats === 1 ? "chat" : "chats"}
      </span>
    </Link>
  )
}

export function ChatList({ current, label = "Chats" }: { current?: string; label?: string }) {
  const chats = useChats()
  // A chat in which pi asked for an approval says so here: the owner may not have it open.
  const pending = usePending()
  const waiting = new Set(pending.data?.map((a) => a.chat_id))

  if (chats.data?.length === 0) {
    return <p className="px-2 py-6 text-center text-xs text-muted-foreground">No chats yet.</p>
  }

  return (
    <nav aria-label="Chats" className="-mx-2 min-h-0 flex-1 overflow-y-auto px-2">
      <p className="px-2.5 pt-2 pb-1 text-xs font-medium text-muted-foreground">{label}</p>
      <div className="flex flex-col gap-px">
        {chats.data?.map((c) => (
          <ChatRow key={c.id} chat={c} current={c.id === current} waits={waiting.has(c.id)} />
        ))}
      </div>
    </nav>
  )
}

function ChatRow({ chat, current, waits }: { chat: Chat; current: boolean; waits: boolean }) {
  return (
    <Link
      to="/chats/$chatId"
      params={{ chatId: chat.id }}
      aria-current={current ? "page" : undefined}
      // A chat is dragged onto an open chat, or a pane of the grid, to be shown beside it (issue #235).
      onDragStart={(event) => {
        event.dataTransfer.effectAllowed = "move"
        event.dataTransfer.setData(tabDrag, `chat:${chat.id}`)
        setDragged({ tab: { kind: "chat", chat: chat.id }, from: null })
      }}
      onDragEnd={() => setDragged(null)}
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
        {waits ? (
          <span className="font-medium text-needs-you">Needs you</span>
        ) : chat.working ? (
          <span className="inline-flex items-center gap-1 font-medium text-info-foreground">
            <CircleDashed className="size-3.5 animate-spin [animation-duration:3s]" aria-hidden />
            Working
          </span>
        ) : (
          since(chat.updated_at)
        )}
      </span>
    </Link>
  )
}

// PageHeader is the quiet bar above a page: on a phone a way to the start page, the title, and the page's tools.
// MenuLink goes to the menu on a phone, where there is no sidebar. A desktop does not show it.
export function MenuLink({ className }: { className?: string }) {
  return (
    <Link
      to="/"
      aria-label="Menu"
      className={cn(
        "flex size-8 shrink-0 items-center justify-center rounded-lg text-muted-foreground hover:bg-accent hover:text-foreground md:hidden",
        className,
      )}
    >
      <Menu className="size-4" />
    </Link>
  )
}

// The bar carries a detail page's breadcrumb ("Agents / reviewer", t3code's SettingsBreadcrumb.tsx) or a page's
// tools; a list page, whose title is its PageHeading, gives it neither, and it shows only on a phone, for the menu.
export function PageHeader({ title, children }: { title?: ReactNode; children?: ReactNode }) {
  return (
    <header
      className={cn(
        "flex min-h-(--workspace-topbar-height) shrink-0 flex-wrap items-center gap-x-2 gap-y-2 bg-background px-3 py-2 sm:px-5",
        !title && !children && "md:hidden",
      )}
    >
      <MenuLink className="-ms-1.5" />
      {title && <h1 className="min-w-0 truncate text-sm font-medium text-foreground">{title}</h1>}
      {children}
    </header>
  )
}

// PageHeading is a list page's head: its title, one plain sentence under it saying what the page holds, the page's
// actions on the right and a rule below, as Orca's settings section (MIT, Copyright (c) 2026 Lovecast Inc.,
// https://github.com/stablyai/orca; components/settings/section-layout/SettingsSection.tsx) and Beszel's systems
// table (MIT, Copyright (c) 2024 henrygd, https://github.com/henrygd/beszel; components/systems-table/
// systems-table.tsx CardHead, ui/card.tsx). On a phone the actions wrap under the title, as Beszel's.
export function PageHeading({
  title,
  description,
  children,
  className,
}: {
  title: ReactNode
  description?: ReactNode
  children?: ReactNode
  className?: string
}) {
  return (
    <div
      className={cn(
        "flex flex-col gap-3 border-b border-border/60 pb-5 md:flex-row md:items-start md:justify-between md:gap-4 md:pt-6",
        className,
      )}
    >
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <h1 className="text-2xl leading-tight font-semibold text-foreground">{title}</h1>
        {description && (
          <p className="max-w-3xl text-sm leading-6 text-muted-foreground">{description}</p>
        )}
      </div>
      {children && <div className="flex flex-wrap items-center gap-2 md:shrink-0">{children}</div>}
    </div>
  )
}
