import { ExternalLink, Telescope, TriangleAlert } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { buttonVariants } from "@/components/ui/button"
import { cn } from "@/lib/utils"

import { MusePageLink } from "./muse-screen"
import { linkHost, museProblem, parseMuseAnswer, type MuseAnswer } from "./muse-state"

const timeFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" })

function when(at: string) {
  const date = new Date(at)

  return Number.isNaN(date.getTime()) ? "" : timeFormat.format(date)
}

// MuseCard is a muse_ask call's answer under the chat's work (issue #782, the UX table's Chat row): the account and
// the time, Open in Muse (the thread in the owner's own Muse app), the answer, and its Sources with their link and
// date, as ai-elements' Sources lists a reply's sources; an answer with no link is marked unverified. "Muse asks"
// names the thread to answer it in; a sign-in or quota error links to the Muse page.
export function MuseCard({ output, error }: { output?: string; error?: boolean }) {
  const answer = parseMuseAnswer(output)

  if (!answer) {
    if (!error || !output) return null

    return (
      <div className="pb-4">
        <section
          aria-label="Muse could not be asked"
          className="flex w-full max-w-xl flex-col gap-1 rounded-xl border bg-card p-3 text-sm shadow-xs"
        >
          <span className="flex items-center gap-2 font-medium text-destructive">
            <TriangleAlert className="size-4" aria-hidden />
            Muse could not be asked
          </span>
          <span className="text-muted-foreground">{output}</span>
          {museProblem(output) && (
            <span>
              <MusePageLink />
            </span>
          )}
        </section>
      </div>
    )
  }

  return (
    <div className="pb-4">
      <MuseAnswerView answer={answer} />
    </div>
  )
}

function MuseAnswerView({ answer }: { answer: MuseAnswer }) {
  const answered = answer.outcome === "answered"

  return (
    <section
      aria-label={`Muse's answer from ${answer.account_label}`}
      className="flex w-full max-w-xl flex-col gap-2 rounded-xl border bg-card p-3 shadow-xs"
    >
      <header className="flex flex-wrap items-center gap-2 text-sm">
        <span className="flex size-6 items-center justify-center rounded-md border bg-muted">
          <Telescope className="size-3.5" aria-hidden />
        </span>
        <span className="font-medium text-foreground">Muse</span>
        <span className="text-xs text-muted-foreground">
          {answer.account_label} · {when(answer.at)}
        </span>
        {answer.unverified && answered && (
          <Badge variant="warning" title="The answer cites no link: check it before relying on it">
            Unverified
          </Badge>
        )}
        {answer.thread_url && (
          <a
            href={answer.thread_url}
            target="_blank"
            rel="noreferrer"
            className={cn(buttonVariants({ variant: "ghost", size: "sm" }), "ms-auto")}
          >
            <ExternalLink data-icon="inline-start" />
            Open in Muse
          </a>
        )}
      </header>
      {answered && answer.answer && (
        <p className="max-h-72 overflow-auto text-sm leading-relaxed whitespace-pre-wrap text-foreground/90">
          {answer.answer}
        </p>
      )}
      {answer.outcome === "asks_permission" && (
        <p className="text-sm text-foreground/90">
          {answer.note}
          {answer.thread_url && (
            <>
              {" "}
              <a
                href={answer.thread_url}
                target="_blank"
                rel="noreferrer"
                className="text-primary underline"
              >
                The thread
              </a>
            </>
          )}
        </p>
      )}
      {!answered && answer.outcome !== "asks_permission" && (
        <p className="text-sm text-muted-foreground">
          {answer.note}
          {answer.note && museProblem(answer.note) && <MusePageLink />}
        </p>
      )}
      {answered && (answer.links?.length ?? 0) > 0 && (
        <details className="text-sm">
          <summary className="cursor-pointer text-xs font-medium text-muted-foreground">
            Used {answer.links?.length} {answer.links?.length === 1 ? "source" : "sources"}
          </summary>
          <ol className="mt-1.5 flex flex-col gap-1">
            {answer.links?.map((link) => (
              <li key={link.url} className="flex min-w-0 items-baseline gap-2">
                <a
                  href={link.url}
                  target="_blank"
                  rel="noreferrer"
                  className="truncate text-primary hover:underline"
                  title={link.url}
                >
                  {link.text || linkHost(link.url)}
                </a>
                <span className="shrink-0 text-2xs text-muted-foreground">
                  {linkHost(link.url)} · {when(answer.at)}
                </span>
              </li>
            ))}
          </ol>
        </details>
      )}
      {(answer.files?.length ?? 0) > 0 && (
        <p className="text-xs text-muted-foreground">
          {answer.files?.filter((f) => f.path).length ?? 0} of {answer.files?.length} files kept
        </p>
      )}
    </section>
  )
}
