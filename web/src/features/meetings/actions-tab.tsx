import { Link } from "@tanstack/react-router"
import { useState } from "react"

import type { MeetingAction } from "@/api/generated/types.gen"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { cn } from "@/lib/utils"

import { clock } from "./meetings-state"
import { useMarkDone, useMeetingActions } from "./use-meetings"

// ActionsTab is the actions of every meeting, filtered by who does them and whether they are done: Fireflies's
// Tasks, Otter's and Nojoin's action lists across meetings. Each opens its meeting at the moment it was said.

type State = "open" | "done" | "all"

const anyone = "*"
const states: { value: State; name: string }[] = [
  { value: "open", name: "Open" },
  { value: "done", name: "Done" },
  { value: "all", name: "Open and done" },
]

export function ActionsTab() {
  const [who, setWho] = useState(anyone)
  const [state, setState] = useState<State>("open")
  const actions = useMeetingActions(who === anyone ? "" : who, state)
  const people = actions.data?.people ?? []
  const rows = actions.data?.data ?? []

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Select value={who} onValueChange={(v) => v && setWho(v)}>
          <SelectTrigger size="sm" aria-label="Who">
            <SelectValue>{(v: string) => (v === anyone ? "Anyone" : v)}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={anyone}>Anyone</SelectItem>
            {people.map((p) => (
              <SelectItem key={p} value={p}>
                {p}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={state} onValueChange={(v) => v && setState(v as State)}>
          <SelectTrigger size="sm" aria-label="State">
            <SelectValue>{(v: string) => states.find((s) => s.value === v)?.name}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {states.map((s) => (
              <SelectItem key={s.value} value={s.value}>
                {s.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {actions.isPending ? (
        <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
      ) : actions.error ? (
        <p className="py-10 text-center text-sm text-destructive">The actions could not be read.</p>
      ) : rows.length === 0 ? (
        <p className="py-16 text-center text-sm text-muted-foreground">
          {state === "open" ? "Nothing is open." : "No actions."}
        </p>
      ) : (
        <div className="min-w-0 overflow-x-auto rounded-xl border bg-card">
          <Table aria-label="Actions">
            <TableHeader>
              <TableRow>
                <TableHead>
                  <span className="sr-only">Done</span>
                </TableHead>
                <TableHead>Action</TableHead>
                <TableHead>Who</TableHead>
                <TableHead>Due</TableHead>
                <TableHead>Meeting</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((a) => (
                <ActionRow key={a.id} action={a} />
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  )
}

function ActionRow({ action }: { action: MeetingAction }) {
  const mark = useMarkDone()

  return (
    <TableRow data-testid="action" aria-label={action.text}>
      <TableCell>
        <Checkbox
          aria-label={action.done ? `${action.text}: done` : `Mark ${action.text} done`}
          checked={action.done}
          disabled={mark.isPending}
          onCheckedChange={(done) =>
            mark.mutate({
              path: { meeting_id: action.meeting_id, item_id: action.id },
              body: { done },
            })
          }
        />
      </TableCell>
      <TableCell>
        <span
          className={cn(
            "block min-w-48 whitespace-normal",
            action.done && "text-muted-foreground line-through",
          )}
        >
          {action.text}
        </span>
      </TableCell>
      <TableCell>{action.who ?? "—"}</TableCell>
      <TableCell>{action.due ?? "—"}</TableCell>
      <TableCell>
        <Link
          to="/meetings/$meetingId"
          params={{ meetingId: action.meeting_id }}
          className="hover:underline"
        >
          {action.meeting_title}
        </Link>
        {action.start_ms != null && (
          <span className="text-muted-foreground tabular-nums"> · {clock(action.start_ms)}</span>
        )}
      </TableCell>
    </TableRow>
  )
}
