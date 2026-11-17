import { describe, expect, it } from "vitest"

import { cost, duration, money, share, tiles, tokens, units, unitsOf } from "./usage-state"

describe("tokens", () => {
  it("says a count short without losing its zeros", () => {
    expect(tokens(950)).toBe("950")
    expect(tokens(750_000)).toBe("750k")
    expect(tokens(310_000)).toBe("310k")
    expect(tokens(12_300)).toBe("12.3k")
    expect(tokens(1_500_000)).toBe("1.5M")
    expect(tokens(18_400_000)).toBe("18.4M")
    expect(tokens(1_000_000)).toBe("1M")
  })
})

describe("money", () => {
  it("says micro-dollars as dollars, and a set partly unpriced", () => {
    expect(money(0)).toBe("$0")
    expect(money(94)).toBe("<$0.0001")
    expect(money(120)).toBe("$0.0001")
    expect(money(12_300)).toBe("$0.0123")
    expect(money(12_340_000)).toBe("$12.34")
    expect(money(1_234_000_000)).toBe("$1,234")
    const counts = {
      requests: 2,
      failed: 0,
      prompt_tokens: 1,
      cached_tokens: 0,
      completion_tokens: 1,
      reasoning_tokens: 0,
      web_searches: 0,
      audio_seconds: 0,
      characters: 0,
      images: 0,
    }
    expect(cost({ ...counts, cost_micros: 12_340_000, unpriced: 1 })).toBe("$12.34+")
    expect(cost({ ...counts, cost_micros: 0, unpriced: 2 })).toBe("No price")
  })
})

describe("share and duration", () => {
  it("say a part and a time", () => {
    expect(share(1, 4)).toBe("25%")
    expect(share(1, 0)).toBe("")
    expect(duration(640)).toBe("640 ms")
    expect(duration(2400)).toBe("2.4 s")
    expect(duration(65_000)).toBe("1 min 5 s")
  })
})

// Issue #723, decision 3.2: each kind in its own unit.
describe("units", () => {
  const row = {
    model: "web/search",
    outcome: "ok" as const,
  }

  it("says what a request used that is not tokens", () => {
    expect(units({ ...row, web_searches: 1 })).toBe("1 search")
    expect(units({ ...row, model: "web/fetch" })).toBe("1 page")
    expect(units({ ...row, model: "web/fetch", outcome: "upstream_error" })).toBe("")
    expect(units({ ...row, model: "audio/stt-async-v5", audio_seconds: 90 })).toBe(
      "1 min 30 s heard",
    )
    expect(units({ ...row, model: "audio/eleven_multilingual_v2", characters: 1200 })).toBe(
      "1,200 characters spoken",
    )
    expect(units({ ...row, model: "image/gpt-image-2", images: 2 })).toBe("2 images")
    expect(units({ ...row, model: "sol", web_searches: 3 })).toBe("+3 searches")
    expect(units({ ...row, model: "sol" })).toBe("")
    // A search that failed searched nothing.
    expect(units({ ...row, outcome: "upstream_error" })).toBe("")
  })

  it("adds a set's units up, and gives each a tile when the period used any", () => {
    const counts = {
      requests: 6,
      failed: 0,
      prompt_tokens: 10,
      cached_tokens: 0,
      completion_tokens: 5,
      reasoning_tokens: 0,
      cost_micros: 106_500,
      unpriced: 0,
      web_searches: 4,
      audio_seconds: 45.5,
      characters: 1200,
      images: 1,
    }

    expect(unitsOf(counts)).toBe("4 searches, 45.5 s heard, 1,200 characters spoken, 1 image")
    expect(tiles(counts).map((t) => `${t.label}: ${t.value}`)).toEqual([
      "Requests: 6",
      "Failed: 0",
      "Tokens in: 10",
      "From cache: 0",
      "Tokens out: 5",
      "Reasoning: 0",
      "Web searches: 4",
      "Audio heard: 45.5 s",
      "Characters spoken: 1.2k",
      "Images: 1",
      "API cost: $0.1065",
    ])

    const quiet = { ...counts, web_searches: 0, audio_seconds: 0, characters: 0, images: 0 }
    expect(unitsOf(quiet)).toBe("")
    expect(tiles(quiet)).toHaveLength(7)
  })
})
