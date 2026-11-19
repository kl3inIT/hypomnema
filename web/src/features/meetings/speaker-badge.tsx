import type { MeetingSpeaker } from "@/api/generated/types.gen"
import { cn } from "@/lib/utils"

import { sameVoice, speakerFill, speakerInitial, type Voice } from "./meetings-state"

// SpeakerBadge is a voice's badge, as MemoryOS's (web/src/features/meetings/speaker-chip.tsx; issue #807): its color,
// filled, with its number or initial on it. Two voices are told apart by what the badge reads as well as by its
// color, so a turn is placed at a glance down a long page. A voice is a label of its track (issue #820).
export function SpeakerBadge({
  speakers,
  voice,
  size = "md",
  className,
}: {
  speakers: MeetingSpeaker[]
  voice: Voice
  size?: "sm" | "md"
  className?: string
}) {
  const speaker = speakers.find((s) => sameVoice(s, voice))
  const label = voice.label

  return (
    <span
      aria-hidden
      className={cn(
        "inline-grid shrink-0 place-items-center rounded-full leading-none font-semibold text-speaker-content",
        size === "sm" ? "size-4 text-[10px]" : "size-6 text-xs",
        speakerFill(speakers, voice),
        className,
      )}
    >
      {speakerInitial(speaker ?? { label, name: "", named: false })}
    </span>
  )
}
