import { expect, test } from "vitest"

import { botChoices, channelsOf, channelState, nameProblem } from "./channels-state"

test("a channel's badge says off, connected or not connected", () => {
  expect(channelState({ enabled: false, connected: false })).toEqual({ label: "Off", good: false })
  // A connection that is open stays until the control plane starts again; a channel that is off takes nothing.
  expect(channelState({ enabled: false, connected: true })).toEqual({ label: "Off", good: false })
  expect(channelState({ enabled: true, connected: true })).toEqual({
    label: "Connected",
    good: true,
  })
  expect(channelState({ enabled: true, connected: false })).toEqual({
    label: "Not connected",
    good: false,
  })
})

test("a new channel's name follows the server's rule and is not one taken", () => {
  expect(nameProblem("  ", [])).toBe("Give the channel a name.")
  expect(nameProblem("Maps", [])).toContain("lowercase")
  expect(nameProblem("2nd", [])).toContain("starting with a letter")
  expect(nameProblem("a".repeat(51), [])).toContain("at most 50")
  expect(nameProblem("main", ["main", "maps"])).toBe("A channel has this name already.")
  expect(nameProblem(" maps-2 ", ["main", "maps"])).toBeNull()
})

test("a Bot's channels are the ones that talk to it", () => {
  const channels = [
    { name: "main", bot: "coordinator" },
    { name: "maps", bot: "vmap" },
    { name: "maps-team", bot: "vmap" },
  ]

  expect(channelsOf("vmap", channels).map((c) => c.name)).toEqual(["maps", "maps-team"])
  expect(channelsOf("researcher", channels)).toEqual([])
})

test("a channel's own Bot is a choice while the Bots are still being read", () => {
  expect(botChoices([], "vmap")).toEqual(["vmap"])
  expect(botChoices(["coordinator", "vmap"], "vmap")).toEqual(["coordinator", "vmap"])
  expect(botChoices(["coordinator"])).toEqual(["coordinator"])
})
