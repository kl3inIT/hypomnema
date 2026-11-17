import { useForm } from "@tanstack/react-form"
import { Ellipsis, Plus } from "lucide-react"
import { z } from "zod"

import type { MeetingFormat } from "@/api/generated/types.gen"
import { ErrorAlert } from "@/components/error-alert"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Field, FieldError, FieldGroup, FieldLabel, FieldTitle } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { said } from "@/lib/said"

import { useCreateFormat, useUpdateFormat } from "./use-meetings"

// FormatEditor makes or changes a notes format of the owner's (issue #747): its name, then its sections, each a
// title and a short instruction of what it holds. It follows Hyprnote's template editor (MIT, Copyright (c)
// 2023-present Fastrepl, Inc.; apps/desktop/src/templates/sections-editor.tsx): a section is a title over its
// instruction, its menu inserts a section above or below, moves it up or down, or deletes it, and "Add section"
// ends the list; a built-in format is copied to be edited, named "… (Copy)" as Hyprnote's duplicate
// (queries.ts getTemplateCopyTitle). Built on this app's Dialog, Field and DropdownMenu.

type Section = { key: string; title: string; about: string }

// The bounds are the server's (meetingstore/formats.go).
const schema = z.object({
  name: z.string().trim().min(1, "Give it a name.").max(100, "At most 100 characters."),
  sections: z
    .array(
      z.object({
        key: z.string(),
        title: z.string().max(100, "At most 100 characters."),
        about: z.string().max(500, "At most 500 characters."),
      }),
    )
    .refine((all) => all.some((s) => s.title.trim()), "Give it at least one section.")
    .refine((all) => all.length <= 20, "At most 20 sections.")
    .refine(
      (all) => all.every((s) => s.title.trim() || !s.about.trim()),
      "Every section with an instruction needs a title.",
    ),
})

function section(title = "", about = ""): Section {
  return { key: crypto.randomUUID(), title, about }
}

// Editing says which format the dialog works on: a new one, the owner's own to change, or a built-in one to copy.
export type Editing = { mode: "new" } | { mode: "edit" | "copy"; format: MeetingFormat }

