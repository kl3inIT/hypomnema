import { describe, expect, it } from "vitest"

import type { ImageProviderLink } from "@/api/generated/types.gen"

import { accountsText, imageState, madeText, modelPattern, modelText } from "./images-state"
import { until } from "./models-state"

const now = new Date("2026-11-14T09:00:00Z").getTime()
const none = { today: { answered: 0, failed: 0 }, month: { answered: 0, failed: 0 } }

const link = (more: Partial<ImageProviderLink>): ImageProviderLink => ({
  provider: "codex",
  on: true,
  state: "ready",
  model: "gpt-image-2",
  default_model: "gpt-image-2",
  model_chosen: false,
  suggested_models: ["gpt-image-2"],
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

    expect(imageState(link({ state: "not_set_up", accounts: 0 })).why).toContain("free plan")
    expect(imageState(link({ provider: "antigravity", state: "not_set_up" })).why).toBe(
      "No Antigravity account is signed in here and in use. Add one on the Models page.",
    )
  })

  it("says its model, whether the owner chose it, its accounts and what it made", () => {
    expect(modelText(link({}))).toBe("gpt-image-2 (default)")
    expect(modelText(link({ model: "gpt-image-2.6", model_chosen: true }))).toBe(
      "gpt-image-2.6 (yours)",
    )
    expect(accountsText(link({ accounts: 1 }))).toBe("1 account")
    expect(accountsText(link({}))).toBe("2 accounts")
    expect(
      madeText({ today: { answered: 3, failed: 0 }, month: { answered: 41, failed: 2 } }),
    ).toBe("Today 3 · this month 41 (+2 failed)")
  })

  it("takes a model's name as the server does", () => {
    expect(modelPattern.test("gemini-nano-banana-2.1")).toBe(true)
    expect(modelPattern.test("gpt-image-2.5-flare")).toBe(true)
    expect(modelPattern.test("no spaces")).toBe(false)
    expect(modelPattern.test("codex/gpt-image-2")).toBe(false)
  })
})
