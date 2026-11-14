import {
  Brain,
  Check,
  ChevronRight,
  CircleAlert,
  Copy,
  FilePen,
  FileText,
  FolderOpen,
  Info,
  ListChecks,
  Search,
  Square,
  SquareTerminal,
  Wrench,
  X,
} from "lucide-react"
import { useState } from "react"

import type { ChatMessage } from "@/api/generated/types.gen"
import { ArtifactCard } from "@/features/artifacts/artifact-card"
import { SetupChangeCard } from "@/features/bots/setup-change-card"
import { RunCard, SetUpCard } from "@/features/routines/chat-cards"
import { cn } from "@/lib/utils"

import { failed, type ChatItem, type Step } from "./chat-state"
import Markdown from "./markdown"
import { MessageFiles } from "./message-files"
import { hasDetail, outputPreview, toolDuration, toolInput, toolPhrase } from "./tool-row"
import { WakeCard } from "./wake-card"
import { WorkLogBlock, WorkLogButton, WorkLogDetails, WorkLogList, WorkLogRow } from "./work-log"

// The rows of a conversation, drawn as t3code draws its timeline (MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code): the owner's message as a bubble on the right
// (chat/TimelineUserMessageRow.tsx), pi's reply as bare prose with its actions on hover
// (chat/TimelineAssistantRows.tsx), its work as dense rows that open (chat/TimelineWorkEntryRow.tsx,
// TimelineWorkGroupRows.tsx), and notices as a line across the lane (chat/TimelineSystemDivider.tsx).

// The markdown renderer is imported with the chat, as t3code's ChatMarkdown is, so a reply never shows its raw
// marks while the renderer loads.

export function ChatItemView({
  item,
  working,
  bot,
}: {
  item: ChatItem
  working: boolean
  bot: string
}) {
  switch (item.kind) {
    case "owner":
      return <OwnerMessage message={item.message} />
    case "notice":
      return <Notice message={item.message} />
    case "stopped":
      return <Stopped />
    case "wake":
      return <WakeCard wake={item.wake} />
    case "artifact":
      return <ArtifactCard card={item.artifact} />
    case "reply":
      return (
        <Reply text={item.text} streaming={item.streaming} createdAt={item.createdAt} bot={bot} />
      )
    case "activity":
      return <Activity steps={item.steps} live={item.open && working} />
    case "routine-setup":
      return <SetUpCard routineId={item.routineId} />
    case "setup-change":
      return <SetupChangeCard change={item.change} />
    case "routine-run":
      return (
        <RunCard
          routineId={item.routineId}
          runId={item.runId}
          text={item.text}
          createdAt={item.createdAt}
        />
      )
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
          // A browser may refuse the clipboard (no permission, no focus); the button then stays as it is.
          void navigator.clipboard
            ?.writeText(text)
            .then(() => {
              setCopied(true)
              setTimeout(() => setCopied(false), 1500)
            })
            .catch(() => undefined)
        }}
      >
        {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
      </button>
      {align === "start" && <time>{formatTime(createdAt)}</time>}
    </div>
  )
}

function OwnerMessage({ message }: { message: ChatMessage }) {
  const files = message.attachments ?? []

  return (
    <div className="group flex flex-col items-end gap-1 pb-4">
      <h3 className="sr-only">You</h3>
      {/* Its files above its text, as t3code's TimelineUserMessageRow.tsx (issue #343). */}
      {files.length > 0 && <MessageFiles chat={message.chat_id} files={files} />}
      {message.text && (
        <div className="relative max-w-[80%] min-w-0 rounded-2xl bg-message p-3 text-sm leading-relaxed break-words whitespace-pre-wrap text-message-foreground">
          {message.text}
        </div>
      )}
      <Meta text={message.text} createdAt={message.created_at} align="end" />
    </div>
  )
}

function Reply({
  text,
  streaming,
  createdAt,
  bot,
}: {
  text: string
  streaming: boolean
  createdAt?: string
  bot: string
}) {
  return (
    <div
      className="group/assistant relative min-w-0 px-1 py-0.5 pb-2"
      data-testid={streaming ? "reply-streaming" : "reply"}
    >
      <h3 className="sr-only">{bot}</h3>
      <Markdown text={text} streaming={streaming} />
      {!streaming && <Meta text={text} createdAt={createdAt} align="start" />}
    </div>
  )
}

