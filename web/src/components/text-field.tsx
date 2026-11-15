import type { AnyFieldApi } from "@tanstack/react-form"
import type { ReactNode } from "react"

import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"

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
