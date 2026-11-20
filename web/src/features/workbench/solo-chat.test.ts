import { expect, test } from "vitest"

import { frontChat } from "./pane"
import { showSoloChat, soloPaneOf } from "./solo-chat"
import { latestPane, setPane, soloId } from "./use-grid"

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

// Scenario: Back to main chat opens the main chat's page, whose pane kept a new chat's draft in front (issue #923):
// the main chat comes in front there first, with the draft kept; a page with no pane kept is left to start afresh.
test("the main chat is brought in front of its own page before the page opens", () => {
  setPane(soloId("cht_main"), {
    tabs: [
      { kind: "chat", chat: "cht_main" },
      { kind: "draft", id: "d1", bot: "writer" },
    ],
    active: "draft:d1",
  })
  showSoloChat("cht_main")
  expect(latestPane(soloId("cht_main"))).toEqual({
    tabs: [
      { kind: "chat", chat: "cht_main" },
      { kind: "draft", id: "d1", bot: "writer" },
    ],
    active: "chat:cht_main",
  })

  showSoloChat("cht_none")
  expect(latestPane(soloId("cht_none"))).toBeUndefined()
  setPane(soloId("cht_main"), null)
})
