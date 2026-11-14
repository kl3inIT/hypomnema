import { describe, expect, it } from "vitest"

import type { WebSearchKey, WebSearchLink } from "@/api/generated/types.gen"

import { until } from "./models-state"
import {
  balanceShare,
  balanceText,
  callsText,
  keyState,
  linkState,
  moved,
  sourceText,
  spendText,
} from "./web-search-state"

const now = new Date("2026-11-14T09:00:00Z").getTime()
const none = { today: { answered: 0, failed: 0 }, month: { answered: 0, failed: 0 } }

const link = (more: Partial<WebSearchLink>): WebSearchLink => ({
  provider: "parallel",
  source: "public",
  on: true,
  state: "ready",
  usage: none,
  ...more,
})

const key = (more: Partial<WebSearchKey>): WebSearchKey => ({
  name: "web-search-tavily",
  state: "ready",
  usage: none,
  ...more,
})

describe("a way of searching", () => {
  it("says where its search comes from", () => {
    expect(sourceText(link({ provider: "exa", source: "key" }))).toBe("With your API keys")
    expect(sourceText(link({}))).toBe("Public, no key")
    expect(sourceText(link({ provider: "codex", source: "subscription" }))).toBe(
      "From your Codex account",
    )
  })

  it("says what it lacks when it cannot be asked", () => {
    const lacks = (more: Partial<WebSearchLink>) =>
      linkState(link({ state: "not_set_up", ...more }), now).label

    expect(lacks({ provider: "tavily", source: "key" })).toBe("No key")
    expect(lacks({ provider: "claude", source: "subscription" })).toBe("No account")
  })

  it("says why it rests and until when, and is off when switched off", () => {
    const rest = "2026-11-14T09:02:00Z"

    expect(
      linkState(link({ state: "resting", rest: "rate_limit", resting_until: rest }), now),
    ).toMatchObject({ label: `Rate limited until ${until(rest, now)}`, tone: "warning" })
    expect(
      linkState(link({ state: "resting", rest: "daily_limit", resting_until: rest }), now).label,
    ).toBe(`Daily limit until ${until(rest, now)}`)
    expect(linkState(link({ state: "off", on: false }), now)).toMatchObject({
      label: "Off",
      tone: "outline",
    })
    expect(linkState(link({}), now)).toMatchObject({ label: "Ready", tone: "success" })
  })
})

describe("a key", () => {
  it("rests out of credits or refused, and is spent when its balance is used up", () => {
    const rest = "2026-11-14T09:05:00Z"

    expect(
      keyState(key({ state: "resting", rest: "no_credits", resting_until: rest }), now),
    ).toMatchObject({
      label: `Out of credits until ${until(rest, now)}`,
      tone: "warning",
    })
    expect(
      keyState(key({ state: "resting", rest: "refused", resting_until: rest }), now).tone,
    ).toBe("destructive")
    expect(keyState(key({ state: "spent" }), now).label).toBe("Spent")
  })

  it("shows its provider's balance as a share, and nothing invented without one", () => {
    const tavily = key({ balance: { used: 250, limit: 1000, remaining: 750, checked_at: "" } })

    expect(balanceShare(tavily)).toBe(25)
    expect(balanceText(tavily)).toBe("750 of 1,000 credits left")
    expect(balanceShare(key({}))).toBeUndefined()
    expect(balanceText(key({}))).toBeUndefined()
  })

  it("counts this control plane's calls and what Exa said they cost", () => {
    const usage = {
      today: { answered: 3, failed: 0 },
      month: { answered: 41, failed: 2 },
      month_dollars: 0.287,
    }

    expect(callsText(usage)).toBe("Today 3 · this month 41 (+2 failed)")
    expect(spendText(usage)).toBe("$0.287 this month")
    expect(spendText(none)).toBeUndefined()
  })
})

describe("the keyed providers' order", () => {
  const order = ["exa", "firecrawl", "parallel", "tavily", "tinyfish"]

  it("puts a dropped provider in the place of the one it was dropped on", () => {
    expect(moved(order, "tavily", "exa")).toEqual([
      "tavily",
      "exa",
      "firecrawl",
      "parallel",
      "tinyfish",
    ])
    expect(moved(order, "exa", "parallel")).toEqual([
      "firecrawl",
      "parallel",
      "exa",
      "tavily",
      "tinyfish",
    ])
    expect(moved(order, "exa", "exa")).toBe(order)
  })
})
