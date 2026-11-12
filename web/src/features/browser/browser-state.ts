// What the Browser page works out from the control plane's list of tabs.
import { useQuery } from "@tanstack/react-query"

import "@/api/client"
import { getBrowser } from "@/api/generated/sdk.gen"
import type { BrowserTab } from "@/api/generated/types.gen"

export const browserKey = ["browser"] as const

// The owner's own tab, where a sign-in is made. Watching it makes it, so it is always offered.
export const ownerTab = "owner"

// whileOpen is how often the tabs are read again: an agent opens and leaves a tab without an event.
const whileOpen = 5000

export function useBrowser() {
  return useQuery({
    queryKey: browserKey,
    queryFn: async () => (await getBrowser()).data,
    refetchInterval: whileOpen,
    retry: false,
  })
}

// noBrowser says the control plane runs without a browser, which the server answers with "unavailable"; any
// other refusal is a failure.
export function noBrowser(error: unknown) {
  return (error as { code?: string } | null)?.code === "unavailable"
}

// tabsOf is what the strip shows: the owner's own tab first, there or not yet, then the agents'.
export function tabsOf(tabs: BrowserTab[]): [BrowserTab, ...BrowserTab[]] {
  const mine = tabs.find((t) => t.id === ownerTab) ?? {
    id: ownerTab,
    kind: "owner" as const,
    url: "",
    title: "",
    views: 0,
  }

  return [mine, ...tabs.filter((t) => t.id !== ownerTab)]
}

// whose says whose a tab is, in a word and in a line.
export function whose(tab: BrowserTab): { mark: string; line: string } {
  switch (tab.kind) {
    case "owner":
      return { mark: "you", line: "Your own tab. Sign in here: every agent shares the sign-in." }
    case "coordinator":
      return { mark: "pi", line: "The coordinator's tab." }
    case "worker":
      return {
        mark: "worker",
        line: tab.task_id ? `A worker's tab, for task ${tab.task_id}.` : "A worker's tab.",
      }
    default:
      return { mark: "token", line: "A tab opened with the owner's token." }
  }
}

// tabName is what a tab is called: its page's title, as in any browser (Orca's BrowserTab), or what it is while
// it has none.
export function tabName(tab: BrowserTab) {
  if (tab.title && tab.title !== tab.url && tab.title !== "about:blank") return tab.title
  if (tab.kind === "owner") return "My tab"

  return tab.url && tab.url !== "about:blank" ? tab.url : "New tab"
}