export function FormatEditor({
  editing,
  onClose,
}: {
  editing: Editing | null
  onClose: () => void
}) {
  const title =
    editing?.mode === "edit"
      ? `Edit ${editing.format.name}`
      : editing?.mode === "copy"
        ? `Copy ${editing.format.name}`
        : "New format"

  return (
    <Dialog open={editing !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>
            The notes always have a summary, decisions and actions; these sections are written after
            them.
          </DialogDescription>
        </DialogHeader>
        {editing && <Body editing={editing} onDone={onClose} />}
      </DialogContent>
    </Dialog>
  )
}

function Body({ editing, onDone }: { editing: Editing; onDone: () => void }) {
  const create = useCreateFormat()
  const update = useUpdateFormat()
  const from = editing.mode === "new" ? undefined : editing.format
  const form = useForm({
    defaultValues: {
      name: !from ? "" : editing.mode === "copy" ? `${from.name} (Copy)` : from.name,
      sections: from ? from.sections.map((s) => section(s.title, s.about)) : [section()],
    },
    validators: { onSubmit: schema },
    onSubmit: async ({ value }) => {
      const body = {
        name: value.name.trim(),
        sections: value.sections
          .filter((s) => s.title.trim())
          .map((s) => ({ title: s.title.trim(), about: s.about.trim() })),
      }
      if (editing.mode === "edit") {
        await update.mutateAsync({ path: { format_id: editing.format.id }, body })
        said(`Saved ${body.name}`)
      } else {
        await create.mutateAsync({ body })
        said(`Made ${body.name}`)
      }
      onDone()
    },
  })

  return (
    <>
      <DialogPanel>
        <form
          id="format-editor"
          noValidate
          onSubmit={(e) => {
            e.preventDefault()
            void form.handleSubmit()
          }}
        >
          <FieldGroup>
            <form.Field name="name">
              {(field) => {
                const invalid = !field.state.meta.isValid

                return (
                  <Field data-invalid={invalid || undefined}>
                    <FieldLabel htmlFor="format-name">Name</FieldLabel>
                    <Input
                      id="format-name"
                      value={field.state.value}
                      placeholder="Weekly ops review"
                      aria-invalid={invalid || undefined}
                      onBlur={field.handleBlur}
                      onChange={(e) => field.handleChange(e.target.value)}
                    />
                    {invalid && <FieldError errors={field.state.meta.errors} />}
                  </Field>
                )
              }}
            </form.Field>
            <form.Field name="sections" mode="array">
              {(list) => (
                <Field data-invalid={!list.state.meta.isValid || undefined}>
                  <FieldTitle>Sections</FieldTitle>
                  <ol aria-label="Sections" className="flex flex-col gap-2">
                    {list.state.value.map((s, i) => (
                      <li
                        key={s.key}
                        data-testid="format-section"
                        className="flex flex-col gap-1.5 rounded-lg border bg-card p-2"
                      >
                        <div className="flex items-center gap-1">
                          <form.Field name={`sections[${i}].title`}>
                            {(field) => (
                              <Input
                                aria-label={`Section ${i + 1} title`}
                                placeholder="Untitled"
                                value={field.state.value}
                                onChange={(e) => field.handleChange(e.target.value)}
                              />
                            )}
                          </form.Field>
                          <DropdownMenu>
                            <DropdownMenuTrigger
                              render={
                                <Button
                                  type="button"
                                  variant="ghost"
                                  size="icon-sm"
                                  aria-label={`Section ${i + 1} actions`}
                                />
                              }
                            >
                              <Ellipsis />
                            </DropdownMenuTrigger>
                            <DropdownMenuContent align="end">
                              <DropdownMenuItem onClick={() => list.insertValue(i, section())}>
                                Insert above
                              </DropdownMenuItem>
                              <DropdownMenuItem onClick={() => list.insertValue(i + 1, section())}>
                                Insert below
                              </DropdownMenuItem>
                              <DropdownMenuItem
                                disabled={i === 0}
                                onClick={() => list.moveValue(i, i - 1)}
                              >
                                Move up
                              </DropdownMenuItem>
                              <DropdownMenuItem
                                disabled={i === list.state.value.length - 1}
                                onClick={() => list.moveValue(i, i + 1)}
                              >
                                Move down
                              </DropdownMenuItem>
                              <DropdownMenuItem
                                variant="destructive"
                                onClick={() => list.removeValue(i)}
                              >
                                Delete
                              </DropdownMenuItem>
                            </DropdownMenuContent>
                          </DropdownMenu>
                        </div>
                        <form.Field name={`sections[${i}].about`}>
                          {(field) => (
                            <Textarea
                              aria-label={`Section ${i + 1} instruction`}
                              placeholder="What it holds, such as: what broke and how it was fixed"
                              rows={2}
                              value={field.state.value}
                              onChange={(e) => field.handleChange(e.target.value)}
                            />
                          )}
                        </form.Field>
                      </li>
                    ))}
                  </ol>
                  <div>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      disabled={list.state.value.length >= 20}
                      onClick={() => list.pushValue(section())}
                    >
                      <Plus data-icon="inline-start" />
                      Add section
                    </Button>
                  </div>
                  {!list.state.meta.isValid && <FieldError errors={list.state.meta.errors} />}
                </Field>
              )}
            </form.Field>
            <ErrorAlert error={create.error ?? update.error} />
          </FieldGroup>
        </form>
      </DialogPanel>
      <DialogFooter>
        <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
        <form.Subscribe selector={(s) => s.isSubmitting}>
          {(submitting) => (
            <Button type="submit" form="format-editor" disabled={submitting}>
              {submitting && <Spinner data-icon="inline-start" />}
              Save
            </Button>
          )}
        </form.Subscribe>
      </DialogFooter>
    </>
  )
}
