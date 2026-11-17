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
  const open = vi.fn<(tab: string) => void>()
  const { rerender } = renderHook(
    ({ tabs }: { tabs?: BrowserTab[] }) => useFollowTabs(tabs, ["c1"], open),
    { initialProps: { tabs: undefined as BrowserTab[] | undefined } },
  )

  // The tabs open when the pane is first seen are left as they are.
  rerender({ tabs: [owner] })
  expect(open).not.toHaveBeenCalled()

  // The chat's Bot opens a tab: it opens beside the chat, once.
  rerender({ tabs: [owner, mine] })
  expect(open).toHaveBeenCalledExactlyOnceWith("b-c1")

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
  expect(open).toHaveBeenLastCalledWith("b-c1")
})

test("the tabs already open when the pane is first seen are not opened", () => {
  const open = vi.fn<(tab: string) => void>()
  renderHook(() => useFollowTabs([owner, mine], ["c1"], open))

  expect(open).not.toHaveBeenCalled()
})
