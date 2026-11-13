import { useMutation, useQueryClient } from "@tanstack/react-query"
import { MessageCircleQuestion } from "lucide-react"
import { useState } from "react"

import "@/api/client"
import { answerQuestion } from "@/api/generated/sdk.gen"
import type { Question } from "@/api/generated/types.gen"
import { since } from "@/lib/time"
import { cn } from "@/lib/utils"

import { boardKey, questionsKey } from "@/features/work/use-work"

// QuestionForm answers a worker's question, after t3code's pending user-input panel (MIT, Copyright (c) 2026 T3
// Tools Inc., https://github.com/pingdotgg/t3code; chat/ComposerPendingUserInputPanel.tsx): the options as radio
// rows that 1 to 9 pick, and text where the chosen option, or a question without options, takes it. Enter sends,
// never while an input method composes. The first answer wins on the server; a second is refused.
export function QuestionForm({
  question,
  onAnswered,
}: {
  question: Question
  onAnswered?: () => void
}) {
  const queryClient = useQueryClient()
  const [pick, setPick] = useState<number | undefined>(question.options.length ? 0 : undefined)
  const [text, setText] = useState("")

  const chosen = pick === undefined ? undefined : question.options[pick]
  const takesText = question.options.length === 0 || !!chosen?.allows_text
  const ready = question.options.length ? pick !== undefined : text.trim() !== ""

  const send = useMutation({
    mutationFn: async () =>
      answerQuestion({
        path: { question_id: question.id },
        body: { option: pick, text: takesText && text.trim() ? text.trim() : undefined },
      }),
    onSuccess: () => {
      for (const queryKey of [boardKey, questionsKey, ["run"]])
        void queryClient.invalidateQueries({ queryKey })
      onAnswered?.()
    },
  })

  const submit = () => {
    if (ready && !send.isPending) send.mutate()
  }

  // On an option, 1 to 9 pick one and Enter sends, as in t3code's panel.
  const pickByNumber = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") {
      e.preventDefault()
      submit()

      return
    }

    const n = Number(e.key)
    if (n >= 1 && n <= Math.min(9, question.options.length)) {
      e.preventDefault()
      setPick(n - 1)
    }
  }

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
      <p className="my-2 text-sm leading-relaxed whitespace-pre-wrap text-foreground/90">
        {question.text}
      </p>

      {question.options.length > 0 && (
        <fieldset className="m-0 -mx-2 flex flex-col border-0 p-0">
          <legend className="sr-only">Options</legend>
          {question.options.map((o, i) => (
            <label
              key={o.label}
              className={cn(
                "flex min-h-9 w-full cursor-pointer items-center gap-2.5 rounded-lg px-2 py-1.5 text-sm has-focus-visible:ring-1 has-focus-visible:ring-primary/40 has-focus-visible:ring-inset max-sm:min-h-11",
                pick === i ? "text-foreground" : "text-foreground/70 hover:bg-muted/30",
              )}
            >
              <input
                type="radio"
                name={`q-${question.id}`}
                checked={pick === i}
                onChange={() => setPick(i)}
                onKeyDown={pickByNumber}
                className="peer sr-only"
              />
              <span
                aria-hidden
                className={cn(
                  "relative inline-flex size-4 shrink-0 items-center justify-center rounded-full border",
                  pick === i
                    ? "border-primary bg-primary"
                    : "border-input bg-background dark:bg-input/32",
                )}
              >
                {pick === i && <span className="size-1.5 rounded-full bg-primary-foreground" />}
              </span>
              <span className="min-w-0 flex-1 font-medium">{o.label}</span>
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
          aria-label={question.options.length ? "Details" : "Answer"}
          rows={2}
          value={text}
          placeholder={question.options.length ? "Add details (optional)" : "Your answer"}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key !== "Enter" || e.shiftKey || e.nativeEvent.isComposing) return
            e.preventDefault()
            submit()
          }}
          className="mt-1.5 block field-sizing-content max-h-40 min-h-[calc(2lh+0.5rem)] w-full resize-none border-t bg-transparent py-2.5 text-base leading-relaxed outline-none placeholder:text-muted-foreground sm:text-sm"
        />
      )}

      <div className="flex items-center gap-2 pt-1">
        <span className="text-2xs text-muted-foreground">
          {send.error ? (
            <span className="text-destructive">Not sent: {errorText(send.error)}</span>
          ) : question.options.length ? (
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

function errorText(error: unknown) {
  if (error && typeof error === "object" && "message" in error) return String(error.message)

  return "try again"
}
