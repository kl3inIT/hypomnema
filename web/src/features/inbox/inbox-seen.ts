import { useCallback, useState } from "react"

import type { Row } from "./inbox-state"

// What the owner has opened, so a row he has never opened shows bold and one he read once is regular, as OpenMausBot
// reads a row when it is opened (InboxPanel.tsx readInboxRow, InboxRow.tsx row.unread) and Chatwoot's card is bold
// only while it holds what was not seen (ConversationCard.vue hasUnread). A Bot's later edit of an item, its draft or
// its due, does not make it unread again (issue #1110: the Thư ký edits the items it added, and each edit made a read
// row bold again).
// It is kept in this browser only; the server keeps no read state.
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

export function useSeen() {
  const [seen, setSeen] = useState(read)

  const markSeen = useCallback((row: Row) => {
    setSeen((before) => {
      if (row.key in before) return before

      const next = Object.fromEntries(
        [...Object.entries(before), [row.key, new Date().toISOString()]].slice(-most),
      )
      try {
        window.localStorage.setItem(key, JSON.stringify(next))
      } catch {
        // A browser that keeps nothing shows every row as unread.
      }

      return next
    })
  }, [])

  return { unread: (row: Row) => !(row.key in seen), markSeen }
}
