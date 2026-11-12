import { Ellipsis, ShieldQuestion } from "lucide-react"
import type { ComponentType, SVGProps } from "react"

import type { Approval } from "@/api/generated/types.gen"
import {
  GmailIcon,
  GoogleCalendarIcon,
  GoogleDocsIcon,
  GoogleDriveIcon,
} from "@/components/icons/brand-icons"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { toast } from "@/components/ui/toast"

import { readable, sessionOf, useDecide, usePending } from "./use-approvals"

// The mark of the service an action belongs to, by the first part of the action's id (gmail.send).
const marks: Record<string, ComponentType<SVGProps<SVGSVGElement>>> = {
  gmail: GmailIcon,
  calendar: GoogleCalendarIcon,
  drive: GoogleDriveIcon,
  docs: GoogleDocsIcon,
}

function Mark({ action }: { action: string }) {
  const Icon = marks[action.split(".")[0] ?? ""] ?? ShieldQuestion

  return <Icon className="size-4 shrink-0" aria-hidden />
}

// ApprovalCard shows one request that waits for the owner, decoded as he would read it (who a mail goes to, its
// subject and text), and takes his answer. Its parts follow Onyx's ApprovalCard and PayloadView: what is asked,
// the request as labelled lines, approve once, approve for the session, reject. As t3code's
// ComposerPendingApprovalActions.tsx, Decline and Approve are the two buttons and approving for the session waits
// in the "…" menu beside them.
export function ApprovalCard({ approval }: { approval: Approval }) {
  const decide = useDecide(approval)
  const session = sessionOf(approval)

  const answer = (decision: "approve" | "approve_session" | "reject", done: string) =>
    decide.mutate(decision, { onSuccess: () => toast.add({ title: done, type: "success" }) })

  return (
    <section
      aria-label={`Approval needed: ${approval.title}`}
      className="overflow-hidden rounded-xl border border-needs-you/40 bg-card text-sm"
    >
      <header className="flex items-center gap-2 bg-needs-you-soft px-3 py-2.5 font-medium">
        <Mark action={approval.action} />
        <h3 className="min-w-0 flex-1 break-words">Approval needed: {approval.title}</h3>
        <Badge variant="warning">Waits for you</Badge>
      </header>

      <dl className="grid max-h-80 grid-cols-[5.5rem_minmax(0,1fr)] gap-x-3 gap-y-1.5 overflow-y-auto px-3 py-3">
        {approval.payload.map((field) => (
          <div key={field.label} className="contents">
            <dt className="truncate pt-px text-xs text-muted-foreground" title={field.label}>
              {readable(field.label)}
            </dt>
            <dd className="min-w-0 break-words whitespace-pre-wrap">{field.value}</dd>
          </div>
        ))}
      </dl>

      <footer className="flex flex-wrap items-center gap-2 border-t px-3 py-2.5">
        <p className="me-auto text-xs text-muted-foreground">
          {approval.integration}:{approval.label} · {approval.action_name} · asked by{" "}
          {approval.asked_by}
        </p>
        {/* The answers stay together when the footer wraps on a phone. */}
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            disabled={decide.isPending}
            onClick={() => answer("reject", "Declined")}
          >
            Decline
          </Button>
          <Button
            size="sm"
            disabled={decide.isPending}
            onClick={() => answer("approve", "Approved")}
          >
            Approve
          </Button>
          {session && (
            <DropdownMenu>
              <DropdownMenuTrigger
                disabled={decide.isPending}
                render={
                  <Button variant="outline" size="icon-sm" aria-label="More approval options" />
                }
              >
                <Ellipsis />
              </DropdownMenuTrigger>
              <DropdownMenuContent side="top" align="end" width="content">
                <DropdownMenuItem
                  title={`Stop asking for "${approval.action_name}" for the rest of ${session}`}
                  onClick={() => answer("approve_session", `Approved for ${session}`)}
                >
                  Approve for {session}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>
      </footer>

      {decide.error && (
        <p role="alert" className="border-t px-3 py-2 text-xs text-destructive">
          Not answered: {errorText(decide.error)}
        </p>
      )}
    </section>
  )
}

// PendingApprovals lists what waits for the owner in one chat or for one Task; nothing when nothing waits.
export function PendingApprovals({ chatId, taskId }: { chatId?: string; taskId?: string }) {
  const pending = usePending({ chatId, taskId })

  if (!pending.data?.length) return null

  return (
    <div className="flex flex-col gap-3">
      {pending.data.map((approval) => (
        <ApprovalCard key={approval.id} approval={approval} />
      ))}
    </div>
  )
}

function errorText(error: unknown) {
  if (error && typeof error === "object" && "message" in error && error.message) {
    return String(error.message)
  }

  return "the server refused. Try again."
}
