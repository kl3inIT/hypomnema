import { ExternalLink } from "lucide-react"

import { Button } from "@/components/ui/button"

import { browserOpenUrl, chatBotTab, tabName, useBrowser } from "./browser-state"
import { useOpenBrowserTab } from "./open-tab-context"
import { TabIcon } from "./tab-icon"

// BrowserTabCard is a call of agent_browser_open in the chat (issue #703), as OpenClaw shows a browser call's tab
// as a card in the chat that the browser panel follows (ui/src/lib/chat/browser-tab-preview.ts,
// latestBrowserTabCards): the page's icon, its title and address, and "Open the tab", which opens the chat's Bot's
// tab where the pane opens it. The tab is shown as it is now; once it is closed, the card gives the address that
// was opened, and nothing to open.
export function BrowserTabCard({ args }: { args?: Record<string, unknown> }) {
  const browser = useBrowser()
  const opener = useOpenBrowserTab()
  const tab = opener ? chatBotTab(browser.data?.tabs, opener.chat) : undefined
  const opened = browserOpenUrl(args)
  const url = tab?.url && tab.url !== "about:blank" ? tab.url : opened
  const title = tab ? tabName(tab) : opened || "A page"

  return (
    <div className="pb-4">
      <section
        aria-label={`Browser tab: ${title}`}
        className="flex w-full max-w-md items-center gap-3 rounded-xl border bg-card p-2 ps-3 shadow-xs"
      >
        <span className="flex size-8 shrink-0 items-center justify-center rounded-lg border bg-muted">
          <TabIcon tab={tab} className="size-4" />
        </span>
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="truncate text-sm font-medium text-foreground">{title}</span>
          <span className="truncate text-xs text-muted-foreground" title={url}>
            {tab ? url : browser.data ? "The tab is closed" : opened}
          </span>
        </span>
        {opener && tab && (
          <Button variant="outline" size="sm" onClick={() => opener.open(tab.id)}>
            <ExternalLink />
            Open the tab
          </Button>
        )}
      </section>
    </div>
  )
}
