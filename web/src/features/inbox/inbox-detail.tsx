import { Link } from "@tanstack/react-router"
import { ChevronDown, ChevronLeft, ChevronUp, Copy, ExternalLink, Pencil } from "lucide-react"
import { useState, type ReactNode } from "react"

import type { Todo, TodoEvent } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { ApprovalCard } from "@/features/approvals/approval-card"
import { QuestionForm } from "@/features/questions/question-form"
import { safeLink } from "@/features/work/work-state"
import { since } from "@/lib/time"

import { KindIcon, RowActions } from "./inbox-row"
import {
  detailActions,
  dueOf,
  groupNames,
  sourceNames,
  sourceOf,
  type Action,
  type GroupId,
  type Row,
} from "./inbox-state"
import { useTodo } from "./use-inbox"
import { copyDraft } from "./use-row-actions"

// InboxDetail is the selected item, after OpenMausBot's InboxReading (its place in the list, stepping up and down)
// and Chatwoot's conversation view beside its list: a top bar with the item's actions, then where it came from, its
// fields, what was said, the draft to copy, and its history. An Approval shows its card and a worker's question its
// form, the same parts and calls as in the chat and on Team. No Send: a Bot never sends for the owner (owner,
// 2026-11-17); he copies the draft and sends it himself.
export function InboxDetail({
  row,
  now,
  position,
  phone,
  pending,
  botName,
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
  onBack: () => void
  onStep: (by: -1 | 1) => void
  onAction: (action: Action, until?: number) => void
  onAnswered: () => void
  onSaveDraft: (draft: string, onSaved: () => void) => void
}) {
  const actions = detailActions(row, now)

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
        {actions.length > 0 && (
          <RowActions
            className="ms-auto flex flex-wrap"
            labelled
            actions={actions}
            onAction={(action, until) => !pending && onAction(action, until)}
          />
        )}
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-4 sm:px-6">
        <div className="mx-auto flex max-w-2xl flex-col gap-5">
          {row.kind === "todo" && (
            <TodoBody
              row={row}
              now={now}
              botName={botName}
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
  path: string
  children: ReactNode
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
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

// sourceLink is where "Open in <source>" leads: the chat a Bot added it from, or the message's own link when it is
// a web address. A Zalo, WhatsApp or Lark chat has no web address to open.
function sourceLink(todo: Todo): { chat?: string; href?: string } | null {
  if (todo.source.kind === "chat" && todo.source.id) return { chat: todo.source.id }

  const href = todo.source.ref?.kind === "url" ? safeLink(todo.source.ref.ref) : undefined

  return href ? { href } : null
}

function OpenSource({ todo, name }: { todo: Todo; name: string }) {
  const link = sourceLink(todo)
  if (!link) return null
  if (link.chat) return <OpenChat chatId={link.chat}>Open in {name}</OpenChat>

  return (
    <a href={link.href} target="_blank" rel="noreferrer" className={openClass}>
      <ExternalLink className="size-3.5" aria-hidden />
      Open in {name}
    </a>
  )
}

function TodoBody({
  row,
  now,
  botName,
  onAnswered,
  onSaveDraft,
}: {
  row: Extract<Row, { kind: "todo" }>
  now: number
  botName: (id: string) => string
  onAnswered: () => void
  onSaveDraft: (draft: string, onSaved: () => void) => void
}) {
  const { todo } = row
  const detail = useTodo(todo.id)
  const source = sourceNames[sourceOf(row)]
  const path = [source, todo.source.account, todo.source.ref?.label].filter(Boolean).join(" › ")
  const due = dueOf(todo, now)
  const doneWhen =
    todo.done_when ||
    (todo.why_open === "asked_directly"
      ? "You answer in this chat; it closes by itself"
      : "Not said")
  const linked = linkedOf(todo, botName)

  return (
    <>
      <Head row={row} now={now} path={path}>
        {todo.title}
      </Head>
      <Fields
        rows={[
          ["Who", whoOf(todo, botName)],
          ["Due", due ? due.text : "No due date"],
          ["Done when", doneWhen],
          ...(linked ? ([["Linked", linked]] as [string, ReactNode][]) : []),
          ...(todo.snoozed_until && Date.parse(todo.snoozed_until) > now
            ? ([["Snoozed", `until ${new Date(todo.snoozed_until).toLocaleString()}`]] as [
                string,
                ReactNode,
              ][])
            : []),
          ...(todo.close_reason ? ([["Closed", todo.close_reason]] as [string, ReactNode][]) : []),
        ]}
      />
      {todo.notes && <p className="text-sm whitespace-pre-wrap text-foreground/85">{todo.notes}</p>}
      {todo.source.quote && (
        <Section title="Said">
          <blockquote className="border-l-2 border-border-strong py-0.5 ps-3 text-sm whitespace-pre-wrap text-foreground/85">
            {todo.source.quote}
          </blockquote>
          <OpenSource todo={todo} name={source} />
        </Section>
      )}
      {row.approval && (
        <Section title="Waits for your approval">
          <ApprovalCard approval={row.approval} onAnswered={onAnswered} />
        </Section>
      )}
      <Draft key={`${todo.id}:${todo.revision}`} todo={todo} source={source} onSave={onSaveDraft} />
      <Section title="Activity">
        {detail.data?.events?.length ? (
          <ol className="flex flex-col gap-1.5 text-sm">
            {detail.data.events
              .slice()
              .reverse()
              .map((e) => (
                <li key={e.id} className="flex items-baseline gap-2">
                  <span className="min-w-0 flex-1 text-foreground/85">
                    {eventWords(e, botName)}
                  </span>
                  <span className="shrink-0 text-2xs text-muted-foreground tabular-nums">
                    {since(e.at, now)}
                  </span>
                </li>
              ))}
          </ol>
        ) : (
          <p className="text-sm text-muted-foreground">
            {detail.isPending ? "Reading…" : "Nothing yet."}
          </p>
        )}
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

// Draft is the reply a Bot wrote for the owner to send himself: Copy, Edit and Open in its source, never Send.
function Draft({
  todo,
  source,
  onSave,
}: {
  todo: Todo
  source: string
  onSave: (draft: string, onSaved: () => void) => void
}) {
  const [editing, setEditing] = useState(false)
  const [text, setText] = useState(todo.draft)

  if (!todo.draft && !editing) {
    return (
      <Section title="Draft">
        <p className="text-sm text-muted-foreground">
          No draft. A Bot drafts a reply to a client, a lead or a boss for you to send; a password
          or a code is never written into the Inbox.
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
            <Button size="sm" variant="ghost" onClick={() => setEditing(false)}>
              Cancel
            </Button>
          </>
        ) : (
          <>
            <Button size="sm" onClick={() => copyDraft(todo.draft)}>
              <Copy data-icon="inline-start" />
              Copy
            </Button>
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

// eventWords says one change of the item's history in words: who, and what.
function eventWords(e: TodoEvent, botName: (id: string) => string) {
  const who =
    e.actor.kind === "owner"
      ? "You"
      : e.actor.kind === "bot"
        ? botName(e.actor.id ?? "")
        : "Hypomnema"
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
      return `${who} changed ${Object.keys(after).join(", ") || "it"}`
    default:
      return `${who}: ${e.kind}`
  }
}
