import { CircleAlert, Music, Pause, Play, Plus, Trash2 } from "lucide-react"
import { useEffect, useId, useRef, useState } from "react"

import type { VideoMusic, VideoMusicTrack } from "@/api/generated/types.gen"
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
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Slider } from "@/components/ui/slider"
import { Spinner } from "@/components/ui/spinner"
import { clock } from "@/features/takes/take-state"
import { errorText } from "@/lib/server-error"
import { cn } from "@/lib/utils"

import type { LookFields } from "./look-state"
import { amount, audioUrl, gainText, goneOf, playable, trackName } from "./music-state"
import { useDeleteTrack, useUploadTrack, useVideoMusic } from "./use-music"

type MusicFields = LookFields["music"]

// The formats an upload is taken in (row 6b), for the file picker.
const accept = "audio/*,.mp3,.m4a,.mp4,.wav,.flac,.ogg,.opus"

// MusicGroup is the Music of the Look (issue #910, row 18): the owner's library from GET /videos/music as a list to
// pick from, No music first, as Cap's audio library lists its tracks in rows under its Import button
// (apps/desktop-gpui/src/editor_audio.rs:165-298; AGPLv3, layout only). Each row plays its track from its audio
// endpoint, with an error state when this browser cannot play it, and deletes it after a confirm. "Add music…"
// uploads a file and picks it, saying the server's reason when it refuses (411, 413, 429, 507). A track the look
// names that was deleted stays in the list as "(deleted)", with what the server will do; a track whose file is
// missing says so. The level moves the track from 3 LU under the voice, −20 to +10 dB. The fields are sent as
// {track, gain_db} only when changed (look-state.ts).
export function MusicGroup({
  music,
  stored,
  onMusic,
  enabled,
}: {
  music: MusicFields
  // stored: the music of the look the fields start from (the default's or the revision's), which kept the name of
  // a track since deleted, or the track it fell back from.
  stored: VideoMusic | undefined
  onMusic: (next: MusicFields) => void
  // enabled: the Look is shown, so the library is read.
  enabled: boolean
}) {
  const id = useId()
  const library = useVideoMusic(enabled)
  const tracks = library.data?.tracks
  const upload = useUploadTrack()
  const remove = useDeleteTrack()
  const input = useRef<HTMLInputElement>(null)
  const [deleting, setDeleting] = useState<VideoMusicTrack>()
  const audio = useRef<HTMLAudioElement>(null)
  const player = usePlayer(audio)
  // latest is the newest onMusic, for an upload or a delete that ends after the owner changed the look meanwhile.
  const latest = useRef({ music, onMusic })
  useEffect(() => {
    latest.current = { music, onMusic }
  })

  const gone = goneOf(music.track, tracks, stored)
  const fellBack = stored?.track === "none" ? stored.fell_back_from : undefined

  function choose(file: File | undefined) {
    if (!file) return
    upload.mutate(file, {
      onSuccess: (track) => latest.current.onMusic({ ...latest.current.music, track: track.id }),
    })
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <input
          ref={input}
          type="file"
          accept={accept}
          className="hidden"
          aria-label="Music file"
          onChange={(event) => {
            choose(event.target.files?.[0])
            event.target.value = ""
          }}
        />
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={upload.isPending}
          onClick={() => input.current?.click()}
        >
          {upload.isPending ? <Spinner data-icon="inline-start" /> : <Plus />}
          Add music…
        </Button>
        {library.data && (
          <span className="text-xs text-muted-foreground" data-testid="music-usage">
            {amount(library.data.used_bytes)} of {amount(library.data.quota_bytes)} used
          </span>
        )}
      </div>
      {upload.isPending && upload.variables && (
        <output className="flex items-center gap-2 text-xs text-muted-foreground">
          Uploading “{trackName(upload.variables.name)}” and checking that it plays whole…
        </output>
      )}
      {upload.error && (
        <Alert variant="destructive">
          <CircleAlert />
          <AlertTitle>
            {upload.variables ? `“${trackName(upload.variables.name)}” was not added` : "Not added"}
          </AlertTitle>
          <AlertDescription>{errorText(upload.error)}</AlertDescription>
        </Alert>
      )}
      {fellBack && music.track === "none" && (
        <p role="note" className="text-xs text-muted-foreground" data-testid="music-fell-back">
          “{fellBack.name}” was deleted, so this look was made without music.
        </p>
      )}
      {library.isError && (
        <p role="alert" className="text-xs text-destructive">
          The music could not be read: {errorText(library.error)}
        </p>
      )}
      <RadioGroup
        aria-label="Music"
        value={music.track}
        onValueChange={(next) => onMusic({ ...music, track: String(next) })}
      >
        <Row id={`${id}-none`} value="none" title="No music" />
        {gone && (
          <Row
            id={`${id}-gone`}
            value={gone.track}
            title={`${gone.name ?? gone.track} (deleted)`}
            detail={gone.length_ms ? clock(gone.length_ms) : undefined}
            disabled
            note="This track was deleted. Left as it is, the video is made without music; choose another or No music."
            tone="warning"
          />
        )}
        {tracks?.map((t) => {
          const broken = player.broken.has(t.id)

          return (
            <Row
              key={t.id}
              id={`${id}-${t.id}`}
              value={t.id}
              title={t.name}
              detail={`${clock(t.length_ms)} · ${amount(t.bytes)}`}
              badge={t.missing ? "missing" : undefined}
              disabled={t.missing && music.track !== t.id}
              note={
                t.missing
                  ? "Its file is missing, as after restoring an older backup."
                  : broken
                    ? "This browser cannot play this track."
                    : undefined
              }
              tone={t.missing ? "warning" : "destructive"}
            >
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label={player.playing === t.id ? `Pause ${t.name}` : `Play ${t.name}`}
                disabled={t.missing || broken}
                onClick={() => player.toggle(t)}
              >
                {player.playing === t.id ? <Pause /> : <Play />}
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label={`Delete ${t.name}`}
                onClick={() => {
                  remove.reset()
                  setDeleting(t)
                }}
              >
                <Trash2 />
              </Button>
            </Row>
          )
        })}
      </RadioGroup>
      {library.isPending && enabled && (
        <output className="flex items-center gap-2 text-xs text-muted-foreground">
          <Spinner /> Reading the music…
        </output>
      )}
      {tracks?.length === 0 && (
        <p className="text-xs text-muted-foreground" data-testid="music-empty">
          No tracks yet. Add one of yours: mp3, m4a, wav, flac or ogg, 10 seconds to 20 minutes, up
          to 64 MB.
        </p>
      )}
      <div className={cn("flex flex-col gap-1.5", music.track === "none" && "opacity-60")}>
        <div className="flex items-center justify-between text-sm">
          <span id={`${id}-level`}>Level</span>
          <span className="text-xs text-muted-foreground tabular-nums" data-testid="music-level">
            {gainText(music.gain_db)}
          </span>
        </div>
        <Slider
          aria-labelledby={`${id}-level`}
          min={-20}
          max={10}
          step={1}
          value={[music.gain_db]}
          disabled={music.track === "none"}
          onValueChange={(value) => {
            const next = Array.isArray(value) ? value[0] : value
            if (typeof next === "number" && Number.isFinite(next)) {
              onMusic({ ...music, gain_db: next })
            }
          }}
        />
        <p className="text-xs text-muted-foreground">
          0 dB lays the track 3 LU under the voice, quieter still while it speaks.
        </p>
      </div>
      {/* oxlint-disable-next-line jsx-a11y/media-has-caption -- music, with no words to caption. */}
      <audio
        ref={audio}
        preload="none"
        className="hidden"
        data-testid="music-player"
        onPlaying={player.onPlaying}
        onPause={player.onStop}
        onEnded={player.onStop}
        onError={player.onError}
      />
      <AlertDialog
        open={deleting !== undefined}
        onOpenChange={(open) => open || setDeleting(undefined)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete “{deleting?.name}”?</AlertDialogTitle>
            <AlertDialogDescription>
              Videos made with it keep their sound. A retry or a look change of them is made without
              music. If your saved look uses it, it uses no music after this.
            </AlertDialogDescription>
          </AlertDialogHeader>
          {remove.error && (
            <Alert variant="destructive">
              <AlertDescription>{errorText(remove.error)}</AlertDescription>
            </Alert>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel>Keep it</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={remove.isPending}
              onClick={(event) => {
                event.preventDefault()
                const track = deleting
                if (!track) return
                remove.mutate(
                  { path: { music_id: track.id } },
                  {
                    onSuccess: () => {
                      setDeleting(undefined)
                      if (player.playing === track.id) player.stop()
                      const now = latest.current
                      if (now.music.track === track.id) now.onMusic({ ...now.music, track: "none" })
                    },
                  },
                )
              }}
            >
              {remove.isPending && <Spinner data-icon="inline-start" />}
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

// Row is one choice of the list: its radio and name, a line under it, and its buttons beside.
function Row({
  id,
  value,
  title,
  detail,
  badge,
  note,
  tone,
  disabled,
  children,
}: {
  id: string
  value: string
  title: string
  detail?: string
  badge?: string
  note?: string
  tone?: "warning" | "destructive"
  disabled?: boolean
  children?: React.ReactNode
}) {
  return (
    <div
      className="flex min-w-0 items-center gap-2.5 rounded-lg border px-2.5 py-1.5 has-data-checked:border-primary has-data-checked:bg-primary/5"
      data-testid="music-row"
    >
      <RadioGroupItem id={id} value={value} disabled={disabled} />
      <label
        htmlFor={id}
        className={cn("flex min-w-0 flex-1 flex-col py-0.5", !disabled && "cursor-pointer")}
      >
        <span className="flex min-w-0 items-center gap-1.5 text-sm">
          {value === "none" ? (
            <span className="truncate">{title}</span>
          ) : (
            <>
              <Music className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
              <span className="truncate">{title}</span>
            </>
          )}
          {badge && <Badge variant="warning">{badge}</Badge>}
        </span>
        {detail && <span className="text-xs text-muted-foreground tabular-nums">{detail}</span>}
        {note && (
          <span
            className={cn(
              "text-xs",
              tone === "destructive" ? "text-destructive" : "text-needs-you",
            )}
            role={tone === "destructive" ? "alert" : undefined}
          >
            {note}
          </span>
        )}
      </label>
      {children && <span className="flex shrink-0 items-center">{children}</span>}
    </div>
  )
}

// usePlayer is the one player of the group (row 18): a track plays from its audio endpoint, one at a time, pressed
// again to pause, as OpenCut's sound list plays a sound's preview (MIT, Copyright (c) OpenCut;
// apps/web/src/sounds/components/assets-view.tsx:199-224 at cf5e79e: one playing id, the playing one paused, the id
// cleared when it ends, errs or its play is refused). Changed: one <audio> element for the group, and a track whose
// type this browser says it cannot play (canPlayType, as chromium.org/audio-video lists what it plays), or that it
// fails to play, is kept in an error state rather than only stopped.
function usePlayer(ref: React.RefObject<HTMLAudioElement | null>) {
  const [playing, setPlaying] = useState<string>()
  const [broken, setBroken] = useState<ReadonlySet<string>>(new Set())
  // loaded: the track the element holds.
  const loaded = useRef<string>(undefined)

  const fail = (track: string) => {
    setBroken((was) => new Set(was).add(track))
    setPlaying((now) => (now === track ? undefined : now))
  }

  function toggle(track: VideoMusicTrack) {
    const audio = ref.current
    if (!audio) return
    if (playing === track.id) {
      audio.pause()

      return
    }
    if (!playable(track.media_type, audio)) {
      fail(track.id)

      return
    }
    if (loaded.current !== track.id) {
      loaded.current = track.id
      audio.src = audioUrl(track.id)
    }
    audio.play().catch((error: unknown) => {
      // A play the page dropped, as another track's taking its place, is no failure of the track's.
      if (error instanceof DOMException && error.name === "AbortError") return
      fail(track.id)
    })
  }

  return {
    playing,
    broken,
    toggle,
    stop: () => ref.current?.pause(),
    onPlaying: () => setPlaying(loaded.current),
    onStop: () => setPlaying(undefined),
    onError: () => loaded.current && fail(loaded.current),
  }
}
