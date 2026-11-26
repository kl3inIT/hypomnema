import {
  AlarmClock,
  Check,
  CheckCircle2,
  Circle,
  CircleDashed,
  Copy,
  Lightbulb,
  MessageCircleQuestion,
  RotateCcw,
  ShieldQuestion,
  Trash2,
  X,
  type LucideIcon,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { PrioritySelector } from "@/components/ui/priority"
import { ProjectDot, type ProjectChoice } from "@/components/ui/project-selector"
import { since } from "@/lib/time"
import { cn } from "@/lib/utils"

import { SnoozeMenu } from "./snooze-menu"
import { SourceAvatar } from "./source-icon"
import {
  actionsOf,
  atOf,
  botOf,
  dueOf,
  dueTones,
  fromLineOf,
  isClosed,
  quoteOf,
  sourceOf,
  titleOf,
  type Action,
  type Row,
} from "./inbox-state"
import { useOrigin, type KeptChats } from "./use-origin"

// The kind a row is, as its icon shows it: an Approval, a question, a to-do, one a Bot is doing, a proposal, a
// closed one, a snoozed one.
type Kind = "approval" | "question" | "todo" | "doing" | "proposed" | "done" | "snoozed"

const kinds: Record<Kind, { Icon: LucideIcon; label: string; tone: string }> = {
  approval: { Icon: ShieldQuestion, label: "Approval", tone: "text-needs-you" },
  question: { Icon: MessageCircleQuestion, label: "Question", tone: "text-needs-you" },
  todo: { Icon: Circle, label: "To-do", tone: "text-muted-foreground" },
  doing: { Icon: CircleDashed, label: "A Bot is doing it", tone: "text-running" },
  proposed: { Icon: Lightbulb, label: "Proposed", tone: "text-muted-foreground" },
  done: { Icon: CheckCircle2, label: "Closed", tone: "text-done" },
  snoozed: { Icon: AlarmClock, label: "Snoozed", tone: "text-muted-foreground" },
}

function kindOf(row: Row, now: number): Kind {
  if (row.kind !== "todo") return row.kind
  if (row.approval) return "approval"

  const { todo } = row
  if (isClosed(todo)) return "done"
  if (todo.snoozed_until && Date.parse(todo.snoozed_until) > now) return "snoozed"
  if (todo.state === "proposed") return "proposed"
  if (todo.state === "doing" || todo.state === "review") return "doing"

  return "todo"
}

export function KindIcon({ row, now, className }: { row: Row; now: number; className?: string }) {
  const { Icon, label, tone } = kinds[kindOf(row, now)]

  return (
    <span title={label} className={cn("inline-flex shrink-0", className)}>
      <Icon aria-hidden className={cn("size-4", tone)} />
      <span className="sr-only">{label}</span>
    </span>
  )
}

// The look of each action, as OpenMausBot's ACTION_LOOK (InboxRow.tsx): an icon and its name.
const actionLook: Record<Action, { Icon: LucideIcon; label: string }> = {
  approve: { Icon: Check, label: "Approve" },
  decline: { Icon: X, label: "Decline" },
  answer: { Icon: MessageCircleQuestion, label: "Answer" },
  copy: { Icon: Copy, label: "Copy draft" },
  done: { Icon: Check, label: "Done" },
  snooze: { Icon: AlarmClock, label: "Snooze" },
  accept: { Icon: Check, label: "Accept" },
  drop: { Icon: Trash2, label: "Drop" },
  reopen: { Icon: RotateCcw, label: "Reopen" },
  unsnooze: { Icon: RotateCcw, label: "Unsnooze" },
}

// InboxRow is one line of the list, after OpenMausBot's InboxRowItem and Chatwoot's ConversationCard: the platform
// it came from as its avatar, with no state on it, which its group's heading says (issue #1140); the title, bold
// while unread, on two lines on a phone; what was said, on one muted line; who said it and where, its date labelled
// and coloured when it is today or late, and a draft chip; and the time of the message it came from. Pointed at or focused, its actions take the
// time's place; a phone shows them in the detail instead. It is a row that opens, its own design rather than a
// Button. A to-do's priority starts the row, as circle's IssueLine starts with its PrioritySelector (MIT, Copyright
// (c) 2025 lndev-ui; components/common/issues/issue-line.tsx), one click to change it; its project is a chip on the
// line of who and where, as circle's ProjectBadge (issue #1157).
export function InboxRow({
  row,
  chats,
  botName,
  now,
  selected,
  unread,
  actions = true,
  projects = [],
  onOpen,
  onAction,
  onPriority,
}: {
  row: Row
  chats: KeptChats
  botName: (id: string) => string
  now: number
  selected: boolean
  unread: boolean
  actions?: boolean
  projects?: ProjectChoice[]
  onOpen: () => void
  onAction: (action: Action, until?: number) => void
  onPriority?: (priority: number) => void
}) {
  const origin = useOrigin(row, chats)
  const title = titleOf(row)
  const todo = row.kind === "todo" ? row.todo : undefined
  const due = todo ? dueOf(todo, now) : null
  const quote = quoteOf(row)
  const from = fromLineOf(row, origin, botName)
  const bot = botOf(row)
  const project = todo?.project_id ? projects.find((p) => p.id === todo.project_id) : undefined

  return (
    <li
      data-testid="inbox-row"
      data-key={row.key}
      data-unread={unread || undefined}
      aria-current={selected || undefined}
      className={cn(
        "group relative flex min-w-0 items-start rounded-lg",
        selected
          ? "bg-sidebar-row-active"
          : "focus-within:bg-sidebar-row-hover hover:bg-sidebar-row-hover",
      )}
    >
      <span className="flex w-7 shrink-0 justify-center ps-1 pt-2">
        {todo && (
          <PrioritySelector
            value={todo.priority}
            disabled={!onPriority || isClosed(todo)}
            title={todo.priority_reason || undefined}
            onChange={(priority) => onPriority?.(priority)}
          />
        )}
      </span>
      <button
        type="button"
        onClick={onOpen}
        aria-label={title}
        className="flex min-w-0 flex-1 items-start gap-3 rounded-lg py-2 ps-1.5 pe-2.5 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset"
      >
        <SourceAvatar
          source={sourceOf(row)}
          bot={bot ? botName(bot) : undefined}
          className="mt-0.5"
        />
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="flex min-w-0 items-baseline gap-2">
            <span
              data-slot="title"
              className={cn(
                "line-clamp-2 min-w-0 flex-1 text-sm break-words md:line-clamp-1",
                unread ? "font-semibold text-foreground" : "text-foreground/85",
              )}
            >
              {title}
            </span>
            <span
              className={cn(
                "shrink-0 text-2xs text-muted-foreground tabular-nums",
                actions && "md:group-focus-within:invisible md:group-hover:invisible",
              )}
            >
              {since(atOf(row, origin), now)}
            </span>
          </span>
          {quote && (
            <span data-slot="quote" className="truncate text-xs text-muted-foreground">
              {quote}
            </span>
          )}
          <span className="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
            <span data-slot="from" className="min-w-0 truncate">
              {from}
            </span>
            {project && (
              <span data-slot="project" className="flex shrink-0 items-center gap-1">
                <ProjectDot color={project.color} size="sm" />
                {project.name}
              </span>
            )}
            {due && (
              <span className={cn("shrink-0", dueTones[due.tone])}>
                {due.tone === "late" ? `Late · ${due.text}` : due.text}
              </span>
            )}
            {todo?.draft && (
              <span className="shrink-0">
                <Badge variant="outline">draft</Badge>
              </span>
            )}
          </span>
        </span>
      </button>
      {actions && (
        <RowActions
          className="absolute top-1.5 right-1.5 hidden rounded-md bg-popover shadow-xs md:group-focus-within:flex md:group-hover:flex"
          actions={actionsOf(row, now)}
          onAction={onAction}
        />
      )}
    </li>
  )
}

export function RowActions({
  actions,
  onAction,
  className,
  labelled = false,
}: {
  actions: Action[]
  onAction: (action: Action, until?: number) => void
  className?: string
  labelled?: boolean
}) {
  return (
    <span className={cn("items-center gap-0.5", className)}>
      {actions.map((action) => {
        const { Icon, label } = actionLook[action]
        const button = (
          <Button
            key={action}
            variant={labelled ? "outline" : "ghost"}
            size={labelled ? "sm" : "icon-sm"}
            aria-label={label}
            title={label}
            onClick={action === "snooze" ? undefined : () => onAction(action)}
          >
            <Icon data-icon={labelled ? "inline-start" : undefined} />
            {labelled && label}
          </Button>
        )

        return action === "snooze" ? (
          <SnoozeMenu
            key={action}
            trigger={button}
            onSnooze={(until) => onAction("snooze", until)}
          />
        ) : (
          button
        )
      })}
    </span>
  )
}
