import { useState } from "react"

// A table's column visibility, after Beszel's systems-table (MIT, Copyright (c) 2024 henrygd,
// https://github.com/henrygd/beszel; internal/site/src/components/systems-table/systems-table.tsx): its
// columnVisibility state, kept in this browser under one key per table ("besz-cols" there). Changed: a table names
// the columns hidden until the owner chooses, where Beszel shows them all; a plain hook instead of TanStack Table's,
// as these tables are drawn from plain column lists. The View menu (columns-menu.tsx) changes it.

// Visibility is TanStack Table's VisibilityState, as Beszel keeps it: a column missing from it is shown.
export type Visibility = Record<string, boolean>

export function isVisible(visibility: Visibility, id: string) {
  return visibility[id] !== false
}

// readVisibility is the owner's choice for a table, over the columns it hides until then.
export function readVisibility(key: string, hidden: readonly string[]): Visibility {
  const defaults: Visibility = Object.fromEntries(hidden.map((id) => [id, false]))

  try {
    const saved: unknown = JSON.parse(window.localStorage.getItem(key) ?? "{}")
    if (saved && typeof saved === "object" && !Array.isArray(saved)) {
      const choice = Object.entries(saved).filter(([, v]) => typeof v === "boolean")
      return { ...defaults, ...Object.fromEntries(choice) }
    }
  } catch {
    // An unreadable choice is no choice.
  }

  return defaults
}

// useColumnVisibility is a table's columnVisibility state, kept in this browser under key.
export function useColumnVisibility(key: string, hidden: readonly string[]) {
  const [visibility, setVisibility] = useState(() => readVisibility(key, hidden))

  function show(id: string, visible: boolean) {
    const next = { ...visibility, [id]: visible }
    setVisibility(next)

    try {
      window.localStorage.setItem(key, JSON.stringify(next))
    } catch {
      // Kept until the page closes.
    }
  }

  return [visibility, show] as const
}

// useStatusFilter is the View menu's status choice for a list, kept in this browser under key as Beszel keeps its
// statusFilter ("besz-statusFilter"); a choice that is no longer offered is "all".
export function useStatusFilter<S extends string>(key: string, values: readonly S[]) {
  const [status, setStatus] = useState<S | "all">(() => {
    try {
      const saved = window.localStorage.getItem(key)
      return values.find((v) => v === saved) ?? "all"
    } catch {
      return "all"
    }
  })

  function choose(next: string) {
    const value = values.find((v) => v === next) ?? "all"
    setStatus(value)

    try {
      window.localStorage.setItem(key, value)
    } catch {
      // Kept until the page closes.
    }
  }

  return [status, choose] as const
}
