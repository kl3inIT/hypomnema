import { createContext, useContext } from "react"

// PlayerContext is how a time on a meeting's page reaches its player (meeting-player.tsx): seek plays from a
// moment, at is where the recording is, in ms.
export type Player = { seek: (ms: number) => void; at: number }

export const PlayerContext = createContext<Player | null>(null)

// useSeek is how a time on the page plays that moment; nothing when the page has no player.
export function useSeek() {
  return useContext(PlayerContext)?.seek
}

// usePlayingAt is the time the recording is at, in ms, for the line being said; -1 without a player.
export function usePlayingAt() {
  return useContext(PlayerContext)?.at ?? -1
}
