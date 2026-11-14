import { ShieldCheck, ShieldX } from "lucide-react"

import type { ChatApproval } from "@/api/generated/types.gen"
import { cn } from "@/lib/utils"

import { WorkLogBlock, WorkLogRow } from "./work-log"

// An Approval the owner answered, kept in its chat as a row (issue #465). Adapted from t3code (MIT, Copyright (c)
// 2026 T3 Tools Inc., https://github.com/pingdotgg/t3code): chat/TimelinePendingApprovalRow.tsx keeps a request in
// the timeline as a work-log row, a shield icon, what was asked and a line of detail under it. Once answered it
// reads as AI Elements' packages/elements/src/confirmation.tsx (https://github.com/vercel/ai-elements, Apache
// License 2.0) does with ConfirmationAccepted and ConfirmationRejected: approved or rejected, said in words beside a
// mark. Changed: the request is answered in the card above the composer, so the row is the answer, written when it
// was given; it shows how far an approval reaches and when. Rows are shown whatever the Bot shows of its tools, as
// t3code and OpenClaw show approvals when tools are hidden.

const scopes: Record<NonNullable<ChatApproval["scope"]>, string> = {
  once: "This once",
  session: "For the rest of this chat",
  routine: "Always for this Routine",
}

const time = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" })

// approvalWords is what an answered Approval's row says: the answer, and how far it reaches.
function approvalWords(a: ChatApproval) {
  const approved = a.status === "approved"

  return {
    approved,
    answer: approved ? "Approved" : "Denied",
    scope: approved && a.scope ? scopes[a.scope] : undefined,
  }
}

export function ApprovalRow({ approval }: { approval: ChatApproval }) {
  const { approved, answer, scope } = approvalWords(approval)
  const Icon = approved ? ShieldCheck : ShieldX
  const at = approval.decided_at

  return (
    <WorkLogBlock>
      <div role="note" aria-label={`${answer}: ${approval.title}`} data-approval={approval.status}>
        <WorkLogRow
          wrapLabel
          icon={
            <Icon
              aria-hidden="true"
              className={cn("size-4 stroke-2", approved ? "text-done" : "text-destructive/80")}
            />
          }
          label={
            <span className="break-words">
              <span className={cn("font-medium", approved ? "text-done" : "text-destructive")}>
                {answer}
              </span>
              <span className="ms-1.5 text-foreground/80">{approval.title}</span>
            </span>
          }
          trailing={
            at && (
              <time dateTime={at} className="shrink-0 text-xs text-muted-foreground tabular-nums">
                {time.format(new Date(at))}
              </time>
            )
          }
        >
          <p className="ms-7.5 pb-1 text-xs text-muted-foreground">
            {approval.action_name}
            {scope && <> · {scope}</>}
          </p>
        </WorkLogRow>
      </div>
    </WorkLogBlock>
  )
}
