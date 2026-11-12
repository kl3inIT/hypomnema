import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import type { Approval } from "@/api/generated/types.gen"

import { PendingApprovals } from "./approval-card"
import { readable } from "./use-approvals"

afterEach(() => vi.restoreAllMocks())

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function mail(over: Partial<Approval> = {}): Approval {
  return {
    id: "apr_1",
    integration: "google",
    label: "work",
    action: "gmail.send",
    action_name: "Send mail",
    asked_by: "pi",
    chat_id: "cht_1",
    title: "Send mail: minh@example.com",
    payload: [
      { label: "To", value: "minh@example.com" },
      { label: "Subject", value: "Re: October invoice" },
      { label: "Body", value: "Received.\nWe pay before the 15th." },
    ],
    status: "pending",
    used: false,
    created_at: "2026-11-11T02:00:00Z",
    ...over,
  }
}

// The server: the Approvals that wait, and the answers it was given. An answered Approval no longer waits; a
// refused answer leaves it waiting.
function server(waiting: Approval[], refuse?: string) {
  const requests: { call: string; query: string; body: unknown }[] = []

  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    const url = new URL(request.url)
    const call = `${request.method} ${url.pathname.replace("/api/v1", "")}`

    if (call === "GET /approvals") {
      requests.push({ call, query: url.search, body: undefined })

      return json({ data: waiting })
    }

    requests.push({ call, query: "", body: await request.clone().json() })
    if (refuse) return json({ code: "conflict", message: refuse }, 409)

    const id = url.pathname.split("/").at(-2)
    const answered = waiting.find((a) => a.id === id)
    waiting = waiting.filter((a) => a.id !== id)

    return json({ ...answered, status: "approved" })
  })

  return requests
}

function show(where: { chatId?: string; taskId?: string }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })

  return render(
    <QueryClientProvider client={client}>
      <PendingApprovals {...where} />
    </QueryClientProvider>,
  )
}

// Scenario: pi asked to send a mail in a chat. The owner reads who it goes to, its subject and its text as
// written, and approves it once. The card goes: nothing waits any more.
test("the owner reads a held mail and approves it once", async () => {
  const user = userEvent.setup()
  const requests = server([mail()])
  show({ chatId: "cht_1" })

  const card = within(
    await screen.findByRole("region", { name: "Approval needed: Send mail: minh@example.com" }),
  )
  expect(card.getByText("Waits for you")).toBeInTheDocument()
  expect(card.getByText("minh@example.com")).toBeInTheDocument()
  expect(card.getByText("Re: October invoice")).toBeInTheDocument()
  expect(card.getByText(/Received\.\s+We pay before the 15th\./)).toBeInTheDocument()
  expect(card.getByText("google:work · Send mail · asked by pi")).toBeInTheDocument()
  expect(
    card.getAllByRole("button").map((b) => b.getAttribute("aria-label") ?? b.textContent),
  ).toEqual(["Decline", "Approve", "More approval options"])

  await user.click(card.getByRole("button", { name: "Approve" }))
  await waitFor(() => expect(screen.queryByRole("region")).not.toBeInTheDocument())

  expect(requests[0]).toEqual({
    call: "GET /approvals",
    query: "?status=pending&chat_id=cht_1",
    body: undefined,
  })
  expect(requests).toContainEqual({
    call: "POST /approvals/apr_1/decision",
    query: "",
    body: { decision: "approve" },
  })
})

// Scenario: a worker's request, shown in its Task. It can be approved for the rest of the Run from the "…" menu,
// or declined. A request nobody's session is behind has no menu.
test("a worker's request is approved for its Run or declined", async () => {
  const user = userEvent.setup()
  const requests = server([
    mail({
      id: "apr_2",
      chat_id: undefined,
      task_id: "tsk_1",
      run_id: "run_1",
      asked_by: "worker",
    }),
    mail({
      id: "apr_3",
      chat_id: undefined,
      title: "Send mail: lan@example.com",
      asked_by: "owner",
    }),
  ])
  show({ taskId: "tsk_1" })

  const first = within(
    await screen.findByRole("region", { name: "Approval needed: Send mail: minh@example.com" }),
  )
  const second = within(
    screen.getByRole("region", { name: "Approval needed: Send mail: lan@example.com" }),
  )
  expect(second.queryByRole("button", { name: "More approval options" })).not.toBeInTheDocument()

  await user.click(first.getByRole("button", { name: "More approval options" }))
  await user.click(await screen.findByRole("menuitem", { name: "Approve for this Run" }))
  await waitFor(() =>
    expect(
      screen.queryByRole("region", { name: "Approval needed: Send mail: minh@example.com" }),
    ).not.toBeInTheDocument(),
  )

  await user.click(second.getByRole("button", { name: "Decline" }))
  await waitFor(() => expect(screen.queryByRole("region")).not.toBeInTheDocument())

  expect(requests).toContainEqual({
    call: "POST /approvals/apr_2/decision",
    query: "",
    body: { decision: "approve_session" },
  })
  expect(requests).toContainEqual({
    call: "POST /approvals/apr_3/decision",
    query: "",
    body: { decision: "reject" },
  })
})

// Scenario: the answer is refused, because someone answered first. The card stays and says why.
test("a refused answer is said on the card", async () => {
  const user = userEvent.setup()
  server([mail()], "approval apr_1 was already answered")
  show({ chatId: "cht_1" })

  const card = within(await screen.findByRole("region"))
  await user.click(card.getByRole("button", { name: "Decline" }))

  expect(await card.findByRole("alert")).toHaveTextContent(
    "Not answered: approval apr_1 was already answered",
  )
})

// Scenario: nothing waits. Nothing is shown.
test("nothing is shown when nothing waits", async () => {
  const requests = server([])
  const { container } = show({ chatId: "cht_1" })

  await waitFor(() => expect(requests).toHaveLength(1))
  expect(container).toBeEmptyDOMElement()
})

// Scenario: a Drive share's fields are the request's own JSON keys; they read as words, and a label the server
// already wrote for people stays as it is.
test("a request's field names read as words", () => {
  expect(readable("emailAddress")).toBe("Email address")
  expect(readable("role")).toBe("Role")
  expect(readable("send_notification_email")).toBe("Send notification email")
  expect(readable("X-Forward-Copy")).toBe("X-Forward-Copy")
  expect(readable("Subject")).toBe("Subject")
  expect(readable("…")).toBe("…")
})
