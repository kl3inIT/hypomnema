import { describe, expect, it } from "vitest"

import type { Listening, ListeningSource } from "@/api/generated/types.gen"

import {
  accountState,
  backendState,
  codeLabel,
  failureShown,
  groupInfo,
  groupOrder,
  itemsLine,
  moved,
  needsYou,
  readLine,
  shownSources,
  signedStep,
} from "./listening-state"

const now = Date.parse("2026-11-28T09:00:00Z")

const source = (over: Partial<ListeningSource> = {}): ListeningSource => ({
  name: "reddit",
  label: "Reddit",
  adds: "Reddit threads.",
  on: true,
  group: "working",
  regressed: false,
  items_this_week: 0,
  backends: [],
  ...over,
})

const failure = (at: string) => ({ code: "refused", detail: "HTTP 403", at, fix: "Wait." })

describe("the four groups", () => {
  it("are counted in doctor's order, each with its words", () => {
    expect(groupOrder).toEqual(["working", "unverified", "broken", "could_be_on"])
    expect(groupInfo("unverified").label).toBe("On but unverified")
    expect(groupInfo("broken").tone).toBe("destructive")
    expect(groupInfo("could_be_on").why).toContain("Switched off")
  })

  it("show one group's sources when one is chosen", () => {
    const listening: Listening = {
      counts: { working: 1, unverified: 0, broken: 1, could_be_on: 0 },
      sources: [source(), source({ name: "web", group: "broken" })],
    }

    expect(shownSources(listening, null)).toHaveLength(2)
    expect(shownSources(listening, "broken").map((s) => s.name)).toEqual(["web"])
  })
})

describe("a card", () => {
  it("wears the ! badge only when broken", () => {
    expect(needsYou(source({ group: "broken" }))).toBe(true)
    expect(needsYou(source({ group: "unverified" }))).toBe(false)
    expect(needsYou(source({ group: "could_be_on", on: false }))).toBe(false)
  })

  it("says when it was read and how many items it read", () => {
    expect(readLine(source({ on: false }))).toBe("Not read yet")
    expect(readLine(source())).toBe("Not read yet")
    expect(readLine(source({ checked_at: "2026-11-28T08:00:00Z" }), now)).toBe(
      `Read ${new Intl.RelativeTimeFormat(undefined, { style: "narrow" }).format(-1, "hour")}`,
    )
    expect(itemsLine(source({ items_this_week: 1 }))).toBe("1 item this week")
    expect(itemsLine(source({ items_this_week: 12 }))).toBe("12 items this week")
  })

  it("always shows the last error, marked when the source worked again after it", () => {
    const old = "2026-11-27T09:00:00Z"
    const recent = "2026-11-28T08:00:00Z"

    expect(failureShown(source())).toBeNull()
    expect(failureShown(source({ group: "broken", last_error: failure(old) }))).toBe("now")
    expect(failureShown(source({ last_error: failure(old), last_success_at: recent }))).toBe(
      "before",
    )
    expect(failureShown(source({ last_error: failure(recent), last_success_at: old }))).toBe("now")
    // An error kept in the same millisecond as the last success came with or after it: it is not "before".
    expect(
      failureShown(
        source({ group: "broken", last_error: failure(recent), last_success_at: recent }),
      ),
    ).toBe("now")
    // Not read for a day, it is unverified: an error older than its last success is still from before it.
    expect(
      failureShown(
        source({ group: "unverified", last_error: failure(old), last_success_at: recent }),
      ),
    ).toBe("before")
    expect(
      failureShown(source({ group: "could_be_on", on: false, last_error: failure(old) })),
    ).toBe("now")
  })

  it("names a code and a backend's rest in words", () => {
    expect(codeLabel("rate_limited")).toBe("Rate limited")
    expect(codeLabel("something_new")).toBe("something new")

    const resting = backendState({
      name: "reddit_json",
      opt_in: false,
      state: "resting",
      resting_until: "2026-11-28T15:00:00Z",
      rest_code: "refused",
    })
    expect(resting.label).toMatch(/^Resting until /)
    expect(resting.why).toContain("Refused")
    expect(backendState({ name: "web_search", opt_in: false, state: "ready" }).label).toBe("Ready")
  })

  it("moves a backend one place", () => {
    expect(moved(["a", "b", "c"], "b", "up")).toEqual(["b", "a", "c"])
    expect(moved(["a", "b", "c"], "b", "down")).toEqual(["a", "c", "b"])
    expect(moved(["a", "b"], "a", "up")).toEqual(["a", "b"])
  })
})

describe("a signed-in source (issue #1291)", () => {
  const signed = (over: Partial<NonNullable<ListeningSource["signed"]>> = {}) =>
    source({ name: "x", label: "X", group: "could_be_on", on: false, signed: { consent: "Risks.", ...over } })
  const account = (state: "signed_in" | "need_login" | "refused", stopped_until?: string) => ({
    label: "secondary",
    state,
    cause: state === "signed_in" ? "" : "X answered 401",
    signed_in_at: "2026-11-27T09:00:00Z",
    stopped_until,
  })

  it("asks for its consent first, then a sign-in, then shows its account", () => {
    expect(signedStep(source())).toBeNull()
    expect(signedStep(signed())).toBe("consent")
    expect(signedStep(signed({ consented_at: "2026-11-28T08:00:00Z" }))).toBe("sign_in")
    expect(
      signedStep(signed({ consented_at: "2026-11-28T08:00:00Z", account: account("signed_in") })),
    ).toBe("account")
  })

  it(`wears the "!" when its account must be signed in again or was refused, while the web serves it`, () => {
    const working = { group: "working" as const, on: true }
    expect(needsYou({ ...signed({ account: account("need_login") }), ...working })).toBe(true)
    expect(needsYou({ ...signed({ account: account("refused") }), ...working })).toBe(true)
    expect(needsYou({ ...signed({ account: account("signed_in") }), ...working })).toBe(false)
  })

  it("says a stopped account's end, and a signed-in one's state", () => {
    expect(accountState(signed({ account: account("signed_in") }), now)?.label).toBe("Signed in")
    expect(accountState(signed({ account: account("refused") }), now)?.why).toContain("security check")
    const stopped = accountState(signed({ account: account("signed_in", "2026-11-29T09:00:00Z") }), now)
    expect(stopped?.tone).toBe("warning")
    expect(stopped?.label).toMatch(/^Stopped until /)
    expect(accountState(signed(), now)).toBeNull()
  })
})
