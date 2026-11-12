import { useQuery } from "@tanstack/react-query"
import { Globe, Plus, X } from "lucide-react"
import type { ReactNode } from "react"

import "@/api/client"
import { getChat, listRuns } from "@/api/generated/sdk.gen"
import type { BrowserTab } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { AgentAvatar } from "@/features/agents/agent-avatar"
import { ownerTab, tabName, useBrowser, whose } from "@/features/browser/browser-state"
import { BrowserView } from "@/features/browser/browser-view"
import { ChatBody } from "@/features/chat/chat-body"
import { cn } from "@/lib/utils"

import {
  browserTabId,
  chatTab,
  closeBrowser,
  openBrowser,
  showTab,
  usePaneTabs,
} from "./use-pane-tabs"

// ChatTabs is a chat with what was opened beside it (issue #235, step 2): a strip of tabs, the chat first, and a
// "+" that opens a tool in this pane. The tools are tabs, opened from "+" and not from the sidebar, as in Orca
// (tab-bar-static-create-menu.tsx: New Terminal, New Browser Tab, New Mobile Emulator) and Omnigent
// (WorkspacePanel.tsx, "Open new"). An item is offered only when the control plane has it; the owner's own
// browser tab, of which there is one, is gone to when it is open, as Orca's emulator is. The kinds are `chat` and
// `browser` for now. The strip is the top of the pane, with no header above it, as Orca's tab bar is; the chat's
// tab carries its Bot's avatar as Orca's terminal tab carries its agent's icon (TerminalTabLeadingIcon.tsx).
// `start` and `end` are what the place showing the pane puts at the strip's ends: the menu on a phone, a grid
// pane's buttons. `drag` makes the chat's tab the handle that carries the chat elsewhere.
export function ChatTabs({
  chatId,
  start,
  end,
  drag,
}: {
  chatId: string
  start?: ReactNode
  end?: ReactNode
  drag?: { onDragStart: (event: React.DragEvent) => void; onDragEnd: () => void }
}) {
  const tabs = usePaneTabs(chatId)
  const chat = useQuery({
    queryKey: ["chat", chatId],
    queryFn: async () => (await getChat({ path: { chat_id: chatId } })).data,
  })
  const browser = useBrowser()
  const runs = useQuery({
    queryKey: ["runs"],
    queryFn: async () => (await listRuns({ query: { limit: 100 } })).data,
    enabled: browser.isSuccess,
  })

  // The browser tabs of this chat's workers: those of the Runs its coordinator created.
  const mine = new Set(runs.data?.data.filter((r) => r.chat_id === chatId).map((r) => r.id))
  const workers = (browser.data?.tabs ?? []).filter(
    (t) => t.kind === "worker" && t.run_id !== undefined && mine.has(t.run_id),
  )
  const known = (id: string): BrowserTab =>
    browser.data?.tabs.find((t) => t.id === id) ?? {
      id,
      kind: id === ownerTab ? "owner" : "worker",
      url: "",
      title: "",
      views: 0,
    }
  const label = (tab: BrowserTab) => (tab.id === ownerTab ? "My browser" : tabName(tab))

  // A tab that is not open any more, kept from another day, gives way to the chat.
  const active =
    tabs.active === chatTab || tabs.browser.some((id) => browserTabId(id) === tabs.active)
      ? tabs.active
      : chatTab
  const shown = tabs.browser.find((id) => browserTabId(id) === active)

  return (
    <>
      <div className="flex h-10 shrink-0 items-stretch border-b bg-sidebar">
        {start && <div className="flex shrink-0 items-center ps-1.5">{start}</div>}
        <div
          role="tablist"
          aria-label="Tabs of this chat"
          className="flex min-w-0 flex-1 items-stretch overflow-x-auto"
        >
          <TabButton
            selected={active === chatTab}
            title={chat.data && `Chat with ${chat.data.bot}`}
            onPick={() => showTab(chatId, chatTab)}
            drag={drag}
          >
            {chat.data && <AgentAvatar name={chat.data.bot} className="size-5 shrink-0" />}
            <span className="truncate">{chat.data?.title ?? "Chat"}</span>
          </TabButton>
          {tabs.browser.map((id) => {
            const tab = known(id)

            return (
              <TabButton
                key={id}
                selected={active === browserTabId(id)}
                title={whose(tab).line}
                onPick={() => showTab(chatId, browserTabId(id))}
                onClose={() => closeBrowser(chatId, id)}
                closeLabel={`Close ${label(tab)}`}
              >
                <Globe className="size-3.5 shrink-0" aria-hidden />
                <span className="truncate">{label(tab)}</span>
                {tab.id !== ownerTab && (
                  <span className="text-xs text-secondary-label">{whose(tab).mark}</span>
                )}
              </TabButton>
            )
          })}
          {browser.isSuccess && (
            <div className="ms-1 flex shrink-0 items-center">
              <DropdownMenu>
                <DropdownMenuTrigger
                  render={<Button variant="ghost" size="icon-sm" aria-label="Open new" />}
                >
                  <Plus />
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start">
                  <DropdownMenuGroup>
                    <DropdownMenuLabel>Open new</DropdownMenuLabel>
                    <DropdownMenuItem onClick={() => openBrowser(chatId, ownerTab)}>
                      <Globe />
                      {tabs.browser.includes(ownerTab) ? "Go to my browser" : "My browser"}
                    </DropdownMenuItem>
                  </DropdownMenuGroup>
                  {workers.length > 0 && (
                    <>
                      <DropdownMenuSeparator />
                      <DropdownMenuGroup>
                        <DropdownMenuLabel>Workers of this chat</DropdownMenuLabel>
                        {workers.map((tab) => (
                          <DropdownMenuItem
                            key={tab.id}
                            onClick={() => openBrowser(chatId, tab.id)}
                          >
                            <Globe />
                            {tabName(tab)}
                          </DropdownMenuItem>
                        ))}
                      </DropdownMenuGroup>
                    </>
                  )}
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          )}
        </div>
        {end && <div className="flex shrink-0 items-center gap-1 px-1.5">{end}</div>}
      </div>
      {/* The chat stays as it is behind a tool, so that what was typed is still there. */}
      <div
        className={cn("min-h-0 min-w-0 flex-1 flex-col", active === chatTab ? "flex" : "hidden")}
      >
        <ChatBody chatId={chatId} />
      </div>
      {shown !== undefined && (
        <BrowserView key={shown} tab={shown} label={`The page of ${label(known(shown))}`} />
      )}
    </>
  )
}

