import { Link } from "@tanstack/react-router"
import {
  Bot as BotIcon,
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronUp,
  Copy,
  ExternalLink,
  Pencil,
  Send,
} from "lucide-react"
import { useEffect, useState, type ReactNode } from "react"

import type { Todo, TodoEvent } from "@/api/generated/types.gen"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { toast } from "@/components/ui/toast"
import { ApprovalCard } from "@/features/approvals/approval-card"
import { QuestionForm } from "@/features/questions/question-form"
import { useServices } from "@/features/settings/use-integrations"
import { errorText } from "@/lib/server-error"
import { since } from "@/lib/time"

import { HandDialog } from "./hand-dialog"
import { KindIcon, RowActions } from "./inbox-row"
import {
  detailActions,
  dueOf,
  groupNames,
  placeOf,
  sourceNames,
  sourceOf,
  sourceTarget,
  type Action,
  type GroupId,
  type Origin,
  type Row,
} from "./inbox-state"
import {
  canHand,
  canSend,
  handoverWords,
  sendQuestion,
  threadOf,
  throughWhom,
} from "./inbox-thread"
import { SourceIcon } from "./source-icon"
import { useCommentTodo, useSendDraft, useTodo } from "./use-inbox"
import { useOrigin, type KeptChats } from "./use-origin"
import { copyDraft } from "./use-row-actions"

// InboxDetail is the selected item, after OpenMausBot's InboxReading (its place in the list, stepping up and down)
// and Chatwoot's conversation view beside its list: a top bar with the item's actions, then where it came from, its
// fields, what was said, the draft to copy or send, and its thread. An Approval shows its card and a worker's
// question its form, the same parts and calls as in the chat and on Team. A to-do is handed to the main Bot with a
// prompt from the top bar (issue #1111). Send is the owner's own, through his account, after a confirmation: a Bot
// never sends for him (owner, 2026-11-17; Send pressed by him, 2026-11-23).
export function InboxDetail({
  row,
  now,
  position,
  phone,
  pending,
  botName,
  mainBot,
  chats,
  onBack,
  onStep,
  onAction,
  onAnswered,
  onSaveDraft,
}: {
  row: Row
  now: number
  position: { index: number; count: number; group: GroupId } | null
  phone: boolean
  pending: boolean
  botName: (id: string) => string
  // mainBot is the Bot "Hand to" gives an item to: the main Bot, by its id; empty while the Bots are read.
  mainBot: string
  chats: KeptChats
  onBack: () => void
  onStep: (by: -1 | 1) => void
  onAction: (action: Action, until?: number) => void
  onAnswered: () => void
  onSaveDraft: (draft: string, onSaved: () => void) => void
}) {
  const actions = detailActions(row, now)
  const [handing, setHanding] = useState(false)
  const handTo = mainBot ? botName(mainBot) : "Hypo"

  return (
    <article aria-label="Item" className="flex min-h-0 min-w-0 flex-1 flex-col">
      <header className="flex min-h-12 shrink-0 flex-wrap items-center gap-1 border-b px-2 py-1.5">
        {phone && (
          <Button variant="ghost" size="sm" onClick={onBack}>
            <ChevronLeft data-icon="inline-start" />
            Inbox
          </Button>
        )}
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Previous"
          title="Previous (k)"
          disabled={!position || position.index === 0}
          onClick={() => onStep(-1)}
        >
          <ChevronUp />
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Next"
          title="Next (j)"
          disabled={!position || position.index >= position.count - 1}
          onClick={() => onStep(1)}
        >
          <ChevronDown />
        </Button>
        {position && (
          <span className="px-1 text-xs text-muted-foreground tabular-nums">
            {position.index + 1} of {position.count} in {groupNames[position.group]}
          </span>
        )}
        {row.kind === "todo" && canHand(row.todo) && (
          <span className="ms-auto">
            <Button variant="outline" size="sm" disabled={pending} onClick={() => setHanding(true)}>
              <BotIcon data-icon="inline-start" />
              Hand to {handTo}
            </Button>
          </span>
        )}
        {actions.length > 0 && (
          <RowActions
            className={
              row.kind === "todo" && canHand(row.todo) ? "flex flex-wrap" : "ms-auto flex flex-wrap"
            }
            labelled
            actions={actions}
            onAction={(action, until) => !pending && onAction(action, until)}
          />
        )}
      </header>
      {handing && row.kind === "todo" && (
        <HandDialog
          todo={row.todo}
          bot={mainBot || undefined}
          botName={handTo}
          onClose={() => setHanding(false)}
        />
      )}

      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-4 sm:px-6">
        <div className="mx-auto flex max-w-2xl flex-col gap-5">
          {row.kind === "todo" && (
            <TodoBody
              row={row}
              now={now}
              botName={botName}
              chats={chats}
              onAnswered={onAnswered}
              onSaveDraft={onSaveDraft}
            />
          )}
          {row.kind === "approval" && (
            <>
              <Head
                row={row}
                now={now}
                path={`Approval · ${row.approval.integration}:${row.approval.label}`}
              >
                {row.approval.title}
              </Head>
              <Fields
                rows={[
                  ["Asked by", row.approval.asked_by],
                  ["Action", row.approval.action_name],
                  ["Account", `${row.approval.integration}:${row.approval.label}`],
                  ["Asked", since(row.approval.created_at, now)],
                ]}
              />
              <ApprovalCard approval={row.approval} onAnswered={onAnswered} />
              {row.approval.chat_id && (
                <OpenChat chatId={row.approval.chat_id}>Open in its chat</OpenChat>
              )}
            </>
          )}
          {row.kind === "question" && (
            <>
              <Head row={row} now={now} path="Team · a worker asks">
                {firstLine(row.question.text)}
              </Head>
              <QuestionForm key={row.question.id} question={row.question} onAnswered={onAnswered} />
              <OpenTask task={row.question.task_id} />
            </>
          )}
        </div>
      </div>
    </article>
  )
}

