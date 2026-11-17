import type { BrowserTab } from "@/api/generated/types.gen"
import { cn } from "@/lib/utils"

import { ownerTab, tabName, whose } from "./browser-state"
import { TabIcon } from "./tab-icon"

// BrowserStrip is the strip of tabs over a browser pane (issue #703), as OpenClaw's browser panel has its
// PanelTabStrip over the page (ui/src/components/browser/browser-panel-render.tsx, BrowserTabs): each tab with its
// page's icon and title, and here also whose it is, as the pane's own tabs say; the one shown is marked, and a
// click shows another. The tabs are `stripTabs` (./browser-state.ts).
export function BrowserStrip({
  tabs,
  shown,
  botName,
  onPick,
}: {
  tabs: BrowserTab[]
  shown: string
  botName: (id: string) => string
  onPick: (tab: string) => void
}) {
  return (
    <div
      role="tablist"
      aria-label="Tabs of the browser"
      className="flex h-8 shrink-0 items-stretch gap-0.5 overflow-x-auto border-b bg-muted/40 px-1"
    >
      {tabs.map((tab) => {
        const selected = tab.id === shown
        const name = tab.id === ownerTab ? "My browser" : tabName(tab)
        const who = whose(tab, botName)

        return (
          <button
            key={tab.id}
            type="button"
            role="tab"
            aria-selected={selected}
            title={`${name}\n${who.line}`}
            onClick={() => onPick(tab.id)}
            className={cn(
              "relative my-1 flex max-w-56 shrink-0 items-center gap-1.5 rounded-md px-2 text-xs whitespace-nowrap text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50",
              selected && "bg-background text-foreground shadow-xs",
            )}
          >
            <TabIcon tab={tab} />
            <span className="truncate">{name}</span>
            <span className="shrink-0 text-2xs text-secondary-label">{who.mark}</span>
          </button>
        )
      })}
    </div>
  )
}
