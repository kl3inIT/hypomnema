import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, describe, expect, it, test, vi } from "vitest"

import type { ChatContextUsage } from "@/api/generated/types.gen"

import { ContextMeter } from "./context-meter"
import { formatTokens, meterLine, meterOf } from "./context-meter-state"

afterEach(() => vi.restoreAllMocks())

const used = (tokens: number, percent?: number): ChatContextUsage => ({
  context_window: 128000,
  tokens,
  ...(percent !== undefined && { percent }),
})

describe("the context meter's states (issue #384)", () => {
  it("shows nothing before pi reported a window", () => {
    expect(meterOf(undefined)).toBeNull()
    expect(meterOf({ context_window: 0 })).toBeNull()
  })

  it("is normal below 80 %, amber from 80 % and red from 90 %, by pi's share", () => {
    expect(meterOf(used(51200, 40))).toMatchObject({ percent: 40, tone: "normal" })
    expect(meterOf(used(102400, 79.6))).toMatchObject({ percent: 80, tone: "warning" })
    expect(meterOf(used(115200, 90))).toMatchObject({ percent: 90, tone: "critical" })
    // A share worked out when pi gave none.
    expect(meterOf(used(64000))).toMatchObject({ percent: 50 })
  })

  it("marks where pi compacts on its own: the window less 16384", () => {
    const m = meterOf(used(51200, 40))
    expect(m?.autoCompact).toBe(111616)
    expect(m?.tick).toBeCloseTo(87.2)
    expect(meterOf({ context_window: 8000, tokens: 10 })?.tick).toBeNull()
  })

  it("says – after a compaction, until pi's next answer", () => {
    const m = meterOf({ context_window: 128000 })
    expect(m).toMatchObject({ used: null, percent: null, tone: "normal" })
    expect(m && meterLine(m)).toBe("– / 128k")
  })

  it("writes used / window (percent)", () => {
    const m = meterOf(used(51200, 40))
    expect(m && meterLine(m)).toBe("51k / 128k (40%)")
    expect([950, 1500, 51200, 1_200_000].map(formatTokens)).toEqual(["950", "1.5k", "51k", "1.2m"])
  })
})

const client = new QueryClient()

const meter = (usage: ChatContextUsage | undefined, working = false) => (
  <QueryClientProvider client={client}>
    <ContextMeter chatId="cht_1" usage={usage} working={working} />
  </QueryClientProvider>
)

// Scenario: the owner reads the meter and compacts. The ring says how full the context is; its card names the
// numbers and Compact, which asks the server; while pi works Compact waits.
test("the meter's card shows the numbers and compacts", async () => {
  const asked: string[] = []
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const req = input as Request
    asked.push(`${req.method} ${new URL(req.url).pathname}`)

    return new Response(null, { status: 202 })
  })

  const user = userEvent.setup()
  const { rerender } = render(meter(used(115200, 90)))

  const ring = screen.getByRole("button", { name: "Context: 90% used" })
  expect(ring).toHaveAttribute("data-tone", "critical")
  await user.click(ring)
  expect(await screen.findByText("115k / 128k (90%)")).toBeInTheDocument()
  expect(screen.getByText("pi compacts on its own at about 111,616 tokens.")).toBeInTheDocument()

  await user.click(screen.getByRole("button", { name: "Compact" }))
  await vi.waitFor(() => expect(asked).toEqual(["POST /api/v1/chats/cht_1/compact"]))

  rerender(meter({ context_window: 128000 }, true))
  expect(screen.getByRole("button", { name: "Compact" })).toBeDisabled()
})

test("no ring before pi reported, and a dash after a compaction", () => {
  const { container, unmount } = render(meter(undefined))
  expect(container).toBeEmptyDOMElement()
  unmount()

  render(meter({ context_window: 128000 }))
  expect(
    screen.getByRole("button", { name: "Context: not measured since it was compacted" }),
  ).toHaveTextContent("–")
})
