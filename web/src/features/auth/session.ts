import { useSyncExternalStore } from "react"

import { client } from "@/api/generated/client.gen"
import { createSession, deleteSession, getMe } from "@/api/generated/sdk.gen"

// Whether this browser is signed in. The session is an HttpOnly cookie the server sets (issue #114, as Omnigent's):
// the page cannot read it, so it asks the server who it is, and holds nothing in storage.

export type SessionState = "checking" | "signed-in" | "signed-out"

const listeners = new Set<() => void>()
let state: SessionState = "checking"

function set(next: SessionState) {
  if (state === next) return

  state = next
  for (const notify of listeners) notify()
}

function subscribe(notify: () => void) {
  listeners.add(notify)

  return () => listeners.delete(notify)
}

export function useSession(): SessionState {
  return useSyncExternalStore(
    subscribe,
    () => state,
    () => "checking" as const,
  )
}

// A request the server refuses for want of a session ends the one this page thought it had: it ran out, or a new
// password ended it.
client.interceptors.response.use((response) => {
  if (response.status === 401) set("signed-out")

  return response
})

// Asks the server whether the cookie this browser has, if any, is a session.
export async function checkSession() {
  try {
    await getMe()
    set("signed-in")
  } catch {
    set("signed-out")
  }
}

// What the server says when it refuses a sign-in: a wrong name or password, or too many wrong tries.
export class SignInError extends Error {}

export async function signIn(username: string, password: string) {
  try {
    await createSession({ body: { username, password } })
  } catch (error) {
    const refused = error as { code?: string; message?: string } | undefined
    throw new SignInError(
      refused?.code === "rate_limited" && refused.message
        ? `${refused.message[0]?.toUpperCase()}${refused.message.slice(1)}.`
        : "The name or the password is wrong.",
    )
  }

  set("signed-in")
}

export async function signOut() {
  try {
    await deleteSession()
  } catch {
    // The page shows the sign-in screen either way.
  }

  set("signed-out")
}

// For tests: a page starts not knowing.
export function resetSession() {
  set("checking")
}
