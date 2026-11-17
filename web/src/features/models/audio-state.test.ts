import { describe, expect, it } from "vitest"

import type { AudioProviderLink } from "@/api/generated/types.gen"

import { audioState, audioUsageText, heardText } from "./audio-state"
import { until } from "./models-state"

const now = new Date("2026-11-17T09:00:00Z").getTime()

const link = (more: Partial<AudioProviderLink>): AudioProviderLink => ({
  provider: "soniox",
  on: true,
  state: "ready",
  keys: [{ name: "speech-soniox", state: "ready" }],
  ...more,
})

describe("an audio provider", () => {
  it("says its state, and why when pointed at", () => {
    expect(audioState(link({})).label).toBe("Ready")
    expect(audioState(link({ on: false, state: "off" })).label).toBe("Off")
    expect(audioState(link({ state: "not_set_up", keys: [] })).why).toBe(
      "Soniox is asked once one of your keys is in the vault.",
    )

    const rest = "2026-11-17T09:20:00Z"
    const refused = audioState(
      link({ state: "resting", rest: "refused", resting_until: rest }),
      now,
    )
    expect(refused.label).toBe(`Refused until ${until(rest, now)}`)
    expect(refused.tone).toBe("destructive")
  })

  it("says what each kind heard or spoke", () => {
    const usage = {
      today: { answered: 2, failed: 0, seconds: 90, characters: 0 },
      month: { answered: 7, failed: 1, seconds: 5400, characters: 1200 },
    }

    expect(audioUsageText("stt", usage)).toBe("Today 2 · this month 7 (+1 failed) · 1.5 h heard")
    expect(audioUsageText("tts", usage)).toBe(
      "Today 2 · this month 7 (+1 failed) · 1,200 characters spoken",
    )
    expect(heardText(42)).toBe("42 s")
    expect(heardText(600)).toBe("10 min")
  })
})
