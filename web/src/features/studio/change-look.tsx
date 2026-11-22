import { useMutation } from "@tanstack/react-query"
import { Palette } from "lucide-react"
import { useId, useState } from "react"

import "@/api/client"
import { changeVideoLookMutation } from "@/api/generated/@tanstack/react-query.gen"
import type { Video, VideoRevision } from "@/api/generated/types.gen"
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
import { Spinner } from "@/components/ui/spinner"
import { errorText } from "@/lib/server-error"

import { LookSection } from "./look-section"
import { fieldsOf, sampleOf, sentLook, type LookFields } from "./look-state"
import { useVideoLooks } from "./use-look"

// ChangeLook makes a made revision again in another look (issue #908, rows 4 and 16; #938): its Look starts from the
// revision's, its sample is drawn from that revision (rev, so its first line and its look are the base), and Change
// look posts only the sections changed to POST /videos/{id}/revisions/{rev}/look, with an idempotency key made when
// the dialog opens. The server queues a new revision after the latest, with this one as its parent, its lines and
// its voice; the video shows it.
export function ChangeLook({
  video,
  rev,
  onChanged,
}: {
  video: Video
  rev: VideoRevision
  onChanged: (video: Video) => void
}) {
  const formId = useId()
  const [open, setOpen] = useState(false)
  const [key, setKey] = useState(() => crypto.randomUUID())
  const [look, setLook] = useState<LookFields>()
  const looks = useVideoLooks(open)
  const start = fieldsOf(rev.look)
  const fields = look ?? start
  const sent = sentLook(start, fields)
  const change = useMutation({
    ...changeVideoLookMutation(),
    onSuccess: (next) => {
      setOpen(false)
      onChanged(next)
    },
  })

  function toggle(next: boolean) {
    setOpen(next)
    if (next) {
      setKey(crypto.randomUUID())
      setLook(undefined)
      change.reset()
    }
  }

  return (
    <Dialog open={open} onOpenChange={toggle}>
      <DialogTrigger render={<Button variant="outline" size="sm" />}>
        <Palette />
        Change look
      </DialogTrigger>
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle>Change the look</DialogTitle>
          <DialogDescription>
            A new revision of revision {rev.rev} in another look: its lines and its voice, made
            again. Revision {rev.rev} stays as it is.
          </DialogDescription>
        </DialogHeader>
        <DialogPanel>
          <form
            id={formId}
            className="flex flex-col gap-5"
            onSubmit={(event) => {
              event.preventDefault()
              if (!sent) return
              change.mutate({
                headers: { "Idempotency-Key": key },
                path: { video_id: video.id, rev: rev.rev },
                body: { look: sent },
              })
            }}
          >
            {looks.data ? (
              <LookSection
                looks={looks.data}
                value={fields}
                onChange={setLook}
                music={rev.look.music}
                captions={rev.captions}
                sample={sampleOf({
                  take: video.take,
                  aspect: rev.aspect,
                  captions: rev.captions,
                  rev: rev.rev,
                  ...(sent ? { look: sent } : {}),
                })}
                open={open}
              />
            ) : (
              <p
                role={looks.isError ? "alert" : "status"}
                className="flex items-center gap-2 text-sm text-muted-foreground"
              >
                {looks.isError ? (
                  `The looks could not be read: ${errorText(looks.error)}`
                ) : (
                  <>
                    <Spinner /> Reading the looks…
                  </>
                )}
              </p>
            )}
            {change.error && (
              <Alert variant="destructive">
                <AlertDescription>{errorText(change.error)}</AlertDescription>
              </Alert>
            )}
          </form>
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          <Button
            type="submit"
            form={formId}
            disabled={change.isPending || !sent}
            title={sent ? undefined : "Change something in the look first"}
          >
            {change.isPending && <Spinner data-icon="inline-start" />}
            Change look
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
