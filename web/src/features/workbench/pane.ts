// A pane is a group of tabs (issue #235), as a tab group is in Orca (src/renderer/src/components/tab-group): what
// is open in it, in order, and which tab is in front. A tab is a chat, a tab of the shared browser, the phone (issue
// #417), a file, a take filmed of the phone (issue #817), a video made of a take (issue #818), or a new chat that
// has no message yet.

export type PaneTab =
  | { kind: "chat"; chat: string }
  // A tab of the shared browser. `chat` is the chat whose browser tabs the strip over it lists, kept when the strip
  // shows another tab in its place (issue #703), as OpenClaw's browser panel lists its session's tabs whichever
  // one it shows.
  | { kind: "browser"; tab: string; chat?: string }
  // The Android phone a node holds, of which there is one.
  | { kind: "phone" }
  // A new chat is made on its first message, as on the New chat page; until then it is only this tab. An empty bot
  // is the server's first.
  | { kind: "draft"; id: string; bot: string }
  // A file of a chat's folder, opened from the Files panel (issue #255): its text, or its diff since HEAD.
  | { kind: "file"; chat: string; workspace: string; path: string; view: FileView }
  // A take an agent filmed of the phone (issue #817), opened from its card in a chat.
  | { kind: "take"; take: string }
  // A video made of a take (issue #818), opened from the take's tab.
  | { kind: "video"; video: string }

type FileView = "file" | "diff"

// files says whether the Files panel is open beside the pane's tabs; artifact is the artifact open there instead
// (issue #402), as one panel at a time stands beside the chat.
export type Pane = { tabs: PaneTab[]; active: string; files?: boolean; artifact?: string }

export function tabKey(tab: PaneTab): string {
  switch (tab.kind) {
    case "chat":
      return `chat:${tab.chat}`
    case "browser":
      return `browser:${tab.tab}`
    case "phone":
      return "phone"
    case "draft":
      return `draft:${tab.id}`
    case "file":
      return `file:${tab.chat}:${tab.workspace}:${tab.view}:${tab.path}`
    case "take":
      return `take:${tab.take}`
    case "video":
      return `video:${tab.video}`
  }
}

export const paneOf = (tab: PaneTab): Pane => ({ tabs: [tab], active: tabKey(tab) })

export const paneChats = (pane: Pane | undefined): string[] =>
  (pane?.tabs ?? []).flatMap((tab) => (tab.kind === "chat" ? [tab.chat] : []))

export const hasTab = (pane: Pane, key: string) => pane.tabs.some((tab) => tabKey(tab) === key)

// openTab puts a tab in front, adding it at the end when it is not open already.
export function openTab(pane: Pane, tab: PaneTab): Pane {
  const key = tabKey(tab)

  return { ...pane, tabs: hasTab(pane, key) ? pane.tabs : [...pane.tabs, tab], active: key }
}

// closeTab takes a tab out; the one after it, or else before it, comes in front. null when it was the last.
export function closeTab(pane: Pane, key: string): Pane | null {
  const index = pane.tabs.findIndex((tab) => tabKey(tab) === key)
  if (index < 0) return pane

  const tabs = pane.tabs.filter((_, at) => at !== index)
  if (tabs.length === 0) return null
  if (pane.active !== key) return { ...pane, tabs }

  const next = tabs[Math.min(index, tabs.length - 1)]

  return { ...pane, tabs, active: next === undefined ? pane.active : tabKey(next) }
}

// replaceTab puts a tab in the place of another, in front when that one was: a new chat that became a chat.
export function replaceTab(pane: Pane, key: string, tab: PaneTab): Pane {
  return {
    ...pane,
    tabs: pane.tabs.map((each) => (tabKey(each) === key ? tab : each)),
    active: pane.active === key ? tabKey(tab) : pane.active,
  }
}

// frontOf is the tab in front, or the first when the one kept is not open any more.
export function frontOf(pane: Pane): PaneTab | undefined {
  return pane.tabs.find((tab) => tabKey(tab) === pane.active) ?? pane.tabs[0]
}

// frontChat is the chat of the tab in front of a pane, or null when that tab is not a chat (issue #923).
export function frontChat(pane: Pane): string | null {
  const front = frontOf(pane)

  return front?.kind === "chat" ? front.chat : null
}

// parsePane reads a pane kept in the browser, or null when it is not one.
export function parsePane(value: unknown): Pane | null {
  if (!value || typeof value !== "object") return null

  const { tabs, active, files, artifact } = value as Record<string, unknown>
  if (!Array.isArray(tabs) || typeof active !== "string") return null

  const kept = tabs.filter((tab): tab is PaneTab => {
    if (!tab || typeof tab !== "object") return false

    const t = tab as Record<string, unknown>
    switch (t.kind) {
      case "chat":
        return typeof t.chat === "string"
      case "browser":
        return typeof t.tab === "string" && (t.chat === undefined || typeof t.chat === "string")
      case "phone":
        return true
      case "draft":
        return typeof t.id === "string" && typeof t.bot === "string"
      case "take":
        return typeof t.take === "string"
      case "video":
        return typeof t.video === "string"
      case "file":
        return (
          typeof t.chat === "string" &&
          typeof t.workspace === "string" &&
          typeof t.path === "string" &&
          (t.view === "file" || t.view === "diff")
        )
      default:
        return false
    }
  })

  return kept.length === 0
    ? null
    : {
        tabs: kept,
        active,
        ...(files === true && { files }),
        ...(typeof artifact === "string" && artifact !== "" && { artifact }),
      }
}
