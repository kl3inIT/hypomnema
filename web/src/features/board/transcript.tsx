import {
  Brain,
  ChevronRight,
  FilePen,
  FileText,
  FolderOpen,
  Info,
  ListChecks,
  Search,
  SquareTerminal,
  Wrench,
  X,
} from "lucide-react"
import { lazy, Suspense, useMemo, useState } from "react"

import {
  WorkLogBlock,
  WorkLogButton,
  WorkLogDetails,
  WorkLogList,
  WorkLogRow,
} from "@/features/chat/work-log"
import { cn } from "@/lib/utils"

import { splitEnvPrefix } from "./tool-title"
import {
  command,
  diff,
  failed,
  fold,
  kind,
  preview,
  rest,
  rows,
  settled,
  summary,
  title,
  type Call,
  type Entry,
  type Row,
} from "./transcript-state"
import { useTranscript } from "./use-transcript"

const Markdown = lazy(() => import("@/features/chat/markdown"))

// Rows out of view are not laid out until they are near (Omnara's message-scroller): a transcript of folded
// groups stays short, so this stands in for a virtual list.
const offscreen = "[contain-intrinsic-size:auto_2rem] [content-visibility:auto]"

// Transcript is what a worker said and did during one Dispatch: prose for its messages, and between them its
// work, folded under one line that says it in words, with one row a call. The rows are the chat's (after
// t3code); what a row says and what it opens to follow Omnigent's ToolCard and Orca's NativeChatToolLine. While
// the worker runs, new items come in as the node sends them and its last steps stay out of the fold. With
// worked, the time a worker that succeeded took, all but its last message folds under "Worked for 12m", as
// t3code and Omnigent fold a finished turn.
export function Transcript({
  dispatchId,
  live,
  worked,
}: {
  dispatchId: string
  live: boolean
  worked?: string
}) {
  const transcript = useTranscript(dispatchId)
  const list = useMemo(() => rows(transcript.items), [transcript.items])
  const [unfolded, setUnfolded] = useState(false)

  if (!transcript.loaded) return <p className="text-xs text-muted-foreground">Loading…</p>

  if (list.length === 0) {
    return (
      <p className="rounded-lg border border-dashed border-border-strong px-3 py-2.5 text-xs text-muted-foreground">
        {live
          ? "The worker has not said or done anything yet."
          : "No transcript was kept for this attempt."}
      </p>
    )
  }

  const { before, last } = worked && !live ? settled(list) : { before: list, last: undefined }
  const folds = last !== undefined
  const open = !folds || unfolded

  return (
    <div role="log" aria-label="Transcript" className="flex min-w-0 flex-col">
      {folds && (
        <WorkLogBlock>
          <WorkLogButton
            aria-expanded={unfolded}
            onClick={() => setUnfolded((o) => !o)}
            label={`Worked for ${worked}`}
            trailing={<Chevron open={unfolded} />}
          />
        </WorkLogBlock>
      )}
      {open && transcript.earlier && (
        <button
          type="button"
          onClick={() => void transcript.loadEarlier()}
          disabled={transcript.loadingEarlier}
          className="mb-2 h-7 self-center rounded-lg border border-input px-3 text-xs text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-50"
        >
          Load earlier
        </button>
      )}
      {open &&
        before.map((row, i) => (
          <RowView key={row.key} row={row} live={live && i === before.length - 1} />
        ))}
      {last && <RowView row={last} live={false} />}
      {live && (
        <p className="h-6 px-1 text-sm leading-relaxed text-muted-foreground" aria-live="polite">
          <span className="live-tool-shine">Working</span>
        </p>
      )}
    </div>
  )
}

