import { Pause, Play } from "lucide-react"
import { useRef, useState, type ReactNode } from "react"

import { Button } from "@/components/ui/button"
import { Slider } from "@/components/ui/slider"

import { clock } from "./meetings-state"
import { PlayerContext, useSeek } from "./player-context"

// The recording's player, on top of a meeting's page as Otter's, Fireflies's and Nojoin's, with its time shown and
// seeked from any time on the page (a decision's, an action's, a line's). Adapted from assistant-ui's AudioPlayer
// (MIT, Copyright (c) 2026 AgentbaseAI Inc., https://github.com/assistant-ui/assistant-ui;
// packages/ui/src/components/react/assistant-ui/elements/media-player.tsx): a hidden <audio> with a play button, a
// seek bar and "current / length", its playback state kept from the element's own events. Changed: built from this
// app's Button and Slider; the length comes from the meeting when the file has not told it yet; a time elsewhere on
// the page seeks it through PlayerContext.
export function PlayerProvider({
  src,
  durationMs,
  children,
}: {
  src: string
  durationMs: number
  children: (player: ReactNode) => ReactNode
}) {
  const audio = useRef<HTMLAudioElement>(null)
  const [at, setAt] = useState(0)
  const [playing, setPlaying] = useState(false)
  const [length, setLength] = useState(durationMs / 1000)
  const [broken, setBroken] = useState(false)

  function seek(ms: number) {
    const a = audio.current
    if (!a || broken) return

    a.currentTime = ms / 1000
    setAt(ms / 1000)
    void a.play().catch(() => setPlaying(false))
  }

  function toggle() {
    const a = audio.current
    if (!a || broken) return

    if (playing) a.pause()
    else void a.play().catch(() => setPlaying(false))
  }

  const player = (
    <div
      data-slot="meeting-player"
      className="flex w-full items-center gap-3 rounded-xl border bg-card px-3 py-2"
    >
      <Button
        variant="secondary"
        size="icon"
        aria-label={playing ? "Pause" : "Play"}
        disabled={broken}
        onClick={toggle}
      >
        {playing ? <Pause /> : <Play />}
      </Button>
      <span
        className="shrink-0 font-mono text-xs text-muted-foreground tabular-nums"
        data-testid="player-time"
      >
        {clock(at * 1000)} / {length > 0 ? clock(length * 1000) : "--:--"}
      </span>
      <div className="min-w-0 flex-1">
        <Slider
          aria-label="Seek"
          min={0}
          max={Math.max(length, 0.1)}
          step={0.1}
          value={[Math.min(at, length)]}
          disabled={broken}
          onValueChange={(value) => {
            const next = Array.isArray(value) ? value[0] : value
            if (audio.current && Number.isFinite(next)) {
              audio.current.currentTime = next
              setAt(next)
            }
          }}
        />
      </div>
      {broken && (
        <span role="alert" className="shrink-0 text-xs text-muted-foreground">
          Can&apos;t play this recording
        </span>
      )}
      {/* oxlint-disable-next-line jsx-a11y/media-has-caption -- the transcript on the page is the recording's text. */}
      <audio
        ref={audio}
        src={src}
        preload="metadata"
        className="hidden"
        onLoadedMetadata={(e) => {
          const d = e.currentTarget.duration
          if (Number.isFinite(d) && d > 0) setLength(d)
        }}
        onTimeUpdate={(e) => setAt(e.currentTarget.currentTime)}
        onPlay={() => setPlaying(true)}
        onPause={() => setPlaying(false)}
        onError={() => {
          setBroken(true)
          setPlaying(false)
        }}
      />
    </div>
  )

  return (
    <PlayerContext.Provider value={{ seek, at: at * 1000 }}>
      {children(player)}
    </PlayerContext.Provider>
  )
}

// TimeLink is a time on the page that plays the recording from there. It is its own design, not a Button: a time
// inside a line of text, as the prototype and Otter show it, in the link's color with a small ▶.
export function TimeLink({ ms, quiet }: { ms: number; quiet?: boolean }) {
  const seek = useSeek()

  if (!seek) return <span className="text-xs tabular-nums">{clock(ms)}</span>

  return (
    <button
      type="button"
      aria-label={`Play from ${clock(ms)}`}
      onClick={() => seek(ms)}
      className={
        quiet
          ? "inline-flex items-center gap-0.5 text-xs text-muted-foreground tabular-nums hover:text-link"
          : "inline-flex items-center gap-0.5 text-xs text-link tabular-nums hover:underline"
      }
    >
      <Play className="size-3" aria-hidden />
      {clock(ms)}
    </button>
  )
}
