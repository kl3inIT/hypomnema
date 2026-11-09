import {
  Brain,
  Check,
  ChevronRight,
  CircleAlert,
  Copy,
  FileText,
  Info,
  ListChecks,
  Search,
  SquareTerminal,
  Wrench,
  X,
} from "lucide-react"
import { lazy, Suspense, useState, type ReactNode } from "react"

import type { ChatMessage } from "@/api/generated/types.gen"
import { cn } from "@/lib/utils"

import { argLine, type ChatItem, type Step } from "./chat-state"
import { WorkLogBlock, WorkLogButton, WorkLogDetails, WorkLogList, WorkLogRow } from "./work-log"

// The rows of a conversation, drawn as t3code draws its timeline (MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code): the owner's message as a bubble on the right
// (chat/TimelineUserMessageRow.tsx), pi's reply as bare prose with its actions on hover
// (chat/TimelineAssistantRows.tsx), its work as dense rows that open (chat/TimelineWorkEntryRow.tsx,
// TimelineWorkGroupRows.tsx), and notices as a line across the lane (chat/TimelineSystemDivider.tsx).

const Markdown = lazy(() => import("./markdown"))

export function ChatItemView({ item, working }: { item: ChatItem; working: boolean }) {
  switch (item.kind) {
    case "owner":
      return <OwnerMessage message={item.message} />
    case "notice":
      return <Notice message={item.message} />
    case "reply":
      return <Reply text={item.text} streaming={item.streaming} createdAt={item.createdAt} />
    case "activity":
      return <Activity steps={item.steps} live={item.open && working} />
  }
}

const time = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" })

function formatTime(iso?: string) {
  return iso ? time.format(new Date(iso)) : ""
}

