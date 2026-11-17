import type { UseMutationResult } from "@tanstack/react-query"
import { useState } from "react"

import type { RunDetail } from "@/api/generated/types.gen"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { ErrorAlert } from "@/components/error-alert"
import { firstLine } from "@/lib/text"

import { allEnded, useRunActions } from "./use-run-actions"

// RunActions are a Run's Finish and Cancel, on the Run docked in its chat (issue #314). Each
// asks first, as every action that ends something does here (docs/conventions.md, AlertDialog), and says the
// server's words when it refuses. Finish waits until every Task has ended, which the server also requires.
export function RunActions({ run, size = "default" }: { run: RunDetail; size?: "default" | "xs" }) {
  const { finish, cancel } = useRunActions(run.id)
  const ready = allEnded(run.tasks)
  const name = firstLine(run.objective, 80)

  if (run.status !== "open") return null

  return (
    <div className="flex items-center gap-1.5">
      <ConfirmAction
        trigger={
          <Button
            variant="outline"
            size={size}
            disabled={!ready}
            title={ready ? undefined : "Every Task must end first"}
          >
            Finish Run
          </Button>
        }
        title={`Finish the Run "${name}"?`}
        consequence="The Run is closed as done. Its Tasks and their results stay."
        action="Finish Run"
        mutation={finish}
      />
      <ConfirmAction
        trigger={
          <Button variant="destructive" size={size}>
            Cancel Run
          </Button>
        }
        title={`Cancel the Run "${name}"?`}
        consequence="Its workers are stopped and the Tasks that have not ended are canceled. This cannot be undone."
        action="Cancel Run"
        destructive
        mutation={cancel}
      />
    </div>
  )
}

type Mutation = Pick<
  UseMutationResult<unknown, Error, void>,
  "error" | "isPending" | "mutate" | "reset"
>

// ConfirmAction asks before an action that ends something, with its consequence in words, and says the server's
// words when it refuses: a Run's Finish and Cancel here, a Task's Stop in the chat's dock (issue #687). keep is the
// button that leaves things as they are.
export function ConfirmAction({
  trigger,
  title,
  consequence,
  action,
  keep = "Keep it open",
  destructive,
  mutation,
}: {
  trigger: React.ReactElement
  title: string
  consequence: string
  action: string
  keep?: string
  destructive?: boolean
  mutation: Mutation
}) {
  const [open, setOpen] = useState(false)

  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (!next) mutation.reset()
      }}
    >
      <AlertDialogTrigger render={trigger} />
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{consequence}</AlertDialogDescription>
        </AlertDialogHeader>
        <ErrorAlert error={mutation.error} />
        <AlertDialogFooter>
          <AlertDialogCancel>{keep}</AlertDialogCancel>
          <AlertDialogAction
            variant={destructive ? "destructive" : "default"}
            focusableWhenDisabled={mutation.isPending}
            disabled={mutation.isPending}
            onClick={() => mutation.mutate(undefined, { onSuccess: () => setOpen(false) })}
          >
            {mutation.isPending && <Spinner data-icon="inline-start" />}
            {action}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