function firstLine(text: string) {
  return text.split("\n")[0] ?? text
}

function Head({
  row,
  now,
  path,
  children,
}: {
  row: Row
  now: number
  path: ReactNode
  children: ReactNode
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <p className="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
        <KindIcon row={row} now={now} className="size-3.5" />
        {path}
      </p>
      <h2 className="text-lg leading-snug font-semibold break-words text-foreground">{children}</h2>
    </div>
  )
}

function Fields({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-3 gap-y-2 text-sm">
      {rows.map(([label, value]) => (
        <div key={label} className="contents">
          <dt className="text-muted-foreground">{label}</dt>
          <dd className="min-w-0 break-words">{value}</dd>
        </div>
      ))}
    </dl>
  )
}

const openClass = "inline-flex w-fit items-center gap-1 text-sm text-link hover:underline"

// OpenChat leads to a chat of the app; OpenTask to a Task on Team.
function OpenChat({ chatId, children }: { chatId: string; children: ReactNode }) {
  return (
    <Link to="/chats/$chatId" params={{ chatId }} className={openClass}>
      <ExternalLink className="size-3.5" aria-hidden />
      {children}
    </Link>
  )
}

function OpenTask({ task }: { task: string }) {
  return (
    <Link to="/team" search={{ task }} className={openClass}>
      <ExternalLink className="size-3.5" aria-hidden />
      Open on Team
    </Link>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section aria-label={title} className="flex flex-col gap-2">
      <h3 className="text-xs font-medium tracking-wide text-muted-foreground uppercase">{title}</h3>
      {children}
    </section>
  )
}

// whoOf names who does a to-do: the owner, a Bot by its name, a person, or nobody known yet.
function whoOf(todo: Todo, botName: (id: string) => string) {
  switch (todo.assignee.kind) {
    case "me":
      return "You"
    case "bot":
      return botName(todo.assignee.name ?? "")
    case "person":
      return todo.assignee.name ?? "Someone"
    default:
      return "Not known yet"
  }
}

// OpenSource leads to where a to-do was read (sourceTarget): its chat, a Gmail thread in the owner's account the
// Bot read it with, a meeting at its line, or the message's web address.
function OpenSource({ todo, name }: { todo: Todo; name: string }) {
  const services = useServices()
  const google = services.data?.find((s) => s.id === "google")?.connections ?? []
  const target = sourceTarget(todo, (label) => google.find((c) => c.label === label)?.account)
  if (!target) return null
  if ("chat" in target) return <OpenChat chatId={target.chat}>Open in {name}</OpenChat>
  if ("meeting" in target) {
    return (
      <Link
        to="/meetings/$meetingId"
        params={{ meetingId: target.meeting }}
        search={target.line ? { line: target.line } : {}}
        className={openClass}
      >
        <ExternalLink className="size-3.5" aria-hidden />
        Open meeting
      </Link>
    )
  }

  return (
    <a href={target.href} target="_blank" rel="noreferrer" className={openClass}>
      <ExternalLink className="size-3.5" aria-hidden />
      Open in {name}
    </a>
  )
}

