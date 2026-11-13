import "@/api/client"
import { getChat } from "@/api/generated/sdk.gen"

// The place the owner was last in, a chat or the grid, kept in this browser so that opening the app goes back to it
// (owner, 2026-11-13), as OpenMausBot keeps its last conversation (src/lib/last-conversation.ts,
// openmausbot.lastConversation.v1) and opens it again when it loads. Only the app's first page is sent back: the
// start page asked for later, by New chat, stays the start page.
const placeKey = "hypomnema.lastPlace"

export type Place = { kind: "chat"; chat: string } | { kind: "grid" }

export function rememberPlace(place: Place) {
  try {
    window.localStorage.setItem(placeKey, JSON.stringify(place))
  } catch {
    // A browser that keeps nothing starts at the start page.
  }
}

function readPlace(): Place | null {
  try {
    const value: unknown = JSON.parse(window.localStorage.getItem(placeKey) ?? "null")
    if (!value || typeof value !== "object") return null
    const place = value as Record<string, unknown>
    if (place.kind === "grid") return { kind: "grid" }
    if (place.kind === "chat" && typeof place.chat === "string" && place.chat) {
      return { kind: "chat", chat: place.chat }
    }
  } catch {
    // Not one of ours.
  }

  return null
}

// The path the page was opened at: only an app opened at the start page goes back.
const openedAtStart = typeof window !== "undefined" && window.location.pathname === "/"
let asked = false

// placeToResume is where the app opened at the start page goes, once: the last chat while it is still there, or the
// grid; null otherwise.
export async function placeToResume(): Promise<Place | null> {
  if (asked || !openedAtStart) return null
  asked = true

  const place = readPlace()
  if (place?.kind !== "chat") return place

  try {
    await getChat({ path: { chat_id: place.chat } })

    return place
  } catch {
    // Deleted, or not this account's: the start page.
    return null
  }
}
