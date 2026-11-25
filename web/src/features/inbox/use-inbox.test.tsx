import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, renderHook } from "@testing-library/react"
import type { ReactNode } from "react"
import { afterEach, expect, test, vi } from "vitest"

import type { Todo } from "@/api/generated/types.gen"

import { useTodoAct } from "./use-inbox"

afterEach(() => vi.restoreAllMocks())

// Scenario: issue #1142. Take it back gives an item the owner waits on a person for back to him: one PATCH with
// assignee me at the revision he read, which the server answers by clearing why_open waiting.
test("take it back gives the item to the owner at the revision read", async () => {
  const calls: { call: string; body: unknown }[] = []
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    if (request.method !== "GET") {
      calls.push({
        call: `${request.method} ${new URL(request.url).pathname.replace("/api/v1", "")}`,
        body: await request.json(),
      })
    }

    return new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } })
  })
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={new QueryClient()}>{children}</QueryClientProvider>
  )
  const { result } = renderHook(() => useTodoAct(), { wrapper })
  const todo = {
    id: "tdo_1",
    revision: 7,
    assignee: { kind: "person", name: "Việt" },
    why_open: "waiting",
  } as Todo

  await act(() => result.current.mutateAsync({ todo, change: { act: "take_back" } }))

  expect(calls).toEqual([
    { call: "PATCH /inbox/tdo_1", body: { revision: 7, assignee: { kind: "me" } } },
  ])
})
