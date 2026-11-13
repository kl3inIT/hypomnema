import { describe, expect, it } from "vitest"

import { duration, share, tokens } from "./usage-state"

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

describe("share and duration", () => {
  it("say a part and a time", () => {
    expect(share(1, 4)).toBe("25%")
    expect(share(1, 0)).toBe("")
    expect(duration(640)).toBe("640 ms")
    expect(duration(2400)).toBe("2.4 s")
    expect(duration(65_000)).toBe("1 min 5 s")
  })
})
