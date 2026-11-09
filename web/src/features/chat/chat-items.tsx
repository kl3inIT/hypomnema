import { ChevronRight, CircleAlert, Loader2, Terminal } from "lucide-react"
import { lazy, Suspense } from "react"

import type { ChatMessage } from "@/api/generated/types.gen"

import { argLine, type ChatItem, type Step } from "./chat-state"

// The pieces of a conversation. Learned from Omnara's AgentChatMessage and AgentToolGroup
// (.tmp/omnara/frontend/apps/web/src/components/agents/), Omnigent's ai-elements message
// (.tmp/omnigent/web/src/components/ai-elements/message.tsx) and pi-web's MessageView
// (.tmp/pi-web/components/MessageView.tsx): the owner on the right, pi's replies as markdown, its work as one
// line that opens.

const Markdown = lazy(() => import("./markdown"))

export function ChatItemView({ item }: { item: ChatItem }) {
  switch (item.kind) {
    case "owner":
      return <OwnerMessage message={item.message} />
    case "notice":
      return <Notice message={item.message} />
    case "reply":
      return <Reply text={item.text} streaming={item.streaming} />
    case "activity":
      return <Activity steps={item.steps} open={item.open} />
  }
}

function OwnerMessage({ message }: { message: ChatMessage }) {
  return (
    <div className="flex justify-end">
      <div className="max-w-[85%] rounded-2xl bg-muted px-3.5 py-2 break-words whitespace-pre-wrap">
        {message.text}
      </div>
    </div>
  )
}

// A wake-up from the control plane, or a notice such as a failed model call.
function Notice({ message }: { message: ChatMessage }) {
  const wake = message.event_seq !== undefined

  return (
    <div
      role="note"
      className="flex items-start gap-2 text-sm text-muted-foreground"
      aria-label={wake ? "Update from the control plane" : "Notice"}
    >
      <CircleAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
      <span className="break-words">{message.text}</span>
    </div>
  )
}

function Reply({ text, streaming }: { text: string; streaming: boolean }) {
  return (
    <div className="min-w-0 break-words" data-testid={streaming ? "reply-streaming" : "reply"}>
      <Suspense fallback={<p className="whitespace-pre-wrap">{text}</p>}>
        <Markdown text={text} streaming={streaming} />
      </Suspense>
    </div>
  )
}

// Activity is pi's work between replies: its thoughts and the tools it ran, one line that opens. The line names the
// tool running now, or counts what was done.
function Activity({ steps, open }: { steps: Step[]; open: boolean }) {
  const tools = steps.filter((s) => s.kind === "tool")
  const running = tools.find((s) => !s.result)
  const failed = tools.some((s) => s.result?.error)

  const summary = running
    ? `Running ${running.name}${argLine(running.args) ? `: ${argLine(running.args)}` : ""}`
    : tools.length > 0
      ? `Ran ${tools.length} ${tools.length === 1 ? "tool" : "tools"}`
      : "Thought"

  return (
    <details className="group/activity text-sm" open={open || undefined}>
      <summary className="flex cursor-pointer list-none items-center gap-1.5 text-muted-foreground select-none hover:text-foreground">
        <ChevronRight
          className="size-4 shrink-0 transition-transform group-open/activity:rotate-90"
          aria-hidden
        />
        {running && <Loader2 className="size-3.5 shrink-0 animate-spin" aria-hidden />}
        <span className="truncate">{summary}</span>
        {failed && <span className="text-destructive">· failed</span>}
      </summary>
      <ol className="mt-2 ml-2 flex flex-col gap-2 border-l border-border pl-3">
        {steps.map((s) => (
          <li key={s.key}>
            {s.kind === "thinking" ? <Thought text={s.text} /> : <Tool step={s} />}
          </li>
        ))}
      </ol>
    </details>
  )
}

function Thought({ text }: { text: string }) {
  return <p className="whitespace-pre-wrap text-muted-foreground italic">{text}</p>
}

function Tool({ step }: { step: Extract<Step, { kind: "tool" }> }) {
  const r = step.result
  const line = argLine(step.args)

  return (
    <details className="group/tool">
      <summary className="flex cursor-pointer list-none items-center gap-1.5 select-none">
        {r ? (
          <Terminal className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
        ) : (
          <Loader2 className="size-3.5 shrink-0 animate-spin text-muted-foreground" aria-hidden />
        )}
        <span className="font-medium">{step.name}</span>
        {line && <code className="truncate font-mono text-xs text-muted-foreground">{line}</code>}
        {r?.error && <span className="text-xs text-destructive">error</span>}
      </summary>
      {r?.output !== undefined && (
        <pre
          className={`mt-1.5 max-h-80 overflow-auto rounded-md bg-code p-2 font-mono text-xs whitespace-pre-wrap ${
            r.error ? "text-destructive" : ""
          }`}
        >
          {r.output || "(no output)"}
          {r.truncated && "\n… cut at 16 KiB"}
        </pre>
      )}
    </details>
  )
}
