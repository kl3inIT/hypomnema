import { useForm } from "@tanstack/react-form"
import { useQueryClient } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { ChevronDown, Mic } from "lucide-react"
import { useState } from "react"
import { z } from "zod"

import "@/api/client"
import { listMeetingsQueryKey } from "@/api/generated/@tanstack/react-query.gen"
import { startLiveMeeting } from "@/api/generated/sdk.gen"
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
import {
  Field,
  FieldContent,
  FieldDescription,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSet,
  FieldTitle,
} from "@/components/ui/field"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { TextField } from "@/components/text-field"
import { everyCall } from "@/lib/query-keys"

import { captureSupport, openMeetingSources, sourceError } from "./meeting-capture"
import { startRecording } from "./meeting-session"
import { languages, listOf } from "./meetings-state"

// RecordMeeting is the "Record" button beside "Upload recording" and its dialog (issue #820), after MemoryOS's
// new-meeting-dialog.tsx: how the meeting is held, online (the microphone and a shared tab) or in person (the
// microphone only), its language, and behind "More details" the title and the people, as the upload asks them.
// Starting asks for the tab first, while the click still counts as the person's (Nojoin's order), then the
// microphone, then the meeting is made and its page opens with the recording bar. A tab's sound can only be shared
// from Chromium on a desktop, so elsewhere the dialog offers In person only and says why.

const schema = z.object({
  kind: z.enum(["online", "in_person"]),
  language: z.string(),
  title: z.string().max(200, "At most 200 characters."),
  people: z.string(),
})

export function RecordMeeting() {
  const [open, setOpen] = useState(false)

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button variant="outline" />}>
        <Mic data-icon="inline-start" />
        Record
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Record a meeting</DialogTitle>
          <DialogDescription>
            It is transcribed as it is spoken, with who said what, and kept so you can play it back.
            Your notes model writes it up when you stop.
          </DialogDescription>
        </DialogHeader>
        {open && <Body onDone={() => setOpen(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function Body({ onDone }: { onDone: () => void }) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [failure, setFailure] = useState<unknown>(null)
  const sharesTabs = captureSupport() === "sharedAudio"
  const form = useForm({
    defaultValues: {
      kind: (sharesTabs ? "online" : "in_person") as "online" | "in_person",
      language: "auto",
      title: "",
      people: "",
    },
    validators: { onSubmit: schema },
    onSubmit: async ({ value }) => {
      setFailure(null)
      let sources: Awaited<ReturnType<typeof openMeetingSources>>
      try {
        sources = await openMeetingSources(value.kind)
      } catch (error) {
        setFailure(new Error(sourceError(error)))

        return
      }

      const stopAll = () => {
        for (const track of [
          ...sources.microphone.getTracks(),
          ...(sources.tab?.getTracks() ?? []),
        ])
          track.stop()
      }

      try {
        const people = listOf(value.people)
        const made = (
          await startLiveMeeting({
            body: {
              kind: value.kind,
              ...(value.language !== "auto" ? { language: value.language } : {}),
              ...(value.title.trim() ? { title: value.title.trim() } : {}),
              ...(people.length ? { people } : {}),
            },
          })
        ).data
        await cache.invalidateQueries({ queryKey: everyCall(listMeetingsQueryKey()) })
        await startRecording(
          made.id,
          [
            { track: "mic", stream: sources.microphone, offsetMs: 0 },
            ...(sources.tab ? [{ track: "tab" as const, stream: sources.tab, offsetMs: 0 }] : []),
          ],
          cache,
        )
        onDone()
        await navigate({ to: "/meetings/$meetingId", params: { meetingId: made.id } })
      } catch (error) {
        stopAll()
        setFailure(error)
      }
    },
  })

  return (
    <>
      <DialogPanel>
        <form
          id="record-meeting"
          noValidate
          onSubmit={(e) => {
            e.preventDefault()
            void form.handleSubmit()
          }}
        >
          <FieldGroup>
            <form.Field name="kind">
              {(field) => (
                <FieldSet>
                  <FieldLegend variant="label">Where it is held</FieldLegend>
                  <RadioGroup
                    value={field.state.value}
                    onValueChange={(next) => field.handleChange(next as "online" | "in_person")}
                  >
                    <FieldLabel htmlFor="record-online">
                      <Field orientation="horizontal" data-disabled={!sharesTabs || undefined}>
                        <RadioGroupItem id="record-online" value="online" disabled={!sharesTabs} />
                        <FieldContent>
                          <FieldTitle>Online</FieldTitle>
                          <FieldDescription>
                            {sharesTabs
                              ? "Your microphone and the call's tab, shared with its sound."
                              : "A tab's sound can be shared only from Chrome or Edge on a computer."}
                          </FieldDescription>
                        </FieldContent>
                      </Field>
                    </FieldLabel>
                    <FieldLabel htmlFor="record-in-person">
                      <Field orientation="horizontal">
                        <RadioGroupItem id="record-in-person" value="in_person" />
                        <FieldContent>
                          <FieldTitle>In person</FieldTitle>
                          <FieldDescription>The room, through your microphone.</FieldDescription>
                        </FieldContent>
                      </Field>
                    </FieldLabel>
                  </RadioGroup>
                </FieldSet>
              )}
            </form.Field>
            <form.Field name="language">
              {(field) => (
                <Field>
                  <FieldLabel htmlFor="record-language">Language</FieldLabel>
                  <Select
                    value={field.state.value}
                    onValueChange={(v) => v && field.handleChange(v)}
                  >
                    <SelectTrigger id="record-language" width="full">
                      <SelectValue>
                        {(v: string) =>
                          v === "auto"
                            ? "Find it as it is spoken"
                            : (languages.find((l) => l.code === v)?.name ?? v)
                        }
                      </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      {languages.map((l) => (
                        <SelectItem key={l.code} value={l.code}>
                          {l.code === "auto" ? "Find it as it is spoken" : l.name}
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
                            description="Who is there, separated by commas. They help name the speakers."
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
                to="/meetings/settings"
                hash="dictionary"
                className="underline underline-offset-2 hover:text-foreground"
              >
                Dictionary
              </Link>
              .
            </p>
            <ErrorAlert error={failure} />
          </FieldGroup>
        </form>
      </DialogPanel>
      <DialogFooter>
        <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
        <form.Subscribe selector={(s) => s.isSubmitting}>
          {(submitting) => (
            <Button type="submit" form="record-meeting" disabled={submitting}>
              {submitting ? <Spinner data-icon="inline-start" /> : <Mic data-icon="inline-start" />}
              Start recording
            </Button>
          )}
        </form.Subscribe>
      </DialogFooter>
    </>
  )
}
