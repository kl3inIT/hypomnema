import { useMutation, useQueryClient } from "@tanstack/react-query"
import {
  Check,
  CircleAlert,
  Clapperboard,
  Download,
  Film,
  LoaderCircle,
  RotateCcw,
  Trash2,
} from "lucide-react"
import { useState } from "react"

import "@/api/client"
import {
  approveVideoRevisionMutation,
  deleteVideoMutation,
  getVideoQueryKey,
  listStudioItemsInfiniteQueryKey,
  listVideosQueryKey,
  requestVideoChangesMutation,
  retryVideoRevisionMutation,
} from "@/api/generated/@tanstack/react-query.gen"
import type { Video, VideoRevision } from "@/api/generated/types.gen"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { clock } from "@/features/takes/take-state"
import { errorText } from "@/lib/server-error"
import { cn } from "@/lib/utils"

import { ChangeLook } from "./change-look"
import {
  latest,
  made,
  making,
  posterUrl,
  sceneDone,
  stages,
  stateName,
  stateTone,
  useVideo,
  videoUrl,
  type Stage,
} from "./studio-state"

// VideoView is a video as a tab (issue #818, rows 10 and 12): a bar with the take it was made of, a switch between
// its revisions, the MP4's download and the video's delete; under it the revision picked. While it is made, its
// stages and its scenes say how far it has come, as aidemo's render job says its stage and scenes done
// (src/mcp/jobs.ts:64-67); a failure says the stage and why, with "Make again", which adds a revision made as the
// failed one was asked: its lines, look and voice (POST /videos/{id}/revisions/{rev}/retry, #937). Once made, the player, and beside it or under it the review: each scene's line, editable,
// and a note; Approve, or Request changes, which queues a revision with the lines changed, reusing the voice of each
// line kept, or keeps the note alone for the agent; and Change look, which makes it again in another look (#938).
export function VideoView({
  id,
  onClose,
  onOpenTake,
}: {
  id: string
  onClose: () => void
  onOpenTake?: (take: string) => void
}) {
  const read = useVideo(id)
  const video = read.data
  const queryClient = useQueryClient()
  // picked: the revision the owner chose; none, the latest.
  const [picked, setPicked] = useState<number | null>(null)
  const [deleting, setDeleting] = useState(false)
  const rev = video?.revisions.find((r) => r.rev === picked) ?? (video && latest(video))
  const refresh = () =>
    queryClient.invalidateQueries({ queryKey: listStudioItemsInfiniteQueryKey() })
  const remove = useMutation({
    ...deleteVideoMutation(),
    onSuccess: () => {
      void refresh()
      void queryClient.invalidateQueries({ queryKey: listVideosQueryKey() })
      queryClient.removeQueries({ queryKey: getVideoQueryKey({ path: { video_id: id } }) })
      onClose()
    },
  })
  // A change that made a revision, or a make again, shows the new one.
  const changed = (next: Video) => {
    queryClient.setQueryData(getVideoQueryKey({ path: { video_id: id } }), next)
    void refresh()
    setPicked(null)
  }

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col" data-testid="video-view">
      <div className="flex h-11 shrink-0 items-center gap-2 border-b px-3">
        <Film className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <p className="min-w-0 flex-1 truncate text-sm">
          <span className="font-medium">{video?.title ?? "Video"}</span>
          {rev && made(rev) && (
            <span className="ms-2 text-muted-foreground">{clock(rev.length_ms)}</span>
          )}
        </p>
        {video && onOpenTake && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => onOpenTake(video.take)}
            aria-label="Open its take"
          >
            <Clapperboard />
            <span className="max-sm:hidden">Take</span>
          </Button>
        )}
        {video && video.revisions.length > 1 && rev && (
          <Select
            value={String(rev.rev)}
            onValueChange={(next) => next && setPicked(Number(next))}
            items={Object.fromEntries(
              video.revisions.map((r) => [String(r.rev), revisionLabel(r)]),
            )}
          >
            <SelectTrigger size="sm" aria-label="Revision">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {video.revisions.map((r) => (
                <SelectItem key={r.rev} value={String(r.rev)}>
                  {revisionLabel(r)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        {video && rev && made(rev) && (
          <Button
            variant="ghost"
            size="sm"
            nativeButton={false}
            render={
              <a
                href={videoUrl(id, rev.rev)}
                download={`${id}-${rev.rev}.mp4`}
                aria-label="Download the video"
              />
            }
          >
            <Download />
            <span className="max-sm:hidden">Download</span>
          </Button>
        )}
        {video && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setDeleting(true)}
            aria-label="Delete the video"
          >
            <Trash2 />
            <span className="max-sm:hidden">Delete</span>
          </Button>
        )}
      </div>
      {!video || !rev ? (
        <div className="flex flex-1 items-center justify-center bg-muted px-6 py-10">
          <p
            role={read.isError ? "alert" : "status"}
            className="flex items-center gap-2 text-sm text-muted-foreground"
          >
            {!read.isError && <Spinner />}
            {read.isError ? errorText(read.error) : "Reading the video…"}
          </p>
        </div>
      ) : made(rev) ? (
        <Made key={`${id}-${rev.rev}`} video={video} rev={rev} onChanged={changed} />
      ) : (
        // Keyed by its revision, as Made is: Make again of another revision is another request, with its own key.
        <Making key={`${id}-${rev.rev}`} video={video} rev={rev} onChanged={changed} />
      )}
      <AlertDialog open={deleting} onOpenChange={setDeleting}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete this video?</AlertDialogTitle>
            <AlertDialogDescription>
              Every revision and its file are removed from the server. The take it was made of
              stays. This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          {remove.error && (
            <p role="alert" className="text-sm text-destructive">
              {errorText(remove.error)}
            </p>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel>Keep it</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={remove.isPending}
              onClick={(event) => {
                event.preventDefault()
                remove.mutate({ path: { video_id: id } })
              }}
            >
              Delete the video
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

// revisionLabel names a revision in the switch: its number and its state.
const revisionLabel = (r: VideoRevision) => `Revision ${r.rev} · ${stateName("video", r.state)}`

const stageTone: Record<Stage["status"], string> = {
  done: "text-foreground",
  now: "text-running font-medium",
  waiting: "text-muted-foreground",
  failed: "text-destructive font-medium",
  skipped: "text-muted-foreground line-through",
}

// Making is a revision queued, being made, or failed: its stages, then its scenes, each marked done once the stage
// being made has done it.
function Making({
  video,
  rev,
  onChanged,
}: {
  video: Video
  rev: VideoRevision
  onChanged: (video: Video) => void
}) {
  const failed = rev.state === "failed"
  const steps = stages(rev)
  const current = steps.find((s) => s.status === "now")
  const again = useMutation({ ...retryVideoRevisionMutation(), onSuccess: onChanged })
  const [key] = useState(() => crypto.randomUUID())

  return (
    <div className="min-h-0 flex-1 overflow-y-auto bg-muted px-3 py-6 sm:px-6">
      <div className="mx-auto flex max-w-xl flex-col gap-4">
        <Alert
          variant={failed ? "destructive" : "default"}
          aria-label="Progress"
          data-testid="video-progress"
        >
          {failed ? <CircleAlert /> : <LoaderCircle className="animate-spin" />}
          <AlertTitle>
            {failed
              ? `Revision ${rev.rev} failed`
              : rev.state === "queued"
                ? `Revision ${rev.rev} is queued`
                : `Making revision ${rev.rev}: ${current?.name ?? stateName("video", rev.state)}`}
            {!failed && rev.progress && (
              <span className="ms-2 font-normal text-muted-foreground tabular-nums">
                {rev.progress.scenes_done} of {rev.progress.scenes_total} scenes
              </span>
            )}
          </AlertTitle>
          <AlertDescription>
            <div className="flex flex-col gap-2">
              <ol className="flex flex-wrap gap-x-4 gap-y-1 text-xs" aria-label="Stages">
                {steps.map((s) => (
                  <li
                    key={s.state}
                    className={cn("flex items-center gap-1", stageTone[s.status])}
                    data-status={s.status}
                  >
                    {s.status === "done" ? (
                      <Check className="size-3" aria-hidden />
                    ) : s.status === "now" ? (
                      <Spinner />
                    ) : (
                      <span className="size-1.5 rounded-full bg-current" aria-hidden />
                    )}
                    {s.name}
                    {s.status === "skipped" && <span className="sr-only">(captions off)</span>}
                  </li>
                ))}
              </ol>
              {failed && (
                <p>
                  {rev.failed_cause === "interrupted"
                    ? "The server restarted while it was made."
                    : (rev.failed_cause ?? "It could not be made.")}
                </p>
              )}
              {failed && (
                <div>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={again.isPending}
                    onClick={() =>
                      again.mutate({
                        headers: { "Idempotency-Key": key },
                        path: { video_id: video.id, rev: rev.rev },
                      })
                    }
                  >
                    {again.isPending ? <Spinner data-icon="inline-start" /> : <RotateCcw />}
                    Make again
                  </Button>
                  {again.error && (
                    <p role="alert" className="pt-2 text-destructive">
                      {errorText(again.error)}
                    </p>
                  )}
                </div>
              )}
            </div>
          </AlertDescription>
        </Alert>
        <section aria-label="Scenes" className="rounded-xl border bg-background">
          <h2 className="border-b px-4 py-2 text-xs font-medium text-muted-foreground">
            {rev.lines.length} {rev.lines.length === 1 ? "scene" : "scenes"}
          </h2>
          <ol>
            {rev.lines.map((line, i) => {
              const done = sceneDone(rev, i)
              const now =
                !failed &&
                making(rev.state) &&
                rev.progress !== undefined &&
                i === rev.progress.scenes_done

              return (
                <li
                  // Two scenes may say the same line.
                  key={i}
                  data-testid="video-scene"
                  data-done={done || undefined}
                  className="flex items-start gap-3 border-b px-4 py-2.5 text-sm last:border-b-0"
                >
                  <span className="mt-0.5 flex size-4 shrink-0 items-center justify-center">
                    {done ? (
                      <Check className="size-4 text-done" aria-label="done" />
                    ) : now ? (
                      <Spinner />
                    ) : (
                      <span className="size-1.5 rounded-full bg-muted-foreground/50" aria-hidden />
                    )}
                  </span>
                  <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
                    {i + 1}
                  </span>
                  <span className="min-w-0">{line}</span>
                </li>
              )
            })}
          </ol>
        </section>
      </div>
    </div>
  )
}

// Made is a made revision: the player, and the review.
function Made({
  video,
  rev,
  onChanged,
}: {
  video: Video
  rev: VideoRevision
  onChanged: (video: Video) => void
}) {
  const [lines, setLines] = useState(rev.lines)
  const [note, setNote] = useState("")
  const [key] = useState(() => crypto.randomUUID())
  const approve = useMutation({ ...approveVideoRevisionMutation(), onSuccess: onChanged })
  const changes = useMutation({ ...requestVideoChangesMutation(), onSuccess: onChanged })
  const reviewable = rev.state === "review" || rev.state === "changes_requested"
  const edited = lines.some((l, i) => l.trim() !== rev.lines[i])
  const emptyLine = lines.some((l) => l.trim() === "")
  const path = { video_id: video.id, rev: rev.rev }
  const error = approve.error ?? changes.error

  return (
    // The container is the outer box: a container query styles what is inside it, not the container itself.
    <div className="@container/video flex min-h-0 min-w-0 flex-1">
      <div className="flex min-h-0 min-w-0 flex-1 flex-col @3xl/video:flex-row">
        <div className="flex min-h-0 min-w-0 flex-1 items-center justify-center bg-muted p-3">
          {/* oxlint-disable-next-line jsx-a11y/media-has-caption -- the captions are burned into the picture */}
          <video
            key={rev.rev}
            src={videoUrl(video.id, rev.rev)}
            poster={posterUrl(video.id, rev.rev)}
            aria-label={`The video ${video.title}, revision ${rev.rev}`}
            data-testid="video-player"
            controls
            playsInline
            preload="metadata"
            className="max-h-full max-w-full rounded-lg bg-black shadow-lg"
          />
        </div>
        <aside
          aria-label="Review"
          className="flex max-h-[55%] min-h-0 shrink-0 flex-col overflow-y-auto border-t @3xl/video:max-h-none @3xl/video:w-96 @3xl/video:border-s @3xl/video:border-t-0"
        >
          <div className="flex items-center gap-2 border-b px-3 py-2">
            <h2 className="text-xs font-medium text-muted-foreground">Revision {rev.rev}</h2>
            <Badge variant={stateTone("video", rev.state)}>{stateName("video", rev.state)}</Badge>
            {rev.parent !== undefined && (
              <span className="text-xs text-muted-foreground">from revision {rev.parent}</span>
            )}
            <div className="ms-auto">
              <ChangeLook video={video} rev={rev} onChanged={onChanged} />
            </div>
          </div>
          {rev.note && (
            <p className="border-b px-3 py-2 text-sm">
              <span className="block text-xs font-medium text-muted-foreground">Your note</span>
              {rev.note}
            </p>
          )}
          {rev.warnings.length > 0 && (
            <ul className="border-b px-3 py-2 text-xs text-muted-foreground">
              {rev.warnings.map((w) => (
                <li key={w}>{w}</li>
              ))}
            </ul>
          )}
          <form
            className="flex flex-col gap-3 px-3 py-3"
            onSubmit={(event) => {
              event.preventDefault()
              changes.mutate({
                path,
                headers: { "Idempotency-Key": key },
                body: {
                  ...(edited ? { lines: lines.map((l) => l.trim()) } : {}),
                  note: note.trim(),
                },
              })
            }}
          >
            <fieldset className="flex flex-col gap-2" disabled={!reviewable}>
              <legend className="pb-1 text-xs font-medium text-muted-foreground">
                What each scene says
              </legend>
              {lines.map((line, i) => (
                <label key={i} className="flex items-start gap-2 text-sm">
                  <span className="w-4 shrink-0 pt-2 text-end text-xs text-muted-foreground tabular-nums">
                    {i + 1}
                  </span>
                  <Textarea
                    aria-label={`Scene ${i + 1}`}
                    value={line}
                    maxLength={1000}
                    rows={1}
                    onChange={(event) =>
                      setLines(lines.map((l, k) => (k === i ? event.target.value : l)))
                    }
                  />
                </label>
              ))}
            </fieldset>
            {reviewable && (
              <>
                <div className="flex flex-col gap-1.5 text-sm">
                  <label
                    htmlFor={`note-${video.id}-${rev.rev}`}
                    className="text-xs font-medium text-muted-foreground"
                  >
                    Note for the agent
                  </label>
                  <Textarea
                    id={`note-${video.id}-${rev.rev}`}
                    value={note}
                    maxLength={4000}
                    placeholder="What to change"
                    onChange={(event) => setNote(event.target.value)}
                  />
                </div>
                <p className="text-xs text-muted-foreground">
                  {edited
                    ? "Request changes makes a new revision with your lines; the voice of each line kept is reused."
                    : "A note alone is kept for the agent and makes no new revision."}
                </p>
                {error && (
                  <p role="alert" className="text-sm text-destructive">
                    {errorText(error)}
                  </p>
                )}
                <div className="flex flex-wrap gap-2">
                  {rev.state === "review" && (
                    <Button
                      type="button"
                      disabled={approve.isPending || edited}
                      title={edited ? "Your lines changed: request changes instead" : undefined}
                      onClick={() => approve.mutate({ path })}
                    >
                      {approve.isPending ? <Spinner data-icon="inline-start" /> : <Check />}
                      Approve
                    </Button>
                  )}
                  <Button
                    type="submit"
                    variant="outline"
                    disabled={changes.isPending || emptyLine || (!edited && note.trim() === "")}
                  >
                    {changes.isPending && <Spinner data-icon="inline-start" />}
                    Request changes
                  </Button>
                </div>
              </>
            )}
            {rev.state === "approved" && (
              <p className="text-sm text-muted-foreground">
                Approved. An approved revision is never changed; make a new one from its take.
              </p>
            )}
          </form>
        </aside>
      </div>
    </div>
  )
}
