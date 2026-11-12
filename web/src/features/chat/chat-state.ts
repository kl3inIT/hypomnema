import type { ChatDelta, ChatMessage, ChatPart, ChatToolActivity } from "@/api/generated/types.gen"

// What a chat shows, from its stream (GET /chats/{id}/stream): the kept messages, and what pi is doing that is not
// kept yet. As Omnara's delta previews (.tmp/omnara/frontend/packages/react/src/domains/agent-chat-messages.ts),
// the live text is shown until the kept reply that ends it arrives, and a tool shows as running until its kept
// result arrives.

export type LiveBlock = { kind: "text" | "thinking"; text: string }

export type RunningTool = { id: string; name: string; args?: Record<string, unknown> }

export type ChatView = {
  messages: ChatMessage[]
  // live holds the reply being written, by content block.
  live: Record<number, LiveBlock>
  running: RunningTool[]
  working: boolean
  // reconnecting is true while the stream is down and being opened again (Omnigent's ConnectionIndicator).
  reconnecting: boolean
}

export type ChatAction =
  | { type: "message"; message: ChatMessage }
  | { type: "reset" }
  | { type: "delta"; delta: ChatDelta }
  | { type: "tool"; activity: ChatToolActivity }
  | { type: "state"; working: boolean }
  | { type: "connection"; reconnecting: boolean }

export const emptyChat: ChatView = {
  messages: [],
  live: {},
  running: [],
  working: false,
  reconnecting: false,
}

export function chatReducer(view: ChatView, action: ChatAction): ChatView {
  switch (action.type) {
    case "message": {
      const m = action.message
      if (view.messages.some((kept) => kept.seq === m.seq)) return view

      const messages = [...view.messages, m].sort((a, b) => a.seq - b.seq)

      if (m.role === "coordinator") return { ...view, messages, live: {} }

      if (m.role === "tool") {
        const ended = new Set(m.parts?.map((p) => p.id))

        return { ...view, messages, running: view.running.filter((t) => !ended.has(t.id)) }
      }

      return { ...view, messages }
    }
    case "reset":
      return { ...view, live: {} }
    case "delta": {
      const { kind, index, delta } = action.delta
      const block = view.live[index]

      return {
        ...view,
        live: {
          ...view.live,
          [index]: { kind, text: (block?.kind === kind ? block.text : "") + delta },
        },
      }
    }
    case "tool": {
      const a = action.activity
      if (a.phase === "end") return view
      if (view.running.some((t) => t.id === a.id)) return view

      return {
        ...view,
        running: [...view.running, { id: a.id, name: a.name ?? "tool", args: a.args }],
      }
    }
    case "state":
      return action.working
        ? { ...view, working: true }
        : { ...view, working: false, live: {}, running: [] }
    case "connection":
      return view.reconnecting === action.reconnecting
        ? view
        : { ...view, reconnecting: action.reconnecting }
  }
}

// What the conversation draws, in order.
export type ChatItem =
  | { kind: "owner"; key: string; message: ChatMessage }
  | { kind: "notice"; key: string; message: ChatMessage }
  | { kind: "stopped"; key: string; message: ChatMessage }
  | { kind: "reply"; key: string; text: string; streaming: boolean; createdAt?: string }
  | { kind: "activity"; key: string; steps: Step[]; open: boolean }

// One step of pi's work: a thought, or a tool with its result (absent while it runs).
export type Step =
  | { kind: "thinking"; key: string; text: string }
  | { kind: "tool"; key: string; name: string; args?: Record<string, unknown>; result?: ChatPart }

// What the owner did in this page that the stream does not carry: the messages sent and not kept yet, shown at once
// as t3code's optimisticUserMessages.ts.
export type Local = { sending?: ChatMessage[] }

// failed is a notice that the model's call failed.
export function failed(m: ChatMessage) {
  return m.role === "system" && m.kind === "failed"
}

// chatItems turns the view into what is drawn. As Omnara's groupToolRuns
// (.tmp/omnara/frontend/apps/web/src/components/agents/AgentChatMessage.tsx), consecutive thoughts and tool calls
// become one group, so a burst of work reads as one line that opens; a reply's text ends the group.
//
// A turn the owner stopped ends in a row the server keeps as stopped, which reads as one (t3code's "You stopped
// this response", MessagesTimeline.logic.ts; assistant-ui's StoppedRun).
export function chatItems(view: ChatView, local: Local = {}): ChatItem[] {
  const items: ChatItem[] = []
  let group: Step[] | null = null
  const kept = new Set(view.messages.map((m) => m.seq))
  const messages = [...view.messages, ...(local.sending ?? []).filter((m) => !kept.has(m.seq))]

  const step = (s: Step, key: string) => {
    if (!group) {
      group = []
      items.push({ kind: "activity", key, steps: group, open: false })
    }

    group.push(s)
  }
  const close = () => {
    group = null
  }

  for (const m of messages) {
    const key = String(m.seq)

    switch (m.role) {
      case "owner":
        close()
        items.push({ kind: "owner", key, message: m })
        break
      case "system":
        close()
        if (m.kind === "stopped") {
          items.push({ kind: "stopped", key, message: m })
        } else {
          items.push({ kind: "notice", key, message: m })
        }
        break
      case "tool":
        for (const p of m.parts ?? []) {
          step(
            {
              kind: "tool",
              key: `${key}-${p.id ?? ""}`,
              name: p.name ?? "tool",
              args: p.args,
              result: p,
            },
            `a${key}`,
          )
        }
        break
      case "coordinator": {
        const parts = m.parts ?? [{ type: "text", text: m.text }]
        parts.forEach((p, i) => {
          if (p.type === "thinking" && p.text) {
            step({ kind: "thinking", key: `${key}-${i}`, text: p.text }, `a${key}`)
          } else if (p.type === "text" && p.text) {
            close()
            items.push({
              kind: "reply",
              key: `${key}-${i}`,
              text: p.text,
              streaming: false,
              createdAt: m.created_at,
            })
          }
        })
        break
      }
    }
  }

  // What is live comes last: thinking and running tools join the open group, text is the reply being written.
  const blocks = Object.entries(view.live).sort(([a], [b]) => Number(a) - Number(b))

  for (const [index, b] of blocks) {
    if (b.kind === "thinking")
      step({ kind: "thinking", key: `live-${index}`, text: b.text }, "live")
  }

  for (const t of view.running) {
    step({ kind: "tool", key: `running-${t.id}`, name: t.name, args: t.args }, "live")
  }

  const text = blocks
    .filter(([, b]) => b.kind === "text")
    .map(([, b]) => b.text)
    .join("")

  if (text) {
    close()
    items.push({ kind: "reply", key: "live-reply", text, streaming: true })
  }

  // The group pi is still working in stays open.
  const last = items.at(-1)
  if (view.working && last?.kind === "activity") last.open = true

  return items
}

// argLine is a tool call in one line: the command for bash, the path for file tools, as the CLI prints it.
export function argLine(args?: Record<string, unknown>): string {
  for (const k of ["command", "path", "file_path", "pattern"]) {
    const v = args?.[k]
    if (typeof v === "string" && v !== "") return v.split("\n")[0] ?? ""
  }

  return ""
}
