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
import { ErrorAlert } from "@/features/settings/settings-parts"
import { firstLine } from "@/features/work/work-state"

import { allEnded, useRunActions } from "./use-run-actions"

// RunActions are a Run's Finish and Cancel, on the Run's page and on the Run docked in its chat (issue #314). Each
// asks first, as every action that ends something does here (docs/conventions.md, AlertDialog), and says the
// server's words when it refuses. Finish waits until every Task has ended, which the server also requires.
export function RunActions({ run, size = "default" }: { run: RunDetail; size?: "default" | "xs" }) {
  const { finish, cancel } = useRunActions(run.id)
  const ready = allEnded(run.tasks)
  const name = firstLine(run.objective, 80)

  if (run.status !== "open") return null

  return (
    <div className="flex items-center gap-1.5">
      <Confirm
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
      <Confirm
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

type Mutation = ReturnType<typeof useRunActions>["finish"]

function Confirm({
  trigger,
  title,
  consequence,
  action,
  destructive,
  mutation,
}: {
  trigger: React.ReactElement
  title: string
  consequence: string
  action: string
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
          <AlertDialogCancel>Keep it open</AlertDialogCancel>
          <AlertDialogAction
            variant={destructive ? "destructive" : "default"}
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
