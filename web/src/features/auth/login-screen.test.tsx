import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, test, vi } from "vitest"

import { LoginScreen } from "./login-screen"
import { resetSession, useSession } from "./session"

function State() {
  return <output>{useSession()}</output>
}

const answer = (status: number, body?: object) =>
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(body ? JSON.stringify(body) : null, {
      status,
      headers: body ? { "Content-Type": "application/json" } : {},
    }),
  )

async function fill(user: ReturnType<typeof userEvent.setup>, name: string, password: string) {
  await user.type(screen.getByLabelText("Username"), name)
  await user.type(screen.getByLabelText("Password"), password)
  await user.click(screen.getByRole("button", { name: "Sign in" }))
}

beforeEach(() => resetSession())
afterEach(() => vi.restoreAllMocks())

// Scenario: the owner signs in with the name and the password. They go to the server once, in the body; the page
// keeps neither, and nothing is put in storage: the session is the server's cookie.
test("a name and a password sign in", async () => {
  const user = userEvent.setup()
  const fetch = answer(204)
  render(
    <>
      <LoginScreen />
      <State />
    </>,
  )

  await fill(user, "admin", "correct horse")

  expect(await screen.findByText("signed-in")).toBeInTheDocument()

  const request = fetch.mock.calls[0]?.[0] as Request
  expect(request.method).toBe("POST")
  expect(new URL(request.url).pathname).toBe("/api/v1/session")
  expect(await request.json()).toEqual({ username: "admin", password: "correct horse" })
  expect(request.headers.get("Authorization")).toBeNull()
  expect(window.localStorage.length).toBe(0)
})

// Scenario: a wrong password. The page says so without saying which of the two was wrong, and stays signed out.
test("a wrong password is refused", async () => {
  const user = userEvent.setup()
  answer(401, { code: "unauthorized", message: "the name or the password is wrong" })
  render(
    <>
      <LoginScreen />
      <State />
    </>,
  )

  await fill(user, "admin", "nope")

  expect(await screen.findByRole("alert")).toHaveTextContent("The name or the password is wrong.")
  expect(screen.getByText("signed-out")).toBeInTheDocument()
})

// Scenario: too many wrong tries. The page passes on how long the server says to wait.
test("a locked address is told how long to wait", async () => {
  const user = userEvent.setup()
  answer(429, { code: "rate_limited", message: "too many wrong tries; try again in 30s" })
  render(<LoginScreen />)

  await fill(user, "admin", "nope")

  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Too many wrong tries; try again in 30s.",
  )
})
