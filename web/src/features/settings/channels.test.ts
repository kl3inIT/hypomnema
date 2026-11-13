import { expect, test } from "vitest"

import {
  appNameOf,
  botChoices,
  channelsOf,
  channelState,
  groupAsked,
  groupName,
  nameProblem,
} from "./channels-state"

test("a group is shown by its name, else its chat id, and says when it was last asked", () => {
  expect(groupName({ name: "Maps team", chat_id: "oc_1" })).toBe("Maps team")
  expect(groupName({ name: " ", chat_id: "oc_1" })).toBe("oc_1")

  const now = Date.parse("2026-11-12T12:00:00Z")
  expect(groupAsked({}, now)).toBe("not asked yet")
  expect(groupAsked({ asked_at: "2026-11-12T09:00:00Z" }, now)).toMatch(
    /^asked 3\s?h(r|ours?)? ago$/,
  )
})

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

test("a Channel is named by the app of the Lark Connection that has its app id", () => {
  const services = [
    { id: "google", connections: [{ account: "me@example.com" }] },
    {
      id: "lark",
      connections: [
        { app_id: "cli_work", account: "Work app" },
        { app_id: "cli_report", account: "Report app" },
      ],
    },
  ]

  expect(appNameOf({ app_id: "cli_report" }, services)).toBe("Report app")
  expect(appNameOf({ app_id: "cli_other" }, services)).toBe("")
  expect(appNameOf({ app_id: "cli_report" }, undefined)).toBe("")
})
