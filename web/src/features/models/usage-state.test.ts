import { describe, expect, it } from "vitest"

import { cost, duration, money, share, tokens } from "./usage-state"

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
