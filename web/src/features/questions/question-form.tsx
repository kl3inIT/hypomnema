import { useMutation, useQueryClient } from "@tanstack/react-query"
import { MessageCircleQuestion } from "lucide-react"
import { useEffect, useRef, useState } from "react"

import "@/api/client"
import {
  getRunQueryKey,
  listQuestionsQueryKey,
  listTasksQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import { answerQuestion } from "@/api/generated/sdk.gen"
import type { Choice, Question, QuestionPart } from "@/api/generated/types.gen"
import { since } from "@/lib/time"
import { callsOf } from "@/lib/query-keys"
import { cn } from "@/lib/utils"

// answeredReads matches what an answer changes: the Tasks, the questions and each Run.
const answeredReads = callsOf(
  listTasksQueryKey(),
  listQuestionsQueryKey(),
  getRunQueryKey({ path: { run_id: "" } }),
)

// One decision of a question: the first is the question's own header, text and options; the more follow (issue #1219).
type Decision = Pick<QuestionPart, "header" | "text" | "options">

// QuestionForm answers a worker's question, after t3code's pending user-input panel (MIT, Copyright (c) 2026 T3
// Tools Inc., https://github.com/pingdotgg/t3code; chat/ComposerPendingUserInputPanel.tsx): the options as radio
// rows that 1 to 9 pick, and text where the chosen option, or a question without options, takes it. Enter sends,
// never while an input method composes. The first answer wins on the server; a second is refused.
//
// Issue #1219: each option's description sits under its label, as t3code's (ComposerPendingUserInputPanel.tsx:
// 300-311). A question of several decisions shows them one under another, each with its header, and is sent once,
// as Paperclip's question card (MIT, Copyright (c) 2025 Paperclip AI, https://github.com/paperclipai/paperclip;
// ui/src/components/IssueThreadInteractionCard.tsx:1482-1580, "Submit once after you finish the full form"), not
// one at a time as t3code's, so the owner reads every decision beside the others. A Gate shows the worker's
// summary above, a few lines that open to the whole, as Omnara's and Paperclip's cards sit in the agent's
// conversation; the Task's panel, which shows the summary below, leaves it out.
export function QuestionForm({
  question,
  onAnswered,
  showSummary = true,
}: {
  question: Question
  onAnswered?: () => void
  showSummary?: boolean
}) {
  const queryClient = useQueryClient()
  const decisions: Decision[] = [question, ...(question.more ?? [])]
  const [picks, setPicks] = useState<(number | undefined)[]>(() =>
    decisions.map((d) => (d.options.length ? 0 : undefined)),
  )
  const [texts, setTexts] = useState<string[]>(() => decisions.map(() => ""))

  const choiceOf = (d: Decision, i: number): Choice => {
    const pick = picks[i]
    const takesText =
      d.options.length === 0 || (pick !== undefined && !!d.options[pick]?.allows_text)
    const text = texts[i]?.trim()

    return { option: pick, text: takesText && text ? text : undefined }
  }

  const ready = decisions.every((d, i) =>
    d.options.length ? picks[i] !== undefined : (texts[i] ?? "").trim() !== "",
  )

  const send = useMutation({
    mutationFn: async () => {
      const [first, ...more] = decisions.map(choiceOf)

      return answerQuestion({
        path: { question_id: question.id },
        body: { ...first, more: more.length ? more : undefined },
      })
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ predicate: answeredReads })
      onAnswered?.()
    },
  })

  const submit = () => {
    if (ready && !send.isPending) send.mutate()
  }

  const several = decisions.length > 1

  return (
    <form
      aria-label="Answer"
      className="rounded-2xl border border-needs-you/35 bg-surface-raised px-4 pt-3 pb-2.5"
      onSubmit={(e) => {
        e.preventDefault()
        submit()
      }}
    >
      <div className="flex items-center gap-2 text-xs text-needs-you">
        <MessageCircleQuestion className="size-3.5" aria-hidden />
        <span className="font-medium">
          {question.mode === "gate"
            ? "The worker stopped for your decision"
            : "The worker waits for you"}
        </span>
        <span className="text-muted-foreground">· {since(question.created_at)}</span>
      </div>

      {showSummary && question.summary && <Summary text={question.summary} />}

      {decisions.map((d, i) => (
        <DecisionRows
          key={`${i}:${d.text}`}
          name={`q-${question.id}-${i}`}
          decision={d}
          number={several ? i + 1 : undefined}
          pick={picks[i]}
          onPick={(n) => setPicks((all) => all.map((p, j) => (j === i ? n : p)))}
          text={texts[i] ?? ""}
          onText={(t) => setTexts((all) => all.map((x, j) => (j === i ? t : x)))}
          takesText={d.options.length === 0 || !!d.options[picks[i] ?? -1]?.allows_text}
          onSubmit={submit}
        />
      ))}

      <div className="flex items-center gap-2 pt-1">
        <span className="text-2xs text-muted-foreground">
          {send.error ? (
            <span className="text-destructive">Not sent: {errorText(send.error)}</span>
          ) : decisions.some((d) => d.options.length) ? (
            "Enter sends · number keys pick"
          ) : (
            "Enter sends"
          )}
        </span>
        <button
          type="submit"
          aria-label="Send answer"
          disabled={!ready || send.isPending}
          className="ms-auto flex size-9 items-center justify-center rounded-full bg-message-action text-message-action-foreground shadow-xs transition-colors hover:bg-message-action-hover enabled:inset-shadow-control-highlight disabled:bg-foreground/12 disabled:text-muted-foreground sm:size-8"
        >
          <svg
            width="14"
            height="14"
            viewBox="0 0 14 14"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.8"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden="true"
          >
            <path d="M7 11.5V2.5M7 2.5L3 6.5M7 2.5L11 6.5" />
          </svg>
        </button>
      </div>
    </form>
  )
}

