import { useMutation } from "@tanstack/react-query"
import { useState, type ReactElement } from "react"

import { ErrorAlert } from "@/components/error-alert"
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
import { Spinner } from "@/components/ui/spinner"
import { said } from "@/lib/said"

// ConfirmDialog asks before something is taken away and says what follows from it. It stays open while the
// server works and when the server refuses, with the reason. Without a trigger it is opened by its owner, as from
// a menu's item (open and onOpenChange; issue #484).
export function ConfirmDialog({
  trigger,
  open: shown,
  onOpenChange,
  title,
  consequence,
  action,
  run,
  onDone,
  done,
}: {
  trigger?: ReactElement
  open?: boolean
  onOpenChange?: (open: boolean) => void
  title: string
  consequence: string
  action: string
  run: () => Promise<unknown>
  onDone: () => Promise<unknown> | void
  // done is what a toast says once it is taken away.
  done: string
}) {
  const [ownOpen, setOwnOpen] = useState(false)
  const open = shown ?? ownOpen
  const setOpen = (next: boolean) => {
    setOwnOpen(next)
    onOpenChange?.(next)
  }
  const remove = useMutation({
    mutationFn: run,
    onSuccess: async () => {
      await onDone()
      setOpen(false)
      said(done)
    },
  })

  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (!next) remove.reset()
      }}
    >
      {trigger && <AlertDialogTrigger render={trigger} />}
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{consequence}</AlertDialogDescription>
        </AlertDialogHeader>
        <ErrorAlert error={remove.error} />
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            focusableWhenDisabled={remove.isPending}
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending && <Spinner data-icon="inline-start" />}
            {action}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
