import type { MuseAccount } from "@/api/generated/types.gen"
import type { Tone } from "@/features/models/models-state"

// What the Muse page and a chat's muse_ask row show (issue #782), worked out from what the server and the tool
// answer. The states are the pool's (internal/controlplane/muse, row 7), each with the cause it observed.

export type MuseState = { label: string; tone: Tone; why: string }

// museState is an account's badge: what the pool last saw, and why when pointed at.
export function museState(account: MuseAccount): MuseState {
  if (!account.on) {
    return { label: "Off", tone: "outline", why: "Switched off: no new thread is started with it." }
  }
  if (account.resting) {
    return {
      label: "Out of quota",
      tone: "warning",
      why: "Muse said it is out of quota. It rests until Settings → Usage in Muse shows room, read at most every six hours.",
    }
  }
  switch (account.state) {
    case "needs_sign_in":
      return {
        label: "Needs sign-in",
        tone: "destructive",
        why: "Muse no longer takes its session. Sign in again.",
      }
    case "refused":
      return {
        label: "Refused",
        tone: "warning",
        why: "muse.ai answered 403: its region, network or access. It is tried again at the next question.",
      }
    case "workspace_not_ready":
      return {
        label: "Workspace not ready",
        tone: "warning",
        why: "Its Muse workspace was not assigned or its machine not running. It is tried again at the next question.",
      }
    case "page_not_ready":
      return {
        label: "Page not ready",
        tone: "warning",
        why: "muse.ai's page did not become ready. It is tried again at the next question.",
      }
    default:
      return { label: "Signed in", tone: "success", why: "New threads take it in its turn." }
  }
}

// MuseLink is a link inside Muse's answer.
type MuseLink = { url: string; text?: string }

// MuseFile is a file of the answer, kept or not with why.
type MuseFile = { url: string; path?: string; mime?: string; size?: number; error?: string }

// MuseAnswer is muse_ask's result, as the tool answers it (agentapi/muse.go).
export type MuseAnswer = {
  outcome: "answered" | "interrupted" | "unknown" | "asks_permission" | "error"
  answer?: string
  links?: MuseLink[]
  unverified?: boolean
  files?: MuseFile[]
  thread?: string
  thread_url?: string
  account: string
  account_label: string
  note?: string
  at: string
}

const outcomes = new Set(["answered", "interrupted", "unknown", "asks_permission", "error"])

// parseMuseAnswer reads muse_ask's output; null when it is not one (an error said as text).
export function parseMuseAnswer(output?: string): MuseAnswer | null {
  if (!output) return null
  try {
    const value: unknown = JSON.parse(output)
    if (typeof value !== "object" || value === null) return null
    const outcome = Reflect.get(value, "outcome")
    if (typeof outcome !== "string" || !outcomes.has(outcome)) return null

    return value as MuseAnswer
  } catch {
    return null
  }
}

// museProblem says whether a muse_ask failure is the pool's to fix on the Muse page: a sign-in or quota.
export function museProblem(text: string): boolean {
  return /signed in again|sign in|out of quota|resting|no Muse account/i.test(text)
}

// linkHost is a link's site, for a source's line.
export function linkHost(url: string): string {
  try {
    return new URL(url).hostname.replace(/^www\./, "")
  } catch {
    return url
  }
}

// usageLine is an account's usage as the page shows it.
export function usageLine(account: MuseAccount): string {
  return account.usage === "unknown" ? "Usage unknown" : account.usage
}

// isMuseAsk says whether a step's tool is muse_ask, bare or under Hypomnema's MCP server.
export function isMuseAsk(name: string): boolean {
  return name === "muse_ask" || name.endsWith("__muse_ask")
}
