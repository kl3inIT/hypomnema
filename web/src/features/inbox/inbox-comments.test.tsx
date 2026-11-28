import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { afterEach, expect, test, vi } from "vitest"

import type { Todo, TodoEvent } from "@/api/generated/types.gen"

import { CommentComposer, CommentTimeline } from "./inbox-comments"

afterEach(() => vi.restoreAllMocks())

const json = (body: unknown) =>
  new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  })

const todo = (m: Partial<Todo> = {}): Todo => ({
  id: "tdo_1",
  revision: 4,
  title: "Send the invoice to ELITEX",
  notes: "",
  done_when: "",
  draft: "",
  due: { kind: "none" },
  assignee: { kind: "me" },
  state: "open",
  source: { kind: "zalo", account: "main" },
  created_by: { kind: "bot", id: "bot_main" },
  close_reason: "",
  evidence: [],
  stale: false,
  priority: 0,
  priority_reason: "",
  created_at: "2026-11-25T03:00:00Z",
  updated_at: "2026-11-25T03:00:00Z",
  ...m,
})

const names: Record<string, string> = { bot_main: "Hypo", bot_beyond: "BeyondPilot" }
const botName = (id: string) => names[id] ?? id

// server is the control plane: each call that is not a read is kept with its body; a reopen moves the revision on.
function server() {
  const calls: { call: string; body: unknown }[] = []
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    const path = new URL(request.url).pathname.replace("/api/v1", "")
    const body = request.method === "GET" ? undefined : await request.json()
    if (request.method !== "GET") calls.push({ call: `${request.method} ${path}`, body })

    return json(todo({ revision: path.endsWith("/reopen") ? 5 : 6 }))
  })

  return calls
}

const wrap = (children: ReactNode) => (
  <QueryClientProvider client={new QueryClient()}>{children}</QueryClientProvider>
)

async function write(item: Todo, button: string) {
  render(wrap(<CommentComposer todo={item} mainBot="bot_main" botName={botName} />))
  await userEvent.type(screen.getByRole("textbox", { name: "Message" }), "Draft a reply to Kai")
  await userEvent.click(screen.getByRole("button", { name: button }))
}

// Scenario: issue #1143, Logic 1. On an item no Bot has, the composer reads "Message Hypo…" and Hand to Hypo hands
// it to the main Bot with the text as its prompt, and the revision read.
test("send on an item not handed hands it with the text", async () => {
  const calls = server()
  await write(todo(), "Hand to Hypo")

  await waitFor(() =>
    expect(calls).toEqual([
      {
        call: "POST /inbox/tdo_1/hand",
        body: { revision: 4, bot: "bot_main", prompt: "Draft a reply to Kai" },
      },
    ]),
  )
  expect(screen.getByRole("textbox", { name: "Message" })).toHaveAttribute(
    "placeholder",
    "Message Hypo…",
  )
})

// Scenario: issue #1143, Logic 2. On an item a Bot has in its chat, Send adds a comment, which reaches that Bot.
test("send on a handed item adds a comment", async () => {
  const calls = server()
  await write(
    todo({ state: "doing", assignee: { kind: "bot", name: "bot_beyond" }, chat_id: "cht_1" }),
    "Send",
  )

  await waitFor(() =>
    expect(calls).toEqual([
      { call: "POST /inbox/tdo_1/comments", body: { text: "Draft a reply to Kai" } },
    ]),
  )
})

test("send on a review stall replies to the coordinator without handing over the item", async () => {
  const calls = server()
  await write(todo({ reply_to_coordinator: true }), "Send")

  await waitFor(() =>
    expect(calls).toEqual([
      { call: "POST /inbox/tdo_1/comments", body: { text: "Draft a reply to Kai" } },
    ]),
  )
})

// Scenario: issue #1143, Logic 6. On an item closed as done, Reopen and send reopens it with the revision read,
// then hands it back to its Bot with the text and the revision the reopen made.
test("send on a done item reopens it", async () => {
  const calls = server()
  await write(
    todo({ state: "done", assignee: { kind: "bot", name: "bot_beyond" }, chat_id: "cht_1" }),
    "Reopen and send",
  )

  await waitFor(() =>
    expect(calls).toEqual([
      { call: "POST /inbox/tdo_1/reopen", body: { revision: 4 } },
      {
        call: "POST /inbox/tdo_1/hand",
        body: { revision: 5, bot: "bot_beyond", prompt: "Draft a reply to Kai" },
      },
    ]),
  )
})

