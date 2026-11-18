import { Plus } from "lucide-react"
import { useState, type ReactElement } from "react"

import type { MeetingDetail, MeetingLine } from "@/api/generated/types.gen"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { toast } from "@/components/ui/toast"
import { ErrorAlert } from "@/components/error-alert"

import { sameName } from "./meetings-state"
import { SpeakerBadge } from "./speaker-badge"
import { useLineSpeaker, useNameSpeaker } from "./use-meetings"

// SpeakerPicker says who spoke a line, from the line's speaker name. Its shape is MemoryOS's LineSpeakerPicker
// (web/src/features/meetings/line-speaker-picker.tsx, the owner's own): a choice between this line and every line of
// the voice, then the names to pick; the scopes are Nojoin's (backend/models/pipeline.py:77-81, utterance_only and
// speaker_everywhere_in_recording) and its picker (frontend/src/components/SpeakerAssignmentPopover.tsx, read only:
// AGPL) lists suggestions first. The names are: a name a voice gave itself or one of the people the owner listed
// (MemoryOS SpeakerIntroductions, Otter's suggestions), the meeting's other voices, where the whole meeting given one
// merges the two (Otter, Nojoin, silent-notetaker), and a new name typed in. Built on this app's Command, as a
// searchable list of choices is everywhere else.
type Scope = "line" | "meeting"

export function SpeakerPicker({
  meeting,
  line,
  children,
}: {
  meeting: MeetingDetail
  line: MeetingLine
  children: ReactElement
}) {
  const [open, setOpen] = useState(false)
  const [scope, setScope] = useState<Scope>("line")
  const [typed, setTyped] = useState("")
  const id = meeting.meeting.id
  const forLine = useLineSpeaker(id)
  const forMeeting = useNameSpeaker(id)
  const speakers = meeting.meeting.speakers
  const self = speakers.find((s) => s.label === line.speaker)
  const current = self?.name ?? ""
  const pending = forLine.isPending || forMeeting.isPending

  const suggested: { name: string; why: string }[] = []
  const add = (name: string, why: string) => {
    if (sameName(name, current)) return
    const known = suggested.find((s) => sameName(s.name, name))
    if (known) known.why = `${known.why} · ${why}`
    else suggested.push({ name, why })
  }

  for (const s of meeting.suggestions) {
    if (s.label !== line.speaker) continue
    const said = meeting.lines.find((l) => l.n === s.line)
    add(s.name, said ? `said it at line ${s.line}` : "said it")
  }

  for (const person of meeting.meeting.people) {
    if (!speakers.some((s) => sameName(s.name, person))) add(person, "in People")
  }

  const others = speakers.filter((s) => s.label !== line.speaker)

  function choose(name: string) {
    const next = name.trim()
    if (!next || pending) return

    const merging = others.find((s) => sameName(s.name, next))
    const done = () => {
      setOpen(false)
      setTyped("")
      toast.add({
        title:
          scope === "meeting"
            ? merging
              ? `${current} and ${merging.name} are one speaker now`
              : `${current} is ${next} now`
            : `Line said by ${next}`,
        type: "success",
      })
    }

    if (scope === "meeting") {
      forMeeting.mutate(
        { path: { meeting_id: id, label: line.speaker }, body: { name: next } },
        { onSuccess: done },
      )
    } else {
      forLine.mutate(
        { path: { meeting_id: id, n: line.n }, body: { name: next } },
        { onSuccess: done },
      )
    }
  }

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (next) {
          setScope("line")
          setTyped("")
          forLine.reset()
          forMeeting.reset()
        }
      }}
    >
      <PopoverTrigger render={children} />
      <PopoverContent align="start" aria-label={`Who said line ${line.n}`}>
        <ToggleGroup
          aria-label="Change for"
          value={[scope]}
          onValueChange={(next: string[]) => next[0] && setScope(next[0] as Scope)}
          variant="outline"
          size="sm"
          spacing={0}
        >
          <ToggleGroupItem value="line">This line</ToggleGroupItem>
          <ToggleGroupItem value="meeting">Whole meeting</ToggleGroupItem>
        </ToggleGroup>
        <Command loop>
          <CommandInput
            placeholder="A name…"
            aria-label="A name"
            value={typed}
            onValueChange={setTyped}
            onKeyDown={(e) => {
              // Enter on a name nobody has yet takes it as typed.
              if (e.key === "Enter" && typed.trim() && !hasMatch(typed, suggested, others)) {
                e.preventDefault()
                choose(typed)
              }
            }}
          />
          <CommandList>
            <CommandEmpty>
              {typed.trim() ? "Press Enter to use this name." : "No names yet."}
            </CommandEmpty>
            {suggested.length > 0 && (
              <CommandGroup heading="Suggested">
                {suggested.map((s) => (
                  <CommandItem key={`s:${s.name}`} value={s.name} onSelect={() => choose(s.name)}>
                    <span className="font-medium">{s.name}</span>
                    <span className="ms-auto truncate text-xs text-muted-foreground">{s.why}</span>
                  </CommandItem>
                ))}
              </CommandGroup>
            )}
            {others.length > 0 && (
              <CommandGroup heading="In this meeting">
                {others.map((s) => (
                  <CommandItem key={`o:${s.label}`} value={s.name} onSelect={() => choose(s.name)}>
                    <SpeakerBadge speakers={speakers} label={s.label} size="sm" />
                    {s.name}
                    {scope === "meeting" && (
                      <span className="ms-auto text-xs text-muted-foreground">merges the two</span>
                    )}
                  </CommandItem>
                ))}
              </CommandGroup>
            )}
            {typed.trim() && !hasMatch(typed, suggested, others) && (
              <CommandGroup>
                <CommandItem value={`new ${typed}`} onSelect={() => choose(typed)}>
                  <Plus aria-hidden />
                  New name “{typed.trim()}”
                </CommandItem>
              </CommandGroup>
            )}
          </CommandList>
        </Command>
        <ErrorAlert error={forLine.error ?? forMeeting.error} />
      </PopoverContent>
    </Popover>
  )
}

function hasMatch(typed: string, suggested: { name: string }[], others: { name: string }[]) {
  return [...suggested, ...others].some((s) => sameName(s.name, typed))
}
