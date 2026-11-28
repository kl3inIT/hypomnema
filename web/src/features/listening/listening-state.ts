import type {
  Listening,
  ListeningBackend,
  ListeningGroup,
  ListeningSource,
} from "@/api/generated/types.gen"
import type { Tone } from "@/features/models/models-state"
import { since } from "@/lib/time"

// What the Social listening page shows of each source (issue #1290), worked out from what the server lists. The four
// groups are last30days' doctor's (lib/doctor.py:97-117: WORKING, TURNED ON - UNVERIFIED, NOT WORKING, COULD BE ON),
// in the issue's words; a code is listen_search's, said as a person reads it.

type GroupInfo = { label: string; tone: Tone; why: string }

const groups: Record<ListeningGroup, GroupInfo> = {
  working: {
    label: "Working",
    tone: "success",
    why: "Its last reading in the last 24 hours passed: a search read it, or its live test found items.",
  },
  unverified: {
    label: "On but unverified",
    tone: "warning",
    why: "On, but read only in part, or not read in the last 24 hours. A source not read for a day is tested within the hour.",
  },
  broken: {
    label: "Broken",
    tone: "destructive",
    why: "Its last reading failed. Searches report it as not read, never as nobody talking.",
  },
  could_be_on: {
    label: "Could be on",
    tone: "outline",
    why: "Switched off: no search reads it, and agents are not offered it.",
  },
}

// The groups in the order the page counts them, as doctor prints them.
export const groupOrder: ListeningGroup[] = ["working", "unverified", "broken", "could_be_on"]

export function groupInfo(group: ListeningGroup): GroupInfo {
  return groups[group]
}

const codes: Record<string, string> = {
  need_login: "Needs a sign-in",
  refused: "Refused",
  rate_limited: "Rate limited",
  limit_reached: "Daily quota spent",
  not_found: "Not found",
  timeout: "Timed out",
  broken: "Broken answer",
  no_results: "Found nothing",
}

// codeLabel says a failure's code in words.
export function codeLabel(code: string) {
  return codes[code] ?? code.replaceAll("_", " ")
}

// The names of the backends as a person reads them; an unknown one keeps its own.
const backends: Record<string, string> = {
  web_search: "Web search, keyless",
  reddit_json: "Reddit's public JSON",
  web_arctic: "Web search + Arctic Shift archive",
  graphql: "X's search, as your account",
  rendered_page: "LinkedIn's Posts search, as your account",
  site_search: "Web search on its site, keyless",
}

export function backendName(name: string) {
  return backends[name] ?? name.replaceAll("_", " ")
}

const clock = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" })

// backendState is a backend's badge: ready, or resting until when, for what.
export function backendState(b: ListeningBackend): { label: string; tone: Tone; why?: string } {
  if (b.state === "resting" && b.resting_until) {
    const why = b.rest_code ? codeLabel(b.rest_code) : "Resting"

    return {
      label: `Resting until ${clock.format(new Date(b.resting_until))}`,
      tone: "warning",
      why: `${why}: searches skip it until then; the source's other backends serve meanwhile.`,
    }
  }

  return { label: "Ready", tone: "outline", why: "Asked in its turn." }
}

// needsYou says the card shows the "!" badge (postiz's refreshNeeded): the source broke, or its account must be
// signed in again (issue #1291).
export function needsYou(source: ListeningSource) {
  return source.group === "broken" || accountNeedsYou(source)
}

// accountNeedsYou says a signed-in source's account asked for a sign-in again or was refused.
function accountNeedsYou(source: ListeningSource) {
  const state = source.signed?.account?.state

  return state === "need_login" || state === "refused"
}

type SignedStep = "consent" | "sign_in" | "account"

// signedStep is where a signed-in source stands (issue #1291): its consent card to agree to, an account to sign in,
// or its account; null for a source read without one.
export function signedStep(source: ListeningSource): SignedStep | null {
  if (!source.signed) return null
  if (!source.signed.consented_at) return "consent"

  return source.signed.account ? "account" : "sign_in"
}

// accountState is a signed-in account's badge.
export function accountState(
  source: ListeningSource,
  now = Date.now(),
): { label: string; tone: Tone; why: string } | null {
  const account = source.signed?.account
  if (!account) return null

  switch (account.state) {
    case "need_login":
      return {
        label: "Sign in again",
        tone: "destructive",
        why: "A real search asked this account to sign in again; its other backends serve meanwhile.",
      }
    case "refused":
      return {
        label: "Refused",
        tone: "destructive",
        why: "The site refused this account or asked for a security check: it is not read until you sign in again.",
      }
  }

  if (stoppedUntil(source, now)) {
    return {
      label: `Stopped until ${clock.format(new Date(source.signed!.stopped_until!))}`,
      tone: "warning",
      why: "After a rate-limit page it is not read for 24 hours.",
    }
  }

  return { label: "Signed in", tone: "success", why: "Searches read as this account." }
}

// stoppedUntil is when a signed-in source's site may be asked again after a rate-limit page, null when it is not
// stopped. Nothing is sent to it before then, not even a sign-in's check (review round 1 of PR #1359).
export function stoppedUntil(source: ListeningSource, now = Date.now()): Date | null {
  const until = source.signed?.stopped_until
  if (!until || Date.parse(until) <= now) return null

  return new Date(until)
}

// readLine says when the source was last read and whether that worked, for the card's head.
export function readLine(source: ListeningSource, now = Date.now()) {
  if (!source.checked_at) return "Not read yet"

  return `Read ${since(source.checked_at, now)}`
}

// itemsLine says how many items searches read this week.
export function itemsLine(source: ListeningSource) {
  const n = source.items_this_week

  return n === 1 ? "1 item this week" : `${n} items this week`
}

// failureShown says how the card shows the source's last error, which it always shows (issue #1290's card row):
// "before" when the source worked again after it, "now" otherwise; null when there is none.
export function failureShown(source: ListeningSource): "now" | "before" | null {
  if (!source.last_error) return null
  // The dates decide, not the group: a source not read for a day is unverified, yet its error older than its last
  // success is still from before it worked again (review round 2 of PR #1322).
  if (!source.last_success_at) return "now"

  // Both are kept to the millisecond: an error in the same millisecond as the success came with or after it.
  return new Date(source.last_error.at) >= new Date(source.last_success_at) ? "now" : "before"
}

// moved is an order with one backend moved one place up or down.
export function moved(order: string[], name: string, direction: "up" | "down") {
  const i = order.indexOf(name)
  const j = direction === "up" ? i - 1 : i + 1
  if (i < 0 || j < 0 || j >= order.length) return order

  const out = order.filter((n) => n !== name)
  out.splice(j, 0, name)

  return out
}

// shownSources are the sources of one group, or all when no group is chosen.
export function shownSources(listening: Listening, group: ListeningGroup | null) {
  return group ? listening.sources.filter((s) => s.group === group) : listening.sources
}