// A notice, or a wake-up whose event is not known, as a line across the lane; a failed model call reads as danger.
function Notice({ message }: { message: ChatMessage }) {
  const danger = failed(message)
  const wake = message.kind === "wake"
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

// Stopped is a turn the owner stopped: a quiet line, not a failure (assistant-ui's StoppedRun, its square mark and
// muted words; t3code's "You stopped this response").
function Stopped() {
  return (
    <div
      role="note"
      aria-label="Stopped"
      className="flex min-w-0 items-center gap-2 py-2 text-2xs text-muted-foreground"
    >
      <span aria-hidden="true" className="h-px flex-1 bg-border/70" />
      <span className="flex items-center gap-1.5 rounded-full px-2 py-1">
        <Square className="size-2.5 shrink-0 fill-current" aria-hidden="true" />
        <span className="font-medium">Stopped</span>
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
  const erred = tools.some((s) => s.result?.error)

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
              {erred && (
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
  write: FilePen,
  edit: FilePen,
  grep: Search,
  find: Search,
  ls: FolderOpen,
}

// ToolRow is one tool call (issue #441), as Orca's NativeChatToolLine: an icon by kind of tool, the phrase, the time
// on the right and red when it failed; it opens, when the Bot shows calls in full, to the command or arguments and
// the output, as Orca's ToolLineDetail. A call the Bot shows without detail does not open.
function ToolRow({ step }: { step: Extract<Step, { kind: "tool" }> }) {
  const [open, setOpen] = useState(false)
  const r = step.result
  const { verb, target } = toolPhrase(step.name, step.args)
  const Icon = toolIcons[step.name] ?? Wrench
  const failedCall = r?.error === true
  const icon = (
    <Icon
      className={cn("size-4 stroke-2", failedCall ? "text-destructive/80" : "text-icon-muted")}
    />
  )
  const time = toolDuration(r?.duration_ms)

  const label = (
    <span
      className={cn("block truncate", !r && "live-tool-shine", failedCall && "text-destructive")}
    >
      {verb && (
        <span className={cn("font-medium", !failedCall && "text-foreground/80")}>{verb}</span>
      )}
      {verb && " "}
      <span
        className={cn(
          verb ? "ms-0.5 font-mono text-xs" : "font-medium",
          !verb && !failedCall && "text-foreground/80",
        )}
      >
        {target}
      </span>
    </span>
  )

  if (!r) return <WorkLogRow icon={icon} label={label} />

  const detail = hasDetail(step.args, r)
  const trailing = (
    <span className="flex shrink-0 items-center gap-1.5">
      {failedCall && <X className="size-3 shrink-0 text-tool-error-icon/60" aria-label="failed" />}
      {time && (
        <span className="text-2xs text-muted-foreground tabular-nums" aria-label={`took ${time}`}>
          {time}
        </span>
      )}
      {detail && <Chevron open={open} />}
    </span>
  )

  if (!detail) return <WorkLogRow icon={icon} label={label} trailing={trailing} />

  return (
    <WorkLogRow
      onToggle={() => setOpen((o) => !o)}
      expanded={open}
      icon={icon}
      label={label}
      trailing={trailing}
    >
      {open && (
        <WorkLogDetails kind="panel">
          <ToolDetail
            input={toolInput(step.name, step.args)}
            output={r.output}
            error={failedCall}
            truncated={r.truncated}
          />
        </WorkLogDetails>
      )}
    </WorkLogRow>
  )
}

// ToolDetail is an open call: what it was given, then its output, the first 1,000 characters with "Show all" for
// the rest, as OpenMausBot's ToolActivity shows a preview and the rest on request.
function ToolDetail({
  input,
  output,
  error,
  truncated,
}: {
  input: string
  output?: string
  error?: boolean
  truncated?: boolean
}) {
  const [all, setAll] = useState(false)
  const shown = outputPreview(output ?? "", all)

  return (
    <div className="ms-7 flex flex-col gap-1.5 py-1">
      {input && (
        <pre
          aria-label="Input"
          className="max-h-64 cursor-text overflow-auto rounded-md border bg-muted/40 p-2 font-mono text-2xs leading-relaxed break-words whitespace-pre-wrap text-foreground/80 select-text"
        >
          {input}
        </pre>
      )}
      <pre
        aria-label="Output"
        className={cn(
          "max-h-64 cursor-text overflow-auto font-mono text-2xs leading-relaxed break-words whitespace-pre-wrap select-text",
          error ? "text-destructive" : "text-secondary-label",
        )}
      >
        {shown.text || "(no output)"}
        {shown.more && "…"}
        {!shown.more && truncated && "\n… cut at 16 KiB"}
      </pre>
      {shown.more && (
        <button
          type="button"
          onClick={() => setAll(true)}
          className="self-start text-xs font-medium text-primary hover:underline"
        >
          Show all ({(output ?? "").length.toLocaleString("en")} characters)
        </button>
      )}
    </div>
  )
}
