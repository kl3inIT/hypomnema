import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Film } from "lucide-react"
import { useId, useState } from "react"

import "@/api/client"
import {
  getVideoQueryKey,
  listStudioItemsInfiniteQueryKey,
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

type Aspect = NonNullable<VideoMake["aspect"]>

// MakeVideo is a take's "Make video" button and its dialog (issue #818, rows 5, 6 and 8): the voice, captions on or
// off, and 9:16 or 16:9, as POST /videos takes them; the voice left empty is each provider's set on Settings →
// Audio. The take's first make creates its video, a later one adds a revision with the latest lines. The request
// carries an idempotency key made when the dialog opens, so a make sent twice queues one revision
// (docs/conventions.md). Once queued, the video opens.
export function MakeVideo({
  take,
  again,
  onMade,
}: {
  take: string
  // again: the take has a video already, and a make adds a revision.
  again?: boolean
  onMade: (video: Video) => void
}) {
  const formId = useId()
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const [voice, setVoice] = useState("")
  const [captions, setCaptions] = useState(true)
  const [aspect, setAspect] = useState<Aspect>("9:16")
  const [key, setKey] = useState(() => crypto.randomUUID())
  const make = useMutation({
    ...makeVideoMutation(),
    onSuccess: (video) => {
      queryClient.setQueryData(getVideoQueryKey({ path: { video_id: video.id } }), video)
      void queryClient.invalidateQueries({ queryKey: listStudioItemsInfiniteQueryKey() })
      setOpen(false)
      onMade(video)
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (next) {
      setKey(crypto.randomUUID())
      make.reset()
    }
  }

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger render={<Button variant="ghost" size="sm" />}>
        <Film />
        Make video
      </DialogTrigger>
      <DialogContent>
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
                body: { take, captions, aspect, ...(voice.trim() ? { voice: voice.trim() } : {}) },
              })
            }}
          >
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
                variant="outline"
                spacing={0}
              >
                <ToggleGroupItem value="9:16" aria-label="9:16, upright">
                  9:16
                </ToggleGroupItem>
                <ToggleGroupItem value="16:9" aria-label="16:9, wide">
                  16:9
                </ToggleGroupItem>
              </ToggleGroup>
              <FieldDescription>
                {aspect === "9:16"
                  ? "Upright, the phone's own shape: 720 × 1280."
                  : "Wide, the phone in the middle on black: 1280 × 720."}
              </FieldDescription>
            </Field>
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
