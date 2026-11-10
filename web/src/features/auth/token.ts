import { useSyncExternalStore } from "react"

// The owner's token, kept in this browser after it is pasted once, as OpenClaw's control UI keeps its gateway token
// (.tmp/openclaw/ui/src/app/gateway-store.credentials.ts). Storage can be missing or refuse (a private window); the
// token then lives until the page closes.

const key = "hypomnema.token"
const listeners = new Set<() => void>()
let memory: string | null = null

export function readToken(): string | null {
  try {
    return window.localStorage.getItem(key) ?? memory
  } catch {
    return memory
  }
}

export function saveToken(token: string | null) {
  memory = token

  try {
    if (token === null) window.localStorage.removeItem(key)
    else window.localStorage.setItem(key, token)
  } catch {
    // Kept in memory only.
  }

  for (const notify of listeners) notify()
}

function subscribe(notify: () => void) {
  listeners.add(notify)

  return () => listeners.delete(notify)
}

export function useToken(): string | null {
  return useSyncExternalStore(subscribe, readToken, () => null)
}