// Said is what was said where the item came from, with who said it, as OpenMausBot's InboxReading shows the sender
// above their words: the message's sender or the meeting's speaker, and the meeting's name.
function Said({ todo, origin, children }: { todo: Todo; origin: Origin; children: ReactNode }) {
  const who = origin.fromMe ? "You" : origin.sender || origin.speaker
  const where = origin.meeting

  return (
    <Section title="Said">
      <figure className="flex flex-col gap-1">
        {(who || where) && (
          <figcaption className="text-xs text-muted-foreground">
            {who && <span className="font-medium text-foreground">{who}</span>}
            {who && where && " · "}
            {where}
          </figcaption>
        )}
        <blockquote className="border-l-2 border-border-strong py-0.5 ps-3 text-sm whitespace-pre-wrap text-foreground/85">
          {todo.source.quote}
        </blockquote>
      </figure>
      {children}
    </Section>
  )
}

function TodoBody({
  row,
  now,
  botName,
  chats,
  onAnswered,
  onSaveDraft,
}: {
  row: Extract<Row, { kind: "todo" }>
  now: number
  botName: (id: string) => string
  chats: KeptChats
  onAnswered: () => void
  onSaveDraft: (draft: string, onSaved: () => void) => void
}) {
  const { todo } = row
  const detail = useTodo(todo.id)
  const origin = useOrigin(row, chats)
  const kind = sourceOf(row)
  const source = sourceNames[kind]
  const place = placeOf(row, origin, botName)
  const due = dueOf(todo, now)
  // Only what is said shows: an empty field is left out, not written "Not said" (issue #1110).
  const doneWhen =
    todo.done_when ||
    (todo.why_open === "asked_directly" ? "You answer in this chat; it closes by itself" : "")
  const linked = linkedOf(todo, botName)
  const fields: [string, ReactNode][] = [
    ["Who", throughWhom(todo, whoOf(todo, botName), botName)],
    ...(due ? ([["Date", due.text]] as [string, ReactNode][]) : []),
    ...(doneWhen ? ([["Done when", doneWhen]] as [string, ReactNode][]) : []),
    ...(linked ? ([["Linked", linked]] as [string, ReactNode][]) : []),
    ...(todo.snoozed_until && Date.parse(todo.snoozed_until) > now
      ? ([["Snoozed", `until ${new Date(todo.snoozed_until).toLocaleString()}`]] as [
          string,
          ReactNode,
        ][])
      : []),
    ...(todo.close_reason ? ([["Closed", todo.close_reason]] as [string, ReactNode][]) : []),
  ]

  return (
    <>
      <Head
        row={row}
        now={now}
        path={
          <>
            <SourceIcon source={kind} className="[&>svg]:size-3.5" />
            <span className="min-w-0 truncate">
              {kind === "hand" ? source : [source, place].filter(Boolean).join(" › ")}
            </span>
          </>
        }
      >
        {todo.title}
      </Head>
      <Fields rows={fields} />
      {todo.notes && <p className="text-sm whitespace-pre-wrap text-foreground/85">{todo.notes}</p>}
      {todo.source.quote && (
        <Said todo={todo} origin={origin}>
          <OpenSource todo={todo} name={source} />
        </Said>
      )}
      {row.approval && (
        <Section title="Waits for your approval">
          <ApprovalCard approval={row.approval} onAnswered={onAnswered} />
        </Section>
      )}
      <Draft
        key={`${todo.id}:${todo.revision}`}
        todo={todo}
        source={source}
        recipient={origin.chat || origin.sender}
        onSave={onSaveDraft}
      />
      <Section title="Thread">
        {detail.data?.events?.length ? (
          <ol className="flex flex-col gap-2 text-sm">
            {threadOf(detail.data.events).map((e) =>
              e.kind === "comment" ? (
                <li
                  key={e.id}
                  className="flex flex-col gap-1 rounded-lg border bg-surface-raised px-3 py-2"
                >
                  <span className="flex items-baseline gap-2 text-xs">
                    <span className="font-medium text-foreground">
                      {actorName(e.actor, botName)}
                    </span>
                    <span className="text-2xs text-muted-foreground tabular-nums">
                      {since(e.at, now)}
                    </span>
                  </span>
                  <p className="break-words whitespace-pre-wrap text-foreground/90">{e.text}</p>
                </li>
              ) : (
                <li key={e.id} className="flex items-baseline gap-2 px-1">
                  <span className="min-w-0 flex-1 text-muted-foreground">
                    {eventWords(e.event, botName)}
                  </span>
                  <span className="shrink-0 text-2xs text-muted-foreground tabular-nums">
                    {since(e.at, now)}
                  </span>
                </li>
              ),
            )}
          </ol>
        ) : (
          <p className="text-sm text-muted-foreground">
            {detail.isPending ? "Reading…" : "Nothing yet."}
          </p>
        )}
        {todo.chat_id && (
          <OpenChat chatId={todo.chat_id}>
            Open {todo.assignee.kind === "bot" ? `${botName(todo.assignee.name ?? "")}'s` : "the"}{" "}
            chat for this item
          </OpenChat>
        )}
        <CommentBox key={todo.id} todo={todo} />
      </Section>
    </>
  )
}