// TabButton is a tab of the strip. It is its own design, not a Button: a row of tabs with the picked one marked
// along its foot, as in Orca's and OpenMausBot's strips.
function TabButton({
  selected,
  title,
  onPick,
  onClose,
  closeLabel,
  drag,
  children,
}: {
  selected: boolean
  title?: string
  onPick: () => void
  onClose?: () => void
  closeLabel?: string
  drag?: { onDragStart: (event: React.DragEvent) => void; onDragEnd: () => void }
  children: React.ReactNode
}) {
  return (
    <div
      draggable={drag !== undefined}
      onDragStart={drag?.onDragStart}
      onDragEnd={drag?.onDragEnd}
      className={cn(
        "relative flex max-w-64 shrink-0 items-center border-r text-sm text-muted-foreground hover:bg-accent hover:text-foreground",
        selected &&
          "text-foreground after:absolute after:inset-x-2 after:bottom-0 after:h-0.5 after:rounded-t after:bg-primary",
      )}
    >
      <button
        type="button"
        role="tab"
        aria-selected={selected}
        title={title}
        onClick={onPick}
        className="flex h-full min-w-0 items-center gap-2 px-3 whitespace-nowrap outline-none focus-visible:bg-accent"
      >
        {children}
      </button>
      {onClose && (
        <button
          type="button"
          aria-label={closeLabel}
          onClick={onClose}
          className="me-1.5 flex size-5 shrink-0 items-center justify-center rounded text-muted-foreground outline-none hover:bg-border hover:text-foreground focus-visible:bg-border"
        >
          <X className="size-3" aria-hidden />
        </button>
      )}
    </div>
  )
}
