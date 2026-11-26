// The status icons and the status picker are copied from circle (https://github.com/ln-dev7/circle, commit
// b0ba7dd): mock-data/status.tsx (the pie, check, cross, duplicate and triage icons and their colours) and
// components/common/issues/status-selector.tsx (the picker), adapted to the app's Popover, Command and Button (Base
// UI) and to the Inbox's states: each state takes the icon of circle's status nearest to it, and the picker offers
// only the states the item can move to from where it is (issue #1173).
//
// MIT License
//
// Copyright (c) 2025 lndev-ui | Circle Template
//
// Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated
// documentation files (the "Software"), to deal in the Software without restriction, including without limitation the
// rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to
// permit persons to whom the Software is furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all copies or substantial portions of
// the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO
// THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
// TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

import * as React from "react"

import type { TodoState } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"

// StatusPieIcon is a partly filled circle, for a status not started or under way: fraction is the filled part.
function StatusPieIcon({ color, fraction }: { color: string; fraction: number }) {
  const clamped = Math.max(0, Math.min(fraction, 0.9999))
  const angle = clamped * 2 * Math.PI
  const x = 3.5 + 3.5 * Math.sin(angle)
  const y = 3.5 - 3.5 * Math.cos(angle)
  const largeArc = clamped > 0.5 ? 1 : 0

  return (
    <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden focusable="false">
      <rect x="1" y="1" width="12" height="12" rx="6" stroke={color} strokeWidth="1.5" />
      {clamped > 0 && (
        <path
          fill={color}
          stroke="none"
          d={`M 3.5,3.5 L3.5,0 A3.5,3.5 0 ${largeArc},1 ${x.toFixed(4)},${y.toFixed(4)} z`}
          transform="translate(3.5,3.5)"
        />
      )}
    </svg>
  )
}

// StatusCheckIcon is a filled circle with a check mark, for done.
function StatusCheckIcon({ color }: { color: string }) {
  return (
    <svg width="14" height="14" viewBox="0 0 14 14" fill={color} aria-hidden focusable="false">
      <path
        fillRule="evenodd"
        clipRule="evenodd"
        d="M7 0C3.13401 0 0 3.13401 0 7C0 10.866 3.13401 14 7 14C10.866 14 14 10.866 14 7C14 3.13401 10.866 0 7 0ZM11.101 5.10104C11.433 4.76909 11.433 4.23091 11.101 3.89896C10.7691 3.56701 10.2309 3.56701 9.89896 3.89896L5.5 8.29792L4.10104 6.89896C3.7691 6.56701 3.2309 6.56701 2.89896 6.89896C2.56701 7.2309 2.56701 7.7691 2.89896 8.10104L4.89896 10.101C5.2309 10.433 5.7691 10.433 6.10104 10.101L11.101 5.10104Z"
      />
    </svg>
  )
}

// StatusXIcon is a filled circle with a cross, for canceled (here dropped).
function StatusXIcon({ color }: { color: string }) {
  return (
    <svg width="14" height="14" viewBox="0 0 14 14" fill={color} aria-hidden focusable="false">
      <path
        fillRule="evenodd"
        clipRule="evenodd"
        d="M7 14C10.866 14 14 10.866 14 7C14 3.13401 10.866 0 7 0C3.13401 0 0 3.13401 0 7C0 10.866 3.13401 14 7 14ZM5.03033 3.96967C4.73744 3.67678 4.26256 3.67678 3.96967 3.96967C3.67678 4.26256 3.67678 4.73744 3.96967 5.03033L5.93934 7L3.96967 8.96967C3.67678 9.26256 3.67678 9.73744 3.96967 10.0303C4.26256 10.3232 4.73744 10.3232 5.03033 10.0303L7 8.06066L8.96967 10.0303C9.26256 10.3232 9.73744 10.3232 10.0303 10.0303C10.3232 9.73744 10.3232 9.26256 10.0303 8.96967L8.06066 7L10.0303 5.03033C10.3232 4.73744 10.3232 4.26256 10.0303 3.96967C9.73744 3.67678 9.26256 3.67678 8.96967 3.96967L7 5.93934L5.03033 3.96967Z"
        stroke="none"
      />
    </svg>
  )
}

// StatusDuplicateIcon is a filled circle with a slashed equal sign, for a duplicate.
function StatusDuplicateIcon({ color }: { color: string }) {
  return (
    <svg width="14" height="14" viewBox="0 0 14 14" fill={color} aria-hidden focusable="false">
      <path
        fillRule="evenodd"
        clipRule="evenodd"
        d="M7 14C10.866 14 14 10.866 14 7C14 3.13401 10.866 0 7 0C3.13401 0 0 3.13401 0 7C0 10.866 3.13401 14 7 14ZM9.5791 5.71973C9.872 5.42684 10.3468 5.42686 10.6396 5.71973C10.9325 6.01262 10.9325 6.48738 10.6396 6.78027L6.78027 10.6396C6.48738 10.9325 6.01262 10.9325 5.71973 10.6396C5.42686 10.3468 5.42684 9.872 5.71973 9.5791L9.5791 5.71973ZM7.21973 3.36035C7.51261 3.06746 7.98738 3.06747 8.28027 3.36035C8.57315 3.65325 8.57316 4.12801 8.28027 4.4209L4.4209 8.28027C4.12801 8.57316 3.65325 8.57315 3.36035 8.28027C3.06747 7.98738 3.06746 7.51261 3.36035 7.21973L7.21973 3.36035Z"
        stroke="none"
      />
    </svg>
  )
}

