// The page the owner was on before Settings (issue #747), so that "Back to app" returns there rather than home: a link
// into Settings from Meetings or a Bot's channels leads back to that page. No reference was read for this; it is the
// owner's design. It is kept for the tab in sessionStorage, so that a reload inside Settings still knows it, and
// "/" stands in when nothing was kept or storage is refused.

const key = "hypomnema.app-page"
let kept = ""

function inSettings(href: string) {
  const [path = ""] = href.split(/[?#]/, 1)
  // Issue #857 moved the Agents pages and Machines list into Settings. Older tabs may still have their old URLs.
  return (
    path === "/settings" ||
    path.startsWith("/settings/") ||
    /^\/agents(?:\/[^/]+)?\/?$/.test(path) ||
    path === "/machines" ||
    path === "/machines/"
  )
}

// rememberAppPage keeps where the owner is, unless it is a page of Settings.
export function rememberAppPage(href: string) {
  if (!href.startsWith("/") || inSettings(href)) return

  kept = href
  try {
    sessionStorage.setItem(key, href)
  } catch {
    // Storage refused: the page is kept in memory only.
  }
}

// appPage is where "Back to app" goes: the last page outside Settings, else home.
export function appPage() {
  if (kept) return kept

  try {
    const stored = sessionStorage.getItem(key)
    if (stored && stored.startsWith("/") && !inSettings(stored)) return stored
  } catch {
    // Storage refused.
  }

  return "/"
}
