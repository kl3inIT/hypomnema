import { describe, expect, it } from "vitest"

import type { ImageProviderLink } from "@/api/generated/types.gen"

import { accountsText, imageState, madeText } from "./images-state"
import { until } from "./models-state"

const now = new Date("2026-11-14T09:00:00Z").getTime()
const none = { today: { answered: 0, failed: 0 }, month: { answered: 0, failed: 0 } }

const link = (more: Partial<ImageProviderLink>): ImageProviderLink => ({
  provider: "codex",
  on: true,
  state: "ready",
  model: "gpt-image-2",
  model_name: "GPT Image 2 (Codex)",
  accounts: 2,
  usage: none,
  ...more,
})

describe("an image provider", () => {
  it("says its state, and why when pointed at", () => {
    expect(imageState(link({})).label).toBe("Ready")
    expect(imageState(link({ on: false, state: "off" })).label).toBe("Off")

    const rest = "2026-11-14T09:20:00Z"
    const resting = imageState(link({ state: "resting", resting_until: rest }), now)
    expect(resting.label).toBe(`Rate limited until ${until(rest, now)}`)
    expect(resting.tone).toBe("warning")
    // Why its account free first rests, when the server says (issue #1305).
    const used = imageState(
      link({ state: "resting", resting_until: rest, cooling_code: "usage_limit" }),
      now,
    )
    expect(used.label).toBe(`Usage limit, resets ${until(rest, now)}`)

    expect(imageState(link({ state: "not_set_up", accounts: 0 })).why).toContain("free plan")
    expect(imageState(link({ provider: "antigravity", state: "not_set_up" })).why).toBe(
      "No Antigravity account is signed in here and in use. Add one on the Models page.",
    )
  })

  it("says its accounts and what it made", () => {
    expect(accountsText(link({ accounts: 1 }))).toBe("1 account")
    expect(accountsText(link({}))).toBe("2 accounts")
    expect(
      madeText({ today: { answered: 3, failed: 0 }, month: { answered: 41, failed: 2 } }),
    ).toBe("Today 3 · this month 41 (+2 failed)")
  })
})
