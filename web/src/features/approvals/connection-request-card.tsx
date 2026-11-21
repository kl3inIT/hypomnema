import { KeyRound } from "lucide-react"

import type { ConnectionRequest, ConnectionRequestDecision } from "@/api/generated/types.gen"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { toast } from "@/components/ui/toast"
import { useBotName } from "@/features/bots/use-bots"

import { useDecideConnectionRequest, usePendingConnectionRequests } from "./use-connection-requests"

// ConnectionRequestCard is a Bot's request for a Connection it was not given (issue #471, part D, the grant card),
// in the shape of the Approval card (approval-card.tsx, after Onyx's ApprovalCard and t3code's
// ComposerPendingApprovalActions.tsx): what is asked, as labelled lines, and the answers in its footer. It has exactly
// two (owner, 2026-11-21): Always for this Bot, which adds the Connection to the Bot's list, and Decline. Taking it
// back is the Bot's page's, as for any Connection of its list; the card has no Undo. Either answer wakes the Bot
// with a line in the chat.
function ConnectionRequestCard({ request }: { request: ConnectionRequest }) {
  const decide = useDecideConnectionRequest(request)
  const bot = useBotName(request.bot)
  const use = `${request.integration}:${request.label}`

  const answer = (decision: ConnectionRequestDecision["decision"]) =>
    decide.mutate(decision, {
      onSuccess: (decided) => {
        if (decided.state === "stale") {
          toast.add({ title: `Not granted: ${decided.why ?? "the request no longer holds"}` })
        } else if (decided.state === "granted") {
          toast.add({ title: `${use} given to ${bot}`, type: "success" })
        } else {
          toast.add({ title: "Declined", type: "success" })
        }
      },
    })

  return (
    <section
      aria-label={`Connection asked for: ${use}`}
      className="overflow-hidden rounded-xl border border-needs-you/40 bg-card text-sm"
    >
      <header className="flex items-center gap-2 bg-needs-you-soft px-3 py-2.5 font-medium">
        <KeyRound className="size-4 shrink-0" aria-hidden />
        <h3 className="min-w-0 flex-1 break-words">
          {bot} asks for {use}
        </h3>
        <Badge variant="warning">Waits for you</Badge>
      </header>

      <dl className="grid grid-cols-[5.5rem_minmax(0,1fr)] gap-x-3 gap-y-1.5 px-3 py-3">
        <dt className="truncate pt-px text-xs text-muted-foreground">Bot</dt>
        <dd className="min-w-0 break-words">{bot}</dd>
        <dt className="truncate pt-px text-xs text-muted-foreground">Connection</dt>
        <dd className="min-w-0 break-words">{use}</dd>
        <dt className="truncate pt-px text-xs text-muted-foreground">Reason</dt>
        <dd className="min-w-0 break-words whitespace-pre-wrap">{request.reason}</dd>
      </dl>

      <footer className="flex flex-wrap items-center gap-2 border-t px-3 py-2.5">
        <p className="me-auto text-xs text-muted-foreground">
          Taken back on the Bot's page, as any Connection of its list
        </p>
        {/* The answers stay together when the footer wraps on a phone. */}
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            focusableWhenDisabled={decide.isPending}
            disabled={decide.isPending}
            onClick={() => answer("decline")}
          >
            {decide.isPending && decide.variables === "decline" && (
              <Spinner data-icon="inline-start" />
            )}
            Decline
          </Button>
          <Button
            size="sm"
            focusableWhenDisabled={decide.isPending}
            disabled={decide.isPending}
            onClick={() => answer("grant")}
          >
            {decide.isPending && decide.variables === "grant" && (
              <Spinner data-icon="inline-start" />
            )}
            Always for this Bot
          </Button>
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

// PendingConnectionRequests lists the requests that wait for the owner in one chat; nothing when none waits.
export function PendingConnectionRequests({ chatId }: { chatId: string }) {
  const pending = usePendingConnectionRequests(chatId)

  if (!pending.data?.length) return null

  return (
    <div className="flex flex-col gap-3">
      {pending.data.map((request) => (
        <ConnectionRequestCard key={request.id} request={request} />
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
