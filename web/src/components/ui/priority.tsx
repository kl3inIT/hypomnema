// The priority icons and the priority picker are copied from circle (https://github.com/ln-dev7/circle, commit
// b0ba7dd): mock-data/priorities.tsx, components/common/projects/priority-selector.tsx (the icon-only picker of a row)
// and components/layout/sidebar/create-new-issue/priority-selector.tsx (the picker that names the priority), adapted
// to the app's Popover, Command and Button (Base UI) and to the Inbox's 0-4 values (issue #1157).
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
import { cn } from "cn"

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

type IconProps = React.SVGProps<SVGSVGElement>

function NoPriorityIcon({ className, ...props }: IconProps) {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 16 16"
      fill="currentColor"
      className={className}
      aria-hidden
      focusable="false"
      xmlns="http://www.w3.org/2000/svg"
      {...props}
    >
      <rect x="1.5" y="7.25" width="3" height="1.5" rx="0.5" opacity="0.9" />
      <rect x="6.5" y="7.25" width="3" height="1.5" rx="0.5" opacity="0.9" />
      <rect x="11.5" y="7.25" width="3" height="1.5" rx="0.5" opacity="0.9" />
    </svg>
  )
}

function UrgentPriorityIcon({ className, ...props }: IconProps) {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 16 16"
      fill="currentColor"
      className={className}
      aria-hidden
      focusable="false"
      xmlns="http://www.w3.org/2000/svg"
      {...props}
    >
      <path d="M3 1C1.91067 1 1 1.91067 1 3V13C1 14.0893 1.91067 15 3 15H13C14.0893 15 15 14.0893 15 13V3C15 1.91067 14.0893 1 13 1H3ZM7 4L9 4L8.75391 8.99836H7.25L7 4ZM9 11C9 11.5523 8.55228 12 8 12C7.44772 12 7 11.5523 7 11C7 10.4477 7.44772 10 8 10C8.55228 10 9 10.4477 9 11Z" />
    </svg>
  )
}

function HighPriorityIcon({ className, ...props }: IconProps) {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 16 16"
      fill="currentColor"
      className={className}
      aria-hidden
      focusable="false"
      xmlns="http://www.w3.org/2000/svg"
      {...props}
    >
      <rect x="1.5" y="8" width="3" height="6" rx="1" />
      <rect x="6.5" y="5" width="3" height="9" rx="1" />
      <rect x="11.5" y="2" width="3" height="12" rx="1" />
    </svg>
  )
}

function MediumPriorityIcon({ className, ...props }: IconProps) {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 16 16"
      fill="currentColor"
      className={className}
      aria-hidden
      focusable="false"
      xmlns="http://www.w3.org/2000/svg"
      {...props}
    >
      <rect x="1.5" y="8" width="3" height="6" rx="1" />
      <rect x="6.5" y="5" width="3" height="9" rx="1" />
      <rect x="11.5" y="2" width="3" height="12" rx="1" fillOpacity="0.4" />
    </svg>
  )
}

function LowPriorityIcon({ className, ...props }: IconProps) {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 16 16"
      fill="currentColor"
      className={className}
      aria-hidden
      focusable="false"
      xmlns="http://www.w3.org/2000/svg"
      {...props}
    >
      <rect x="1.5" y="8" width="3" height="6" rx="1" />
      <rect x="6.5" y="5" width="3" height="9" rx="1" fillOpacity="0.4" />
      <rect x="11.5" y="2" width="3" height="12" rx="1" fillOpacity="0.4" />
    </svg>
  )
}

// Priority is one step of Linear's scale: 0 no priority, 1 urgent, 2 high, 3 medium, 4 low.
interface Priority {
  value: number
  name: string
  icon: React.FC<IconProps>
}

// priorities are in the order circle lists them: no priority first, then urgent to low.
const priorities: Priority[] = [
  { value: 0, name: "No priority", icon: NoPriorityIcon },
  { value: 1, name: "Urgent", icon: UrgentPriorityIcon },
  { value: 2, name: "High", icon: HighPriorityIcon },
  { value: 3, name: "Medium", icon: MediumPriorityIcon },
  { value: 4, name: "Low", icon: LowPriorityIcon },
]

function priorityOf(value: number): Priority {
  return priorities.find((p) => p.value === value) ?? priorities[0]!
}

// PriorityIcon is a priority's icon, muted as circle draws it.
function PriorityIcon({ value, className }: { value: number; className?: string }) {
  const { icon: Icon } = priorityOf(value)

  return <Icon className={cn("size-4 text-muted-foreground", className)} />
}

interface PrioritySelectorProps {
  value: number
  onChange: (value: number) => void
  // labelled names the priority beside its icon, as circle's create-new-issue picker; else the icon alone, as its
  // row's.
  labelled?: boolean
  disabled?: boolean
  // title is what pointing at the trigger says, such as the reason the priority was set for.
  title?: string
}

// PrioritySelector is circle's priority picker: a button with the priority's icon opening a searchable list of the
// priorities, the current one checked.
function PrioritySelector({ value, onChange, labelled, disabled, title }: PrioritySelectorProps) {
  const [open, setOpen] = React.useState(false)
  const current = priorityOf(value)

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        disabled={disabled}
        render={
          <Button
            variant={labelled ? "secondary" : "ghost"}
            size={labelled ? "xs" : "icon-sm"}
            role="combobox"
            aria-expanded={open}
            aria-label={`Priority: ${current.name}`}
            title={title ?? `Priority: ${current.name}`}
            data-priority={value}
          />
        }
      >
        <current.icon className="size-4 text-muted-foreground" />
        {labelled && <span>{current.name}</span>}
      </PopoverTrigger>
      <PopoverContent className="w-48 p-0" align="start">
        <Command>
          <CommandInput placeholder="Set priority..." />
          <CommandList>
            <CommandEmpty>No priority found.</CommandEmpty>
            <CommandGroup>
              {priorities.map((item) => (
                <CommandItem
                  key={item.value}
                  value={item.name}
                  data-checked={item.value === value}
                  onSelect={() => {
                    setOpen(false)
                    if (item.value !== value) onChange(item.value)
                  }}
                >
                  <item.icon className="size-4 text-muted-foreground" />
                  <span className="text-xs">{item.name}</span>
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}

export { PriorityIcon, PrioritySelector, priorities, priorityOf, type Priority }
