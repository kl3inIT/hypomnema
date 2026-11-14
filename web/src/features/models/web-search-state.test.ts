import { describe, expect, it } from "vitest"

import type { WebSearchLink } from "@/api/generated/types.gen"

import { until } from "./models-state"
import { linkState, shareSwitch, sourceText } from "./web-search-state"

const now = new Date("2026-11-14T09:00:00Z").getTime()

const link = (more: Partial<WebSearchLink>): WebSearchLink => ({
  provider: "parallel",
  source: "public",
  on: true,
  state: "ready",
  ...more,
})

describe("a way of searching", () => {
  it("says where its search comes from", () => {
    expect(sourceText(link({ provider: "exa", source: "key" }))).toBe("With your API key")
    expect(sourceText(link({ provider: "searxng", source: "self_hosted" }))).toBe("Self-hosted")
    expect(sourceText(link({}))).toBe("Public, no key")
    expect(sourceText(link({ provider: "codex", source: "subscription" }))).toBe(
      "From your Codex account",
    )
  })

  it("says what it lacks when it cannot be asked", () => {
    const lacks = (more: Partial<WebSearchLink>) =>
      linkState(link({ state: "not_set_up", ...more }), now).label

    expect(lacks({ provider: "exa", source: "key" })).toBe("Needs a key")
    expect(lacks({ provider: "searxng", source: "self_hosted" })).toBe("No address")
    expect(lacks({ provider: "claude", source: "subscription" })).toBe("No account")
  })

  it("rests until the time its 429 named, and is off when switched off", () => {
    const rest = "2026-11-14T09:02:00Z"

    expect(linkState(link({ state: "resting", resting_until: rest }), now)).toMatchObject({
      label: `Resting until ${until(rest, now)}`,
      tone: "warning",
    })
    expect(linkState(link({ state: "off", on: false }), now)).toMatchObject({
      label: "Off",
      tone: "outline",
    })
    expect(linkState(link({}), now)).toMatchObject({ label: "Ready", tone: "success" })
  })

  it("shares its switch with Exa's other way only", () => {
    const keyed = link({ provider: "exa", source: "key" })
    const parallel = link({})
    const chain = [keyed, link({ provider: "exa" }), parallel]

    expect(shareSwitch(chain, keyed)).toBe(true)
    expect(shareSwitch(chain, parallel)).toBe(false)
  })
})