// StatusTriageIcon is a filled circle with opposite arrows, for what waits to be triaged (here proposed).
function StatusTriageIcon({ color }: { color: string }) {
  return (
    <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden focusable="false">
      <path
        fill={color}
        d="M7 14C10.866 14 14 10.866 14 7C14 3.13403 10.866 0 7 0C3.134 0 0 3.13403 0 7C0 10.866 3.134 14 7 14ZM8.0126 9.50781V7.98224H5.9874V9.50787C5.9874 9.92908 5.4767 10.1549 5.14897 9.8786L2.17419 7.37073C1.94194 7.17493 1.94194 6.82513 2.17419 6.62933L5.14897 4.12146C5.4767 3.84515 5.9874 4.07098 5.9874 4.49219V6.01764H8.0126V4.49213C8.0126 4.07092 8.5233 3.84509 8.85103 4.1214L11.8258 6.62927C12.0581 6.82507 12.0581 7.17487 11.8258 7.37067L8.85103 9.87854C8.5233 10.1548 8.0126 9.92902 8.0126 9.50781Z"
      />
    </svg>
  )
}

// Status is an Inbox state as circle draws a status: its name and icon.
interface Status {
  state: TodoState
  name: string
  icon: () => React.ReactElement
}

// statuses are the Inbox's states in circle's workflow order (triage, unstarted, started, completed, canceled), each
// with the icon and colour of circle's nearest status: proposed is Triage, open Todo, doing In Progress, review
// Technical Review, done Done, dropped Canceled and duplicate Duplicate.
const statuses: Status[] = [
  { state: "proposed", name: "Proposed", icon: () => <StatusTriageIcon color="#f2790f" /> },
  { state: "open", name: "Open", icon: () => <StatusPieIcon color="#99a2b2" fraction={0} /> },
  { state: "doing", name: "Doing", icon: () => <StatusPieIcon color="#facc15" fraction={0.25} /> },
  {
    state: "review",
    name: "In review",
    icon: () => <StatusPieIcon color="#22c55e" fraction={0.4} />,
  },
  { state: "done", name: "Done", icon: () => <StatusCheckIcon color="#5e6ad2" /> },
  { state: "dropped", name: "Dropped", icon: () => <StatusXIcon color="#95a2b3" /> },
  { state: "duplicate", name: "Duplicate", icon: () => <StatusDuplicateIcon color="#95a2b3" /> },
]

function statusOf(state: TodoState): Status {
  return statuses.find((s) => s.state === state) ?? statuses[1]!
}

interface StatusSelectorProps<Move extends string> {
  value: TodoState
  // moves are the states it can go to from value, each with what moves it there.
  moves: { state: TodoState; move: Move }[]
  onChange: (move: Move) => void
  disabled?: boolean
}

// StatusSelector is circle's status picker: a button with the status's icon and, as circle's properties panel shows
// it beside the icon, its name, opening a searchable list of the statuses it can go to, the current one checked.
function StatusSelector<Move extends string>({
  value,
  moves,
  onChange,
  disabled,
}: StatusSelectorProps<Move>) {
  const [open, setOpen] = React.useState(false)
  const current = statusOf(value)
  const items = [
    { status: current, move: undefined as Move | undefined },
    ...moves
      .filter((m) => m.state !== value)
      .map((m) => ({ status: statusOf(m.state), move: m.move as Move | undefined })),
  ]

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        disabled={disabled || moves.length === 0}
        render={
          <Button
            variant="secondary"
            size="xs"
            role="combobox"
            aria-expanded={open}
            aria-label={`Status: ${current.name}`}
            title={`Status: ${current.name}`}
            data-state={value}
          />
        }
      >
        <current.icon />
        <span>{current.name}</span>
      </PopoverTrigger>
      <PopoverContent className="w-48 p-0" align="start">
        <Command>
          <CommandInput placeholder="Set status..." />
          <CommandList>
            <CommandEmpty>No status found.</CommandEmpty>
            <CommandGroup>
              {items.map(({ status, move }) => (
                <CommandItem
                  key={status.state}
                  value={status.name}
                  data-checked={status.state === value}
                  onSelect={() => {
                    setOpen(false)
                    if (move) onChange(move)
                  }}
                >
                  <status.icon />
                  <span className="text-xs">{status.name}</span>
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}

export { StatusSelector }
