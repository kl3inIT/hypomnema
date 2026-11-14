import type { AnyFieldApi } from "@tanstack/react-form"
import { useMutation } from "@tanstack/react-query"
import { CircleAlert, type LucideIcon } from "lucide-react"
import { useState, type ReactElement, type ReactNode } from "react"

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

import { errorText, said } from "./use-integrations"

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
  done,
}: {
  trigger: ReactElement
  title: string
  consequence: string
  action: string
  run: () => Promise<unknown>
  onDone: () => Promise<unknown> | void
  // done is what a toast says once it is taken away.
  done: string
}) {
  const [open, setOpen] = useState(false)
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
  action,
}: {
  field: AnyFieldApi
  label: string
  description?: string
  placeholder?: string
  secret?: boolean
  // action sits at the end of the label's line, such as a button that puts the field back to its default.
  action?: ReactNode
}) {
  const invalid = field.state.meta.isTouched && !field.state.meta.isValid

  return (
    <Field data-invalid={invalid || undefined}>
      {action ? (
        <div className="flex items-center justify-between gap-2">
          <FieldLabel htmlFor={field.name}>{label}</FieldLabel>
          {action}
        </div>
      ) : (
        <FieldLabel htmlFor={field.name}>{label}</FieldLabel>
      )}
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

// SettingsSection is a group of a page under a heading with its icon and a line of what it is for, as the Models page
// has its subscriptions, gateway models and API providers.
export function SettingsSection({
  id,
  icon: Icon,
  title,
  note,
  children,
}: {
  id: string
  icon: LucideIcon
  title: string
  note: string
  children: ReactNode
}) {
  return (
    <section aria-labelledby={id} className="flex flex-col gap-3">
      <div className="flex flex-col gap-1">
        <h2 id={id} className="flex items-center gap-2 text-sm font-semibold text-foreground">
          <Icon className="size-4 text-muted-foreground" aria-hidden />
          {title}
        </h2>
        <p className="text-xs text-muted-foreground">{note}</p>
      </div>
      {children}
    </section>
  )
}
