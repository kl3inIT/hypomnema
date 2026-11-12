import { useQuery } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { Maximize2, Minimize2, X } from "lucide-react"
import { useState } from "react"

import "@/api/client"
import { getChat } from "@/api/generated/sdk.gen"
import { Button } from "@/components/ui/button"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { AgentAvatar } from "@/features/agents/agent-avatar"
import { ChatBody } from "@/features/chat/chat-screen"
import { AppSidebar, PageHeader } from "@/features/shell/app-shell"
import { cn } from "@/lib/utils"

import { GridDrop } from "./grid-drop"
import { gridChats, removeChat, splitGrid, swapChats, type GridLayout } from "./grid-layout"
import { chatDrag, setDragged, setGrid, useGrid, useGridOffered } from "./use-grid"

// GridScreen is several chats on one screen (issue #235), as Omnigent's grid of sessions (web/src/grid, Apache
// License 2.0): up to four panes, two rows by two columns. A chat dragged from the sidebar onto a pane's edge takes
// that half; a pane's header dragged onto another pane changes places with it; a pane can fill the screen for a
// while, or leave the grid. With one chat left there is no grid any more, and that chat is shown alone. The grid
// is kept in this browser, and is not offered on a phone.
export function GridScreen() {
  const layout = useGrid()
  const offered = useGridOffered()
  const navigate = useNavigate()
  const [full, setFull] = useState<string | null>(null)

  if (!offered || layout === null) {
    return (
      <>
        <AppSidebar />
        <main className="flex min-w-0 flex-1 flex-col">
          <PageHeader title="Grid" />
          <p className="m-auto max-w-sm px-6 text-center text-sm text-muted-foreground">
            {offered
              ? "No grid is kept in this browser. Open a chat and drag another chat from the sidebar onto it."
              : "The grid needs a desktop's screen. Open a chat from the menu."}
          </p>
        </main>
      </>
    )
  }

  const close = (chat: string) => {
    const next = removeChat(layout, chat)
    if (full === chat) setFull(null)

    // One chat is not a grid: it is shown alone, as any chat.
    if (next === null || typeof next === "string") {
      setGrid(null)
      void (next === null
        ? navigate({ to: "/" })
        : navigate({ to: "/chats/$chatId", params: { chatId: next } }))

      return
    }

    setGrid(next)
  }

  const draw = (node: GridLayout) =>
    typeof node === "string" ? (
      <Pane
        key={node}
        chat={node}
        full={full === node}
        onFull={() => setFull(full === node ? null : node)}
        onClose={() => close(node)}
        splits={(dragged, edge) => splitGrid(layout, node, dragged, edge) !== null}
        onSplit={(dragged, edge) => setGrid(splitGrid(layout, node, dragged, edge) ?? layout)}
        onSwap={(dragged) => setGrid(swapChats(layout, dragged, node))}
      />
    ) : (
      <div
        key={gridChats(node).join()}
        className={cn("flex min-h-0 min-w-0 flex-1 gap-1.5", node.axis === "rows" && "flex-col")}
      >
        {node.children.map(draw)}
      </div>
    )

  return (
    <>
      <AppSidebar />
      <main aria-label="Grid of chats" className="relative flex min-w-0 flex-1 p-1.5">
        {draw(layout)}
      </main>
    </>
  )
}

function Pane({
  chat,
  full,
  onFull,
  onClose,
  splits,
  onSplit,
  onSwap,
}: {
  chat: string
  full: boolean
  onFull: () => void
  onClose: () => void
  splits: React.ComponentProps<typeof GridDrop>["splits"]
  onSplit: React.ComponentProps<typeof GridDrop>["onSplit"]
  onSwap: (dragged: string) => void
}) {
  const info = useQuery({
    queryKey: ["chat", chat],
    queryFn: async () => (await getChat({ path: { chat_id: chat } })).data,
  })
  const title = info.data?.title ?? "Chat"

  return (
    <section
      aria-label={title}
      className={cn(
        "relative flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden rounded-lg border bg-background",
        // A pane alone on the screen lies over the others, which stay as they are underneath.
        full && "absolute inset-1.5 z-20",
      )}
    >
      <header
        draggable
        title="Drag onto another pane to change places"
        className="flex h-10 shrink-0 cursor-grab items-center gap-2 border-b bg-sidebar px-3 select-none"
        onDragStart={(event) => {
          event.dataTransfer.effectAllowed = "move"
          event.dataTransfer.setData(chatDrag, chat)
          setDragged({ chat, from: "pane" })
        }}
        onDragEnd={() => setDragged(null)}
      >
        <h2 className="min-w-0 flex-1 truncate text-sm font-medium">{title}</h2>
        {info.data && (
          <Link
            to="/bots/$name"
            params={{ name: info.data.bot }}
            aria-label={`Bot ${info.data.bot}`}
            draggable={false}
            className="flex shrink-0 items-center gap-1.5 rounded-md px-1.5 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
          >
            <AgentAvatar name={info.data.bot} className="size-5" />
            {info.data.bot}
          </Link>
        )}
        <PaneButton label={full ? "Back to the grid" : "Full screen"} onClick={onFull}>
          {full ? <Minimize2 /> : <Maximize2 />}
        </PaneButton>
        <PaneButton label="Remove from the grid" onClick={onClose}>
          <X />
        </PaneButton>
      </header>
      <ChatBody chatId={chat} />
      <GridDrop chat={chat} splits={splits} onSplit={onSplit} onSwap={onSwap} />
    </section>
  )
}

function PaneButton({
  label,
  onClick,
  children,
}: {
  label: string
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={<Button variant="ghost" size="icon-xs" aria-label={label} onClick={onClick} />}
      >
        {children}
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  )
}
