import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import type { Question } from "@/api/generated/types.gen"

import { question, task } from "./fixtures"
import { QuestionForm } from "./question-form"

afterEach(() => vi.restoreAllMocks())

const asked = question("q1", task("t", "run_1"))

// The server: an answer is taken and the question comes back answered.
function answerServer() {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const answer: unknown = await (input as Request).clone().json()

    return new Response(JSON.stringify({ ...asked, status: "answered", answer }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })
  })
}

function renderForm(q: Question = asked) {
  const onAnswered = vi.fn<() => void>()
  render(
    <QueryClientProvider client={new QueryClient()}>
      <QuestionForm question={q} onAnswered={onAnswered} />
    </QueryClientProvider>,
  )

  return onAnswered
}

async function sent(fetch: ReturnType<typeof answerServer>) {
  await vi.waitFor(() => expect(fetch).toHaveBeenCalled())
  const request = fetch.mock.calls[0]?.[0] as Request

  return { path: new URL(request.url).pathname, body: (await request.clone().json()) as unknown }
}

// Scenario: on an option, a number key picks one and Enter sends it, as in t3code's panel.
test("a number key picks an option and Enter sends it", async () => {
  const user = userEvent.setup()
  const fetch = answerServer()
  const onAnswered = renderForm()

  await user.click(screen.getByRole("radio", { name: /Public/ }))
  await user.keyboard("2")
  expect(screen.getByRole("radio", { name: /Behind a login/ })).toBeChecked()
  await user.keyboard("{Enter}")

  expect(await sent(fetch)).toEqual({ path: "/api/v1/questions/q1/answer", body: { option: 1 } })
  await vi.waitFor(() => expect(onAnswered).toHaveBeenCalled())
})

// Scenario: details go with an option that takes text, and only then.
test("text goes with an option that takes it", async () => {
  const user = userEvent.setup()
  const fetch = answerServer()
  renderForm()

  expect(screen.queryByRole("textbox")).toBeNull()
  await user.click(screen.getByRole("radio", { name: /Other/ }))
  await user.type(screen.getByRole("textbox", { name: "Details" }), "only on the tailnet")
  await user.click(screen.getByRole("button", { name: "Send answer" }))

  expect((await sent(fetch)).body).toEqual({ option: 2, text: "only on the tailnet" })
})

// Scenario: a question without options takes text, and cannot be sent empty.
test("a question without options takes text", async () => {
  const user = userEvent.setup()
  const fetch = answerServer()
  renderForm({ ...asked, options: [] })

  expect(screen.getByRole("button", { name: "Send answer" })).toBeDisabled()
  await user.type(screen.getByRole("textbox", { name: "Answer" }), "use port 18990{Enter}")

  expect((await sent(fetch)).body).toEqual({ text: "use port 18990" })
})
