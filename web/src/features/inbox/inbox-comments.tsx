import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { ChevronRight, ListChecks, User } from "lucide-react"
import { useState } from "react"

import { getChatOptions } from "@/api/generated/@tanstack/react-query.gen"
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
import { ArtifactCard } from "@/features/artifacts/artifact-card"
import { useArtifact } from "@/features/artifacts/artifact-state"
import { AttachDrop } from "@/features/chat/attach-drop"
import { useAttachments } from "@/features/chat/attachments"
import { newChatId } from "@/features/chat/chat-id"
import { Composer } from "@/features/chat/composer"
import { MessageImages } from "@/features/chat/message-files"
import { ReplyFiles } from "@/features/chat/reply-files"
import { imageType } from "@/features/files/files-state"
import { useServices } from "@/features/settings/use-integrations"
import { problemOf } from "@/lib/server-error"
import { since } from "@/lib/time"
import { cn } from "@/lib/utils"

import { InboxMarkdown } from "./inbox-markdown"
import { sourceTarget } from "./inbox-state"
import {
  actorName,
  eventLine,
  reportOf,
  threadOf,
  waitingOf,
  waitingPlace,
  writeOf,
  type ThreadEntry,
  type Waiting,
} from "./inbox-thread"
import { useWriteTodo } from "./use-inbox"
import { useMessage, type KeptChats } from "./use-origin"

