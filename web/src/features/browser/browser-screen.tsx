import { Bot, Globe, KeyRound, User, Wrench } from "lucide-react"
import { useState } from "react"

import type { BrowserTab } from "@/api/generated/types.gen"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { errorText } from "@/features/machines/machines-state"
import { PageHeader } from "@/features/shell/app-shell"
import { cn } from "@/lib/utils"

import { ownerTab, tabName, tabsOf, useBrowser, whose } from "./browser-state"
import { BrowserView } from "./browser-view"

const kindIcon = { owner: User, coordinator: Bot, worker: Wrench, token: KeyRound } as const

// BrowserScreen is the browser every agent shares (issue #226, part of #193): a strip of its tabs and, under it,
// the tab that is picked, watched and, with control, used. The strip follows OpenMausBot's ChromeRow and names a
// tab as Orca's tab bar does, by its page's title with a mark of whose it is. No tab is opened or closed here:
// agents open theirs, and the owner's own is made by being watched.
export function BrowserScreen() {
  const browser = useBrowser()
  const [picked, setPicked] = useState(ownerTab)
  const tabs = tabsOf(browser.data?.tabs ?? [])
  // A tab that went away while it was picked gives way to the owner's own.
  const current = tabs.find((t) => t.id === picked) ?? tabs[0]

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader title="Browser">
        {browser.data?.held && (
          <output className="text-xs whitespace-nowrap text-muted-foreground">
            You hold the browser: agents' browser tools are paused
          </output>
        )}
      </PageHeader>
      {browser.isError ? (
        <div className="p-4">
          <Alert variant="destructive">
            <Globe />
            <AlertTitle>The browser is not available</AlertTitle>
            <AlertDescription>{errorText(browser.error)}</AlertDescription>
          </Alert>
        </div>
      ) : (
        <>
          <div
            role="tablist"
            aria-label="Tabs of the shared browser"
            className="flex h-9 shrink-0 items-stretch overflow-x-auto border-b"
          >
            {tabs.map((tab) => (
              <TabButton
                key={tab.id}
                tab={tab}
                selected={tab.id === current.id}
                onPick={() => setPicked(tab.id)}
              />
            ))}
          </div>
          <BrowserView
            key={current.id}
            tab={current.id}
            label={`The page of ${tabName(current)}`}
          />
        </>
      )}
    </main>
  )
}

// TabButton is a tab of the strip. It is its own design, not a Button: a row of tabs with the picked one marked
// along its foot, as in OpenMausBot's and Orca's strips.
function TabButton({
  tab,
  selected,
  onPick,
}: {
  tab: BrowserTab
  selected: boolean
  onPick: () => void
}) {
  const Icon = kindIcon[tab.kind]
  const owner = whose(tab)

  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <button
            type="button"
            role="tab"
            aria-label={`${tabName(tab)}, ${owner.mark}`}
            aria-selected={selected}
            onClick={onPick}
            className={cn(
              "relative flex max-w-56 shrink-0 items-center gap-2 border-r px-3 text-sm whitespace-nowrap text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:bg-accent",
              selected &&
                "text-foreground after:absolute after:inset-x-2 after:bottom-0 after:h-0.5 after:rounded-t after:bg-primary",
            )}
          />
        }
      >
        <Icon className="size-3.5 shrink-0" aria-hidden />
        <span className="truncate">{tabName(tab)}</span>
        <span className="text-xs text-secondary-label">{owner.mark}</span>
      </TooltipTrigger>
      <TooltipContent>
        <span className="block max-w-72 text-pretty">
          {owner.line}
          {tab.url && tab.url !== "about:blank" && (
            <span className="block break-all">{tab.url}</span>
          )}
        </span>
      </TooltipContent>
    </Tooltip>
  )
}
