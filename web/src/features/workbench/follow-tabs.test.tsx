import { renderHook } from "@testing-library/react"
import { expect, test, vi } from "vitest"

import type { BrowserTab } from "@/api/generated/types.gen"

import { useFollowTabs } from "./follow-tabs"

const tab = (id: string, chat?: string): BrowserTab => ({
  id,
  kind: chat ? "coordinator" : "owner",
  ...(chat && { chat_id: chat }),
  url: "https://example.com/",
  title: "Example",
  views: 0,
})

const owner = tab("owner")
const mine = tab("b-c1", "c1")
const theirs = tab("b-c2", "c2")

test("a new tab of the pane's chat opens once, and again only when it is new again", () => {
  const open = vi.fn<(tabs: string[]) => void>()
  const { rerender } = renderHook(
    ({ tabs }: { tabs?: BrowserTab[] }) => useFollowTabs(tabs, ["c1"], open),
    { initialProps: { tabs: undefined as BrowserTab[] | undefined } },
  )

  // The tabs open when the pane is first seen are left as they are.
  rerender({ tabs: [owner] })
  expect(open).not.toHaveBeenCalled()

  // The chat's Bot opens a tab: it opens beside the chat, once.
  rerender({ tabs: [owner, mine] })
  expect(open).toHaveBeenCalledExactlyOnceWith(["b-c1"])

  // The owner closed it in the pane; the browser still has it, and the next lists do not open it again.
  rerender({ tabs: [owner, { ...mine, title: "Signed in" }] })
  rerender({ tabs: [owner, mine] })
  expect(open).toHaveBeenCalledTimes(1)

  // Another chat's new tab is not this pane's.
  rerender({ tabs: [owner, mine, theirs] })
  expect(open).toHaveBeenCalledTimes(1)

  // The tab was closed by its agent, and a new one opened: it opens again.
  rerender({ tabs: [owner, theirs] })
  rerender({ tabs: [owner, theirs, mine] })
  expect(open).toHaveBeenCalledTimes(2)
  expect(open).toHaveBeenLastCalledWith(["b-c1"])
})

test("the tabs already open when the pane is first seen are not opened", () => {
  const open = vi.fn<(tabs: string[]) => void>()
  renderHook(() => useFollowTabs([owner, mine], ["c1"], open))

  expect(open).not.toHaveBeenCalled()
})

test("each of several new tabs of the pane's chats opens once, none dropped", () => {
  const opened: string[] = []
  const open = (tabs: string[]) => {
    opened.push(...tabs)
  }
  const worker = tab("d-1", "c1")
  const { rerender } = renderHook(
    ({ tabs }: { tabs?: BrowserTab[] }) => useFollowTabs(tabs, ["c1", "c2"], open),
    { initialProps: { tabs: [owner] as BrowserTab[] | undefined } },
  )

  // Both chats' Bots open a tab between two reads of the list: both open.
  rerender({ tabs: [owner, mine, theirs] })
  expect(opened).toEqual(["b-c1", "b-c2"])

  // The next read opens neither again.
  rerender({ tabs: [owner, mine, theirs] })
  expect(opened).toEqual(["b-c1", "b-c2"])

  // A worker of the first chat opens a tab as its Bot's is read again: it opens too.
  rerender({ tabs: [owner, mine, theirs, worker] })
  expect(opened).toEqual(["b-c1", "b-c2", "d-1"])
})
