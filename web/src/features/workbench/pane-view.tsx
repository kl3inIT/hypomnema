import {
  Clapperboard,
  FileDiff,
  FileText,
  Film,
  FolderTree,
  Globe,
  MessageSquarePlus,
  Plus,
  Smartphone,
  X,
} from "lucide-react"
import { useState, type ReactNode } from "react"

import "@/api/client"
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
import { AgentAvatar } from "@/components/agent-avatar"
import { mainBotOf } from "@/features/bots/bots-state"
import { useBotName, useBotNames, useBots } from "@/features/bots/use-bots"
import { chatTabs, ownerTab, tabName, useBrowser, whose } from "@/features/browser/browser-state"
import { BrowserStrip } from "@/features/browser/browser-strip"
import { BrowserView } from "@/features/browser/browser-view"
import { TabIcon } from "@/features/browser/tab-icon"
import { ChatBody } from "@/features/chat/chat-body"
import { hasPhone, useDevice } from "@/features/device/device-state"
import { PhoneView } from "@/features/device/phone-view"
import { ArtifactSide } from "@/features/artifacts/artifact-side"
import { FileTab } from "@/features/files/file-tab"
import { FilesPanel } from "@/features/files/files-panel"
import { baseName, isUnsaved, useUnsaved } from "@/features/files/files-state"
import { useChat } from "@/features/shell/chats"
import { useVideo } from "@/features/studio/studio-state"
import { VideoView } from "@/features/studio/video-view"
import { useTake } from "@/features/takes/take-state"
import { TakeView } from "@/features/takes/take-view"
import { cn } from "@/lib/utils"

import { DraftChat } from "./draft-chat"
import { useFollowTabs } from "./follow-tabs"
import { browserTab, paneStrip, pickFromStrip } from "./strip"
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

export type Beside = { open: (...tabs: PaneTab[]) => void; has: (key: string) => boolean }

