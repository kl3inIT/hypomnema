// The project picker is copied from circle (https://github.com/ln-dev7/circle, commit b0ba7dd):
// components/layout/sidebar/create-new-issue/project-selector.tsx, adapted to the app's Popover, Command and Button
// (Base UI) and to the Inbox's projects (issue #1157), which are the sidebar's departments (issue #1188): a name each,
// shown with circle's Box, since a department has no icon of its own.
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
import { Box, FolderIcon } from "lucide-react"

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

// ProjectChoice is a project as the picker shows it: a department, by its id and name.
interface ProjectChoice {
  id: string
  name: string
}

interface ProjectSelectorProps {
  projects: ProjectChoice[]
  // value is the project's id; undefined for none.
  value: string | undefined
  onChange: (project: string | undefined) => void
  disabled?: boolean
}

// ProjectSelector is circle's project picker: a button naming the project, opening a searchable list of the projects,
// in the sidebar's order, with No project first, the current one checked.
function ProjectSelector({ projects, value, onChange, disabled }: ProjectSelectorProps) {
  const [open, setOpen] = React.useState(false)
  const current = projects.find((p) => p.id === value)
  const pick = (id: string | undefined) => {
    setOpen(false)
    if (id !== value) onChange(id)
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        disabled={disabled}
        render={
          <Button
            variant="secondary"
            size="xs"
            role="combobox"
            aria-expanded={open}
            aria-label={`Project: ${current?.name ?? "No project"}`}
          />
        }
      >
        <Box className="size-4" />
        <span>{current?.name ?? "No project"}</span>
      </PopoverTrigger>
      <PopoverContent className="w-56 p-0" align="start">
        <Command>
          <CommandInput placeholder="Set project..." />
          <CommandList>
            <CommandEmpty>No projects found.</CommandEmpty>
            <CommandGroup>
              <CommandItem
                value="No project"
                data-checked={value === undefined}
                onSelect={() => pick(undefined)}
              >
                <FolderIcon className="size-4" />
                No project
              </CommandItem>
              {projects.map((project) => (
                <CommandItem
                  key={project.id}
                  value={`${project.name} ${project.id}`}
                  data-checked={value === project.id}
                  onSelect={() => pick(project.id)}
                >
                  <Box className="size-4" />
                  {project.name}
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}

export { ProjectSelector, type ProjectChoice }
