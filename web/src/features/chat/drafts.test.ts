import { afterEach, beforeEach, expect, test, vi } from "vitest"

// The drafts are held in memory once read, as a page holds them while it lives; each test loads the module anew, as
// a page that opens or reloads does.
const load = async () => {
  vi.resetModules()

  return import("./drafts")
}

const report = { name: "report.pdf", path: "attachments/cht_1/report.pdf", size: 15 }

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

// Scenario: the owner writes in a chat and attaches a file, then reloads the page. The text and the file are back.
test("a draft is kept across a reload", async () => {
  const before = await load()
  before.writeDraft(before.chatDraft("cht_1"), { text: "add a health page" })
  before.writeDraft(before.chatDraft("cht_1"), { files: [report] })
  before.saveDrafts()

  const after = await load()
  expect(after.readDraft(after.chatDraft("cht_1"))).toEqual({
    text: "add a health page",
    files: [report],
  })
  expect(after.readDraft(after.chatDraft("cht_2"))).toEqual({ text: "", files: [] })
})

// Scenario: the owner writes on the New chat page; a moment later the draft is in the browser with the chat's id
// and the Bot picked.
test("a draft is saved a moment after it is written", async () => {
  vi.useFakeTimers()
  const drafts = await load()
  drafts.writeDraft(drafts.newChatDraft, { text: "a new page", chat: "cht_new", bot: "builder" })
  expect(localStorage.getItem(drafts.draftsKey)).toBeNull()

  vi.advanceTimersByTime(300)
  expect(JSON.parse(localStorage.getItem(drafts.draftsKey) ?? "{}")).toEqual({
    new: { text: "a new page", files: [], chat: "cht_new", bot: "builder" },
  })
})

// Scenario: the owner sends the message. Its draft goes, and the page loaded again has none.
test("a draft sent goes", async () => {
  const before = await load()
  before.writeDraft(before.chatDraft("cht_1"), { text: "deploy it", files: [report] })
  before.clearDraft(before.chatDraft("cht_1"))
  expect(before.readDraft(before.chatDraft("cht_1"))).toEqual({ text: "", files: [] })

  const after = await load()
  expect(after.readDraft(after.chatDraft("cht_1"))).toEqual({ text: "", files: [] })
  expect(localStorage.getItem(after.draftsKey)).toBe("{}")
})

// Scenario: two tabs write drafts of different chats. A tab's save keeps the other's.
test("a save keeps the drafts another tab wrote", async () => {
  const one = await load()
  one.readDraft(one.chatDraft("cht_1"))
  localStorage.setItem(
    one.draftsKey,
    JSON.stringify({ "chat:cht_2": { text: "other tab", files: [] } }),
  )
  one.writeDraft(one.chatDraft("cht_1"), { text: "this tab" })
  one.saveDrafts()

  expect(JSON.parse(localStorage.getItem(one.draftsKey) ?? "{}")).toEqual({
    "chat:cht_1": { text: "this tab", files: [] },
    "chat:cht_2": { text: "other tab", files: [] },
  })
})

// Scenario: the browser refuses the storage (private browsing, a full quota). The draft is kept while the page
// lives, and a write refused is saved with the next save the storage takes.
test("a storage that throws leaves the draft in memory", async () => {
  const drafts = await load()
  const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new DOMException("full", "QuotaExceededError")
  })
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
    throw new DOMException("denied", "SecurityError")
  })

  drafts.writeDraft(drafts.chatDraft("cht_1"), { text: "still here" })
  expect(() => drafts.saveDrafts()).not.toThrow()
  expect(setItem).toHaveBeenCalled()
  expect(drafts.readDraft(drafts.chatDraft("cht_1")).text).toBe("still here")
  expect(drafts.readDraft(drafts.chatDraft("cht_3"))).toEqual({ text: "", files: [] })

  vi.restoreAllMocks()
  drafts.saveDrafts()
  expect(JSON.parse(localStorage.getItem(drafts.draftsKey) ?? "{}")).toEqual({
    "chat:cht_1": { text: "still here", files: [] },
  })
})

// Scenario: what the browser kept is not a draft (hand edited, or of another shape). It is read as none.
test("a draft that is not one is read as empty", async () => {
  localStorage.setItem(
    "hypomnema:chat-drafts:v1",
    JSON.stringify({
      "chat:cht_1": { text: 3 },
      "chat:cht_2": { text: "ok", files: [{ name: 1 }] },
    }),
  )
  const drafts = await load()

  expect(drafts.readDraft(drafts.chatDraft("cht_1"))).toEqual({ text: "", files: [] })
  expect(drafts.readDraft(drafts.chatDraft("cht_2"))).toEqual({ text: "ok", files: [] })
})
