import { ChevronRight, ListChecks, User } from "lucide-react"

import type { Todo, TodoEvent } from "@/api/generated/types.gen"
import { AgentAvatar } from "@/components/agent-avatar"
import {
  ChainOfThought,
  ChainOfThoughtContent,
  ChainOfThoughtHeader,
  ChainOfThoughtStep,
} from "@/components/chain-of-thought"
import {
  Timeline,
  TimelineContent,
  TimelineHeader,
  TimelineIndicator,
  TimelineItem,
  TimelineSeparator,
  TimelineTitle,
} from "@/components/reui/timeline"
import { Badge } from "@/components/ui/badge"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import { Message, MessageContent, MessageHeader } from "@/components/ui/message"
import { Composer } from "@/features/chat/composer"
import Markdown from "@/features/chat/markdown"
import { since } from "@/lib/time"
import { cn } from "@/lib/utils"

import { actorName, eventLine, reportOf, threadOf, writeOf, type ThreadEntry } from "./inbox-thread"
import { useWriteTodo } from "./use-inbox"

// An Inbox item's comments (issue #1143), on a line as GitHub's issue timeline: ReUI's Timeline holds each entry
// with its icon on the line; a comment is a card of the chat's message parts with the chat's Markdown; changes in a
// row by one actor fold into AI Elements' chain of thought, adapted in components/chain-of-thought.tsx; a Bot's
// report folds its <details> sections in the app's Collapsible, as CodeRabbit's summary comment; and the chat's
// composer writes, handing the item over when no Bot has it.

const clock = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" })
const clockOf = (iso: string) => clock.format(new Date(iso))

// The indicator and line of an entry: an icon of 1.5rem on a line of 2px, as ReUI's timeline with icons sets them.
const itemClass = "group-data-[orientation=vertical]/timeline:ms-10"
const indicatorClass =
  "flex size-6 items-center justify-center overflow-hidden border-none bg-muted text-muted-foreground group-data-[orientation=vertical]/timeline:-left-7"
const separatorClass =
  "bg-border group-data-[orientation=vertical]/timeline:-left-7 group-data-[orientation=vertical]/timeline:h-[calc(100%-1.5rem-0.5rem)] group-data-[orientation=vertical]/timeline:translate-y-7"

export function CommentTimeline({
  events,
  loading,
  now,
  from,
  botName,
}: {
  events?: readonly TodoEvent[]
  loading: boolean
  now: number
  // from is where the item came from, said on its first line ("Zalo · Laura Nguyen").
  from?: string
  botName: (id: string) => string
}) {
  const entries = threadOf(events)
  if (entries.length === 0) {
    return <p className="text-sm text-muted-foreground">{loading ? "Reading…" : "Nothing yet."}</p>
  }

  return (
    // A history, not a stepper: no step is marked completed, so the line keeps the border's colour, as GitHub's.
    <Timeline value={0} render={<ol />}>
      {entries.map((entry, index) => (
        <TimelineItem
          key={`${entry.kind}:${entry.id}`}
          step={index + 1}
          render={<li />}
          className={itemClass}
        >
          <Entry entry={entry} now={now} from={from} botName={botName} />
        </TimelineItem>
      ))}
    </Timeline>
  )
}

function Entry({
  entry,
  now,
  from,
  botName,
}: {
  entry: ThreadEntry
  now: number
  from?: string
  botName: (id: string) => string
}) {
  switch (entry.kind) {
    case "comment":
      return (
        <>
          <TimelineSeparator className={separatorClass} />
          <TimelineIndicator className={indicatorClass}>
            <Face actor={entry.actor} botName={botName} />
          </TimelineIndicator>
          <TimelineContent>
            <Comment entry={entry} now={now} botName={botName} />
          </TimelineContent>
        </>
      )
    case "activity": {
      const line = eventLine(
        entry.event,
        botName,
        entry.event.kind === "created" ? from : undefined,
      )
      const Icon = line.icon
      const reason = (entry.event.after as Record<string, unknown>).close_reason

      return (
        <>
          <TimelineSeparator className={separatorClass} />
          <TimelineIndicator
            className={cn(indicatorClass, line.done && "bg-closed text-primary-foreground")}
          >
            <Icon className="size-3.5" />
          </TimelineIndicator>
          <TimelineHeader className="flex min-h-6 flex-wrap items-baseline gap-x-2 pt-0.5">
            <TimelineTitle
              render={<p />}
              className={cn(
                "font-normal text-muted-foreground",
                line.done && "font-medium text-closed",
              )}
            >
              {line.words}
            </TimelineTitle>
            <time dateTime={entry.at} className="text-2xs text-muted-foreground tabular-nums">
              {since(entry.at, now)}
            </time>
          </TimelineHeader>
          {line.done && typeof reason === "string" && reason && (
            <TimelineContent className="pt-1 break-words">{reason}</TimelineContent>
          )}
        </>
      )
    }
    case "steps":
      return (
        <>
          <TimelineSeparator className={separatorClass} />
          <TimelineIndicator className={indicatorClass}>
            <ListChecks className="size-3.5" />
          </TimelineIndicator>
          <TimelineContent className="pt-0.5">
            <ChainOfThought>
              <ChainOfThoughtHeader
                label={`${actorName(entry.actor, botName)} did ${entry.steps.length} steps, ${clockOf(entry.from)} to ${clockOf(entry.to)}`}
              >
                {actorName(entry.actor, botName)} did {entry.steps.length} steps
                <span className="text-2xs tabular-nums">
                  {" "}
                  · {clockOf(entry.from)}–{clockOf(entry.to)}
                </span>
              </ChainOfThoughtHeader>
              <ChainOfThoughtContent>
                {entry.steps.map((step) => {
                  const line = eventLine(step, botName)

                  return (
                    <ChainOfThoughtStep
                      key={step.id}
                      icon={line.icon}
                      label={line.words}
                      description={
                        <time dateTime={step.at} className="tabular-nums">
                          {clockOf(step.at)}
                        </time>
                      }
                    />
                  )
                })}
              </ChainOfThoughtContent>
            </ChainOfThought>
          </TimelineContent>
        </>
      )
  }
}

