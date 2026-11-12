import { useQuery } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { Maximize2, Minimize2, X } from "lucide-react"
import { useState } from "react"

import "@/api/client"
import { getChat } from "@/api/generated/sdk.gen"
import { Button } from "@/components/ui/button"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { AppSidebar, PageHeader } from "@/features/shell/app-shell"
import { cn } from "@/lib/utils"

import { GridDrop } from "./grid-drop"
import { gridPanes, type GridLayout } from "./grid-layout"
import { alone, dropTab, withoutPane, type Bench } from "./moves"
import { frontOf, type Pane } from "./pane"
import { PaneView } from "./pane-view"
import { newPaneId, setBench, setPane, soloId, useBench, useGridOffered } from "./use-grid"

// GridScreen is several panes on one screen (issue #235), as Omnigent's grid of sessions (web/src/grid, Apache
// License 2.0): up to four, two rows by two columns. A pane is a group of tabs, as in Orca: a tab dropped on a
// pane's side makes a new pane there, one dropped in its middle joins it, and a pane left with no tab leaves the
// grid. A pane can fill the screen for a while, or leave the grid. With one pane left that has a chat, there is no
// grid any more, and that chat's page shows the pane. The grid is kept in this browser, and is not offered on a
// phone.
export function GridScreen() {
  const bench = useBench()
  const offered = useGridOffered()
  const navigate = useNavigate()
  const [full, setFull] = useState<string | null>(null)

  if (!offered || bench === null) {
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

  // commit keeps what a change made of the grid. One pane with a chat is not a grid: it is that chat's page.
  const commit = (next: Bench | null) => {
    if (full !== null && !(next && gridPanes(next.layout).includes(full))) setFull(null)
    if (next === null) {
      setBench(null)
      void navigate({ to: "/" })

      return
    }

    const chat = alone(next)
    if (chat === null) {
      setBench(next)

      return
    }

    setPane(soloId(chat), next.panes[next.layout as string] ?? null)
    setBench(null)
    void navigate({ to: "/chats/$chatId", params: { chatId: chat } })
  }

  const draw = (node: GridLayout) =>
    typeof node === "string" ? (
      <GridPane
        key={node}
        id={node}
        pane={bench.panes[node] ?? { tabs: [], active: "" }}
        full={full === node}
        onFull={() => setFull(full === node ? null : node)}
        onRemove={() => commit(withoutPane(bench, node))}
        onChange={(next) =>
          commit(
            next === null
              ? withoutPane(bench, node)
              : { ...bench, panes: { ...bench.panes, [node]: next } },
          )
        }
        drop={
          <GridDrop
            takes={(dragged, zone) => dropTab(bench, node, zone, dragged, "new") !== null}
            onDrop={(dragged, zone) => {
              const next = dropTab(bench, node, zone, dragged, newPaneId())
              if (next !== null) commit(next)
            }}
          />
        }
      />
    ) : (
      <div
        key={gridPanes(node).join()}
        className={cn("flex min-h-0 min-w-0 flex-1 gap-1.5", node.axis === "rows" && "flex-col")}
      >
        {node.children.map(draw)}
      </div>
    )

  return (
    <>
      <AppSidebar />
      <main aria-label="Grid of chats" className="relative flex min-w-0 flex-1 p-1.5">
        {draw(bench.layout)}
      </main>
    </>
  )
}

function GridPane({
  id,
  pane,
  full,
  onFull,
  onRemove,
  onChange,
  drop,
}: {
  id: string
  pane: Pane
  full: boolean
  onFull: () => void
  onRemove: () => void
  onChange: (next: Pane | null) => void
  drop: React.ReactNode
}) {
  // The pane is named by its tab in front.
  const front = frontOf(pane)
  const chat = front?.kind === "chat" ? front.chat : null
  const info = useQuery({
    queryKey: ["chat", chat],
    queryFn: async () => (await getChat({ path: { chat_id: chat ?? "" } })).data,
    enabled: chat !== null,
  })
  const title =
    front?.kind === "chat"
      ? (info.data?.title ?? "Chat")
      : front?.kind === "draft"
        ? "New chat"
        : "Browser"

  return (
    <section
      aria-label={title}
      className={cn(
        "relative flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden rounded-lg border bg-background",
        // A pane alone on the screen lies over the others, which stay as they are underneath.
        full && "absolute inset-1.5 z-20",
      )}
    >
      <PaneView
        id={id}
        pane={pane}
        onChange={onChange}
        end={
          <>
            <PaneButton label={full ? "Back to the grid" : "Full screen"} onClick={onFull}>
              {full ? <Minimize2 /> : <Maximize2 />}
            </PaneButton>
            <PaneButton label="Remove from the grid" onClick={onRemove}>
              <X />
            </PaneButton>
          </>
        }
      />
      {drop}
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