function linkedOf(todo: Todo, botName: (id: string) => string): ReactNode {
  if (todo.work?.kind === "chat")
    return (
      <Link
        to="/chats/$chatId"
        params={{ chatId: todo.work.id }}
        className="text-link hover:underline"
      >
        {botName(todo.work.bot)}'s chat
      </Link>
    )
  if (todo.work?.kind === "run") return `${botName(todo.work.bot)}'s Routine run`
  if (todo.duplicate_of) return `A duplicate of ${todo.duplicate_of}`

  return null
}

// copiedFor is how long Copy says "Copied", as OpenMausBot's copy status stays until the next action.
const copiedFor = 2_000

// Draft is the reply a Bot wrote for the owner to send himself, after OpenMausBot's InboxAnswer: Copy is the card's
// primary button and says "Copied" once it copied, beside Edit, Open in its source, and Send, which the owner
// presses and confirms, through his own account to the item's own chat or thread (issue #1111). An item someone
// asked the owner about and that has no draft yet says that a Bot drafts one; any other needs none and shows nothing.
function Draft({
  todo,
  source,
  recipient,
  onSave,
}: {
  todo: Todo
  source: string
  // recipient names who a Send reaches, as the server keeps the chat.
  recipient?: string
  onSave: (draft: string, onSaved: () => void) => void
}) {
  const [editing, setEditing] = useState(false)
  const [text, setText] = useState(todo.draft)
  const sendable = canSend(todo)
  // shown is what Send sends: the text as it is on screen, edits included.
  const shown = editing ? text : todo.draft
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    if (!copied) return
    const timer = window.setTimeout(() => setCopied(false), copiedFor)

    return () => window.clearTimeout(timer)
  }, [copied])

  if (!todo.draft && !editing) {
    if (todo.why_open !== "asked_directly") return null

    return (
      <Section title="Draft">
        <p className="text-sm text-muted-foreground">
          No draft yet. A Bot drafts a reply to a client, a lead or a boss for you to send; a
          password or a code is never written into the Inbox.
        </p>
      </Section>
    )
  }

  return (
    <Section title="Draft">
      {editing ? (
        <Textarea
          aria-label="Draft"
          value={text}
          rows={5}
          onChange={(e) => setText(e.target.value)}
        />
      ) : (
        <p className="rounded-lg border bg-surface-raised px-3 py-2.5 text-sm whitespace-pre-wrap">
          {todo.draft}
        </p>
      )}
      <div className="flex flex-wrap items-center gap-2">
        {editing ? (
          <>
            <Button size="sm" onClick={() => onSave(text, () => setEditing(false))}>
              Save
            </Button>
            {sendable && <SendDraft todo={todo} text={shown} recipient={recipient} />}
            <Button size="sm" variant="ghost" onClick={() => setEditing(false)}>
              Cancel
            </Button>
          </>
        ) : (
          <>
            <Button
              size="sm"
              aria-label={copied ? "Copied" : "Copy"}
              onClick={() => void copyDraft(todo.draft).then(setCopied)}
            >
              {copied ? <Check data-icon="inline-start" /> : <Copy data-icon="inline-start" />}
              {copied ? "Copied" : "Copy"}
            </Button>
            {sendable && <SendDraft todo={todo} text={shown} recipient={recipient} />}
            <Button size="sm" variant="outline" onClick={() => setEditing(true)}>
              <Pencil data-icon="inline-start" />
              Edit
            </Button>
            <OpenSource todo={todo} name={source} />
          </>
        )}
      </div>
    </Section>
  )
}