// An Inbox item's comments (issue #1143), on a line as GitHub's issue timeline: ReUI's Timeline holds each entry
// with its icon on the line; a comment is a card of the chat's message parts with the chat's Markdown, its ids as
// links (InboxMarkdown, issue #1173), and a Bot's files and Artifacts under its text, drawn by the chat's ReplyFiles
// and ArtifactCard, as Gitea draws a comment's attachments under it (issue #1207); changes in a
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
  projectName,
  source,
  chats,
}: {
  events?: readonly TodoEvent[]
  loading: boolean
  now: number
  // from is where the item came from, said on its first line ("Zalo · Laura Nguyen").
  from?: string
  botName: (id: string) => string
  // projectName names a project by its id (issue #1157).
  projectName?: (id: string) => string | undefined
  // source and chats place the message that shows whom the item waits on (issue #1142).
  source?: Todo["source"]
  chats?: KeptChats
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
          <Entry
            entry={entry}
            now={now}
            from={from}
            botName={botName}
            projectName={projectName}
            source={source}
            chats={chats}
          />
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
  projectName,
  source,
  chats,
}: {
  entry: ThreadEntry
  now: number
  from?: string
  botName: (id: string) => string
  projectName?: (id: string) => string | undefined
  source?: Todo["source"]
  chats?: KeptChats
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
        projectName,
      )
      const Icon = line.icon
      const reason = (entry.event.after as Record<string, unknown>).close_reason
      const waiting = waitingOf(entry.event.after)

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
          {waiting?.quote && (
            <TimelineContent className="pt-1">
              <WaitingQuote waiting={waiting} source={source} chats={chats} now={now} />
            </TimelineContent>
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
                  const line = eventLine(step, botName, undefined, projectName)

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

// WaitingQuote is the words that show whom an item waits on, under its line, where a close's reason goes (issue
// #1142), as the detail's Said shows a source's words: with who wrote them and when, as the server keeps the item's
// Zalo or WhatsApp message; a link to the meeting's line or the Gmail thread; the name alone for Lark.
function WaitingQuote({
  waiting,
  source,
  chats,
  now,
}: {
  waiting: Waiting
  source?: Todo["source"]
  chats?: KeptChats
  now: number
}) {
  const place = source ? waitingPlace(source, waiting) : ({ kind: "name" } as const)
  const kept =
    place.kind === "message" ? chats?.get(`${place.integration}:${place.chat}`) : undefined
  const message = useMessage(
    place.kind === "message"
      ? {
          integration: place.integration,
          label: kept?.label ?? source?.account,
          chat: place.chat,
          id: place.id,
        }
      : {},
  )
  const m = message.data
  const who = m?.from_me ? "You" : m?.sender_name || waiting.by

  return (
    <figure className="flex flex-col gap-1">
      <blockquote className="border-l-2 border-border-strong py-0.5 ps-3 text-sm whitespace-pre-wrap text-foreground/85">
        {waiting.quote}
      </blockquote>
      <figcaption className="flex flex-wrap items-baseline gap-x-1.5 text-xs text-muted-foreground">
        <span className="font-medium text-foreground">{who}</span>
        {m?.at && (
          <time dateTime={m.at} className="tabular-nums">
            {since(m.at, now)}
          </time>
        )}
        {place.kind === "meeting" && (
          <Link
            to="/meetings/$meetingId"
            params={{ meetingId: place.meeting }}
            search={place.line ? { line: place.line } : {}}
            className="text-link hover:underline"
          >
            {place.line ? `Line ${place.line} of the meeting` : "The meeting"}
          </Link>
        )}
        {place.kind === "mail" && source && <ThreadLink source={source} />}
      </figcaption>
    </figure>
  )
}

// ThreadLink opens the item's Gmail thread in the owner's account the Bot read it with (sourceTarget).
function ThreadLink({ source }: { source: Todo["source"] }) {
  const services = useServices()
  const google = services.data?.find((s) => s.id === "google")?.connections ?? []
  const target = sourceTarget({ source }, (label) => google.find((c) => c.label === label)?.account)
  if (!target || !("href" in target)) return null

  return (
    <a href={target.href} target="_blank" rel="noreferrer" className="text-link hover:underline">
      The thread
    </a>
  )
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
  const images = entry.attachments?.filter((file) => imageType(file.path)) ?? []
  const files = entry.attachments
    ? entry.attachments.filter((file) => !imageType(file.path)).map((file) => file.path)
    : entry.files

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
            {entry.chat && <MessageImages chat={entry.chat} images={images} inComment />}
            {entry.chat && files.length > 0 && <ReplyFiles chat={entry.chat} paths={files} />}
            {entry.artifacts.length > 0 && (
              <ul aria-label="Artifacts" className="mt-2 flex flex-col">
                {entry.artifacts.map((id) => (
                  <li key={id}>
                    <CommentArtifact id={id} />
                  </li>
                ))}
              </ul>
            )}
          </div>
        </MessageContent>
      </Message>
    </article>
  )
}

// CommentArtifact is an Artifact a Bot's comment carries (issue #1207), drawn by the chat's own card once it is
// read; one the server refuses says the server's message in its place.
function CommentArtifact({ id }: { id: string }) {
  const artifact = useArtifact(id)
  if (artifact.data) return <ArtifactCard card={artifact.data} />
  if (artifact.isError) {
    return (
      <p className="pb-2 text-sm text-destructive">
        An Artifact could not be opened: {problemOf(artifact.error).message}
      </p>
    )
  }

  return <p className="pb-2 text-sm text-muted-foreground">Reading the Artifact…</p>
}

// Report shows a comment with its <details> sections folded under their titles (reportOf).
function Report({ text }: { text: string }) {
  return (
    <div className="flex flex-col gap-2">
      {reportOf(text).map((part, index) =>
        part.kind === "text" ? (
          <InboxMarkdown key={index} text={part.text} />
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
  // An item given back can still point at its former Bot's chat. Check
  // which Bot owns it before deciding where to upload the Hand's files.
  const checkChat = !!todo.chat_id && todo.assignee.kind !== "bot"
  const previous = useQuery({
    ...getChatOptions({ path: { chat_id: todo.chat_id ?? "" } }),
    enabled: checkChat,
  })
  const existing = checkChat
    ? previous.data?.bot === write.bot
      ? todo.chat_id
      : undefined
    : todo.chat_id
  const send = useWriteTodo()
  const [sending, setSending] = useState(false)
  const [reservation, setReservation] = useState(() => ({
    previous: todo.chat_id,
    id: newChatId(),
  }))
  if (reservation.previous !== todo.chat_id) {
    setReservation({ previous: todo.chat_id, id: newChatId() })
  }

  const chat = existing ?? reservation.id
  const bot = write.bot || undefined
  const checking = checkChat && previous.isPending
  const canAttach = !!(existing || bot) && (!checkChat || previous.isSuccess)
  // A pass can arrive while Send is in flight. Its attachments still belong
  // to the outgoing message until the server answers.
  const attachments = useAttachments(chat, bot, undefined, sending || !canAttach)
  const filesWaiting = !canAttach && attachments.items.length > 0

  return (
    <AttachDrop attachments={canAttach ? attachments : undefined}>
      <Composer
        attachments={canAttach || filesWaiting ? attachments : undefined}
        botName={bot ? botName(bot) : "Hypo"}
        placeholder={write.placeholder}
        submit={write.label}
        working={false}
        disabled={checking || filesWaiting}
        onStop={() => undefined}
        onSendingChange={setSending}
        onSend={(text, files) =>
          send.mutateAsync({ todo, write, text, files, chatId: existing ? undefined : chat })
        }
      />
    </AttachDrop>
  )
}
