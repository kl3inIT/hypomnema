import { Globe } from "lucide-react"

import type { BrowserTab } from "@/api/generated/types.gen"
import { cn } from "@/lib/utils"

// TabIcon is a browser tab's page icon, as OpenClaw's tab strip shows a tab's favicon and else a globe
// (ui/src/components/browser/browser-panel-render.tsx, browser-panel-tabs.ts). The control plane reads the icon
// from the page and gives it as a data: address (issue #703).
export function TabIcon({ tab, className }: { tab?: BrowserTab; className?: string }) {
  return tab?.favicon ? (
    <img src={tab.favicon} alt="" className={cn("size-3.5 shrink-0 object-contain", className)} />
  ) : (
    <Globe className={cn("size-3.5 shrink-0", className)} aria-hidden />
  )
}
