import { useQuery } from "@tanstack/react-query"
import { FileDiff, FileText, FolderTree, Globe, MessageSquarePlus, Plus, X } from "lucide-react"
import { useState, type ReactNode } from "react"

import "@/api/client"
import { getChat, listRuns } from "@/api/generated/sdk.gen"
import type { BrowserTab } from "@/api/generated/types.gen"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { AgentAvatar } from "@/features/agents/agent-avatar"
import { firstBot } from "@/features/bots/bots-state"
import { useBots } from "@/features/bots/use-bots"
import { ownerTab, tabName, useBrowser, whose } from "@/features/browser/browser-state"
import { BrowserView } from "@/features/browser/browser-view"
import { ChatBody } from "@/features/chat/chat-body"
import { FileTab } from "@/features/files/file-tab"
import { FilesPanel } from "@/features/files/files-panel"
import { baseName, isUnsaved, useUnsaved } from "@/features/files/files-state"
import { cn } from "@/lib/utils"

import { DraftChat } from "./draft-chat"
import {
  closeTab,
  frontOf,
  hasTab,
  openTab,
  paneChats,
  replaceTab,
  tabKey,
  type Pane,
  type PaneTab,
} from "./pane"
import { setDragged, tabDrag } from "./use-grid"

// PaneView is a pane (issue #235): a strip of its tabs, and the tab in front under it. "+" opens a new chat, the
// owner's browser tab, or a browser tab of a worker of a chat in the pane, as Orca's tab bar does
// (tab-bar-static-create-menu.tsx: New Terminal, New Browser Tab, New Mobile Emulator) and Omnigent's workspace
// panel ("Open new", WorkspacePanel.tsx). An item is offered only when the control plane has it; the owner's own
// browser tab, of which there is one, is gone to when it is open, as Orca's emulator is. The strip is the top of the
// pane, with no header above it, as Orca's tab bar is; a chat's tab carries its Bot's avatar as Orca's terminal tab
// carries its agent's icon (TerminalTabLeadingIcon.tsx). Every tab is dragged by itself to another pane, or to a
// side of a pane, as Orca's are. `start` and `end` are what the place showing the pane puts at the strip's ends:
// the menu on a phone, a grid pane's buttons. `kept` is a tab that cannot be closed: a chat on its own page.
export function PaneView({
  id,
  pane,
  onChange,
  kept,
  start,
  end,
}: {
  id: string
  pane: Pane
  onChange: (next: Pane | null) => void
  kept?: string
  start?: ReactNode
  end?: ReactNode
}) {
  const browser = useBrowser()
  const bots = useBots().data ?? []
  const runs = useQuery({
    queryKey: ["runs"],
    queryFn: async () => (await listRuns({ query: { limit: 100 } })).data,
    enabled: browser.isSuccess,
  })

  // The browser tabs of the workers of the chats in this pane: those of the Runs their coordinators created.
  const chats = new Set(paneChats(pane))
  const mine = new Set(
    runs.data?.data.filter((r) => r.chat_id && chats.has(r.chat_id)).map((r) => r.id),
  )
  const workers = (browser.data?.tabs ?? []).filter(
    (t) => t.kind === "worker" && t.run_id !== undefined && mine.has(t.run_id),
  )
  const known = (tab: string): BrowserTab =>
    browser.data?.tabs.find((t) => t.id === tab) ?? {
      id: tab,
      kind: tab === ownerTab ? "owner" : "worker",
      url: "",
      title: "",
      views: 0,
    }
  const label = (tab: BrowserTab) => (tab.id === ownerTab ? "My browser" : tabName(tab))

  const front = frontOf(pane)
  // A file tab with text not saved, which the owner asked to close.
  const [closing, setClosing] = useState<string | null>(null)
  const frontKey = front === undefined ? "" : tabKey(front)
  const open = (tab: PaneTab) => onChange(openTab(pane, tab))
  const newChat = (bot: string) => open({ kind: "draft", id: crypto.randomUUID().slice(0, 8), bot })
  // The Files panel shows the folders of the chat in front, or of the pane's first chat (issue #255).
  const filesOf =
    front?.kind === "chat" || front?.kind === "file" ? front.chat : (paneChats(pane)[0] ?? null)

  return (
    <>
      <div className="flex h-10 shrink-0 items-stretch border-b bg-sidebar">
        {start && <div className="flex shrink-0 items-center ps-1.5">{start}</div>}
        <div
          role="tablist"
          aria-label="Tabs of this pane"
          className="flex min-w-0 flex-1 items-stretch overflow-x-auto"
        >
          {pane.tabs.map((tab) => {
            const key = tabKey(tab)
            const close =
              key === kept
                ? undefined
                : () =>
                    // A file with text not saved asks first (Orca marks such a tab and asks on close).
                    isUnsaved(key) ? setClosing(key) : onChange(closeTab(pane, key))
            const common = {
              selected: key === frontKey,
              onPick: () => onChange({ ...pane, active: key }),
              onDragStart: (event: React.DragEvent) => {
                event.dataTransfer.effectAllowed = "move"
                event.dataTransfer.setData(tabDrag, key)
                setDragged({ tab, from: id })
              },
            }

            switch (tab.kind) {
              case "chat":
                return <ChatTab key={key} chat={tab.chat} onClose={close} {...common} />
              case "draft":
                return (
                  <TabButton
                    key={key}
                    title="A new chat"
                    onClose={close}
                    closeLabel="Close New chat"
                    {...common}
                  >
                    <AgentAvatar name={tab.bot || firstBot} className="size-5 shrink-0" />
                    <span className="truncate">New chat</span>
                  </TabButton>
                )
              case "file":
                return (
                  <TabButton
                    key={key}
                    title={`${tab.path}${tab.view === "diff" ? ", its change since the last commit" : ""}`}
                    onClose={close}
                    closeLabel={`Close ${baseName(tab.path)}`}
                    {...common}
                  >
                    {tab.view === "diff" ? (
                      <FileDiff className="size-3.5 shrink-0" aria-hidden />
                    ) : (
                      <FileText className="size-3.5 shrink-0" aria-hidden />
                    )}
                    <span className="truncate">{baseName(tab.path)}</span>
                    <UnsavedMark tab={key} />
                  </TabButton>
                )
              case "browser": {
                const shown = known(tab.tab)

                return (
                  <TabButton
                    key={key}
                    title={whose(shown).line}
                    onClose={close}
                    closeLabel={`Close ${label(shown)}`}
                    {...common}
                  >
                    <Globe className="size-3.5 shrink-0" aria-hidden />
                    <span className="truncate">{label(shown)}</span>
                    {shown.id !== ownerTab && (
                      <span className="text-xs text-secondary-label">{whose(shown).mark}</span>
                    )}
                  </TabButton>
                )
              }
            }
          })}
          <div className="ms-1 flex shrink-0 items-center">
            <DropdownMenu>
              <DropdownMenuTrigger
                render={<Button variant="ghost" size="icon-sm" aria-label="Open new" />}
              >
                <Plus />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" width="content">
                <DropdownMenuGroup>
                  <DropdownMenuLabel>Open new</DropdownMenuLabel>
                  {bots.length > 1 ? (
                    <DropdownMenuSub>
                      <DropdownMenuSubTrigger>
                        <MessageSquarePlus />
                        New chat
                      </DropdownMenuSubTrigger>
                      <DropdownMenuSubContent>
                        {bots.map((bot) => (
                          <DropdownMenuItem key={bot.name} onClick={() => newChat(bot.name)}>
                            <AgentAvatar name={bot.name} className="size-4" />
                            {bot.name}
                          </DropdownMenuItem>
                        ))}
                      </DropdownMenuSubContent>
                    </DropdownMenuSub>
                  ) : (
                    <DropdownMenuItem onClick={() => newChat("")}>
                      <MessageSquarePlus />
                      New chat
                    </DropdownMenuItem>
                  )}
                  {browser.isSuccess && (
                    <DropdownMenuItem onClick={() => open({ kind: "browser", tab: ownerTab })}>
                      <Globe />
                      {hasTab(pane, `browser:${ownerTab}`) ? "Go to my browser" : "My browser"}
                    </DropdownMenuItem>
                  )}
                </DropdownMenuGroup>
                {workers.length > 0 && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuGroup>
                      <DropdownMenuLabel>Workers of this pane's chats</DropdownMenuLabel>
                      {workers.map((tab) => (
                        <DropdownMenuItem
                          key={tab.id}
                          onClick={() => open({ kind: "browser", tab: tab.id })}
                        >
                          <Globe />
                          <span className="truncate">{tabName(tab)}</span>
                        </DropdownMenuItem>
                      ))}
                    </DropdownMenuGroup>
                  </>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </div>
        {filesOf !== null && (
          <div className="flex shrink-0 items-center ps-1.5">
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Files"
              aria-pressed={pane.files === true}
              title="The folders this chat's agents work in"
              onClick={() => onChange({ ...pane, files: !pane.files })}
            >
              <FolderTree />
            </Button>
          </div>
        )}
        {end && <div className="flex shrink-0 items-center gap-1 px-1.5">{end}</div>}
      </div>
      <div className="relative flex min-h-0 min-w-0 flex-1">
        <div className="flex min-h-0 min-w-0 flex-1 flex-col">
          {/* A chat stays as it is behind another tab, so that what was typed is still there. */}
          {pane.tabs.map((tab) => {
            const key = tabKey(tab)
            const shown = key === frontKey
            if (tab.kind === "browser") {
              return (
                shown && (
                  <BrowserView
                    key={key}
                    tab={tab.tab}
                    label={`The page of ${label(known(tab.tab))}`}
                  />
                )
              )
            }
            if (tab.kind === "file") {
              // Kept behind other tabs, as a chat is, so that text not saved stays.
              return (
                <div
                  key={key}
                  className={cn("min-h-0 min-w-0 flex-1 flex-col", shown ? "flex" : "hidden")}
                >
                  <FileTab
                    tab={key}
                    chat={tab.chat}
                    workspace={tab.workspace}
                    path={tab.path}
                    view={tab.view}
                    shown={shown}
                  />
                </div>
              )
            }

            return (
              <div
                key={key}
                className={cn("min-h-0 min-w-0 flex-1 flex-col", shown ? "flex" : "hidden")}
              >
                {tab.kind === "chat" ? (
                  <ChatBody
                    chatId={tab.chat}
                    // A file attached to a message opens as a file of the panel does, with the panel beside it
                    // where there is room (issue #343).
                    onOpenFile={(workspace, path) =>
                      onChange({
                        ...openTab(pane, {
                          kind: "file",
                          chat: tab.chat,
                          workspace,
                          path,
                          view: "file",
                        }),
                        files: window.matchMedia("(min-width: 768px)").matches,
                      })
                    }
                  />
                ) : (
                  <DraftChat
                    bot={tab.bot}
                    onStarted={(chat) => onChange(replaceTab(pane, key, { kind: "chat", chat }))}
                  />
                )}
              </div>
            )
          })}
        </div>
        {pane.files && filesOf !== null && (
          <FilesPanel
            chat={filesOf}
            // On a phone the panel lies over the pane, as a sheet: it gives way to the file it opened.
            onOpen={(workspace, path, view) =>
              onChange({
                ...openTab(pane, { kind: "file", chat: filesOf, workspace, path, view }),
                files: window.matchMedia("(min-width: 768px)").matches,
              })
            }
            onClose={() => onChange({ ...pane, files: false })}
            onGone={(workspace, path) => {
              // A file or folder deleted or moved from the panel takes its tabs with it.
              let next: Pane | null = pane
              for (const tab of pane.tabs) {
                if (
                  tab.kind === "file" &&
                  tab.chat === filesOf &&
                  tab.workspace === workspace &&
                  (tab.path === path || tab.path.startsWith(`${path}/`))
                ) {
                  next = next && closeTab(next, tabKey(tab))
                }
              }
              if (next) onChange(next)
            }}
          />
        )}
      </div>
      <AlertDialog open={closing !== null} onOpenChange={(open) => !open && setClosing(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Close without saving?</AlertDialogTitle>
            <AlertDialogDescription>
              Your changes to this file are not saved. Closing the tab drops them.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep editing</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (closing) onChange(closeTab(pane, closing))
                setClosing(null)
              }}
            >
              Close without saving
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

// UnsavedMark is a file tab's dot while its text is not saved.
function UnsavedMark({ tab }: { tab: string }) {
  const unsaved = useUnsaved(tab)

  return unsaved ? (
    <span aria-label="not saved" className="size-1.5 shrink-0 rounded-full bg-foreground/70" />
  ) : null
}

type TabProps = {
  selected: boolean
  onPick: () => void
  onDragStart: (event: React.DragEvent) => void
  onClose?: () => void
}

// ChatTab is a chat's tab: its Bot's avatar and its title.
function ChatTab({ chat, onClose, ...props }: TabProps & { chat: string }) {
  const info = useQuery({
    queryKey: ["chat", chat],
    queryFn: async () => (await getChat({ path: { chat_id: chat } })).data,
  })
  const title = info.data?.title ?? "Chat"

  return (
    <TabButton
      title={info.data && `Chat with ${info.data.bot}`}
      onClose={onClose}
      closeLabel={`Close ${title}`}
      {...props}
    >
      {info.data && <AgentAvatar name={info.data.bot} className="size-5 shrink-0" />}
      <span className="truncate">{title}</span>
    </TabButton>
  )
}

// TabButton is a tab of the strip. It is its own design, not a Button: a row of tabs with the picked one marked
// along its foot, as in Orca's and OpenMausBot's strips.
function TabButton({
  selected,
  title,
  onPick,
  onDragStart,
  onClose,
  closeLabel,
  children,
}: TabProps & {
  title?: string
  closeLabel?: string
  children: ReactNode
}) {
  return (
    <div
      draggable
      onDragStart={onDragStart}
      onDragEnd={() => setDragged(null)}
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
