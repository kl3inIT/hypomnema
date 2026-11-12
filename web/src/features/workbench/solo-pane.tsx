import { useNavigate } from "@tanstack/react-router"
import type { ReactNode } from "react"

import { GridDrop } from "./grid-drop"
import { dropTab, type Bench, type Dragged } from "./moves"
import { hasTab, openTab, paneOf, tabKey, type Pane, type PaneTab } from "./pane"
import { PaneView } from "./pane-view"
import { newPaneId, setBench, setPane, soloId, useGridOffered, usePane } from "./use-grid"

// SoloPane is the pane of a chat's own page (issue #235). The chat's tab is always there; what else was opened in
// it is kept in this browser. A tab dropped on a side of it makes a grid of two panes, this one and the tab's; one
// dropped in its middle joins it.
export function SoloPane({ chat, start }: { chat: string; start?: ReactNode }) {
  const id = soloId(chat)
  const navigate = useNavigate()
  const offered = useGridOffered()
  const home: PaneTab = { kind: "chat", chat }
  const stored = usePane(id)
  const pane: Pane =
    stored === undefined
      ? paneOf(home)
      : hasTab(stored, tabKey(home))
        ? stored
        : { tabs: [home, ...stored.tabs], active: stored.active }

  // The page as a grid of one pane, under the id `as`: a split gives the pane a new id on the grid, so that this
  // page starts afresh when it is opened again.
  const bench = (as: string): Bench => ({ layout: as, panes: { [as]: pane } })
  const from = (dragged: Dragged, as: string): Dragged =>
    dragged.from === id ? { ...dragged, from: as } : dragged

  return (
    <>
      <PaneView
        id={id}
        pane={pane}
        onChange={(next) => setPane(id, next ?? paneOf(home))}
        kept={tabKey(home)}
        start={start}
      />
      {offered && (
        <GridDrop
          takes={(dragged, zone) =>
            dropTab(bench("as"), "as", zone, from(dragged, "as"), "new") !== null
          }
          onDrop={(dragged, zone) => {
            if (zone === "center") {
              setPane(id, openTab(pane, dragged.tab))

              return
            }

            const as = newPaneId()
            const next = dropTab(bench(as), as, zone, from(dragged, as), newPaneId())
            if (next === null) return

            setPane(id, null)
            setBench(next)
            void navigate({ to: "/grid" })
          }}
        />
      )}
    </>
  )
}
