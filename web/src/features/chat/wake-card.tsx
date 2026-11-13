import { Link, useNavigate } from "@tanstack/react-router"
import {
  Ban,
  CircleCheck,
  CirclePause,
  CircleX,
  MessageCircleQuestion,
  TriangleAlert,
  type LucideIcon,
} from "lucide-react"
import { useQuery } from "@tanstack/react-query"
import { useMemo } from "react"

import "@/api/client"
import { getRun } from "@/api/generated/sdk.gen"
import type { ChatWake } from "@/api/generated/types.gen"
import { Button, buttonVariants } from "@/components/ui/button"
import { Separator } from "@/components/ui/separator"
import { useWork } from "@/features/work/use-work"
import { firstLine } from "@/features/work/work-state"
import { cn } from "@/lib/utils"

import { useDock } from "./dock-context"
import { wakeLine, type WakeIcon, type WakeLink, type WakeNames, type WakeTone } from "./wake"

// A wake-up as a card across the lane (issue #303), adapted from t3code (MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code): chat/TimelineSystemDivider.tsx (a line across the lane, an icon, a label,
// a detail and a pill to act on) and chat/MarkdownThreadLink.tsx with ThreadContextChip.tsx (other work named by
// its title, as a chip that opens it). The icon by status follows Orca's task rows (MIT, Copyright (c) 2026
// Lovecast Inc., https://github.com/stablyai/orca; native-chat/tools/tasks/NativeChatTaskList.tsx), whose status
// is in words beside the icon. Changed: the Task opens its panel over the chat, and the Run opens its strip docked
// above the composer (issue #314), or its own chat when it is another chat's (issue #327); the line is not read
// again live, as neither reference does on such a line.

const icons: Record<WakeIcon, LucideIcon> = {
  done: CircleCheck,
  failed: CircleX,
  blocked: CirclePause,
  canceled: Ban,
  question: MessageCircleQuestion,
  escalation: TriangleAlert,
}

const tones: Record<WakeTone, string> = {
  done: "text-done",
  failed: "text-failed",
  "needs-you": "text-needs-you",
  quiet: "text-muted-foreground",
}

export function WakeCard({ wake }: { wake: ChatWake }) {
  const names = useWakeNames()
  const line = wakeLine(wake, names)
  const Icon = icons[line.icon]
  const tone = tones[line.tone]
  // The last words take the colon that leads into the detail.
  const colon = line.detail ? ":" : ""

  return (
    <div
      role="note"
      aria-label={line.text}
      data-wake={line.tone}
      className="flex min-w-0 items-center gap-2 py-2 text-2xs text-muted-foreground"
    >
      <div aria-hidden="true" className="min-w-4 flex-1">
        <Separator />
      </div>
      <p className="flex max-w-[calc(100%-3rem)] min-w-0 flex-wrap items-center justify-center gap-x-1.5 gap-y-1 px-1 py-1">
        <Icon aria-hidden="true" className={cn("size-3.5 shrink-0", tone)} />
        <span className="font-medium">
          {line.before}
          {!line.link && !line.after && colon}
        </span>
        {line.link && <WakeChip link={line.link} />}
        {line.after && (
          <span className={cn("font-medium", tone)}>
            {line.after}
            {colon}
          </span>
        )}
        {line.detail && (
          <span className="max-w-80 min-w-0 truncate opacity-80" title={line.detail}>
            {line.detail}
          </span>
        )}
        {line.action && <WakeChip link={line.action} />}
      </p>
      <div aria-hidden="true" className="min-w-4 flex-1">
        <Separator />
      </div>
    </div>
  )
}

// WakeChip opens the work a line names: a Task in its panel over the chat (the page keeps it in its URL, as the
// Team page does), a Run in the dock above the composer when this chat docks it, else in its own chat.
function WakeChip({ link }: { link: WakeLink }) {
  const navigate = useNavigate()
  const dock = useDock()
  const label = <span className="truncate">{link.label}</span>

  if (link.kind === "run" && dock.docked(link.id)) {
    return (
      <Button
        variant="outline"
        size="chip"
        aria-label={`Show Run ${link.label}`}
        onClick={() => dock.show(link.id)}
      >
        {label}
      </Button>
    )
  }

  if (link.kind === "run") return <RunChatChip runId={link.id} label={link.label} />

  return (
    <Button
      variant="outline"
      size="chip"
      aria-label={`Open Task ${link.label}`}
      onClick={() =>
        void navigate({
          to: ".",
          search: (prev: Record<string, unknown>) => ({ ...prev, task: link.id }),
        })
      }
    >
      {label}
    </Button>
  )
}

// RunChatChip opens another chat's Run in that chat, where it is docked (issue #327). The Run is read as the dock
// reads it, so a chip the dock already found foreign opens at once. A Run with no chat opens its first Task's panel
// over this chat, as a Task's chip does; until the Run is read, the chip only names it.
function RunChatChip({ runId, label }: { runId: string; label: string }) {
  const navigate = useNavigate()
  const run = useQuery({
    queryKey: ["run", runId],
    queryFn: async () => (await getRun({ path: { run_id: runId } })).data,
  })
  const chatId = run.data?.chat_id
  const first = run.data?.tasks[0]?.id
  const text = <span className="truncate">{label}</span>

  if (chatId) {
    return (
      <Link
        to="/chats/$chatId"
        params={{ chatId }}
        aria-label={`Open Run ${label} in its chat`}
        className={cn(buttonVariants({ variant: "outline", size: "chip" }))}
      >
        {text}
      </Link>
    )
  }

  return (
    <Button
      variant="outline"
      size="chip"
      aria-label={`Open Run ${label}`}
      disabled={!first}
      onClick={() =>
        void navigate({
          to: ".",
          search: (prev: Record<string, unknown>) => ({ ...prev, task: first }),
        })
      }
    >
      {text}
    </Button>
  )
}

// useWakeNames names a wake-up's ids from the work the app already reads: a Task's first line and its agent, a
// Run's objective, an open question's text.
function useWakeNames(): WakeNames {
  const { cards } = useWork()

  return useMemo(() => {
    const tasks = new Map(
      cards.map((c) => [c.task.id, { title: firstLine(c.task.spec, 80), agent: c.task.profile }]),
    )
    const runs = new Map(cards.map((c) => [c.run.id, c.run.objective]))
    const questions = new Map(
      cards.flatMap((c) =>
        [c.question, c.piQuestion].flatMap((q) => (q ? [[q.id, q.text] as const] : [])),
      ),
    )

    return {
      task: (id) => tasks.get(id),
      run: (id) => runs.get(id),
      question: (id) => questions.get(id),
    }
  }, [cards])
}
