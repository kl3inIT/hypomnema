import { useCallback, useState } from "react"

import type { Row } from "./inbox-state"

// What the owner has opened, so a row he has not read since it last changed shows bold, as Orca marks an item
// changed since the owner last opened the tab (bot-item-groups.ts isBotItemUnseen) and OpenMausBot reads a row when
// it is opened (InboxPanel.tsx readInboxRow). It is kept in this browser only; the server keeps no read state.
const key = "hypomnema.inbox.seen"
const most = 500

type Seen = Record<string, string>

function read(): Seen {
  try {
    const parsed: unknown = JSON.parse(window.localStorage.getItem(key) ?? "{}")

    return parsed && typeof parsed === "object" ? (parsed as Seen) : {}
  } catch {
    return {}
  }
}

// stampOf is what changes when a row changes: a to-do's last change, an ask's own moment.
function stampOf(row: Row) {
  switch (row.kind) {
    case "approval":
      return row.approval.created_at
    case "question":
      return row.question.created_at
    case "todo":
      return row.todo.updated_at
  }
}

export function useSeen() {
  const [seen, setSeen] = useState(read)

  const markSeen = useCallback((row: Row) => {
    setSeen((before) => {
      if (before[row.key] === stampOf(row)) return before

      const next = Object.fromEntries(
        [...Object.entries(before).filter(([k]) => k !== row.key), [row.key, stampOf(row)]].slice(
          -most,
        ),
      )
      try {
        window.localStorage.setItem(key, JSON.stringify(next))
      } catch {
        // A browser that keeps nothing shows every row as unread.
      }

      return next
    })
  }, [])

  return { unread: (row: Row) => seen[row.key] !== stampOf(row), markSeen }
}
