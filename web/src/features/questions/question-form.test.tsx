import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import type { Question } from "@/api/generated/types.gen"

import { question, task } from "@/features/work/fixtures"
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

// A Gate of two decisions, each option saying what it changes, after the worker's summary (issue #1219).
const twoDecisions = question("q2", task("t", "run_1"), {
  mode: "gate",
  summary:
    "Planned #1206.\nThree things were asked; two need you.\nThe rest is in the issue.\nA fourth line.",
  header: "Items",
  text: "Is each request one Inbox item?",
  options: [
    {
      label: "One item each (Recommended)",
      description: "Each request is its own item. From: Linear.",
    },
    { label: "One item for all", description: "No reference does this." },
    { label: "Other", allows_text: true },
  ],
  more: [
    {
      header: "Server",
      text: "Fix the server now?",
      options: [
        { label: "Now", description: "A small change on zm today." },
        { label: "Other", allows_text: true },
      ],
    },
  ],
})

// Scenario: the owner reads both decisions in one card, each with its header and each option with its description,
// under the worker's summary, which opens to the whole; he answers both, and one answer is sent for the two.
test("a gate of two decisions is read and answered as one card", async () => {
  const user = userEvent.setup()
  const fetch = answerServer()
  renderForm(twoDecisions)

  const items = screen.getByRole("group", { name: "Items" })
  const server = screen.getByRole("group", { name: "Server" })
  expect(items).toHaveTextContent("Is each request one Inbox item?")
  expect(within(items).getByRole("radio", { name: /One item each/ })).toHaveAccessibleName(
    /Each request is its own item\. From: Linear\./,
  )
  expect(within(server).getByRole("radio", { name: /Now/ })).toHaveAccessibleName(
    /A small change on zm today/,
  )

  const summary = screen.getByRole("region", { name: "The worker's summary" })
  expect(summary).toHaveTextContent("Planned #1206.")
  await user.click(within(summary).getByRole("button", { name: "Show all" }))
  expect(within(summary).getByRole("button", { name: "Show less" })).toHaveAttribute(
    "aria-expanded",
    "true",
  )

  await user.click(within(server).getByRole("radio", { name: /Other/ }))
  await user.type(within(server).getByRole("textbox", { name: "Details" }), "after the release")
  await user.click(screen.getByRole("button", { name: "Send answer" }))

  expect((await sent(fetch)).body).toEqual({
    option: 0,
    more: [{ option: 1, text: "after the release" }],
  })
})

// Scenario: a question asked before issue #1219, with bare labels and no header, summary or more, reads as it did:
// its text and its rows, no group, no description and no summary.
test("an old gate renders as before", () => {
  renderForm({ ...asked, mode: "gate" })

  expect(screen.getByText("Public or behind a login?")).toBeInTheDocument()
  expect(screen.getAllByRole("radio").map((r) => r.getAttribute("name"))).toEqual([
    "q-q1-0",
    "q-q1-0",
    "q-q1-0",
  ])
  expect(screen.getAllByRole("group")).toEqual([screen.getByRole("group", { name: "Options" })])
  expect(screen.queryByRole("region", { name: "The worker's summary" })).toBeNull()
})
