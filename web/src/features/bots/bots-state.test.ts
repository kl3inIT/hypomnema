import { expect, test } from "vitest"

import { botNameOf } from "./bots-state"

// Scenario: issue #1139. The owner reads an Inbox item the Thư ký Bot added before it was removed: the thread names
// it by the name it had, "Thư ký (removed)"; a Bot removed before its name was kept is "A removed Bot"; a Bot still
// there by its name. Never by its id, except while the Bots are read.
test("a removed Bot is named, never shown by its id", () => {
  const bots = {
    data: [{ id: "bot_main", name: "Hypo" }],
    removed: [{ id: "bot_6lwjzodlaqoat2ktg47yztjfae", name: "Thư ký" }],
  }

  expect(botNameOf(bots, "bot_main")).toBe("Hypo")
  expect(botNameOf(bots, "bot_6lwjzodlaqoat2ktg47yztjfae")).toBe("Thư ký (removed)")
  expect(botNameOf(bots, "bot_unknown")).toBe("A removed Bot")
  expect(botNameOf({ data: bots.data }, "bot_unknown")).toBe("A removed Bot")
  expect(botNameOf(undefined, "bot_main")).toBe("bot_main")
  expect(botNameOf(bots, "")).toBe("")
})
