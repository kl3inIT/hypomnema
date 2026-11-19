import { describe, expect, it } from "vitest"

import type { GatewayKey } from "@/api/generated/types.gen"

import { connectKeys, toolNote } from "./connect-state"

const key = (id: string, name: string, stored: boolean): GatewayKey => ({
  id,
  name,
  prefix: "hyp_gateway_v1_abcdef",
  masked_value: stored ? "hyp_ga•••wxyz" : undefined,
  models: ["*"],
  endpoints: [],
  created_at: new Date().toISOString(),
})

describe("Connect a tool's keys", () => {
  it("offers the keys whose value is stored, by name, and the key it was opened for", () => {
    const keys = [
      key("gwk_1", "zed", true),
      key("gwk_2", "old", false),
      key("gwk_3", "alpha", true),
    ]

    expect(connectKeys(keys).map((k) => k.name)).toEqual(["alpha", "zed"])
    expect(connectKeys(keys, "gwk_2").map((k) => k.name)).toEqual(["alpha", "old", "zed"])
  })
})

describe("a tool's note", () => {
  it("says what the file turns on, and how omp names its model", () => {
    expect(toolNote("claude_code", "m")).toContain("CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY")
    expect(toolNote("codex", "m")).toContain("model_catalog_url")
    expect(toolNote("omp", "claude-subscription/claude-opus-5-5")).toBe(
      "Start omp with --model hypomnema/claude-subscription/claude-opus-5-5, or pick it in /model.",
    )
    expect(toolNote("omp", "")).toBe("")
    expect(toolNote("opencode", "m")).toBe("")
  })
})
