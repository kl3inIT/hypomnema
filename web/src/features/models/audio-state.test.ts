import { describe, expect, it } from "vitest"

import type { AudioProviderLink } from "@/api/generated/types.gen"

import {
  audioState,
  audioUsageText,
  defaultVoice,
  heardText,
  modelChoices,
  voiceChoices,
} from "./audio-state"
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

describe("the model and voice pickers", () => {
  it("list the provider's models, keeping the one in use", () => {
    const models = [
      {
        provider: "elevenlabs" as const,
        id: "eleven_v4",
        name: "Eleven v4",
        languages: ["en", "vi"],
      },
      {
        provider: "elevenlabs" as const,
        id: "eleven_flash_v2_5",
        name: "Flash v2.5",
        languages: [],
      },
    ]

    expect(modelChoices(models, "elevenlabs", "eleven_v4")).toEqual([
      { value: "eleven_v4", label: "Eleven v4", detail: "2 languages" },
      { value: "eleven_flash_v2_5", label: "Flash v2.5", detail: undefined },
    ])
    expect(modelChoices([], "elevenlabs", "eleven_v3")).toEqual([
      { value: "eleven_v3", label: "eleven_v3" },
    ])
  })

  it("list the provider's voices by name after its own default, keeping one set by id", () => {
    const voices = [
      { provider: "elevenlabs" as const, id: "v2", name: "Sơn Trần", language: "vi" },
      { provider: "elevenlabs" as const, id: "v1", name: "Adam" },
      { provider: "soniox" as const, id: "Adrian", name: "Adrian" },
    ]

    expect(voiceChoices(voices, "elevenlabs", "v2").map((c) => c.label)).toEqual([
      "Its own default",
      "Adam",
      "Sơn Trần",
    ])
    expect(voiceChoices(voices, "elevenlabs", "alloy")[1]).toEqual({
      value: "alloy",
      label: "alloy",
    })
    expect(voiceChoices([], "soniox", "")).toEqual([
      { value: defaultVoice, label: "Its own default" },
    ])
  })
})