// PaneView is a pane (issue #235): a strip of its tabs, and the tab in front under it. "+" opens a new chat, the
// owner's browser tab, the phone (issue #417), or a browser tab of a chat in the pane, its Bot's or a worker's (issue #557), as Orca's tab bar does
// (tab-bar-static-create-menu.tsx: New Terminal, New Browser Tab, New Mobile Emulator) and Omnigent's workspace
// panel ("Open new", WorkspacePanel.tsx). An item is offered only when the control plane has it; the owner's own
// browser tab and the phone, of which there is one each, are gone to when open, as Orca's emulator is. The strip is the top of the
// pane, with no header above it, as Orca's tab bar is; a chat's tab carries its Bot's avatar as Orca's terminal tab
// carries its agent's icon (TerminalTabLeadingIcon.tsx). Every tab is dragged by itself to another pane, or to a
// side of a pane, as Orca's are. `start` and `end` are what the place showing the pane puts at the strip's ends:
// the menu on a phone, a grid pane's buttons. Every tab can be closed, as in Orca; a chat's own page decides where
// it goes when its chat's tab is (./solo-pane.tsx). The phone opens beside the chat, in the pane to its right or a
// new one split off there, as Orca opens its emulator (issue #528, `openBeside` in ./moves.ts), and so does a browser
// tab a chat's Bot or worker just opened, or one its card in the chat asks for (issue #703). A browser tab in front
// has a strip of the browser's tabs over it (./browser-strip.tsx).
export function PaneView({
  id,
  pane,
  onChange,
  start,
  end,
  beside,
}: {
  id: string
  pane: Pane
  onChange: (next: Pane | null) => void
  start?: ReactNode
  end?: ReactNode
  // Where the place showing the pane opens a tab beside it, and whether a tab is open there already (issue #528);
  // none on a phone, where a tab opens in the pane.
  beside?: Beside
}) {
  const browser = useBrowser()
  const device = useDevice()
  const bots = useBots().data ?? []

  // The browser tabs of the chats in this pane, a worker's or a Bot's (issue #557).
  const theirs = chatTabs(browser.data?.tabs, new Set(paneChats(pane)))
  const botName = useBotNames()
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
  // Browser tabs of a chat open beside it, or in the pane on a phone (issue #703), all in one change, so that
  // none undoes another; the last is in front.
  const showTabs = (tabs: string[]) => {
    const shown = tabs.map((tab) => browserTab(tab, browser.data?.tabs))
    if (beside) beside.open(...shown)
    else onChange(shown.reduce(openTab, pane))
  }
  const showTab = (tab: string) => showTabs([tab])
  useFollowTabs(browser.data?.tabs, paneChats(pane), showTabs)
  // The strip over a browser tab, and a click on it (./strip.ts).
  const strip = paneStrip(pane, browser.data?.tabs)
  const pickBrowser = (from: string, tab: string) =>
    onChange(pickFromStrip(pane, from, tab, browser.data?.tabs))
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
            // A file with text not saved asks first (Orca marks such a tab and asks on close).
            const close = () => (isUnsaved(key) ? setClosing(key) : onChange(closeTab(pane, key)))
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
                    <AgentAvatar
                      name={botName(tab.bot || mainBotOf(bots))}
                      className="size-5 shrink-0"
                    />
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
              case "take":
                return <TakeTab key={key} take={tab.take} onClose={close} {...common} />
              case "video":
                return <VideoTab key={key} video={tab.video} onClose={close} {...common} />
              case "phone":
                return (
                  <TabButton
                    key={key}
                    title="The Android phone the agents share"
                    onClose={close}
                    closeLabel="Close Phone"
                    {...common}
                  >
                    <Smartphone className="size-3.5 shrink-0" aria-hidden />
                    <span className="truncate">Phone</span>
                  </TabButton>
                )
              case "browser": {
                const shown = known(tab.tab)

                return (
                  <TabButton
                    key={key}
                    title={whose(shown, botName).line}
                    onClose={close}
                    closeLabel={`Close ${label(shown)}`}
                    {...common}
                  >
                    <TabIcon tab={shown} />
                    <span className="truncate">{label(shown)}</span>
                    {shown.id !== ownerTab && (
                      <span className="text-xs text-secondary-label">
                        {whose(shown, botName).mark}
                      </span>
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
                          <DropdownMenuItem key={bot.id} onClick={() => newChat(bot.id)}>
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
                  {hasPhone(device.data) && (
                    <DropdownMenuItem
                      onClick={() =>
                        beside ? beside.open({ kind: "phone" }) : open({ kind: "phone" })
                      }
                    >
                      <Smartphone />
                      {hasTab(pane, "phone") || beside?.has("phone") ? "Go to the phone" : "Phone"}
                    </DropdownMenuItem>
                  )}
                </DropdownMenuGroup>
                {theirs.length > 0 && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuGroup>
                      <DropdownMenuLabel>Tabs of this pane's chats</DropdownMenuLabel>
                      {theirs.map((tab) => (
                        <DropdownMenuItem
                          key={tab.id}
                          onClick={() => open({ kind: "browser", tab: tab.id })}
                        >
                          <Globe />
                          <span className="truncate">{tabName(tab)}</span>
                          <span className="ms-auto ps-2 text-xs text-secondary-label">
                            {whose(tab, botName).mark}
                          </span>
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
              onClick={() => onChange({ ...pane, files: !pane.files, artifact: undefined })}
            >
              <FolderTree />
            </Button>
          </div>
        )}
        {end && <div className="flex shrink-0 items-center gap-1 px-1.5 empty:hidden">{end}</div>}
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
                  <div key={key} className="flex min-h-0 min-w-0 flex-1 flex-col">
                    <BrowserStrip
                      tabs={strip}
                      shown={tab.tab}
                      botName={botName}
                      onPick={(next) => pickBrowser(key, next)}
                    />
                    <BrowserView tab={tab.tab} label={`The page of ${label(known(tab.tab))}`} />
                  </div>
                )
              )
            }
            if (tab.kind === "phone") {
              return shown && <PhoneView key={key} label="The phone's screen" />
            }
            if (tab.kind === "take") {
              return (
                shown && (
                  <TakeView
                    key={key}
                    id={tab.take}
                    onClose={() => onChange(closeTab(pane, key))}
                    // A take's video opens as a tab of the same pane (issue #818).
                    onOpenVideo={(video) => open({ kind: "video", video })}
                  />
                )
              )
            }
            if (tab.kind === "video") {
              return (
                shown && (
                  <VideoView
                    key={key}
                    id={tab.video}
                    onClose={() => onChange(closeTab(pane, key))}
                    onOpenTake={(take) => open({ kind: "take", take })}
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
                        artifact: undefined,
                      })
                    }
                    // An artifact opens in the panel beside the chat, in the place of the Files panel (issue #402).
                    onOpenArtifact={(artifact) => onChange({ ...pane, artifact, files: false })}
                    onOpenBrowserTab={showTab}
                    // A take opens as a tab of the pane, beside the chat where there is room (issue #817).
                    onOpenTake={(take) =>
                      beside ? beside.open({ kind: "take", take }) : open({ kind: "take", take })
                    }
                  />
                ) : (
                  <DraftChat
                    id={tab.id}
                    bot={tab.bot}
                    onStarted={(chat) => onChange(replaceTab(pane, key, { kind: "chat", chat }))}
                  />
                )}
              </div>
            )
          })}
        </div>
        {pane.artifact && !pane.files && (
          <ArtifactSide
            key={pane.artifact}
            id={pane.artifact}
            onClose={() => onChange({ ...pane, artifact: undefined })}
          />
        )}
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
  const info = useChat(chat)
  const title = info.data?.title ?? "Chat"
  // The chat names its Bot by id; the tab shows its name (issue #466).
  const name = useBotName(info.data?.bot ?? "")

  return (
    <TabButton
      title={info.data && `Chat with ${name}`}
      onClose={onClose}
      closeLabel={`Close ${title}`}
      {...props}
    >
      {info.data && <AgentAvatar name={name} className="size-5 shrink-0" />}
      <span className="truncate">{title}</span>
    </TabButton>
  )
}

// TakeTab is a take's tab: a clapperboard and its title.
function TakeTab({ take, onClose, ...props }: TabProps & { take: string }) {
  const title = useTake(take).data?.title ?? "Take"

  return (
    <TabButton
      title={`A take of the phone: ${title}`}
      onClose={onClose}
      closeLabel={`Close ${title}`}
      {...props}
    >
      <Clapperboard className="size-3.5 shrink-0" aria-hidden />
      <span className="truncate">{title}</span>
    </TabButton>
  )
}

// VideoTab is a video's tab: a film and its title.
function VideoTab({ video, onClose, ...props }: TabProps & { video: string }) {
  const title = useVideo(video).data?.title ?? "Video"

  return (
    <TabButton
      title={`A video of a take: ${title}`}
      onClose={onClose}
      closeLabel={`Close ${title}`}
      {...props}
    >
      <Film className="size-3.5 shrink-0" aria-hidden />
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
