import type { MeetingSpeaker } from "@/api/generated/types.gen"
import { cn } from "@/lib/utils"

import { speakerFill, speakerInitial } from "./meetings-state"

// SpeakerBadge is a voice's badge, as MemoryOS's (web/src/features/meetings/speaker-chip.tsx; issue #807): its color,
// filled, with its number or initial on it. Two voices are told apart by what the badge reads as well as by its
// color, so a turn is placed at a glance down a long page.
export function SpeakerBadge({
  speakers,
  label,
  size = "md",
  className,
}: {
  speakers: MeetingSpeaker[]
  label: string
  size?: "sm" | "md"
  className?: string
}) {
  const speaker = speakers.find((s) => s.label === label)

  return (
    <span
      aria-hidden
      className={cn(
        "inline-grid shrink-0 place-items-center rounded-full leading-none font-semibold text-speaker-content",
        size === "sm" ? "size-4 text-[10px]" : "size-6 text-xs",
        speakerFill(speakers, label),
        className,
      )}
    >
      {speakerInitial(speaker ?? { label, name: "", named: false })}
    </span>
  )
}