// Meta is the time and a copy button, shown on hover or focus, always on a touch screen.
function Meta({
  text,
  createdAt,
  align,
}: {
  text: string
  createdAt?: string
  align: "start" | "end"
}) {
  const [copied, setCopied] = useState(false)

  return (
    <div
      className={cn(
        "flex items-center gap-2 text-xs text-muted-foreground tabular-nums opacity-0 transition-opacity duration-200 focus-within:opacity-100 pointer-coarse:opacity-100",
        align === "end"
          ? "w-full max-w-[80%] justify-end pe-1 group-hover:opacity-100"
          : "mt-1.5 group-hover/assistant:opacity-100",
      )}
    >
      {align === "end" && <time>{formatTime(createdAt)}</time>}
      <button
        type="button"
        aria-label="Copy"
        className="inline-flex size-6 items-center justify-center rounded-md hover:bg-accent hover:text-foreground"
        onClick={() => {
          void navigator.clipboard?.writeText(text).then(() => {
            setCopied(true)
            setTimeout(() => setCopied(false), 1500)
          })
        }}
      >
        {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
      </button>
      {align === "start" && <time>{formatTime(createdAt)}</time>}
    </div>
  )
}

function OwnerMessage({ message }: { message: ChatMessage }) {
  return (
    <div className="group flex flex-col items-end gap-1 pb-4">
      <div className="relative max-w-[80%] min-w-0 rounded-2xl bg-message p-3 text-sm leading-relaxed break-words whitespace-pre-wrap text-message-foreground">
        <h3 className="sr-only">You</h3>
        {message.text}
      </div>
      <Meta text={message.text} createdAt={message.created_at} align="end" />
    </div>
  )
}

function Reply({
  text,
  streaming,
  createdAt,
}: {
  text: string
  streaming: boolean
  createdAt?: string
}) {
  return (
    <div
      className="group/assistant relative min-w-0 px-1 py-0.5 pb-2"
      data-testid={streaming ? "reply-streaming" : "reply"}
    >
      <h3 className="sr-only">pi</h3>
      <Suspense
        fallback={
          <p className="text-sm leading-relaxed whitespace-pre-wrap text-foreground/90">{text}</p>
        }
      >
        <Markdown text={text} streaming={streaming} />
      </Suspense>
      {!streaming && <Meta text={text} createdAt={createdAt} align="start" />}
    </div>
  )
}

// A wake-up from the control plane or a notice, as a line across the lane; a failed model call reads as danger.
function Notice({ message }: { message: ChatMessage }) {
  const danger = message.text.startsWith("pi's model call failed")
  const wake = message.event_seq !== undefined
  const [label, detail] = splitNotice(message.text)
  const Icon = danger ? CircleAlert : Info

  return (
    <div
      role="note"
      aria-label={wake ? "Update from the control plane" : "Notice"}
      className={cn(
        "flex min-w-0 items-center gap-2 py-2 text-2xs text-muted-foreground",
        danger && "text-destructive",
      )}
    >
      <span aria-hidden="true" className="h-px flex-1 bg-border/70" />
      <span className="flex min-w-0 flex-wrap items-center justify-center gap-1.5 rounded-full px-2 py-1">
        <Icon className="size-3 shrink-0" />
        <span className="font-medium">{label}</span>
        {detail && <span className="max-w-80 truncate opacity-70">·&nbsp;{detail}</span>}
      </span>
      <span aria-hidden="true" className="h-px flex-1 bg-border/70" />
    </div>
  )
}

// splitNotice cuts "what happened: the command to read it" into a label and its detail.
function splitNotice(text: string): [string, string] {
  const at = text.indexOf(": ")

  return at > 0 ? [text.slice(0, at), text.slice(at + 2)] : [text, ""]
}

// Activity is pi's work between replies. One step is one row; several are a group whose header counts them, open
// while pi works in it.
function Activity({ steps, live }: { steps: Step[]; live: boolean }) {
  const [open, setOpen] = useState(false)
  const expanded = live || open

  if (steps.length === 1 && steps[0]) {
    return (
      <WorkLogBlock>
        <StepRow step={steps[0]} />
      </WorkLogBlock>
    )
  }

  const tools = steps.filter((s) => s.kind === "tool")
  const failed = tools.some((s) => s.result?.error)

  return (
    <>
      <WorkLogBlock layout="group-header">
        <WorkLogButton
          aria-expanded={expanded}
          onClick={() => setOpen((o) => !o)}
          icon={<ListChecks className="size-4 stroke-2 text-icon-muted" />}
          label={summary(tools.length, steps.length - tools.length)}
          trailing={
            <span className="flex shrink-0 items-center gap-1">
              {failed && (
                <X className="size-3 shrink-0 text-tool-error-icon/60" aria-label="failed" />
              )}
              <Chevron open={expanded} />
            </span>
          }
        />
      </WorkLogBlock>
      {expanded && (
        <WorkLogBlock layout="group-content">
          <div className="ms-3 border-s ps-2">
            <WorkLogList>
              {steps.map((s) => (
                <StepRow key={s.key} step={s} />
              ))}
            </WorkLogList>
          </div>
        </WorkLogBlock>
      )}
    </>
  )
}

function summary(tools: number, thoughts: number) {
  if (tools === 0) return "Thought"

  return `Ran ${tools} ${tools === 1 ? "tool" : "tools"}${thoughts > 0 ? `, thought ${thoughts === 1 ? "once" : `${thoughts} times`}` : ""}`
}

function Chevron({ open }: { open: boolean }) {
  return (
    <span className="flex size-4 shrink-0 items-center justify-center">
      <ChevronRight
        className={cn(
          "size-3 shrink-0 text-icon-muted opacity-70 transition-transform duration-200",
          open && "rotate-90",
        )}
      />
    </span>
  )
}

function StepRow({ step }: { step: Step }) {
  return step.kind === "thinking" ? <ThoughtRow text={step.text} /> : <ToolRow step={step} />
}

function ThoughtRow({ text }: { text: string }) {
  const [open, setOpen] = useState(false)
  const preview = text.split("\n")[0] ?? ""

  return (
    <WorkLogRow
      onToggle={() => setOpen((o) => !o)}
      expanded={open}
      icon={<Brain className="size-4 stroke-2 text-icon-muted" />}
      label={
        <span>
          <span className="text-foreground/80">Thinking</span>
          {!open && <span className="ms-1.5">{preview}</span>}
        </span>
      }
      trailing={<Chevron open={open} />}
    >
      {open && (
        <WorkLogDetails>
          <p className="text-sm leading-relaxed whitespace-pre-wrap text-secondary-label">{text}</p>
        </WorkLogDetails>
      )}
    </WorkLogRow>
  )
}

const toolIcons: Record<string, typeof Wrench> = {
  bash: SquareTerminal,
  read: FileText,
  grep: Search,
  find: Search,
  ls: Search,
}

function ToolRow({ step }: { step: Extract<Step, { kind: "tool" }> }) {
  const [open, setOpen] = useState(false)
  const r = step.result
  const line = argLine(step.args)
  const Icon = toolIcons[step.name] ?? Wrench
  const icon = <Icon className="size-4 stroke-2 text-icon-muted" />

  const label = (
    <span className={cn("block truncate", !r && "live-tool-shine")}>
      <span className="font-medium text-foreground/80">{step.name}</span>
      {line && <span className="ms-1.5 font-mono text-xs">{line}</span>}
    </span>
  )

  if (!r) return <WorkLogRow icon={icon} label={label} />

  return (
    <WorkLogRow
      onToggle={() => setOpen((o) => !o)}
      expanded={open}
      icon={icon}
      label={label}
      trailing={
        <span className="flex shrink-0 items-center gap-1">
          {r.error && <X className="size-3 shrink-0 text-tool-error-icon/60" aria-label="failed" />}
          <Chevron open={open} />
        </span>
      }
    >
      {open && (
        <WorkLogDetails kind="panel">
          <ToolOutput output={r.output} error={r.error} truncated={r.truncated} />
        </WorkLogDetails>
      )}
    </WorkLogRow>
  )
}

function ToolOutput({
  output,
  error,
  truncated,
}: {
  output?: string
  error?: boolean
  truncated?: boolean
}) {
  let body: ReactNode = output || "(no output)"
  if (truncated) {
    body = (
      <>
        {body}
        {"\n… cut at 16 KiB"}
      </>
    )
  }

  return (
    <pre
      className={cn(
        "ms-7 max-h-64 cursor-text overflow-auto font-mono text-2xs leading-relaxed break-words whitespace-pre-wrap select-text",
        error ? "text-destructive" : "text-secondary-label",
      )}
    >
      {body}
    </pre>
  )
}