const event = (id: number, m: Partial<TodoEvent>): TodoEvent => ({
  id,
  at: "2026-11-25T03:00:00Z",
  revision: id,
  actor: { kind: "bot", id: "bot_main" },
  kind: "edited",
  before: {},
  after: {},
  ...m,
})

// Scenario: issue #1143, Logic 5 and 4. A Bot's report shows its first line, and its sections are folded until
// the owner opens one; Hypo's steps in a row show as one line that opens to each step.
test("a report's details render folded, and steps group", async () => {
  render(
    wrap(
      <CommentTimeline
        loading={false}
        now={Date.parse("2026-11-25T04:00:00Z")}
        from="Zalo · Laura Nguyen"
        botName={botName}
        events={[
          event(1, { kind: "created" }),
          event(2, { after: { notes: "a" } }),
          event(3, { kind: "state", after: { state: "doing", work_kind: "run" } }),
          event(4, {
            kind: "commented",
            after: {
              text: "The invoice is ready.\n\n<details>\n<summary>Checks</summary>\n\nThe totals match.\n</details>",
            },
          }),
        ]}
      />,
    ),
  )

  expect(screen.getByText("Hypo added it from Zalo · Laura Nguyen")).toBeVisible()
  const comment = screen.getByRole("article", { name: "Comment by Hypo" })
  expect(comment).toHaveTextContent("The invoice is ready.")
  const checks = screen.getByRole("button", { name: "Checks" })
  expect(checks).toHaveAttribute("aria-expanded", "false")
  expect(screen.queryByText("The totals match.")).toBeNull()
  await userEvent.click(checks)
  expect(await screen.findByText("The totals match.")).toBeVisible()

  const steps = screen.getByRole("button", { name: /Hypo did 2 steps/ })
  expect(screen.queryByText("Hypo started a Run")).toBeNull()
  await userEvent.click(steps)
  expect(await screen.findByText("Hypo started a Run")).toBeVisible()
})

// Scenario: issue #1142. A Bot's move to Waiting on others shows on its own line with the words that show it under
// it: for a Zalo item, with the sender and time the server keeps of the message, read in the item's own account and
// chat; for a Lark item, whose messages the server does not keep, with the name alone and no read.
test("a move to Waiting on others shows the quote with who wrote it", async () => {
  const reads: string[] = []
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = new URL((input as Request).url)
    reads.push(`${url.pathname.replace("/api/v1", "")}${url.search}`)

    return json({
      data: [
        {
          id: "m_2",
          chat: "g_team",
          sender_name: "Việt Nguyễn",
          from_me: false,
          at: "2026-11-25T03:30:00Z",
          text: "em nhận",
        },
      ],
    })
  })
  const moved = (ref: { kind: "zalo_message" | "lark_message"; ref: string }) =>
    event(2, {
      after: {
        assignee: "Việt",
        assignee_kind: "person",
        waiting_on: { by: "Việt", quote: "em nhận", ref },
      },
    })
  const timeline = (source: Todo["source"], ref: Parameters<typeof moved>[0]) => (
    <CommentTimeline
      loading={false}
      now={Date.parse("2026-11-25T04:00:00Z")}
      botName={botName}
      source={source}
      chats={new Map([["zalo:g_team", { name: "Team", label: "work" }]])}
      events={[moved(ref)]}
    />
  )

  const { unmount } = render(
    wrap(
      timeline(
        { kind: "zalo", id: "g_team", account: "main" },
        { kind: "zalo_message", ref: "m_2" },
      ),
    ),
  )

  expect(screen.getByText("Hypo moved it to Waiting on Việt")).toBeVisible()
  expect(screen.getByText("em nhận")).toBeVisible()
  expect(await screen.findByText("Việt Nguyễn")).toBeVisible()
  expect(reads).toEqual(["/integrations/zalo/connections/work/messages?chat=g_team&id=m_2&limit=1"])
  unmount()

  reads.length = 0
  render(wrap(timeline({ kind: "lark", id: "oc_1" }, { kind: "lark_message", ref: "om_1" })))
  expect(screen.getByText("Việt")).toBeVisible()
  expect(reads).toEqual([])
})
