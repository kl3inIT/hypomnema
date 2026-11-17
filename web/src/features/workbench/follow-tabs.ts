import { useEffect, useEffectEvent, useRef } from "react"

import type { BrowserTab } from "@/api/generated/types.gen"
import { freshTabs } from "@/features/browser/browser-state"

// useFollowTabs opens a browser tab of a pane's chats when it is new, once (issue #703): the tab a chat's Bot or
// worker just opened is shown, as OpenClaw's chat pane passes its newest browser tab to the browser panel, which
// follows it once (ui/src/pages/chat/chat-pane-layout-render.ts, latestBrowserTabCards; ui/src/components/browser/
// browser-panel-presentation.ts, followPreferredTab). Here a pane may hold several chats, each with its Bot and
// workers: every tab new in one read of the list opens, so that none is dropped. `open` is called once with their
// ids, in the list's order, the last to be in front. The first list the pane sees is only noted: what was open
// before is not opened.
export function useFollowTabs(
  tabs: BrowserTab[] | undefined,
  chats: readonly string[],
  open: (tabs: string[]) => void,
) {
  const seen = useRef<ReadonlySet<string> | null>(null)
  const follow = useEffectEvent(open)
  const of = chats.join(" ")

  useEffect(() => {
    if (tabs === undefined) return

    const fresh = freshTabs(seen.current, tabs, new Set(of.split(" ")))
    seen.current = new Set(tabs.map((t) => t.id))

    if (fresh.length > 0) follow(fresh.map((t) => t.id))
  }, [tabs, of])
}
