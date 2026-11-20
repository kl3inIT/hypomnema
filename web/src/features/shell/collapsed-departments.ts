import { useState } from "react"

// The departments the owner folded in the sidebar (issue #471) are kept in this browser only, as OpenMausBot keeps a
// section's collapse in its sidebar preferences (src/lib/sidebar-preferences.ts) and not on the server. Storage that
// is blocked or full only loses the fold.
const key = "hypomnema.sidebar.collapsed-departments"

function read(): string[] {
  try {
    const value: unknown = JSON.parse(window.localStorage.getItem(key) ?? "[]")

    return Array.isArray(value) ? value.filter((id): id is string => typeof id === "string") : []
  } catch {
    return []
  }
}

function write(ids: string[]) {
  try {
    window.localStorage.setItem(key, JSON.stringify(ids))
  } catch {
    // The fold is a convenience: without storage it lasts until the page is left.
  }
}

// useCollapsedDepartments is the folded departments and a toggle for one.
export function useCollapsedDepartments() {
  const [collapsed, setCollapsed] = useState(() => new Set(read()))

  const toggle = (id: string) => {
    setCollapsed((had) => {
      const next = new Set(had)
      if (!next.delete(id)) next.add(id)
      write([...next])

      return next
    })
  }

  return { collapsed, toggle }
}