// Summary is the worker's last report, three lines that open to the whole (issue #1219, decision 5). Whether it runs
// past three lines is measured on the page, at its width, not guessed from its length (review of PR #1228).
function Summary({ text }: { text: string }) {
  const [open, setOpen] = useState(false)
  const [overflows, setOverflows] = useState(false)
  const ref = useRef<HTMLParagraphElement>(null)

  useEffect(() => {
    const el = ref.current
    if (!el || typeof ResizeObserver === "undefined") return

    // Measured only while clamped: open, the text is whole and nothing overflows.
    const measure = () => {
      if (el.classList.contains("line-clamp-3")) setOverflows(el.scrollHeight > el.clientHeight + 1)
    }
    const observer = new ResizeObserver(measure)
    observer.observe(el)

    return () => observer.disconnect()
  }, [])

  return (
    <section aria-label="The worker's summary" className="mt-2 rounded-lg bg-muted/40 px-3 py-2">
      <p
        ref={ref}
        className={cn(
          "text-sm leading-relaxed whitespace-pre-wrap text-foreground/80",
          !open && "line-clamp-3",
        )}
      >
        {text}
      </p>
      {(open || overflows) && (
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen((o) => !o)}
          className="mt-1 text-xs font-medium text-muted-foreground hover:text-foreground"
        >
          {open ? "Show less" : "Show all"}
        </button>
      )}
    </section>
  )
}

// DecisionRows is one decision: its header and text, its options, and the text its chosen option takes.
function DecisionRows({
  name,
  decision,
  number,
  pick,
  onPick,
  text,
  onText,
  takesText,
  onSubmit,
}: {
  name: string
  decision: Decision
  number?: number
  pick: number | undefined
  onPick: (n: number) => void
  text: string
  onText: (t: string) => void
  takesText: boolean
  onSubmit: () => void
}) {
  const options = decision.options

  // On an option, 1 to 9 pick one of this decision and Enter sends, as in t3code's panel.
  const pickByNumber = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") {
      e.preventDefault()
      onSubmit()

      return
    }

    const n = Number(e.key)
    if (n >= 1 && n <= Math.min(9, options.length)) {
      e.preventDefault()
      onPick(n - 1)
    }
  }

  return (
    <div
      role={number ? "group" : undefined}
      aria-label={number ? decision.header || `Decision ${number}` : undefined}
      className={cn(number && number > 1 && "mt-2 border-t pt-2")}
    >
      {decision.header && (
        <p className="mt-2 text-2xs font-medium tracking-wide text-muted-foreground uppercase">
          {number ? `${number}. ` : ""}
          {decision.header}
        </p>
      )}
      <p
        className={cn(
          "mb-2 text-sm leading-relaxed whitespace-pre-wrap text-foreground/90",
          decision.header ? "mt-0.5" : "mt-2",
        )}
      >
        {decision.text}
      </p>

      {options.length > 0 && (
        <fieldset className="m-0 -mx-2 flex flex-col border-0 p-0">
          <legend className="sr-only">Options</legend>
          {options.map((o, i) => (
            <label
              key={o.label}
              className={cn(
                "flex min-h-9 w-full cursor-pointer gap-2.5 rounded-lg px-2 py-1.5 text-sm has-focus-visible:ring-1 has-focus-visible:ring-primary/40 has-focus-visible:ring-inset max-sm:min-h-11",
                o.description ? "items-start" : "items-center",
                pick === i ? "text-foreground" : "text-foreground/70 hover:bg-muted/30",
              )}
            >
              <input
                type="radio"
                name={name}
                checked={pick === i}
                onChange={() => onPick(i)}
                onKeyDown={pickByNumber}
                className="peer sr-only"
              />
              {/* The h-5 slots match the label's line, so the mark and the key stay on its first line (t3code). */}
              <span
                aria-hidden
                className={cn("flex shrink-0 items-center", o.description && "h-5")}
              >
                <span
                  className={cn(
                    "relative inline-flex size-4 shrink-0 items-center justify-center rounded-full border",
                    pick === i
                      ? "border-primary bg-primary"
                      : "border-input bg-background dark:bg-input/32",
                  )}
                >
                  {pick === i && <span className="size-1.5 rounded-full bg-primary-foreground" />}
                </span>
              </span>
              <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className="font-medium">{o.label}</span>
                {o.description && (
                  <span className="text-xs leading-snug text-muted-foreground">
                    {o.description}
                  </span>
                )}
              </span>
              {i < 9 && (
                <kbd className="hidden h-[18px] min-w-[18px] items-center justify-center rounded border px-1 font-sans text-2xs text-muted-foreground sm:inline-flex">
                  {i + 1}
                </kbd>
              )}
            </label>
          ))}
        </fieldset>
      )}

      {takesText && (
        <textarea
          aria-label={options.length ? "Details" : "Answer"}
          rows={2}
          value={text}
          placeholder={options.length ? "Add details (optional)" : "Your answer"}
          onChange={(e) => onText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key !== "Enter" || e.shiftKey || e.nativeEvent.isComposing) return
            e.preventDefault()
            onSubmit()
          }}
          className="mt-1.5 block field-sizing-content max-h-40 min-h-[calc(2lh+0.5rem)] w-full resize-none border-t bg-transparent py-2.5 text-base leading-relaxed outline-none placeholder:text-muted-foreground sm:text-sm"
        />
      )}
    </div>
  )
}

function errorText(error: unknown) {
  if (error && typeof error === "object" && "message" in error) return String(error.message)

  return "try again"
}