// SendDraft sends the text shown through the owner's own account after he confirms, naming who gets it and where
// ("Send to Kai Yong on WhatsApp?"), as inbox-zero's Send of an AI draft and Chatwoot's reply from the inbox; the
// server closes the item with the message sent as its evidence.
function SendDraft({ todo, text, recipient }: { todo: Todo; text: string; recipient?: string }) {
  const [open, setOpen] = useState(false)
  const send = useSendDraft()

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <Button
        size="sm"
        variant="outline"
        disabled={!text.trim() || send.isPending}
        onClick={() => setOpen(true)}
      >
        <Send data-icon="inline-start" />
        Send
      </Button>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{sendQuestion(todo, recipient)}</AlertDialogTitle>
          <AlertDialogDescription>
            It goes from your own account, as you, and the item is closed.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <p className="max-h-48 overflow-y-auto rounded-lg border bg-surface-raised px-3 py-2.5 text-sm whitespace-pre-wrap">
          {text}
        </p>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={send.isPending}
            onClick={() =>
              send.mutate(
                { todo, text },
                {
                  onSuccess: () => {
                    setOpen(false)
                    toast.add({ title: "Sent", type: "success" })
                  },
                  onError: (error) =>
                    toast.add({ title: "Not sent", description: errorText(error), type: "error" }),
                },
              )
            }
          >
            <Send data-icon="inline-start" />
            Send
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

// CommentBox writes in the item's thread; the owner's comment reaches the Bot in the item's own chat when it has
// one. Ctrl+Enter sends it, as in the hand-over.
function CommentBox({ todo }: { todo: Todo }) {
  const [text, setText] = useState("")
  const comment = useCommentTodo()
  const submit = () => {
    if (!text.trim() || comment.isPending) return
    comment.mutate(
      { todo, text },
      {
        onSuccess: () => setText(""),
        onError: (error) =>
          toast.add({ title: "Not commented", description: errorText(error), type: "error" }),
      },
    )
  }

  return (
    <div className="flex flex-col gap-2">
      <Textarea
        aria-label="Comment"
        value={text}
        rows={2}
        maxLength={10_000}
        placeholder={todo.chat_id ? "Answer the Bot, or add a note" : "Add a note"}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
            e.preventDefault()
            submit()
          }
        }}
      />
      <span className="self-end">
        <Button
          size="sm"
          variant="outline"
          disabled={!text.trim() || comment.isPending}
          onClick={submit}
        >
          Comment
        </Button>
      </span>
    </div>
  )
}

// actorName names who wrote in the thread: the owner, a Bot by its name, or Hypomnema.
function actorName(actor: TodoEvent["actor"], botName: (id: string) => string) {
  if (actor.kind === "owner") return "You"

  return actor.kind === "bot" ? botName(actor.id ?? "") : "Hypomnema"
}

// eventWords says one change of the item's history in words: who, and what.
function eventWords(e: TodoEvent, botName: (id: string) => string) {
  const who = actorName(e.actor, botName)
  const after = e.after as Record<string, unknown>

  switch (e.kind) {
    case "created":
      return `${who} added it`
    case "state":
      return `${who} moved it to ${String(after.state ?? "a new state")}`
    case "snoozed":
      return after.snoozed_until ? `${who} snoozed it` : `${who} brought it back`
    case "linked_approval":
      return `${who} asked for an Approval`
    case "edited":
      return (
        handoverWords(after, who, botName) ??
        `${who} changed ${Object.keys(after).join(", ") || "it"}`
      )
    default:
      return `${who}: ${e.kind}`
  }
}
