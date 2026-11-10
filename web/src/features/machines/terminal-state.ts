// What the terminal page works out without a terminal: why a connection closed, how long to wait before the next
// try, and the WebSocket's address.

export type TerminalStatus =
  | { kind: "connecting" }
  | { kind: "connected" }
  | { kind: "dropped"; retryInSeconds: number }
  | { kind: "ended" }
  | { kind: "replaced" }
  | { kind: "refused"; message: string }

// The reasons the node closes a terminal with (internal/node/terminal).
const shellEnded = "the shell ended"
const takenOver = "another connection took this session over"

// closedBecause reads a WebSocket's close reason: the shell ended, another tab took the session, or the
// connection dropped, which is every other close and the only one tried again without asking.
export function closedBecause(reason: string): "ended" | "replaced" | "dropped" {
  if (reason === shellEnded) return "ended"
  if (reason === takenOver) return "replaced"

  return "dropped"
}

// retryIn is the wait before try number `tries` (from 0) after a drop: 1 s, 2 s, 4 s, then 8 s each time.
export function retryIn(tries: number) {
  return Math.min(8000, 1000 * 2 ** tries)
}

// socketUrl is the terminal's WebSocket on the server that served the page: the ticket is its permission.
export function socketUrl(
  location: { protocol: string; host: string },
  ticket: string,
  rows: number,
  cols: number,
) {
  const scheme = location.protocol === "https:" ? "wss:" : "ws:"

  return `${scheme}//${location.host}/api/v1/terminals/${encodeURIComponent(ticket)}?height=${rows}&width=${cols}`
}
