import { expect, test } from "vitest"

import { frontChat } from "./pane"
import { soloPaneOf } from "./solo-chat"

// Scenario: the chat being viewed on a chat's page is the chat in front of its pane (issue #923): the page's chat
// when nothing else was opened, a chat dragged into the pane once it is in front, and none while the phone is.
test("the chat being viewed is the chat in front of the page's pane", () => {
  expect(frontChat(soloPaneOf("cht_1", undefined))).toBe("cht_1")

  const joined = {
    tabs: [
      { kind: "chat" as const, chat: "cht_1" },
      { kind: "chat" as const, chat: "cht_old" },
    ],
    active: "chat:cht_old",
  }
  expect(frontChat(soloPaneOf("cht_1", joined))).toBe("cht_old")

  // A pane kept without the page's chat gets it back, and keeps what is in front.
  const without = { tabs: [{ kind: "chat" as const, chat: "cht_old" }], active: "chat:cht_old" }
  expect(
    soloPaneOf("cht_1", without).tabs.map((t) => (t.kind === "chat" ? t.chat : t.kind)),
  ).toEqual(["cht_1", "cht_old"])

  expect(frontChat({ tabs: [{ kind: "phone" }], active: "phone" })).toBeNull()
})
