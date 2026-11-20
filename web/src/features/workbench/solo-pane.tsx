import { useNavigate } from "@tanstack/react-router"
import type { ReactNode } from "react"

import { GridDrop } from "./grid-drop"
import { closeHome, dropTab, openAllBeside, type Bench, type Dragged } from "./moves"
import { BackToMainChat } from "./back-to-main"
import { frontChat, hasTab, openTab, tabKey, type PaneTab } from "./pane"
import { showSoloChat, soloPaneOf } from "./solo-chat"
import { PaneView } from "./pane-view"
import { newPaneId, setBench, setPane, soloId, useGridOffered, usePane } from "./use-grid"

// SoloPane is the pane of a chat's own page (issue #235). The chat's tab is there whenever the page is shown; what
// else was opened in it is kept in this browser. Closing the chat's tab leaves the page for where the rest of the
// pane goes (`closeHome` in ./moves.ts), as Orca closes any tab. A tab dropped on a side of it makes a grid of two
// panes, this one and the tab's; one dropped in its middle joins it.
//
// The chat in front of the pane is the one being viewed (issue #923): a chat dragged into its middle comes in front
// while the address still names the page's chat, so the sidebar's Set as main chat (useSoloChat) and Back to main
// chat follow the tab in front, not the address.
export function SoloPane({ chat, start }: { chat: string; start?: ReactNode }) {
  const id = soloId(chat)
  const navigate = useNavigate()
  const offered = useGridOffered()
  const home: PaneTab = { kind: "chat", chat }
  const pane = soloPaneOf(chat, usePane(id))
  const viewed = frontChat(pane)

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
        onChange={(next) => {
          if (next !== null && hasTab(next, tabKey(home))) {
            setPane(id, next)

            return
          }

          // The chat's own tab was closed: the page goes where the rest of its pane is (issue #681).
          const away = closeHome(pane, tabKey(home), offered)
          setPane(id, null)
          switch (away.to) {
            case "new":
              void navigate({ to: "/" })
              break
            case "chat":
              setPane(soloId(away.chat), away.pane)
              void navigate({ to: "/chats/$chatId", params: { chatId: away.chat } })
              break
            case "grid": {
              const as = newPaneId()
              setBench({ layout: as, panes: { [as]: away.pane } })
              void navigate({ to: "/grid" })
              break
            }
          }
        }}
        start={start}
        // Back to main chat brings the main chat in front where the pane has it, and otherwise opens its page with it
        // in front.
        end={
          <BackToMainChat
            chatId={viewed}
            onBack={(main) => {
              if (hasTab(pane, tabKey({ kind: "chat", chat: main }))) {
                setPane(id, openTab(pane, { kind: "chat", chat: main }))

                return
              }

              showSoloChat(main)
              void navigate({ to: "/chats/$chatId", params: { chatId: main } })
            }}
          />
        }
        // A tab opened beside the chat makes a grid of two, as a tab dropped on its side does (issue #528).
        beside={
          offered
            ? {
                open: (...tabs) => {
                  const as = newPaneId()
                  const next = openAllBeside(bench(as), as, tabs, newPaneId)
                  if (typeof next.layout === "string") {
                    setPane(id, next.panes[as] ?? pane)

                    return
                  }

                  setPane(id, null)
                  setBench(next)
                  void navigate({ to: "/grid" })
                },
                has: (key) => hasTab(pane, key),
              }
            : undefined
        }
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
