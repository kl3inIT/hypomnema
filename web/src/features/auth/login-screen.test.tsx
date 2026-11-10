import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, test, vi } from "vitest"

import { LoginScreen } from "./login-screen"
import { readToken, saveToken } from "./token"

const me = (kind: string, status = 200) =>
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(
      JSON.stringify(status === 200 ? { id: "tok_1", name: "web", kind } : { error: "no" }),
      {
        status,
        headers: { "Content-Type": "application/json" },
      },
    ),
  )

beforeEach(() => saveToken(null))
afterEach(() => vi.restoreAllMocks())

// Scenario: the owner pastes an owner token; it is checked with the server and kept.
test("an owner token is checked and kept", async () => {
  const user = userEvent.setup()
  const fetch = me("owner")
  render(<LoginScreen />)

  await user.type(screen.getByLabelText("Owner token"), "secret-owner")
  await user.click(screen.getByRole("button", { name: "Sign in" }))

  expect(readToken()).toBe("secret-owner")
  const request = fetch.mock.calls[0]?.[0] as Request
  expect(request.headers.get("Authorization")).toBe("Bearer secret-owner")
})

// Scenario: a coordinator token, such as pi's, cannot open the owner's chats.
test("a coordinator token is refused", async () => {
  const user = userEvent.setup()
  me("coordinator")
  render(<LoginScreen />)

  await user.type(screen.getByLabelText("Owner token"), "secret-pi")
  await user.click(screen.getByRole("button", { name: "Sign in" }))

  expect(await screen.findByRole("alert")).toHaveTextContent("coordinator token")
  expect(readToken()).toBeNull()
})

test("a token the server rejects is not kept", async () => {
  const user = userEvent.setup()
  me("owner", 401)
  render(<LoginScreen />)

  await user.type(screen.getByLabelText("Owner token"), "wrong")
  await user.click(screen.getByRole("button", { name: "Sign in" }))

  expect(await screen.findByRole("alert")).toHaveTextContent("did not accept")
  expect(readToken()).toBeNull()
})