function RowView({ row, live }: { row: Row; live: boolean }) {
  switch (row.kind) {
    case "message":
      return (
        <div className={cn("min-w-0 px-1 py-0.5 pb-2", offscreen)} data-testid="transcript-message">
          <Suspense
            fallback={
              <p className="text-sm leading-relaxed whitespace-pre-wrap text-foreground/90">
                {row.text}
              </p>
            }
          >
            <Markdown text={row.text} streaming={false} />
          </Suspense>
        </div>
      )
    case "notice":
      return (
        <p role="note" className="flex items-center gap-1.5 py-1.5 text-2xs text-muted-foreground">
          <Info className="size-3 shrink-0" aria-hidden />
          {row.text}
        </p>
      )
    case "work":
      return <Work entries={row.entries} live={live} />
  }
}

// Work is the worker's steps between two messages: what is folded under a line that says it in words, then the
// steps that stay in view (all of them when there is one, the last three of a running worker).
function Work({ entries, live }: { entries: Entry[]; live: boolean }) {
  const [open, setOpen] = useState(false)
  const { folded, shown } = fold(entries, live)
  const failures = failed(folded)

  return (
    <div className={offscreen}>
      {folded.length > 0 && (
        <>
          <WorkLogBlock layout="group-header">
            <WorkLogButton
              aria-expanded={open}
              onClick={() => setOpen((o) => !o)}
              icon={<ListChecks className="size-4 stroke-2 text-icon-muted" />}
              label={summary(folded)}
              trailing={
                <span className="flex shrink-0 items-center gap-1 text-2xs text-tool-error-icon/80">
                  {failures > 0 && `${failures} failed`}
                  <Chevron open={open} />
                </span>
              }
            />
          </WorkLogBlock>
          {open && (
            <WorkLogBlock layout="group-content">
              <div className="ms-3 border-s ps-2">
                <WorkLogList>
                  {folded.map((e) => (
                    <EntryRow key={e.key} entry={e} live={false} />
                  ))}
                </WorkLogList>
              </div>
            </WorkLogBlock>
          )}
        </>
      )}
      {shown.length > 0 && (
        <WorkLogBlock>
          <WorkLogList>
            {shown.map((e, i) => (
              <EntryRow key={e.key} entry={e} live={live && i === shown.length - 1} />
            ))}
          </WorkLogList>
        </WorkLogBlock>
      )}
    </div>
  )
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

function EntryRow({ entry, live }: { entry: Entry; live: boolean }) {
  return entry.kind === "thought" ? (
    <ThoughtRow text={entry.text} />
  ) : (
    <CallRow entry={entry} live={live} />
  )
}

function ThoughtRow({ text }: { text: string }) {
  const [open, setOpen] = useState(false)

  return (
    <WorkLogRow
      onToggle={() => setOpen((o) => !o)}
      expanded={open}
      icon={<Brain className="size-4 stroke-2 text-icon-muted" />}
      label={
        <span>
          <span className="text-foreground/80">Thought</span>
          {!open && <> {text.trim().split("\n")[0]}</>}
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

const icons = {
  shell: SquareTerminal,
  list: FolderOpen,
  read: FileText,
  edit: FilePen,
  search: Search,
  other: Wrench,
}

// The tones of a diff's lines, from the theme's status colours (Omnigent DIFF_TONE_CLASS uses the raw palette).
const tone = {
  add: "text-done",
  del: "text-failed",
}

// CallRow is one tool call as Omnigent's ToolTriggerRow draws it: a verb and what it is about, or for a shell
// the command itself, an edit's line counts, and the MCP server as a tag. It opens only when there is more to
// see: the result, an edit's diff, the rest of a command.
function CallRow({ entry, live }: { entry: Call; live: boolean }) {
  const [open, setOpen] = useState(false)
  const name = title(entry)
  const shell = command(entry)
  const change = diff(entry)
  const input = rest(entry)
  const Icon = icons[kind(entry)]
  const running = live && !entry.result
  const hasDetail = !!entry.result || !!change || !!input || (shell?.more ?? 0) > 0

  return (
    <WorkLogRow
      onToggle={hasDetail ? () => setOpen((o) => !o) : undefined}
      expanded={hasDetail ? open : undefined}
      icon={
        <Icon
          className={cn(
            "size-4 stroke-2",
            entry.result?.failed ? "text-tool-error-icon/70" : "text-icon-muted",
          )}
        />
      }
      label={
        <span className={cn("block truncate", running && "live-tool-shine")} title={name.toolName}>
          {name.verb !== null && (
            <span className="font-medium text-foreground/80">{name.verb}</span>
          )}
          {name.verb !== null && name.body.length > 0 && " "}
          {name.isShellCommand && shell ? (
            <CommandLine first={shell.first} more={shell.more} />
          ) : (
            name.body
          )}
        </span>
      }
      trailing={
        <span className="flex shrink-0 items-center gap-1.5 text-2xs tabular-nums">
          {change && (
            <span>
              {change.added > 0 && <span className={tone.add}>+{change.added}</span>}
              {change.added > 0 && change.removed > 0 && " "}
              {change.removed > 0 && <span className={tone.del}>-{change.removed}</span>}
            </span>
          )}
          {name.server && (
            <span className="rounded border px-1 text-muted-foreground">{name.server}</span>
          )}
          {entry.result?.failed && (
            <X className="size-3 shrink-0 text-tool-error-icon/60" aria-label="failed" />
          )}
          {hasDetail && <Chevron open={open} />}
        </span>
      }
    >
      {open && (
        <WorkLogDetails kind="panel">
          {shell ? (
            <>
              <Output text={`$ ${shell.whole}`} />
              {entry.result?.text && (
                <Output text={entry.result.text} failed={entry.result.failed} />
              )}
            </>
          ) : (
            <>
              {change && <Diff change={change} />}
              {input && <Output text={input} />}
              {entry.result && (!change || entry.result.failed) && (
                <Output text={entry.result.text || "(no output)"} failed={entry.result.failed} />
              )}
            </>
          )}
        </WorkLogDetails>
      )}
    </WorkLogRow>
  )
}

// CommandLine is a command on a row: its environment dimmed so the program stands out, and a count of the lines
// that follow (Omnigent ShellCommandLine).
function CommandLine({ first, more }: { first: string; more: number }) {
  const { env, rest: program } = splitEnvPrefix(first)

  return (
    <span className="font-mono text-xs">
      {env && <span className="opacity-60">{env}</span>}
      {program}
      {more > 0 && (
        <span className="ms-1.5 font-sans opacity-60">
          +{more} {more === 1 ? "line" : "lines"}
        </span>
      )}
    </span>
  )
}

function Diff({ change }: { change: NonNullable<ReturnType<typeof diff>> }) {
  return (
    <pre className="ms-7 mb-1.5 max-h-80 cursor-text overflow-auto font-mono text-2xs leading-relaxed break-words whitespace-pre-wrap select-text">
      {change.lines.map((line, i) => (
        // A diff's lines never reorder.
        // eslint-disable-next-line react/no-array-index-key
        <span key={i} className={cn("block", tone[line.kind])}>
          {line.kind === "add" ? "+ " : "- "}
          {line.text}
        </span>
      ))}
      {change.cut && <span className="block text-muted-foreground">… more lines not shown</span>}
    </pre>
  )
}

// Output shows the start of a long text and, when asked, all of it.
function Output({ text, failed: isFailed }: { text: string; failed?: boolean }) {
  const [all, setAll] = useState(false)
  const cut = preview(text)
  const shown = all ? text : cut.text

  return (
    <div className="ms-7 mb-1.5">
      <pre
        className={cn(
          "max-h-80 cursor-text overflow-auto font-mono text-2xs leading-relaxed break-words whitespace-pre-wrap select-text",
          isFailed ? "text-destructive" : "text-secondary-label",
        )}
      >
        {shown}
      </pre>
      {cut.hidden > 0 && !all && (
        <button
          type="button"
          onClick={() => setAll(true)}
          className="mt-1 text-2xs text-link hover:underline"
        >
          Show all ({cut.hidden} more {cut.hidden === 1 ? "line" : "lines"})
        </button>
      )}
    </div>
  )
}
