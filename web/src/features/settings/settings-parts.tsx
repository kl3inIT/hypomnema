import type { AnyFieldApi } from "@tanstack/react-form"
import { useMutation } from "@tanstack/react-query"
import { CircleAlert } from "lucide-react"
import { useState, type ReactElement } from "react"

import { Alert, AlertDescription } from "@/components/ui/alert"
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
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"

import { errorText } from "./use-integrations"

// The pieces every Settings screen shares, built on the standard components (issue #123).

// ErrorAlert says what the server refused, in the server's words.
export function ErrorAlert({ error }: { error: unknown }) {
  if (!error) return null

  return (
    <Alert variant="destructive">
      <CircleAlert />
      <AlertDescription>{errorText(error)}</AlertDescription>
    </Alert>
  )
}

// ConfirmDialog asks before something is taken away and says what follows from it. It stays open while the
// server works and when the server refuses, with the reason.
export function ConfirmDialog({
  trigger,
  title,
  consequence,
  action,
  run,
  onDone,
}: {
  trigger: ReactElement
  title: string
  consequence: string
  action: string
  run: () => Promise<unknown>
  onDone: () => Promise<unknown> | void
}) {
  const [open, setOpen] = useState(false)
  const remove = useMutation({
    mutationFn: run,
    onSuccess: async () => {
      await onDone()
      setOpen(false)
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
      <AlertDialogTrigger render={trigger} />
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

// TextField is one field of a form: its label, its input, what it is for, and what is wrong with it once the form
// was sent.
export function TextField({
  field,
  label,
  description,
  placeholder,
  secret,
}: {
  field: AnyFieldApi
  label: string
  description?: string
  placeholder?: string
  secret?: boolean
}) {
  const invalid = field.state.meta.isTouched && !field.state.meta.isValid

  return (
    <Field data-invalid={invalid || undefined}>
      <FieldLabel htmlFor={field.name}>{label}</FieldLabel>
      <Input
        id={field.name}
        name={field.name}
        type={secret ? "password" : "text"}
        autoComplete="off"
        placeholder={placeholder}
        value={field.state.value as string}
        aria-invalid={invalid || undefined}
        onBlur={field.handleBlur}
        onChange={(event) => field.handleChange(event.target.value)}
      />
      {description && <FieldDescription>{description}</FieldDescription>}
      {invalid && <FieldError errors={field.state.meta.errors} />}
    </Field>
  )
}
