import { useForm } from "@tanstack/react-form"
import { Link, useNavigate } from "@tanstack/react-router"
import { ChevronDown, Upload } from "lucide-react"
import { useState } from "react"
import { z } from "zod"

import { DropFile } from "@/components/drop-file"
import { ErrorAlert } from "@/components/error-alert"
import { Button } from "@/components/ui/button"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { toast } from "@/components/ui/toast"
import { TextField } from "@/components/text-field"

import { languages, listOf, maxRecording } from "./meetings-state"
import { useUploadMeeting } from "./use-meetings"

// UploadMeeting is the "Upload recording" button and its dialog (issue #721): the file and its language, as
// Fireflies, Meetily and Nojoin ask; then, behind one "More details" control as Nojoin and MemoryOS keep them, the
// title (written from the meeting when empty), and the people (Otter, Hyprnote). The words to hear
// right are not asked here: they are one list on Settings → Dictionary, given to every recording (issue #747, as
// Nojoin's glossary and OpenNoteTaker's key terms are set once). The meeting's page opens once it is sent.

const schema = z.object({
  file: z
    .custom<File | null>()
    .refine((f) => f instanceof File, "Choose a recording.")
    .refine((f) => !f || f.size <= maxRecording, "A recording is at most 128 MB.")
    .refine((f) => !f || f.size > 0, "This file is empty."),
  language: z.string(),
  title: z.string().max(200, "At most 200 characters."),
  people: z.string(),
})

export function UploadMeeting({ label = "Upload recording" }: { label?: string }) {
  const [open, setOpen] = useState(false)

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button />}>
        <Upload data-icon="inline-start" />
        {label}
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Upload a recording</DialogTitle>
          <DialogDescription>
            It is heard by your speech-to-text provider with who spoke when, then your notes model
            writes it up. The file stays on your server so you can play it back.
          </DialogDescription>
        </DialogHeader>
        {open && <Body onDone={() => setOpen(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function Body({ onDone }: { onDone: () => void }) {
  const upload = useUploadMeeting()
  const navigate = useNavigate()
  const form = useForm({
    defaultValues: {
      file: null as File | null,
      language: "auto",
      title: "",
      people: "",
    },
    validators: { onSubmit: schema },
    onSubmit: async ({ value }) => {
      const file = value.file as File
      const people = listOf(value.people)
      const made = await upload.mutateAsync({
        body: file,
        query: {
          file_name: file.name,
          ...(file.type ? { media_type: file.type } : {}),
          ...(value.language !== "auto" ? { language: value.language } : {}),
          ...(value.title.trim() ? { title: value.title.trim() } : {}),
          ...(people.length ? { people } : {}),
        },
      })
      toast.add({
        title: "Recording uploaded",
        description: "It is being transcribed.",
        type: "success",
      })
      onDone()
      await navigate({ to: "/meetings/$meetingId", params: { meetingId: made.id } })
    },
  })

  return (
    <>
      <DialogPanel>
        <form
          id="upload-meeting"
          noValidate
          onSubmit={(e) => {
            e.preventDefault()
            void form.handleSubmit()
          }}
        >
          <FieldGroup>
            <form.Field name="file">
              {(field) => {
                const f = field.state.value
                const invalid = !field.state.meta.isValid

                return (
                  <Field data-invalid={invalid || undefined}>
                    <FieldLabel htmlFor="meeting-file">Recording</FieldLabel>
                    <DropFile
                      id="meeting-file"
                      accept="audio/*,video/*,.m4a,.mp3,.wav,.webm,.ogg,.mp4"
                      file={f}
                      onFile={(picked) => field.handleChange(picked)}
                      invalid={invalid}
                      chosen={
                        f && (
                          <>
                            <span>{f.name}</span>{" "}
                            <span className="text-muted-foreground">
                              · {Math.max(1, Math.round(f.size / 1024 / 1024))} MB
                            </span>
                          </>
                        )
                      }
                      empty={
                        <>
                          <p>Drop a recording here</p>
                          <p className="text-xs">mp3, m4a, wav, webm, ogg or mp4, up to 128 MB</p>
                        </>
                      }
                    />
                    {invalid && <FieldError errors={field.state.meta.errors} />}
                  </Field>
                )
              }}
            </form.Field>
            <form.Field name="language">
              {(field) => (
                <Field>
                  <FieldLabel htmlFor="meeting-language">Language</FieldLabel>
                  <Select
                    value={field.state.value}
                    onValueChange={(v) => v && field.handleChange(v)}
                  >
                    <SelectTrigger id="meeting-language" width="full">
                      <SelectValue>
                        {(v: string) => languages.find((l) => l.code === v)?.name ?? v}
                      </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      {languages.map((l) => (
                        <SelectItem key={l.code} value={l.code}>
                          {l.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <FieldDescription>Saying it hears the words better.</FieldDescription>
                </Field>
              )}
            </form.Field>
            <Collapsible>
              <div className="rounded-lg border p-1">
                <CollapsibleTrigger render={<Button type="button" variant="ghost" size="sm" />}>
                  <ChevronDown
                    className="transition-transform [[data-panel-open]_&]:rotate-180"
                    aria-hidden
                  />
                  More details
                </CollapsibleTrigger>
                <CollapsibleContent>
                  <div className="px-2 pt-2 pb-2">
                    <FieldGroup>
                      <form.Field name="title">
                        {(field) => (
                          <TextField
                            field={field}
                            label="Title"
                            placeholder="Written from the meeting if empty"
                          />
                        )}
                      </form.Field>
                      <form.Field name="people">
                        {(field) => (
                          <TextField
                            field={field}
                            label="People"
                            placeholder="Laura, Kai"
                            description="Who was there, separated by commas. They help name the speakers."
                          />
                        )}
                      </form.Field>
                    </FieldGroup>
                  </div>
                </CollapsibleContent>
              </div>
            </Collapsible>
            <p className="text-xs text-muted-foreground">
              Names and jargon are heard right from your{" "}
              <Link
                to="/settings/dictionary"
                className="underline underline-offset-2 hover:text-foreground"
              >
                Dictionary
              </Link>
              .
            </p>
            <ErrorAlert error={upload.error} />
          </FieldGroup>
        </form>
      </DialogPanel>
      <DialogFooter>
        <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
        <form.Subscribe selector={(s) => s.isSubmitting}>
          {(submitting) => (
            <Button type="submit" form="upload-meeting" disabled={submitting}>
              {submitting && <Spinner data-icon="inline-start" />}
              Upload
            </Button>
          )}
        </form.Subscribe>
      </DialogFooter>
    </>
  )
}
