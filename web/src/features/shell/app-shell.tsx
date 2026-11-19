import { Link, useParams } from "@tanstack/react-router"
import type { MouseEvent } from "react"
import {
  BotMessageSquare,
  Clapperboard,
  LayoutGrid,
  LogOut,
  Mic,
  Repeat2,
  Server,
  Settings,
  SquarePen,
  SquareTerminal,
  UsersRound,
} from "lucide-react"

import "@/api/client"
import { HypomnemaIcon, HypomnemaWordmark } from "@/components/icons/brand-icons"
import { Sidebar, useSidebar } from "@/components/ui/sidebar"
import { signOut } from "@/features/auth/session"
import { useLiveUpdates, useWork } from "@/features/work/use-work"
import { needsYou } from "@/features/work/work-state"
import { useBrowser } from "@/features/browser/browser-state"
import { useDevice } from "@/features/device/device-state"
import { gridPanes } from "@/features/workbench/grid-layout"
import { paneChats } from "@/features/workbench/pane"
import { useBench, useGridOffered } from "@/features/workbench/use-grid"
import { cn } from "@/lib/utils"

import { BotList } from "./bot-list"

// The app's frame, as t3code's (MIT, Copyright (c) 2026 T3 Tools Inc., https://github.com/pingdotgg/t3code;
// components/ui/sidebar.tsx, sidebar/SidebarThreadRow.tsx): a full-height sidebar with the places (a new chat, the
// Team with what waits for the owner, the Routines, the Meetings (issue #721), the Studio of takes and videos (issue
// #818, after Cap's My Caps in its navbar, Navbar/Items.tsx; layout only), then what is set up: the Bots, the agents, the machines; in the
// order they are used, as OpenClaw's navigation, issue #371) and the Bots with
// their chats (bot-list.tsx), and a quiet 52px header over each page. On a phone the sidebar hides and the header's
// ≡ opens the same content as a drawer from the left (AppDrawer, issue #604).

// LiveUpdates keeps every page current from the event stream, from the Tasks' cursor.
export function LiveUpdates() {
  const { after } = useWork()
  useLiveUpdates(after)

  return null
}

const placeClass =
  "flex h-8 items-center gap-2 rounded-lg px-2.5 text-sm font-medium text-muted-foreground/80 hover:bg-sidebar-row-hover hover:text-foreground"

function Places() {
  const { cards } = useWork()
  const waiting = needsYou(cards)

  return (
    <nav aria-label="Places" className="flex flex-col gap-px">
      <Link to="/" activeOptions={{ exact: true }} className={placeClass}>
        <SquarePen className="size-4" />
        New chat
      </Link>
      <Link
        to="/team"
        className={cn(placeClass, "[&.active]:bg-sidebar-row-active [&.active]:text-foreground")}
      >
        <UsersRound className="size-4" />
        Team
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
        to="/routines"
        className={cn(placeClass, "[&.active]:bg-sidebar-row-active [&.active]:text-foreground")}
      >
        <Repeat2 className="size-4" />
        Routines
      </Link>
      <Link
        to="/meetings"
        className={cn(placeClass, "[&.active]:bg-sidebar-row-active [&.active]:text-foreground")}
      >
        <Mic className="size-4" />
        Meetings
      </Link>
      <Link
        to="/studio"
        className={cn(placeClass, "[&.active]:bg-sidebar-row-active [&.active]:text-foreground")}
      >
        <Clapperboard className="size-4" />
        Studio
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
        <SquareTerminal className="size-4" />
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
      <SidebarContents currentChat={currentChat} />
    </aside>
  )
}

// AppDrawer is the app's sidebar on a phone: the same content in shadcn's Sidebar, which on a phone is a Sheet from
// the left that the header's ≡ opens, as t3code's (MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code; components/ui/sidebar.tsx Sidebar's isMobile branch). It is drawn once for
// every page, by the root, so that Settings, which has a sidebar of its own, opens the app's too. A pick that leads
// somewhere closes it, as t3code's sidebar closes after a thread or a place is picked (components/Sidebar.tsx,
// sidebar/SidebarChrome.tsx closeMobileSidebar); the scrim, a swipe and Escape close it as the Sheet does.
export function AppDrawer() {
  const { isMobile, setOpenMobile } = useSidebar()
  const { chatId } = useParams({ strict: false })
  if (!isMobile) return null

  // Every place and chat in the sidebar is a link; a button in it (a Bot's toggle, a menu) leaves the drawer open.
  const closeOnLink = (event: MouseEvent) => {
    if (event.target instanceof Element && event.target.closest("a[href]")) setOpenMobile(false)
  }

  return (
    <Sidebar>
      {/* oxlint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions -- the links inside take the keyboard; Enter on one is a click that bubbles here */}
      <div className="flex min-h-0 flex-1 flex-col" onClick={closeOnLink}>
        <SidebarContents currentChat={chatId} />
      </div>
    </Sidebar>
  )
}

function SidebarContents({ currentChat }: { currentChat?: string }) {
  return (
    <>
      <div className="flex h-(--workspace-topbar-height) shrink-0 items-center px-3.5">
        <Link to="/" aria-label="Hypomnema" className="flex items-center gap-2">
          <HypomnemaIcon className="size-6 shrink-0" />
          <HypomnemaWordmark className="h-[17.5px] w-auto" />
        </Link>
      </div>
      <div className="flex min-h-0 flex-1 flex-col gap-2 px-2">
        <Places />
        <BrowserHeld />
        <PhoneHeld />
        <GridLink />
        <BotList current={currentChat} />
      </div>
      <div className="flex flex-col gap-px p-2">
        <SettingsLink />
        <SignOut />
      </div>
    </>
  )
}

// SettingsLink is the one way to everything the owner sets up, at the sidebar's foot.
function SettingsLink() {
  return (
    <Link to="/settings" className={placeClass}>
      <Settings className="size-4" />
      Settings
    </Link>
  )
}

function SignOut() {
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

// PhoneHeld says that the owner holds the phone, as BrowserHeld says it of the browser (issue #417, step 3).
function PhoneHeld() {
  const device = useDevice()
  if (!device.data?.held) return null

  return (
    <output className="mx-0.5 rounded-md border border-primary/50 bg-primary/10 px-2.5 py-2 text-xs text-foreground">
      You hold the phone. Every agent's phone tools are paused until you hand it back.
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
