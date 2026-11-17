import { describe, expect, it } from "vitest"

import type { MuseAccount } from "@/api/generated/types.gen"

import { linkHost, museProblem, museState, parseMuseAnswer, usageLine } from "./muse-state"

const account = (over: Partial<MuseAccount> = {}): MuseAccount => ({
  id: "acc_1",
  label: "research",
  position: 1,
  on: true,
  state: "signed_in",
  resting: false,
  usage: "unknown",
  ...over,
})

describe("museState", () => {
  it("names each state with its why", () => {
    expect(museState(account()).label).toBe("Signed in")
    expect(museState(account({ on: false })).label).toBe("Off")
    expect(museState(account({ resting: true })).label).toBe("Out of quota")
    expect(museState(account({ state: "needs_sign_in" })).tone).toBe("destructive")
    expect(museState(account({ state: "refused" })).why).toContain("403")
    expect(museState(account({ state: "workspace_not_ready" })).label).toBe("Workspace not ready")
  })

  it("shows usage as Muse writes it, or unknown", () => {
    expect(usageLine(account())).toBe("Usage unknown")
    expect(usageLine(account({ usage: "62% used · resets Sep 30" }))).toBe(
      "62% used · resets Sep 30",
    )
  })
})

describe("parseMuseAnswer", () => {
  it("reads muse_ask's answer and nothing else", () => {
    const answer = parseMuseAnswer(
      JSON.stringify({
        outcome: "answered",
        answer: "x",
        account: "a",
        account_label: "r",
        at: "2026-11-18T09:00:00Z",
      }),
    )
    expect(answer?.answer).toBe("x")
    expect(parseMuseAnswer("no Muse account can take the question")).toBeNull()
    expect(parseMuseAnswer(JSON.stringify({ outcome: "maybe" }))).toBeNull()
    expect(parseMuseAnswer(undefined)).toBeNull()
  })

  it("links sign-in and quota errors to the Muse page", () => {
    expect(museProblem("research needs to be signed in again")).toBe(true)
    expect(museProblem("research is resting (out of quota, resets Sep 30)")).toBe(true)
    expect(museProblem("the prompt is empty")).toBe(false)
    expect(linkHost("https://www.example.com/a")).toBe("example.com")
  })
})
