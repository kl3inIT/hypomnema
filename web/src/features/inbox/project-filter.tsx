// The project filter is adapted from circle (https://github.com/ln-dev7/circle, commit b0ba7dd):
// components/layout/headers/projects/filter.tsx, a button opening a Command list with pages, each page with a back
// button and its own list; adapted to the app's Popover, Command and Button (Base UI) and to the Inbox's projects
// (issue #1157), the sidebar's departments (issue #1188), which are made, renamed and removed in the sidebar, so the
// filter has its first page only.
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

import { Box, FolderIcon } from "lucide-react"
import { useState } from "react"

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
import type { ProjectChoice } from "@/components/ui/project-selector"

import { noProject } from "./inbox-state"

// ProjectFilter picks the project the Inbox shows, "Every project" by default, as Linear's project filter: every
// department in the sidebar's order, and No project. counts is how many rows each project has, by its id, and
// noProject's.
export function ProjectFilter({
  projects,
  counts,
  value,
  onChange,
}: {
  projects: ProjectChoice[]
  counts: Record<string, number>
  value: string | null
  onChange: (project: string | null) => void
}) {
  const [open, setOpen] = useState(false)
  const current = value && value !== noProject ? projects.find((p) => p.id === value) : undefined
  const label = current?.name ?? (value === noProject ? "No project" : "Every project")
  const pick = (project: string | null) => {
    onChange(project)
    setOpen(false)
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={<Button variant="outline" aria-label={`Project: ${label}`} title={label} />}
      >
        <Box />
        <span className="hidden max-w-28 truncate sm:inline">{label}</span>
      </PopoverTrigger>
      <PopoverContent align="end">
        <Command>
          <CommandInput placeholder="Filter by project..." />
          <CommandList>
            <CommandEmpty>No projects found.</CommandEmpty>
            <CommandGroup>
              <CommandItem
                value="Every project"
                data-checked={value === null}
                onSelect={() => pick(null)}
              >
                <Box className="text-muted-foreground" />
                Every project
              </CommandItem>
              <CommandItem
                value="No project"
                data-checked={value === noProject}
                onSelect={() => pick(noProject)}
              >
                <FolderIcon className="text-muted-foreground" />
                <span className="flex-1">No project</span>
                <Count n={counts[noProject]} />
              </CommandItem>
              {projects.map((p) => (
                <CommandItem
                  key={p.id}
                  value={`${p.name} ${p.id}`}
                  data-checked={value === p.id}
                  onSelect={() => pick(p.id)}
                >
                  <Box className="text-muted-foreground" />
                  <span className="min-w-0 flex-1 truncate">{p.name}</span>
                  <Count n={counts[p.id]} />
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}

function Count({ n }: { n?: number }) {
  if (!n) return null

  return <span className="text-xs text-muted-foreground tabular-nums">{n}</span>
}
