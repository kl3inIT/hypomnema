import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Film } from "lucide-react"
import { useId, useState } from "react"

import "@/api/client"
import {
  getVideoQueryKey,
  listStudioItemsInfiniteQueryKey,
  listVideosQueryKey,
  makeVideoMutation,
} from "@/api/generated/@tanstack/react-query.gen"
import type { Video, VideoMake } from "@/api/generated/types.gen"
import { Alert, AlertDescription } from "@/components/ui/alert"
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
  DialogTrigger,
} from "@/components/ui/dialog"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { errorText } from "@/lib/server-error"

import { LookSection } from "./look-section"
import { editsOf, fieldsOf, sentLook, withEdits, type LookEdits } from "./look-state"
import { TemplatePicker } from "./template-picker"
import { aspectHints, aspectLabels, aspectNames, aspects, templateFields } from "./template-state"
import { useVideoLooks, useVideoTemplates } from "./use-look"

type Aspect = NonNullable<VideoMake["aspect"]>

// MakeVideo is a take's "Make video" button and its dialog (issue #818, rows 5, 6 and 8): the voice, captions on or
// off, and 9:16 or 16:9, as POST /videos takes them; the voice left empty is each provider's set on Settings →
// Audio. The take's first make creates its video, a later one adds a revision with the latest lines. The request
// carries an idempotency key made when the dialog opens, so a make sent twice queues one revision
// (docs/conventions.md). Once queued, the video opens. Its Look (#908, row 16) starts from the saved default, and
// the make sends only the sections the owner changed: the server takes the others from the default (#938). Each
// section the owner edited is kept apart, so one left alone follows the default when it is read again, as after a
// track's delete (#1082). A take that cannot be made, one with no steps (#1028), has the button disabled, described
// by the line saying why, as Request changes is disabled with its line (./video-view.tsx); the dialog and its look
// sample never open. A template (#1230, row 5; template-picker.tsx) sets the shape among its own and the captions,
// and its look is where the fields start: its sections over the saved default, its cards titled with the video's
// title; the make and the sample name it, so the server resolves the same look.
export function MakeVideo({
  take,
  title,
  again,
  why,
  onMade,
}: {
  take: string
  // title: the video's title, or the take's before its first make: a template's cards show it.
  title: string
  // again: the take has a video already, and a make adds a revision.
  again?: boolean
  // why: the id of the line saying why the take cannot be made into a video; the button is then disabled.
  why?: string
  onMade: (video: Video) => void
}) {
  const formId = useId()
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const [voice, setVoice] = useState("")
  const [captions, setCaptions] = useState(true)
  const [aspect, setAspect] = useState<Aspect>("9:16")
  // template: the template's id, empty for none.
  const [template, setTemplate] = useState("")
  const [key, setKey] = useState(() => crypto.randomUUID())
  // edits: the Look's sections the owner set; the others are the saved default's, or the template's (#938, #1082).
  const [edits, setEdits] = useState<LookEdits>({})
  const looks = useVideoLooks(open)
  const templates = useVideoTemplates(open)
  const chosen = templates.data?.templates.find((t) => t.id === template)
  const start =
    looks.data && (chosen ? templateFields(chosen, title) : fieldsOf(looks.data.default))
  const fields = start && withEdits(start, edits)
  const sent = start && fields && sentLook(start, fields)
  const named = chosen ? { template: chosen.id } : {}
  const make = useMutation({
    ...makeVideoMutation(),
    onSuccess: (video) => {
      queryClient.setQueryData(getVideoQueryKey({ path: { video_id: video.id } }), video)
      void queryClient.invalidateQueries({ queryKey: listStudioItemsInfiniteQueryKey() })
      // The take's tab finds its video in the list.
      void queryClient.invalidateQueries({ queryKey: listVideosQueryKey() })
      setOpen(false)
      onMade(video)
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (next) {
      setKey(crypto.randomUUID())
      setEdits({})
      setTemplate("")
      make.reset()
    }
  }

  // pick takes a template's shape and captions; a section the owner edited stays his.
  function pick(id: string) {
    setTemplate(id)
    const t = templates.data?.templates.find((x) => x.id === id)
    if (t) {
      setAspect(t.aspects[0]!)
      setCaptions(t.captions)
    }
  }

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger
        render={<Button variant="ghost" size="sm" />}
        disabled={why !== undefined}
        aria-describedby={why}
      >
        <Film />
        Make video
      </DialogTrigger>
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle>Make a video</DialogTitle>
          <DialogDescription>
            {again
              ? "A new revision with the latest lines: dead time cut, a Vietnamese voice for each step, captions and a ring at each tap."
              : "Dead time cut, a Vietnamese voice for each step, captions and a ring at each tap. You approve it once it is made."}
          </DialogDescription>
        </DialogHeader>
        <DialogPanel>
          <form
            id={formId}
            className="flex flex-col gap-5"
            onSubmit={(event) => {
              event.preventDefault()
              make.mutate({
                headers: { "Idempotency-Key": key },
                body: {
                  take,
                  captions,
                  aspect,
                  ...named,
                  ...(voice.trim() ? { voice: voice.trim() } : {}),
                  ...(sent ? { look: sent } : {}),
                },
              })
            }}
          >
            <TemplatePicker
              templates={{ data: templates.data?.templates, error: templates.error }}
              value={template}
              onChange={pick}
            />
            <Field>
              <FieldLabel htmlFor={`${formId}-voice`}>Voice</FieldLabel>
              <Input
                id={`${formId}-voice`}
                autoComplete="off"
                spellCheck={false}
                maxLength={100}
                placeholder="As set in Settings → Audio"
                value={voice}
                onChange={(event) => setVoice(event.target.value)}
              />
              <FieldDescription>
                A voice of the provider that speaks. Leave empty for the voice set for each
                provider.
              </FieldDescription>
            </Field>
            <Field orientation="horizontal">
              <FieldLabel htmlFor={`${formId}-captions`}>Captions</FieldLabel>
              <Switch
                id={`${formId}-captions`}
                checked={captions}
                onCheckedChange={(on) => setCaptions(on)}
              />
            </Field>
            <Field>
              <span className="text-sm font-medium" id={`${formId}-aspect`}>
                Shape
              </span>
              <ToggleGroup
                aria-labelledby={`${formId}-aspect`}
                value={[aspect]}
                onValueChange={(next: string[]) => next[0] && setAspect(next[0] as Aspect)}
                variant="choice"
                spacing={1}
              >
                {aspects.map((a) => (
                  <ToggleGroupItem
                    key={a}
                    value={a}
                    aria-label={aspectLabels[a]}
                    disabled={chosen !== undefined && !chosen.aspects.includes(a)}
                  >
                    {aspectNames[a]}
                  </ToggleGroupItem>
                ))}
              </ToggleGroup>
              <FieldDescription>
                {aspectHints[aspect]}
                {chosen &&
                  chosen.aspects.length === 1 &&
                  ` The template ${chosen.name} is made in it.`}
              </FieldDescription>
            </Field>
            {looks.data && fields ? (
              <LookSection
                looks={looks.data}
                value={fields}
                onChange={(next) => setEdits((was) => editsOf(was, fields, next))}
                music={looks.data.default.music}
                captions={captions}
                sample={{ take, aspect, captions, ...named, ...(sent ? { look: sent } : {}) }}
                open={open}
              />
            ) : (
              <p
                role={looks.isError ? "alert" : "status"}
                className="flex items-center gap-2 text-sm text-muted-foreground"
              >
                {looks.isError ? (
                  `The looks could not be read: ${errorText(looks.error)} The video is made in the saved look.`
                ) : (
                  <>
                    <Spinner /> Reading the looks…
                  </>
                )}
              </p>
            )}
            {make.error && (
              <Alert variant="destructive">
                <AlertDescription>{errorText(make.error)}</AlertDescription>
              </Alert>
            )}
          </form>
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          <Button type="submit" form={formId} disabled={make.isPending}>
            {make.isPending && <Spinner data-icon="inline-start" />}
            Make video
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
