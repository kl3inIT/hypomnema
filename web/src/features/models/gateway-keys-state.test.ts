import { describe, expect, it } from "vitest"

import type { GatewayKey } from "@/api/generated/types.gen"

import {
  connectSnippet,
  routingBadges,
  routingBody,
  routingOf,
  routingSummary,
} from "./gateway-keys-state"

const key = (more: Partial<GatewayKey> = {}): GatewayKey => ({
  id: "gwk_1",
  name: "beyondpilot",
  prefix: "hyp_gateway_v1_abcdef",
  models: ["*"],
  endpoints: [],
  search_order: [],
  created_at: new Date().toISOString(),
  ...more,
})

// Scenario (issue #1277): a key's own routing, as its form holds it, its Routing header says it and the keys table
// marks it; a key that sets none follows Settings and is not marked.
describe("a key's own routing", () => {
  it("follows Settings when nothing is set", () => {
    const routing = routingOf(key())
    expect(routing).toEqual({ accounts: "every", chosen: [], order: "settings", providers: [] })
    expect(routingSummary(routing)).toBe("like Settings")
    expect(routingBadges(key())).toEqual([])
    expect(routingOf()).toEqual(routing)
  })

  it("says the accounts it is limited to and its own order", () => {
    const limited = key({
      accounts: [
        { id: "acc_1", runtime: "claude" },
        { id: "acc_2", runtime: "claude" },
        { id: "acc_3", runtime: "codex" },
      ],
      search_order: ["codex", "claude", "exa"],
    })
    const routing = routingOf(limited)
    expect(routing).toEqual({
      accounts: "some",
      chosen: ["acc_1", "acc_2", "acc_3"],
      order: "own",
      providers: ["codex", "claude", "exa"],
    })
    expect(routingSummary(routing)).toBe("3 accounts · own search order")
    expect(routingSummary({ ...routing, chosen: ["acc_1"], order: "settings" })).toBe("1 account")
    expect(routingBadges(limited)).toEqual([
      "2 Claude accounts",
      "1 Codex account",
      "Own search order",
    ])
    expect(routingBadges(key({ accounts: [] }))).toEqual(["No account"])
  })

  it("sends what a new key sets, and both on an edit", () => {
    const routing = routingOf(key())
    expect(routingBody(routing, false)).toEqual({})
    expect(routingBody(routing, true)).toEqual({ accounts: ["*"], search_order: [] })

    const own = {
      accounts: "some",
      chosen: ["acc_1"],
      order: "own",
      providers: ["claude"],
    } as const
    expect(
      routingBody({ ...own, chosen: [...own.chosen], providers: [...own.providers] }, false),
    ).toEqual({
      accounts: ["acc_1"],
      search_order: ["claude"],
    })
  })
})

it("gives a tool the base URL and the key", () => {
  const snippet = connectSnippet("https://hyp.example", "hyp_gateway_v1_x")
  expect(snippet).toContain(
    "OPENAI_BASE_URL=https://hyp.example/v1\nOPENAI_API_KEY=hyp_gateway_v1_x",
  )
  expect(snippet).toContain(
    "ANTHROPIC_BASE_URL=https://hyp.example\nANTHROPIC_AUTH_TOKEN=hyp_gateway_v1_x",
  )
})