// Face is who wrote a comment on the line: a Bot's face, as the chat shows it, or the owner's mark.
function Face({ actor, botName }: { actor: TodoEvent["actor"]; botName: (id: string) => string }) {
  if (actor.kind === "bot") return <AgentAvatar name={botName(actor.id ?? "")} className="size-6" />

  return <User className="size-3.5" />
}

// Comment is a boxed card, as GitHub's issue comment: who wrote it, a Bot's tag, when, then what was written.
function Comment({
  entry,
  now,
  botName,
}: {
  entry: Extract<ThreadEntry, { kind: "comment" }>
  now: number
  botName: (id: string) => string
}) {
  return (
    <article
      aria-label={`Comment by ${actorName(entry.actor, botName)}`}
      className="overflow-hidden rounded-lg border bg-card"
    >
      <Message>
        <MessageContent>
          <div className="border-b bg-muted/40 py-1.5">
            <MessageHeader>
              <span className="flex min-w-0 items-center gap-2">
                <span className="font-medium text-foreground">
                  {actorName(entry.actor, botName)}
                </span>
                {entry.actor.kind === "bot" && <Badge variant="outline">Bot</Badge>}
                <time dateTime={entry.at} className="font-normal tabular-nums">
                  {since(entry.at, now)}
                </time>
              </span>
            </MessageHeader>
          </div>
          <div className="px-3 pb-2.5">
            <Report text={entry.text} />
          </div>
        </MessageContent>
      </Message>
    </article>
  )
}

// Report shows a comment with its <details> sections folded under their titles (reportOf).
function Report({ text }: { text: string }) {
  return (
    <div className="flex flex-col gap-2">
      {reportOf(text).map((part, index) =>
        part.kind === "text" ? (
          <Markdown key={index} text={part.text} streaming={false} />
        ) : (
          <div key={index} className="rounded-md border">
            <Collapsible>
              <CollapsibleTrigger
                render={
                  <button
                    type="button"
                    aria-label={part.title}
                    className="flex w-full cursor-pointer items-center gap-1.5 rounded-md px-2.5 py-1.5 text-start text-sm font-medium text-foreground outline-none hover:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring"
                  />
                }
              >
                <ChevronRight className="size-3.5 shrink-0 text-muted-foreground transition-transform [[data-panel-open]_&]:rotate-90" />
                {part.title}
              </CollapsibleTrigger>
              <CollapsibleContent>
                <div className="border-t px-3 py-2">
                  <Report text={part.body} />
                </div>
              </CollapsibleContent>
            </Collapsible>
          </div>
        ),
      )}
    </div>
  )
}

// CommentComposer is the chat's composer under the comments: on an item no Bot has, "Hand to Hypo" hands it to the
// main Bot with the text; on one a Bot has, Send writes to that Bot; on a closed one, "Reopen and send" (writeOf).
export function CommentComposer({
  todo,
  mainBot,
  botName,
}: {
  todo: Todo
  mainBot: string
  botName: (id: string) => string
}) {
  const write = writeOf(todo, mainBot, botName)
  const send = useWriteTodo()

  return (
    <Composer
      botName={write.bot ? botName(write.bot) : "Hypo"}
      placeholder={write.placeholder}
      submit={write.label}
      working={false}
      onStop={() => undefined}
      onSend={(text) => send.mutateAsync({ todo, write, text })}
    />
  )
}
